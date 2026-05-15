//go:build linux

package clipboard

import (
	"bytes"
	"context"
	"errors"
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

	cmd := exec.CommandContext(deadlineCtx, wlCopyBin)
	cmd.Args = append(cmd.Args, args...)
	// WaitDelay caps how long cmd.Wait blocks for I/O goroutines after the
	// process exits.  wl-copy often forks a daemon child that inherits the
	// stderr pipe; without this guard the stderr-drain goroutine blocks
	// until the child exits (could be minutes), holding up the cycle.
	cmd.WaitDelay = copyWaitDelay
	cmd.Stdin = bytes.NewReader(stdin)

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if deadlineCtx.Err() != nil {
			return fmt.Errorf("%s timeout after %s: %w", name, timeout, deadlineCtx.Err())
		}

		// wl-copy forks a background daemon child that inherits the stderr
		// pipe write-end. When the parent exits with code 0, Go's WaitDelay
		// fires (the child keeps the pipe open), making cmd.Run() return
		// exec.ErrWaitDelay. The text is already in the clipboard at this
		// point — trust the exit code, not the I/O-drain result.
		if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 0 {
			return nil
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
	wlCopyBin = "wl-copy"
	// copyTimeout caps the total wall time for one wl-copy invocation.
	// wl-copy either exits quickly (forks daemon child) or stays alive as
	// the clipboard owner (foreground mode). Either way 3 s is generous.
	copyTimeout = 3 * time.Second
	// copyWaitDelay bounds how long cmd.Wait blocks for I/O goroutines
	// after the process exits. wl-copy often forks a daemon child that
	// inherits the stderr pipe, so the stderr-drain goroutine can block
	// indefinitely without this guard.  500 ms is enough to capture any
	// stderr that the parent emits before forking.
	copyWaitDelay = 500 * time.Millisecond
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

// CopyTyped writes raw bytes to the Wayland clipboard with an explicit
// MIME type. Used by the clipboard restore-after-paste flow to put back
// non-text payloads (image/png, text/html, …) the user had before the
// transcript replaced them.
//
// Empty data is a no-op — wl-copy with --clear would actively wipe the
// selection, which is the opposite of "restore"; skipping is correct.
func (c *WaylandClipboard) CopyTyped(ctx context.Context, mime string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("clipboard: %w", err)
	}

	if len(data) == 0 {
		return nil
	}

	if mime == "" {
		return fmt.Errorf("clipboard: %w", ErrEmptyMIME)
	}

	args := []string{wlPasteFlagType, mime}
	if err := c.runner.Run(ctx, c.binaryPath, args, data, copyTimeout); err != nil {
		return fmt.Errorf("clipboard: %w", err)
	}

	c.log.Debug("voice: typed payload restored to clipboard",
		slog.String("mime", mime), slog.Int("bytes", len(data)))

	return nil
}
