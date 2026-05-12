//go:build linux

package clipboard

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const maxStderrTruncLen = 200

type execCopyRunner struct{}

func (execCopyRunner) LookPath(name string) (string, error) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("wayland: %w", err)
	}

	return p, nil
}

func (execCopyRunner) Run(
	ctx context.Context, name string, args []string, stdin []byte, timeout time.Duration,
) error {
	// Validate that the binary is the expected wl-copy binary to prevent command injection.
	if filepath.Base(name) != wlCopyBin {
		return fmt.Errorf("wayland: command not allowed: %s", filepath.Base(name))
	}

	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Safe: binary is validated above to be exactly wl-copy, args are caller-controlled and safe.
	cmd := exec.CommandContext(deadlineCtx, name, args...) //nolint:gosec // binary allowlisted
	cmd.Stdin = bytes.NewReader(stdin)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if deadlineCtx.Err() != nil {
			return fmt.Errorf("%s timeout after %s: %w", name, timeout, deadlineCtx.Err())
		}

		tail := strings.TrimSpace(stderr.String())
		if tail != "" {
			return fmt.Errorf("%s: %w (stderr: %s)", name, err, truncate(tail, maxStderrTruncLen))
		}

		return fmt.Errorf("%s: %w", name, err)
	}

	return nil
}

// truncate caps text at maxLen runes and appends a marker.
// Extracted to internal helper if reused across adapters.
func truncate(text string, maxLen int) string {
	if maxLen <= 0 {
		return "...(truncated)"
	}

	runes := []rune(text)
	if len(runes) <= maxLen {
		return text
	}

	return string(runes[:maxLen]) + "...(truncated)"
}

// WaylandClipboard pipes text to wl-copy via stdin.
type WaylandClipboard struct {
	runner     CopyRunner
	log        *slog.Logger
	binaryPath string
}

const (
	wlCopyBin   = "wl-copy"
	copyTimeout = 3 * time.Second // fail-hard on timeout (wrapped ctx.Err)
)

// NewWaylandClipboard binds to the wl-copy binary in PATH.
//
// Errors: fail-hard on missing wl-copy binary (ErrNoBackend).
func NewWaylandClipboard(log *slog.Logger) (*WaylandClipboard, error) {
	return newWaylandClipboard(execCopyRunner{}, log)
}

func newWaylandClipboard(runner CopyRunner, log *slog.Logger) (*WaylandClipboard, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	if runner == nil {
		return nil, ErrNoBackend
	}

	path, err := runner.LookPath(wlCopyBin)
	if err != nil {
		return nil, ErrNoBackend
	}

	log.Info("voice: clipboard backend selected",
		slog.String("backend", "wl-copy"),
		slog.String("binary", filepath.Base(path)),
	)

	return &WaylandClipboard{
		runner:     runner,
		log:        log,
		binaryPath: path,
	}, nil
}

// Copy writes text to the Wayland clipboard via wl-copy.
//
// Errors: fail-hard on context cancellation (returns ctx.Err wrapped);
// runtime errors from wl-copy are wrapped and returned.
// Order of checks: ctx cancellation → empty-text short-circuit.
func (c *WaylandClipboard) Copy(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("clipboard: %w", err)
	}

	if text == "" {
		return nil
	}

	if err := c.runner.Run(ctx, c.binaryPath, nil, []byte(text), copyTimeout); err != nil {
		return fmt.Errorf("clipboard: %w", err)
	}

	c.log.Debug("voice: text copied to clipboard", slog.Int("bytes", len(text)))

	return nil
}
