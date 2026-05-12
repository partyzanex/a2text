//go:build whisper

package factory

import (
	"fmt"
	"log/slog"

	"github.com/partyzanex/a2text/internal/usecases/transcribe"
	"github.com/partyzanex/a2text/pkg/stt"
)

// buildWhisperCpp constructs a whisper.cpp transcriber. Three sub-cases:
//
//   - CloudEnabled && ModelPath==""   → cloud-only (model not present, skip local)
//   - CloudEnabled && ModelPath!=""   → whisper primary + cloud fallback
//   - !CloudEnabled                   → whisper-only; StubMode degrades to
//     an unloaded whisperT instead of returning an error
//
//nolint:ireturn // returns transcribe.Transcriber defined in usecases (consumer owns the interface, per DIP)
func buildWhisperCpp(cfg *Config, log *slog.Logger) (transcribe.Transcriber, error) {
	// Cloud-only path: cloud enabled but no local model configured.
	if cfg.CloudEnabled && cfg.ModelPath == "" {
		return buildCloud(cfg, log)
	}

	whisperT := stt.NewWhisperTranscriber(log)

	// LoadModel may block while loading a large GGML model from disk.
	// Set EagerLoadModel=false to defer loading to the first Transcribe call.
	if loadErr := whisperT.LoadModel(cfg.ModelPath); loadErr != nil {
		if cfg.StubMode {
			if cfg.CloudEnabled {
				// Model present in config but failed to load: fall back to cloud-only.
				log.Warn("whisper model not loaded, falling back to cloud only",
					slog.Any("err", loadErr))

				return buildCloud(cfg, log)
			}

			// No cloud lane: return the unloaded transcriber so the bot can
			// still start in stub mode (Transcribe returns an empty string).
			log.Warn("whisper model not loaded, running in stub mode",
				slog.Any("err", loadErr))

			return whisperT, nil
		}

		return nil, fmt.Errorf("whisper model not loaded: %w", loadErr)
	}

	log.Info("using local whisper transcriber")

	if !cfg.CloudEnabled {
		return whisperT, nil
	}

	cloud, err := buildCloud(cfg, log)
	if err != nil {
		return nil, err
	}

	log.Info("using whisper+cloud fallback transcriber")

	return stt.NewFallbackTranscriber(whisperT, cloud, log), nil
}
