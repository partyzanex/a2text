package factory

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/pkg/audio"
	"github.com/partyzanex/a2text/pkg/audioarchive"
)

// keptAudioArchiver is the bridge between the voice use case's
// KeptAudioArchiver interface and pkg/audioarchive. It checks
// cfg.Privacy.KeepAudio at every Archive call so that toggling the
// flag from the settings window takes effect immediately, with no
// daemon restart.
//
// cfg is held by pointer (not value) for the same reason — the
// settings window mutates it in place when the user clicks Save.
type keptAudioArchiver struct {
	cfg      *config.VoiceConfig
	archiver *audioarchive.Archiver
	log      *slog.Logger
}

// NewKeptAudioArchiver wires production dependencies. The OGG path
// requires an ffmpeg binary to be present on PATH; when missing the
// archiver still works for FormatWAV and logs a warning the first
// time OGG is requested. timeout caps a single OGG encode.
//
// Returns nil if cfg is nil (defensive: the wiring layer should
// never pass a nil cfg, but a panicky factory is unfriendly to
// composition-root code).
func NewKeptAudioArchiver(cfg *config.VoiceConfig, timeout time.Duration, log *slog.Logger) *keptAudioArchiver {
	if cfg == nil {
		return nil
	}

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	transcoder := audio.NewFFmpegOGGEncoder(timeout, log)

	return &keptAudioArchiver{
		cfg:      cfg,
		archiver: audioarchive.NewArchiver(transcoder),
		log:      log,
	}
}

// Archive copies (or transcodes) audioPath into the configured kept-
// audio directory. The returned path is the on-disk archive location;
// callers may log it for the user but should not depend on its
// structure — the archiver picks timestamped names.
//
// When KeepAudio is false, Archive is a no-op and returns ("", nil) so
// the caller's defer chain does nothing. Errors do not propagate to
// the user: archival is best-effort and must not abort a successful
// dictation cycle.
func (k *keptAudioArchiver) Archive(ctx context.Context, audioPath string) (string, error) {
	if k == nil || k.archiver == nil || !k.cfg.Privacy.KeepAudio {
		return "", nil
	}

	destDir := k.resolveDestDir()
	if destDir == "" {
		// Defensive: every resolveDestDir branch supplies a non-empty
		// path. If we ever reach this, the configuration is broken in
		// a way the user has no chance of guessing — log loudly.
		k.log.Warn("kept-audio: no destination directory resolved; archive skipped")

		return "", nil
	}

	format := audioarchive.Format(strings.ToLower(k.cfg.Privacy.KeepAudioFormat))
	if format == "" {
		format = audioarchive.FormatWAV
	}

	savedPath, err := k.archiver.Archive(ctx, audioPath, destDir, format)
	if err != nil {
		return "", fmt.Errorf("kept-audio archive: %w", err)
	}

	return savedPath, nil
}

// resolveDestDir picks the directory to archive into, in priority:
//  1. cfg.Privacy.KeepAudioDir, if the user set it;
//  2. cfg.TempDir, the "working files" location;
//  3. os.TempDir() as the last-resort default.
//
// The chain mirrors the rest of the codebase's temp-dir resolution so
// the file lands somewhere predictable even when the user has not
// touched the new setting at all.
func (k *keptAudioArchiver) resolveDestDir() string {
	if dir := strings.TrimSpace(k.cfg.Privacy.KeepAudioDir); dir != "" {
		return dir
	}

	if dir := strings.TrimSpace(k.cfg.TempDir); dir != "" {
		return dir
	}

	return os.TempDir()
}
