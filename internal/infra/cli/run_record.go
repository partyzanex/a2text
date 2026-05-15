package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/partyzanex/a2text/internal/domain"
	"github.com/partyzanex/a2text/internal/infra/daemon"
	"github.com/partyzanex/a2text/internal/infra/depcheck"
	"github.com/partyzanex/a2text/internal/infra/factory"
	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/usecases/transcribe"
	"github.com/partyzanex/a2text/internal/usecases/voice"
)

// recordSampleRate and recordChannels match the whisper-compatible capture
// format. Cloud providers re-decode anyway, so using a lower sample rate
// costs nothing and avoids resampling in the subprocess adapter.
const (
	recordSampleRate      = 16000
	recordChannels        = 1
	recordAudioPermission = 0o600
)

// RunRecord wires the one-shot record→transcribe→stdout pipeline.
//
// A per-session directory is created under cfg.TempDir; the recorded WAV is
// written directly into it so both are cleaned up together via RemoveAll.
// When cfg.Privacy.KeepAudio is true the directory is preserved instead and
// its path is printed to stdout.
func validateRunRecordArgs(cfg *config.VoiceConfig, duration time.Duration) error {
	if cfg == nil {
		return errors.New("RunRecord: nil config")
	}

	if duration <= 0 {
		return fmt.Errorf("record duration must be positive (got %s)", duration)
	}

	return nil
}

func RunRecord(
	ctx context.Context, cfg *config.VoiceConfig, log *slog.Logger, duration time.Duration,
	stdout io.Writer,
) (err error) {
	err = validateRunRecordArgs(cfg, duration)
	if err != nil {
		return err
	}

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	if stdout == nil {
		stdout = io.Discard
	}

	transcriber, recorder, err := buildRecordDeps(ctx, cfg, log)
	if err != nil {
		return err
	}

	defer func() {
		closeErr := transcriber.Close()
		if closeErr == nil {
			return
		}

		log.Warn("voice: transcriber close failed", slog.String("err", closeErr.Error()))

		if err == nil {
			err = fmt.Errorf("close transcriber: %w", closeErr)
		}
	}()

	err = daemon.WithSessionDir(cfg.TempDir, cfg.Privacy.KeepAudio, log, stdout, func(sessionDir string) error {
		return recordAndTranscribe(ctx, cfg, log, transcriber, recorder, sessionDir, duration)
	})
	if err != nil {
		return fmt.Errorf("run record: %w", err)
	}

	return nil
}

// buildRecordDeps runs depcheck and builds the transcriber and recorder.
func buildRecordDeps(
	ctx context.Context,
	cfg *config.VoiceConfig,
	log *slog.Logger,
) (transcribe.Transcriber, voice.Recorder, error) {
	if _, fatal := daemon.RunDepCheckWith(ctx, depcheck.ModeRecord, cfg, daemon.ExecLookup{}, io.Discard, log); fatal {
		return nil, nil, errors.New("voice: required dependencies missing — check log output for install instructions")
	}

	tr, err := factory.BuildTranscriber(ctx, cfg, log)
	if err != nil {
		return nil, nil, fmt.Errorf("build transcriber: %w", err)
	}

	rec, err := factory.BuildRecorder(log)
	if err != nil {
		return nil, nil, fmt.Errorf("build recorder: %w", err)
	}

	return tr, rec, nil
}

// recordAndTranscribe records audio and runs the transcribe→deliver pipeline.
func recordAndTranscribe(
	ctx context.Context,
	cfg *config.VoiceConfig,
	log *slog.Logger,
	tr transcribe.Transcriber,
	recorder voice.Recorder,
	sessionDir string,
	duration time.Duration,
) error {
	expectedAudioPath := filepath.Join(sessionDir, "recording.wav")

	audioPath, recErr := recorder.RecordToFile(ctx, voice.RecordOptions{
		Duration:   duration,
		OutputPath: expectedAudioPath,
		SampleRate: recordSampleRate,
		Channels:   recordChannels,
	})
	if recErr != nil {
		return fmt.Errorf("record audio: %w", recErr)
	}

	if audioPath != expectedAudioPath {
		return fmt.Errorf("recorder returned unexpected path: %s", filepath.Base(audioPath))
	}

	if chmodErr := os.Chmod(audioPath, recordAudioPermission); chmodErr != nil {
		return fmt.Errorf("voice: set recording perms 0600: %w", chmodErr)
	}

	return transcribeAndDeliver(ctx, cfg, log, tr, audioPath)
}

// transcribeAndDeliver runs the transcribe → trim → deliver pipeline.
func transcribeAndDeliver(
	ctx context.Context, cfg *config.VoiceConfig, log *slog.Logger,
	tr transcribe.Transcriber, audioPath string,
) error {
	text, err := tr.Transcribe(ctx, audioPath, cfg.Language)
	if err != nil {
		return fmt.Errorf("transcribe: %w", err)
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return domain.ErrEmptyResult
	}

	if cfg.Privacy.LogTranscript {
		log.Info("voice: transcript", slog.Int("text_len", len(text)))
	}

	out, buildErr := factory.BuildOutput(cfg, log)
	if buildErr != nil {
		return fmt.Errorf("build output: %w", buildErr)
	}

	if deliverErr := out.Deliver(ctx, text); deliverErr != nil {
		return fmt.Errorf("deliver output: %w", deliverErr)
	}

	return nil
}
