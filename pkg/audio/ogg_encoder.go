package audio

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/partyzanex/a2text/pkg/audioarchive"
)

// FFmpegOGGEncoder is an audioarchive.Transcoder implemented by shelling
// out to ffmpeg. Vorbis is used because the upstream user request was
// "OGG"; libvorbis is ubiquitous in ffmpeg builds (much more than
// libopus) and Q5 produces ~96 kbps voice — adequate for archival of
// 16 kHz mono speech and ~6× smaller than the source WAV.
type FFmpegOGGEncoder struct {
	timeout time.Duration
	log     *slog.Logger
}

// NewFFmpegOGGEncoder constructs an encoder using ffmpeg. timeout caps
// each encode; nil log is replaced with the discard handler.
func NewFFmpegOGGEncoder(timeout time.Duration, log *slog.Logger) *FFmpegOGGEncoder {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &FFmpegOGGEncoder{timeout: timeout, log: log}
}

// Encode runs ffmpeg in re-encode mode. Only audioarchive.FormatOGG is
// accepted; any other format value is treated as a programming error
// (the archiver should already have decided to call us only for OGG).
func (e *FFmpegOGGEncoder) Encode(
	ctx context.Context,
	srcPath, dstPath string,
	format audioarchive.Format,
) error {
	if format != audioarchive.FormatOGG {
		return fmt.Errorf("ffmpeg ogg encoder: unsupported format %q", format)
	}

	if err := validateEncodePaths(srcPath, dstPath); err != nil {
		return err
	}

	ctx, cancel := withTimeout(ctx, e.timeout)
	defer cancel()

	// -y overwrites existing dst (rare — Archiver renames a .partial),
	// -hide_banner / -loglevel error keep stderr quiet on success.
	args := []string{
		"-y",
		"-hide_banner",
		"-loglevel", "error",
		"-i", srcPath,
		"-c:a", "libvorbis",
		"-q:a", "5",
		dstPath,
	}

	cmd := exec.CommandContext(ctx, ffmpegBin)
	cmd.Args = append(cmd.Args, args...)

	out, err := cmd.CombinedOutput()
	if err != nil {
		e.log.Debug("ffmpeg ogg encoder: process failed",
			slog.String("src", filepath.Base(srcPath)),
			slog.String("dst", filepath.Base(dstPath)),
			slog.String("stderr", strings.TrimSpace(string(out))),
			slog.Any("err", err),
		)

		return fmt.Errorf("ffmpeg ogg encoder: %w", err)
	}

	return nil
}

func validateEncodePaths(src, dst string) error {
	if src == "" {
		return errors.New("ffmpeg ogg encoder: src path is empty")
	}

	if dst == "" {
		return errors.New("ffmpeg ogg encoder: dst path is empty")
	}

	return nil
}

// withTimeout overlays a deadline onto ctx if one is configured.
// Callers that want no timeout can pass 0.
func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}

	return context.WithTimeout(ctx, timeout)
}
