package daemon

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// orphanMaxAge is the minimum age of a session directory before it is
// considered an orphan and removed at daemon startup.
const orphanMaxAge = 24 * time.Hour

// sessionDirPermission is the permission mask for session directories (read+write+execute owner only).
const sessionDirPermission = 0o700

// MakeSessionDir creates a per-session subdirectory under baseDir with 0700
// permissions. The "a2text-" prefix lets CleanOrphanDirs identify leftover
// dirs from crashed or killed sessions.
func MakeSessionDir(baseDir string) (string, error) {
	if baseDir == "" {
		baseDir = os.TempDir()
	}

	dir, err := os.MkdirTemp(baseDir, "a2text-")
	if err != nil {
		return "", fmt.Errorf("create session dir: %w", err)
	}

	if chmodErr := os.Chmod(dir, sessionDirPermission); chmodErr != nil {
		// Best-effort rollback: the directory exists but has wrong perms;
		// remove it so the caller does not unknowingly use an insecure path.
		if err := os.Remove(dir); err != nil {
			_ = err
		}

		return "", fmt.Errorf("set session dir perms: %w", chmodErr)
	}

	return dir, nil
}

// WithSessionDir creates a per-session directory under baseDir, calls fn with
// its path, and always runs CleanupSession on return. Cleanup runs even on
// panic: Go's defer mechanism unwinds deferred calls before propagating the
// panic to the caller.
func WithSessionDir(
	baseDir string, keepAudio bool, log *slog.Logger, out io.Writer,
	runInDir func(dir string) error,
) error {
	dir, err := MakeSessionDir(baseDir)
	if err != nil {
		return err
	}

	defer CleanupSession(dir, keepAudio, log, out)

	return runInDir(dir)
}

// CleanupSession removes the session directory unless keepAudio is true.
// When keepAudio is true, the path is written to out so the operator can
// locate the preserved audio. Errors are logged but not propagated —
// cleanup is best-effort and must not overwrite a pipeline error.
func CleanupSession(dir string, keepAudio bool, log *slog.Logger, out io.Writer) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	if out == nil {
		out = io.Discard
	}

	if dir == "" {
		return
	}

	if keepAudio {
		if _, writeErr := fmt.Fprintf(out, "saved audio: %s\n", dir); writeErr != nil {
			log.Debug("voice: write keepAudio output failed", slog.Any("err", writeErr))
		}

		return
	}

	// Guard against accidentally removing directories that were not created
	// by this package — only a2text-prefixed session dirs are ours to remove.
	if !strings.HasPrefix(filepath.Base(dir), "a2text-") {
		log.Warn("voice: session dir cleanup skipped — unexpected prefix",
			slog.String("dir", filepath.Base(dir)),
		)

		return
	}

	if err := os.RemoveAll(dir); err != nil {
		log.Warn("voice: session dir cleanup failed",
			slog.String("dir", filepath.Base(dir)),
			slog.Any("err", err),
		)
	}
}

// CleanOrphanDirs removes a2text-* subdirectories under baseDir that are
// older than orphanMaxAge. Called at daemon startup to recover temp space from
// sessions that crashed or were force-killed before their defer ran.
//
// Non-fatal: errors for individual entries are logged and skipped — a single
// unreadable entry must not abort the sweep of other orphans.
// Symlinks are never followed or removed — only real directories created by
// MakeSessionDir qualify.
func CleanOrphanDirs(baseDir string, log *slog.Logger) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	if baseDir == "" {
		baseDir = os.TempDir()
	}

	entries, err := os.ReadDir(baseDir)
	if err != nil {
		log.Warn("voice: orphan cleanup: cannot read temp dir",
			slog.String("dir", baseDir),
			slog.Any("err", err),
		)

		return
	}

	cutoff := time.Now().Add(-orphanMaxAge)

	for i := range entries {
		cleanOrphanEntry(baseDir, entries[i], cutoff, log)
	}
}

// cleanOrphanEntry checks a single directory entry and removes it if it
// qualifies as an orphaned a2text session directory.
func cleanOrphanEntry(baseDir string, entry os.DirEntry, cutoff time.Time, log *slog.Logger) {
	if !strings.HasPrefix(entry.Name(), "a2text-") {
		return
	}

	fullPath := filepath.Join(baseDir, entry.Name())

	info, infoErr := os.Lstat(fullPath)
	if infoErr != nil {
		log.Warn("voice: orphan cleanup: cannot stat entry",
			slog.String("dir", entry.Name()),
			slog.Any("err", infoErr),
		)

		return
	}

	if !info.Mode().IsDir() {
		return
	}

	if info.Mode().Perm()&0o077 != 0 {
		log.Warn("voice: orphan cleanup: skipping dir with unexpected permissions",
			slog.String("dir", entry.Name()),
			slog.String("perm", fmt.Sprintf("%04o", info.Mode().Perm())),
		)

		return
	}

	if info.ModTime().After(cutoff) {
		return
	}

	if rmErr := os.RemoveAll(fullPath); rmErr != nil {
		log.Warn("voice: orphan cleanup: remove failed",
			slog.String("dir", entry.Name()),
			slog.Any("err", rmErr),
		)
	} else {
		log.Info("voice: orphan session dir removed", slog.String("dir", entry.Name()))
	}
}
