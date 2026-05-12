//go:build linux

package clipboard

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	xclipBin     = "xclip"
	xclipTimeout = 3 * time.Second // fail-hard on timeout (wrapped ctx.Err)
)

// X11Clipboard pipes text to xclip -selection clipboard via stdin.
type X11Clipboard struct {
	runner     CopyRunner
	log        *slog.Logger
	binaryPath string
}

// NewX11Clipboard binds to the xclip binary in PATH.
//
// Errors: fail-hard on missing xclip binary (ErrNoBackend).
func NewX11Clipboard(log *slog.Logger) (*X11Clipboard, error) {
	return newX11Clipboard(execCopyRunner{}, log)
}

func newX11Clipboard(runner CopyRunner, log *slog.Logger) (*X11Clipboard, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	if runner == nil {
		return nil, ErrNoBackend
	}

	path, err := runner.LookPath(xclipBin)
	if err != nil {
		return nil, ErrNoBackend
	}

	log.Info("voice: clipboard backend selected",
		slog.String("backend", xclipBin),
		slog.String("binary", filepath.Base(path)),
	)

	return &X11Clipboard{
		runner:     runner,
		log:        log,
		binaryPath: path,
	}, nil
}

// Copy writes text to the X11 clipboard via xclip -selection clipboard.
// Empty text is a no-op.
func (c *X11Clipboard) Copy(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("clipboard: %w", err)
	}

	if text == "" {
		return nil
	}

	args := []string{"-selection", "clipboard"}
	if err := c.runner.Run(ctx, c.binaryPath, args, []byte(text), xclipTimeout); err != nil {
		return fmt.Errorf("clipboard: %w", err)
	}

	c.log.Debug("voice: text copied to clipboard (x11)", slog.Int("bytes", len(text)))

	return nil
}

// LookPathXclip checks if xclip is available in PATH.
func LookPathXclip() bool {
	_, err := exec.LookPath(xclipBin)

	return err == nil
}
