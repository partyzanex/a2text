package voice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/partyzanex/a2text/internal/domain"
)

// TranscribeFileUseCase transcribes a single audio file from disk and
// delivers the result text to the configured Output.
//
// Format-specific concerns (whether input needs ffmpeg conversion, whether
// the temp file should be cleaned up) live entirely inside Converter — this
// use case stays free of adapter-layer knowledge.
type TranscribeFileUseCase struct {
	transcriber Transcriber
	converter   Converter
	output      Output
	log         *slog.Logger
}

// NewTranscribeFileUseCase wires the use case dependencies.
func NewTranscribeFileUseCase(
	transcriber Transcriber,
	converter Converter,
	out Output,
	log *slog.Logger,
) *TranscribeFileUseCase {
	return &TranscribeFileUseCase{
		transcriber: transcriber,
		converter:   converter,
		output:      out,
		log:         log,
	}
}

// Run executes the file → STT → output pipeline.
func (uc *TranscribeFileUseCase) Run(ctx context.Context, path, lang string) error {
	if err := checkRegularFile(path); err != nil {
		return err
	}

	audioPath, cleanup, err := uc.converter.ToWAV(ctx, path)
	if err != nil {
		return fmt.Errorf("prepare audio %q: %w", path, err)
	}
	defer cleanup()

	uc.log.Info("voice.transcribe_file: starting",
		slog.String("file", filepath.Base(path)),
		slog.String("lang", lang),
	)

	text, err := uc.transcriber.Transcribe(ctx, audioPath, lang)
	if err != nil {
		return fmt.Errorf("transcribe: %w", err)
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return domain.ErrEmptyResult
	}

	uc.log.Info("voice.transcribe_file: completed",
		slog.Int("text_len", len(text)),
	)

	if err := uc.output.Deliver(ctx, text); err != nil {
		return fmt.Errorf("deliver output: %w", err)
	}

	return nil
}

// checkRegularFile fails fast for missing inputs, directories, sockets,
// and other non-regular paths. Without this guard ffmpeg would receive a
// directory and surface a cryptic stderr message far from the cause.
func checkRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", domain.ErrFileNotFound, path)
		}

		return fmt.Errorf("stat %q: %w", path, err)
	}

	if !info.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", domain.ErrNotRegularFile, path)
	}

	return nil
}
