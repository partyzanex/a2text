//go:build linux

package clipboard

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// X11Autopaster sends Ctrl+V via xdotool to the focused window.
type X11Autopaster struct {
	runner     PasteRunner
	log        *slog.Logger
	binaryPath string
}

// xdotoolTimeout caps how long a single xdotool invocation can take.
// fail-hard on timeout (wrapped ctx.Err).
const xdotoolTimeout = 2 * time.Second

const (
	xdotoolKeyCmd  = "key"
	xdotoolCtrlV   = "ctrl+v"
	xdotoolBackend = "xdotool"
)

// NewX11Autopaster binds to the xdotool binary in PATH. cmd selects the
// backend: "" / "auto" / "xdotool" are accepted; any other value returns
// ErrUnsupportedAutopasteBackend (config typo, not a missing binary).
// Returns ErrNoAutopasteBackend if xdotool is not installed.
//
// Errors: fail-hard on unsupported backend or missing xdotool binary.
func NewX11Autopaster(cmd string, log *slog.Logger) (*X11Autopaster, error) {
	return newX11Autopaster(execPasteRunner{}, cmd, log)
}

func newX11Autopaster(runner PasteRunner, cmd string, log *slog.Logger) (*X11Autopaster, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	// Normalise before matching so " AUTO " and "Auto" behave identically.
	cmd = strings.ToLower(strings.TrimSpace(cmd))

	// Validate the command against what X11 actually supports.
	switch cmd {
	case "", xdotoolBackend, "auto":
		// acceptable
	default:
		return nil, fmt.Errorf("%w: %q (X11 only supports %q)", ErrUnsupportedAutopasteBackend, cmd, xdotoolBackend)
	}

	path, err := runner.LookPath(xdotoolBackend)
	if err != nil {
		return nil, ErrNoAutopasteBackend
	}

	log.Info("voice: autopaste backend selected",
		slog.String("backend", "xdotool"),
		slog.String("binary", filepath.Base(path)),
	)

	return &X11Autopaster{
		runner:     runner,
		log:        log,
		binaryPath: path,
	}, nil
}

// Paste sends Ctrl+V to the focused window via xdotool key ctrl+v.
func (a *X11Autopaster) Paste(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("autopaste: %w", err)
	}

	args := []string{xdotoolKeyCmd, xdotoolCtrlV}
	if err := a.runner.Run(ctx, a.binaryPath, args, xdotoolTimeout); err != nil {
		return fmt.Errorf("autopaste: %w", err)
	}

	a.log.Debug("voice: autopaste fired (x11)", slog.String("backend", xdotoolBackend))

	return nil
}

// Backend reports the resolved backend name.
func (a *X11Autopaster) Backend() string {
	return xdotoolBackend
}

// LookPathXdotool checks if xdotool is available.
func LookPathXdotool() bool {
	_, err := exec.LookPath(xdotoolBackend)

	return err == nil
}
