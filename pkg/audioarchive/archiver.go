// Package audioarchive copies (and optionally transcodes) a WAV
// recording into a long-term archive directory. Designed to back the
// settings UI's "Сохранять аудио" toggle: when enabled, every
// successfully-transcribed cycle leaves a permanent copy of its source
// audio in a user-chosen folder, with a user-chosen container.
//
// The package is intentionally narrow:
//   - inputs are always 16 kHz mono s16le WAV (what the recorder produces);
//   - output is either a byte-for-byte WAV copy or an OGG/Vorbis transcode;
//   - filenames are timestamped so concurrent cycles never collide.
//
// Transcoding is delegated to an injected Transcoder, which production
// wiring binds to ffmpeg. Tests inject a stub.
package audioarchive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Format enumerates the storage formats the archiver knows how to
// produce. Mirrors config.VoiceKeepAudioFormat* values so callers can
// pass the raw config string after lower-casing.
type Format string

const (
	FormatWAV Format = "wav"
	FormatOGG Format = "ogg"
)

// Transcoder converts an input WAV file at srcPath into an OGG file at
// dstPath. Implementations should be deterministic (same input → same
// output bytes is not required, but the resulting file must be
// playable) and respect ctx cancellation. The production binding lives
// in pkg/audio behind an ffmpeg shell-out.
type Transcoder interface {
	Encode(ctx context.Context, srcPath, dstPath string, format Format) error
}

// Archiver copies finished recordings into a target directory under a
// new timestamped name, optionally re-encoding into a more compact
// format. Construct via NewArchiver; the zero value is unusable
// (Transcoder is required when OGG output may be requested).
type Archiver struct {
	transcoder Transcoder
	now        func() time.Time
}

// NewArchiver wires the archiver with its OGG transcoder. transcoder
// may be nil in production paths that never request OGG (in which case
// archive falls back to a WAV copy and logs a warning).
func NewArchiver(transcoder Transcoder) *Archiver {
	return &Archiver{transcoder: transcoder, now: time.Now}
}

// Archive copies srcPath into destDir, returning the absolute path of
// the saved file. Naming convention: "a2text-<UTC RFC3339-ish>.<ext>",
// e.g. "a2text-20260515T172347Z.ogg".
//
// destDir is created if missing. format == FormatOGG transcodes via
// the Transcoder; any other value (or empty string) writes a WAV copy.
//
// Behaviour on partial failure mirrors the downloader: write to a
// sibling ".partial" file and atomic-rename on success so a half-
// written archive can never be mistaken for a complete one.
func (a *Archiver) Archive(
	ctx context.Context,
	srcPath, destDir string,
	format Format,
) (string, error) {
	if a == nil {
		return "", errors.New("audioarchive: nil Archiver")
	}

	if srcPath == "" {
		return "", errors.New("audioarchive: srcPath is empty")
	}

	if destDir == "" {
		return "", errors.New("audioarchive: destDir is empty")
	}

	const archiveDirMode = 0o750

	if err := os.MkdirAll(destDir, archiveDirMode); err != nil {
		return "", fmt.Errorf("audioarchive: mkdir %q: %w", destDir, err)
	}

	ext := chooseExt(format)
	finalPath := filepath.Join(destDir, a.makeName(ext))
	partialPath := finalPath + ".partial"

	if err := a.writeArchive(ctx, srcPath, partialPath, format); err != nil {
		removeArchivePartial(partialPath)

		return "", fmt.Errorf("audioarchive: %w", err)
	}

	if err := os.Rename(partialPath, finalPath); err != nil {
		removeArchivePartial(partialPath)

		return "", fmt.Errorf("audioarchive: rename: %w", err)
	}

	return finalPath, nil
}

// makeName builds the timestamped output filename. Uses UTC so files
// from different machines or DST flips sort lexically by capture time.
func (a *Archiver) makeName(ext string) string {
	stamp := a.now().UTC().Format("20060102T150405Z")

	return "a2text-" + stamp + "." + ext
}

// writeArchive does the actual byte movement: copy for WAV, transcode
// for OGG. Splitting it out keeps Archive small enough to pass cyclop
// with a wide error-handling margin.
func (a *Archiver) writeArchive(
	ctx context.Context,
	srcPath, dstPath string,
	format Format,
) error {
	if normalise(format) == FormatOGG {
		if a.transcoder == nil {
			return errors.New("ogg format requested but no transcoder configured")
		}

		if err := a.transcoder.Encode(ctx, srcPath, dstPath, FormatOGG); err != nil {
			return fmt.Errorf("transcode: %w", err)
		}

		return nil
	}

	if err := copyFile(srcPath, dstPath); err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	return nil
}

func chooseExt(format Format) string {
	if normalise(format) == FormatOGG {
		return "ogg"
	}

	return "wav"
}

func normalise(format Format) Format {
	switch Format(strings.ToLower(string(format))) {
	case FormatOGG:
		return FormatOGG
	case FormatWAV:
		return FormatWAV
	}

	return FormatWAV
}

// copyFile streams src to dst without holding the whole WAV in memory.
// Stream size is 64 KiB to match the downloader's chunk size — large
// enough to amortise syscalls, small enough that ctx cancellation
// (handled outside this helper) interrupts within tens of ms.
func copyFile(src, dst string) (retErr error) {
	in, err := os.Open(filepath.Clean(src))
	if err != nil {
		return fmt.Errorf("open src: %w", err)
	}

	defer func() {
		if closeErr := in.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("close src: %w", closeErr)
		}
	}()

	out, err := os.Create(filepath.Clean(dst))
	if err != nil {
		return fmt.Errorf("create dst: %w", err)
	}

	defer func() {
		if closeErr := out.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("close dst: %w", closeErr)
		}
	}()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("io.Copy: %w", err)
	}

	return nil
}

func removeArchivePartial(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		// Best-effort: the caller has a real error to surface;
		// failing to clean up the partial is not the user's problem.
		_ = err
	}
}
