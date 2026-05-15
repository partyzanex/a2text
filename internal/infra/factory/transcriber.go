package factory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	"github.com/partyzanex/a2text/internal/usecases/transcribe"
	"github.com/partyzanex/a2text/pkg/stt"
)

// Provider constants — shared by all callers so they don't need to import
// each other's config packages.
const (
	ProviderGoWhisper  = "go-whisper"
	ProviderWhisperCpp = "whisper-cpp"
	ProviderCloud      = "cloud"

	CloudProviderOpenAI   = "openai"
	CloudProviderDeepgram = "deepgram"
)

// ErrUnknownProvider is returned by Build when cfg.Provider is not one of the
// known provider constants. Callers can test with errors.Is.
var ErrUnknownProvider = errors.New("stt: unknown provider")

// ErrUnknownCloudProvider is returned by buildCloud when cfg.CloudProvider is
// not one of the known cloud provider constants.
var ErrUnknownCloudProvider = errors.New("stt: unknown cloud provider")

// Config carries the provider selection and per-provider tuning knobs.
// Callers map from their own config struct (*config.Config or *config.VoiceConfig)
// and pass a populated Config to Build.
type Config struct {
	// Provider selects the STT backend. Use the Provider* constants above.
	Provider string

	// GoWhisper* are used when Provider == ProviderGoWhisper. GoWhisperURL is
	// the full base URL including any API path segment (e.g. ".../api/whisper").
	GoWhisperURL          string
	GoWhisperModel        string
	GoWhisperTimeout      time.Duration
	GoWhisperAutoDownload bool

	// Cloud* are used when Provider == ProviderCloud or when CloudEnabled is
	// true (secondary lane for go-whisper and whisper-cpp primary providers).
	CloudProvider string // CloudProviderOpenAI | CloudProviderDeepgram
	CloudAPIKey   string
	CloudBaseURL  string
	CloudEnabled  bool

	// ModelPath is the path to the whisper.cpp GGML model file.
	// Used when Provider == ProviderWhisperCpp.
	ModelPath string

	// StubMode: when true and the local whisper.cpp model fails to load,
	// Build degrades gracefully instead of returning an error:
	//   - CloudEnabled=true  → falls back to cloud-only transcriber.
	//   - CloudEnabled=false → returns an unloaded transcriber that yields
	//     empty transcription results (bot remains operational).
	// Only active in builds with the "whisper" tag; ignored otherwise.
	StubMode bool

	// MaxFileSize limits the upload size for cloud providers (bytes). 0 = no limit.
	// Currently enforced only by the Deepgram adapter; OpenAI relies on the
	// API's own server-side limits and does not expose a local file-size check.
	MaxFileSize int64

	// EagerLoadModel: when true, Build calls LoadModelCtx on the go-whisper
	// transcriber before returning. This may block on network I/O (model
	// download or HTTP health check). The bot uses it for fail-fast startup;
	// the voice CLI omits it and relies on depcheck instead.
	EagerLoadModel bool
}

// Build constructs the STT transcriber described by cfg. It is safe to call
// with a nil log (replaced with a discard handler internally).
//
// When EagerLoadModel is true, Build may block on network I/O (model download
// or HTTP health check) before returning.
//
// voice.Transcriber (usecases/voice) is a strict subset of transcribe.Transcriber;
// a returned value satisfies both interfaces, so voice-cmd callers can assign
// the result to voice.Transcriber directly.
//
//nolint:ireturn // returns transcribe.Transcriber defined in usecases (consumer owns the interface, per DIP)
func Build(ctx context.Context, cfg *Config, log *slog.Logger) (transcribe.Transcriber, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	switch cfg.Provider {
	case ProviderGoWhisper:
		return buildGoWhisper(ctx, cfg, log)
	case ProviderWhisperCpp:
		return buildWhisperCpp(cfg, log)
	case ProviderCloud:
		return buildCloud(cfg, log)
	default:
		return nil, fmt.Errorf("%w %q", ErrUnknownProvider, cfg.Provider)
	}
}

//nolint:ireturn // returns transcribe.Transcriber defined in usecases (consumer owns the interface, per DIP)
func buildGoWhisper(ctx context.Context, cfg *Config, log *slog.Logger) (transcribe.Transcriber, error) {
	gowhisper := stt.NewGoWhisperTranscriber(stt.GoWhisperConfig{
		BaseURL:      cfg.GoWhisperURL,
		Model:        cfg.GoWhisperModel,
		Timeout:      cfg.GoWhisperTimeout,
		AutoDownload: cfg.GoWhisperAutoDownload,
	}, log)

	if cfg.EagerLoadModel {
		if err := gowhisper.LoadModelCtx(ctx, cfg.GoWhisperModel); err != nil {
			return nil, fmt.Errorf("go-whisper model %q not ready: %w", cfg.GoWhisperModel, err)
		}
	}

	if cfg.CloudEnabled {
		cloud, err := buildCloud(cfg, log)
		if err != nil {
			return nil, err
		}

		log.Info("using go-whisper+cloud fallback transcriber",
			slog.String("url", sanitizeURL(cfg.GoWhisperURL)),
			slog.String("model", cfg.GoWhisperModel),
			slog.String("cloud", cfg.CloudProvider),
		)

		return stt.NewFallbackTranscriber(gowhisper, cloud, log), nil
	}

	log.Info("using go-whisper transcriber",
		slog.String("url", sanitizeURL(cfg.GoWhisperURL)),
		slog.String("model", cfg.GoWhisperModel),
	)

	return gowhisper, nil
}

//nolint:ireturn // returns transcribe.Transcriber defined in usecases (consumer owns the interface, per DIP)
func buildCloud(cfg *Config, log *slog.Logger) (transcribe.Transcriber, error) {
	if cfg.CloudAPIKey == "" {
		return nil, errors.New("stt: cloud_api_key must not be empty")
	}

	switch cfg.CloudProvider {
	case CloudProviderDeepgram:
		log.Info("using deepgram cloud transcriber")

		return stt.NewDeepgramTranscriber(cfg.CloudAPIKey, cfg.CloudBaseURL, cfg.MaxFileSize, log), nil
	case CloudProviderOpenAI:
		log.Info("using openai cloud transcriber")
		// nil http.Client — use the default transport. OpenAI adapter has no
		// local MaxFileSize guard; the API enforces its own upload limits.

		return stt.NewOpenAITranscriber(cfg.CloudAPIKey, cfg.CloudBaseURL, nil, log), nil
	default:
		return nil, fmt.Errorf("%w %q (supported: %s, %s)",
			ErrUnknownCloudProvider, cfg.CloudProvider, CloudProviderOpenAI, CloudProviderDeepgram,
		)
	}
}

// sanitizeURL strips userinfo from a URL before logging to avoid writing
// embedded credentials to the journal. Returns "[unparseable URL]" when
// parsing fails so a malformed config value is never echoed into logs.
func sanitizeURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "[unparseable URL]"
	}

	if parsed.User == nil {
		return rawURL
	}

	parsed.User = nil

	return parsed.String()
}
