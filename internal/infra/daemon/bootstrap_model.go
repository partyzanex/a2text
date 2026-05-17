package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/infra/sysd"
	"github.com/partyzanex/a2text/pkg/whispercpp"
)

// defaultWhisperCppModel is the model the daemon auto-downloads on
// first launch when provider=whisper-cpp and no model file has been
// picked yet. "tiny" is the smallest multilingual ggml model
// (~75MB) — picked to keep the first-launch UX snappy: a few seconds
// on a normal connection vs ~30s for small (~466MB) or minutes for
// large. Quality is rough on Russian dictation; the settings UI
// surfaces the full lineup (base/small/medium/large) for users who
// want better accuracy, and the Скачать-модель button writes any
// chosen model into the same dir, so the upgrade path is one click.
const defaultWhisperCppModel = "ggml-tiny.bin"

// EnsureWhisperCppModel makes sure a usable .bin model exists on disk
// when the configured STT provider is whisper-cpp. Behaviour:
//
//   - Wrong provider, or ModelPath already pointing at an existing
//     non-empty file → no-op (the user is in control).
//   - ModelPath blank → resolve the conventional models dir
//     (cfg.WhisperCppModelsDir, fallback to XDG default via sysd), make
//     sure ggml-small.bin lives there, download it from the mirror list
//     when missing, then mutate cfg.ModelPath in place so the
//     transcriber factory finds the file immediately afterwards.
//
// Failure is non-fatal: callers should run the lazy-error transcriber
// path on download failure so the settings UI stays reachable and the
// user can pick another provider or retry later. The download itself
// is idempotent (whispercpp.Downloader.Download short-circuits when
// the target exists), so re-running ensureWhisperCppModel costs only
// a stat() on the next launch.
func EnsureWhisperCppModel(ctx context.Context, cfg *config.VoiceConfig, log *slog.Logger) {
	if cfg == nil || cfg.Provider != config.VoiceProviderWhisperCpp {
		return
	}

	if modelExists(cfg.ModelPath) {
		return
	}

	dir, err := resolveModelsDir(cfg.WhisperCppModelsDir)
	if err != nil {
		log.Warn("voice: cannot auto-download whisper.cpp model",
			slog.Any("err", err),
			slog.String("hint", "set whisper_cpp_models_dir or model_path in settings"),
		)

		return
	}

	finalPath := filepath.Join(dir, defaultWhisperCppModel)
	if modelExists(finalPath) {
		cfg.ModelPath = finalPath

		log.Info("voice: using existing whisper.cpp model",
			slog.String("path", finalPath),
		)

		return
	}

	log.Info("voice: auto-downloading whisper.cpp default model",
		slog.String("model", defaultWhisperCppModel),
		slog.String("dest", dir),
		slog.String("hint", "first launch only; ~75MB; subsequent starts skip this"),
	)

	d := whispercpp.Downloader{}

	gotPath, err := d.Download(ctx, defaultWhisperCppModel, dir, nil)
	if err != nil {
		log.Warn("voice: whisper.cpp model auto-download failed",
			slog.String("model", defaultWhisperCppModel),
			slog.String("dest", dir),
			slog.Any("err", err),
			slog.String("hint", "open settings, pick a model manually, or switch provider"),
		)

		return
	}

	cfg.ModelPath = gotPath

	if cfg.WhisperCppModelsDir == "" {
		cfg.WhisperCppModelsDir = dir
	}

	log.Info("voice: whisper.cpp model ready",
		slog.String("path", gotPath),
	)
}

// modelExists reports whether path is a non-empty regular file. A
// dangling symlink or empty placeholder counts as "missing" so the
// downloader is invoked and produces a usable file.
func modelExists(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}

	info, err := os.Stat(path)
	if err != nil {
		return false
	}

	return !info.IsDir() && info.Size() > 0
}

// resolveModelsDir picks the directory to download models into:
// configured value when set, otherwise the XDG default from sysd.
// Returns an error only when both sources are unusable (no
// XDG_DATA_HOME and no resolvable $HOME) — extremely rare.
func resolveModelsDir(configured string) (string, error) {
	if dir := strings.TrimSpace(configured); dir != "" {
		return dir, nil
	}

	dir, err := sysd.WhisperCppModelsDir()
	if err != nil {
		return "", fmt.Errorf("daemon: resolve default models dir: %w", err)
	}

	if dir == "" {
		return "", errors.New("daemon: cannot resolve default whisper.cpp models dir")
	}

	return dir, nil
}
