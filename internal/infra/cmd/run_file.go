package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"

	"github.com/partyzanex/a2text/internal/adapters/output"
	"github.com/partyzanex/a2text/internal/domain"
	"github.com/partyzanex/a2text/internal/infra/cmd/daemon"
	"github.com/partyzanex/a2text/internal/infra/cmd/factory"
	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/usecases/voice"
)

// RunFile executes the one-shot file-transcription pipeline. It is invoked
// when the user passes --file PATH. The pipeline is intentionally simple:
// load deps, run the use case, return the error untouched so the CLI layer
// can format an exit code.
//
// A per-session directory is created under cfg.TempDir so converted WAV files
// are grouped and removed together on success, error, or panic. When
// cfg.Privacy.KeepAudio is true the directory is preserved instead and its
// path is printed to stdout.
//
// Close errors from the transcriber surface only if no other error already
// happened — primary errors take precedence so the user sees the root cause,
// not a misleading "close failed" wrapping it.
func validateRunFileArgs(cfg *config.VoiceConfig, path string) error {
	if cfg == nil {
		return errors.New("RunFile: nil config")
	}

	if path == "" {
		return errors.New("RunFile: file path must not be empty")
	}

	return nil
}

func RunFile(ctx context.Context, cfg *config.VoiceConfig, log *slog.Logger, path string, stdout io.Writer) (err error) {
	err = validateRunFileArgs(cfg, path)
	if err != nil {
		return err
	}

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	if stdout == nil {
		stdout = io.Discard
	}

	sessionDir, err := prepareFileSession(ctx, cfg, log, path)
	if err != nil {
		return err
	}

	defer daemon.CleanupSession(sessionDir, cfg.Privacy.KeepAudio, log, stdout)

	return runFilePipeline(ctx, cfg, log, path, sessionDir)
}

// prepareFileSession runs depcheck and creates the session directory.
func prepareFileSession(
	ctx context.Context,
	cfg *config.VoiceConfig,
	log *slog.Logger,
	path string,
) (string, error) {
	_, fatal := daemon.RunDepCheckWith(ctx, daemon.FileDepCheckMode(path), cfg, daemon.ExecLookup{}, io.Discard, log)
	if fatal {
		return "", errors.New("voice: required dependencies missing — check log output for install instructions")
	}

	sessionDir, err := daemon.MakeSessionDir(cfg.TempDir)
	if err != nil {
		return "", fmt.Errorf("run file: %w", err)
	}

	return sessionDir, nil
}

// runFilePipeline builds the transcriber/converter and runs the use case.
func runFilePipeline(
	ctx context.Context,
	cfg *config.VoiceConfig,
	log *slog.Logger,
	path, sessionDir string,
) (err error) {
	transcriber, err := factory.BuildTranscriber(ctx, cfg, log)
	if err != nil {
		return fmt.Errorf("build transcriber: %w", err)
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

	converter, err := factory.BuildConverter(cfg, sessionDir, log)
	if err != nil {
		return fmt.Errorf("build converter: %w", err)
	}

	out := output.NewStdoutOutput()
	useCase := voice.NewTranscribeFileUseCase(transcriber, converter, out, log)

	if runErr := useCase.Run(ctx, path, cfg.Language); runErr != nil {
		if errors.Is(runErr, domain.ErrEmptyResult) {
			log.Warn("voice: transcription produced no text",
				slog.String("file", filepath.Base(path)),
			)
		}

		return fmt.Errorf("run file: %w", runErr)
	}

	return nil
}
