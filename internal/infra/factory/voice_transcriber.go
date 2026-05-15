package factory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/usecases/transcribe"
	"github.com/partyzanex/a2text/internal/usecases/voice"
	"github.com/partyzanex/a2text/pkg/audio"
	"github.com/partyzanex/a2text/pkg/stt"
)

// retry policy defaults applied when the user enables stt_retry but leaves
// individual fields at zero.
const (
	defaultRetryMaxAttempts  = 2
	defaultRetryInitialDelay = 200 * time.Millisecond
	defaultRetryMaxDelay     = 5 * time.Second
)

// BuildTranscriber selects the STT backend based on cfg.Provider.
//
// Supported providers: go-whisper (HTTP service) and cloud (OpenAI / Deepgram).
// The whisper-cpp local backend requires CGo and the `whisper` build tag and
// is wired in a separate file with a `//go:build whisper` constraint.
//
//nolint:ireturn // returns transcribe.Transcriber defined in usecases (consumer owns the interface, per DIP)
func BuildTranscriber(ctx context.Context, cfg *config.VoiceConfig, log *slog.Logger) (transcribe.Transcriber, error) {
	if cfg == nil {
		// Nil cfg is a programming error — the call path always has a loaded config.
		return nil, errors.New("BuildTranscriber: nil config")
	}

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	base, err := Build(ctx, &Config{
		Provider:              cfg.Provider,
		GoWhisperURL:          cfg.GoWhisper.URL,
		GoWhisperModel:        cfg.GoWhisper.Model,
		GoWhisperTimeout:      cfg.GoWhisper.Timeout,
		GoWhisperAutoDownload: cfg.GoWhisper.AutoDownload,
		CloudProvider:         cfg.CloudProvider,
		CloudAPIKey:           cfg.CloudAPIKey,
		CloudBaseURL:          cfg.CloudBaseURL,
		// Voice CLI uses an explicit "cloud" provider — no implicit fallback lane.
		CloudEnabled:   false,
		ModelPath:      cfg.ModelPath,
		EagerLoadModel: false, // depcheck covers reachability; lazy-check on first use
	}, log)
	if err != nil {
		return nil, fmt.Errorf("transcriber: %w", err)
	}

	if !cfg.STTRetry.Enabled {
		return base, nil
	}

	return wrapWithRetry(base, cfg.STTRetry, log), nil
}

// wrapWithRetry applies sensible defaults to user-supplied STTRetryConfig and
// wraps the backend. Defaults: 2 attempts (1 retry), 200ms initial delay,
// 5s cap. The wrapper passes through LoadModel/ReloadModel/DetectLanguage/
// Close — only Transcribe gets retry.
//
// returns transcribe.Transcriber for DIP reason as BuildTranscriber.
func wrapWithRetry(
	inner transcribe.Transcriber, cfg config.VoiceSTTRetryConfig, log *slog.Logger,
) transcribe.Transcriber {
	rcfg := stt.RetryConfig{
		MaxAttempts:  cfg.MaxAttempts,
		InitialDelay: cfg.InitialDelay,
		MaxDelay:     cfg.MaxDelay,
	}

	if rcfg.MaxAttempts <= 0 {
		rcfg.MaxAttempts = defaultRetryMaxAttempts
	}

	if rcfg.InitialDelay <= 0 {
		rcfg.InitialDelay = defaultRetryInitialDelay
	}

	if rcfg.MaxDelay <= 0 {
		rcfg.MaxDelay = defaultRetryMaxDelay
	}

	log.Info("voice: STT retry enabled",
		slog.Int("max_attempts", rcfg.MaxAttempts),
		slog.Duration("initial_delay", rcfg.InitialDelay),
		slog.Duration("max_delay", rcfg.MaxDelay),
	)

	return stt.NewRetryingTranscriber(inner, rcfg, log)
}

// BuildConverter selects an audio converter compatible with the chosen
// transcriber. go-whisper and cloud providers accept arbitrary formats
// server-side, so we use a passthrough; only the local whisper.cpp path
// requires a real ffmpeg conversion to WAV 16k mono.
//
// tempDir is where ffmpeg writes the converted WAV. Empty string falls back
// to cfg.TempDir, which in turn defaults to os.TempDir(). Callers that
// create a per-session directory pass it here so the WAV lands inside the
// session tree and is cleaned up by RemoveAll.
//
// convert_timeout is only validated for the whisper-cpp provider; go-whisper
// and cloud handle audio decoding server-side and never invoke ffmpeg.
//
// Returns voice.Converter (with cleanup ownership) — see interfaces.go in
// usecases/voice. Errors out for any unknown provider rather than silently
// picking ffmpeg.
//
//nolint:ireturn // returns voice.Converter defined in usecases (consumer owns the interface, per DIP)
func BuildConverter(cfg *config.VoiceConfig, tempDir string, log *slog.Logger) (voice.Converter, error) {
	if cfg == nil {
		// Nil cfg is a programming error — the call path always has a loaded config.
		return nil, errors.New("BuildConverter: nil config")
	}

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	switch cfg.Provider {
	case config.VoiceProviderGoWhisper, config.VoiceProviderCloud:
		log.Info("voice: using passthrough converter (provider handles audio decoding)")

		return passthroughConverter{}, nil

	case config.VoiceProviderWhisperCpp:
		if cfg.ConvertTimeout <= 0 {
			return nil, errors.New("BuildConverter: convert_timeout must be positive for whisper-cpp")
		}

		dir := resolveTempDir(tempDir, cfg.TempDir)

		log.Info("voice: using ffmpeg converter",
			slog.String("temp_dir", filepath.Base(dir)),
			slog.Duration("timeout", cfg.ConvertTimeout),
		)

		inner := audio.NewFFmpegConverter(cfg.ConvertTimeout, dir, log)
		adapter := newFFmpegConverterAdapter(inner)

		return newFfmpegConverter(adapter, log)

	default:
		return nil, fmt.Errorf("unknown provider %q", cfg.Provider)
	}
}

// resolveTempDir returns the first non-empty value from the priority chain:
// explicit caller override → config TempDir → OS default temp directory.
func resolveTempDir(override, cfgTempDir string) string {
	if override != "" {
		return override
	}

	if cfgTempDir != "" {
		return cfgTempDir
	}

	return os.TempDir()
}
