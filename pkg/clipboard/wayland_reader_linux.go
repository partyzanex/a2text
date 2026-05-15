//go:build linux

package clipboard

import (
	"bufio"
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

const (
	wlPasteBin = "wl-paste"
	// wlPasteTimeout caps a single wl-paste invocation. wl-paste exits
	// immediately after draining the selection; 3s mirrors copyTimeout
	// and survives a momentarily wedged compositor.
	wlPasteTimeout = 3 * time.Second

	wlPasteFlagListTypes = "--list-types"
	wlPasteFlagType      = "--type"
	wlPasteFlagNoNewline = "--no-newline"
)

// wlPasteEmptyMarker is the stderr substring wl-paste prints when the
// clipboard is empty (or holds no supported type). We match on a
// substring rather than full text to survive minor wording changes
// across wl-clipboard versions.
const wlPasteEmptyMarker = "No selection"

// WaylandClipboardReader snapshots the Wayland clipboard via wl-paste.
// It is the read-side complement of WaylandClipboard.
type WaylandClipboardReader struct {
	runner     ReadRunner
	log        *slog.Logger
	binaryPath string
}

// NewWaylandClipboardReader binds to the wl-paste binary in PATH.
// Returns ErrNoBackend when wl-paste is missing.
func NewWaylandClipboardReader(log *slog.Logger) (*WaylandClipboardReader, error) {
	return newWaylandClipboardReader(execReadRunner{}, log)
}

func newWaylandClipboardReader(runner ReadRunner, log *slog.Logger) (*WaylandClipboardReader, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	if runner == nil {
		return nil, ErrNoBackend
	}

	path, err := runner.LookPath(wlPasteBin)
	if err != nil {
		return nil, ErrNoBackend
	}

	log.Info("voice: clipboard reader selected",
		slog.String("backend", wlPasteBin),
		slog.String("binary", filepath.Base(path)),
	)

	return &WaylandClipboardReader{
		runner:     runner,
		log:        log,
		binaryPath: path,
	}, nil
}

// Snapshot captures the primary MIME-typed payload from the Wayland
// clipboard. Strategy:
//  1. wl-paste --list-types — order matters; first non-empty line is
//     the source's preferred type.
//  2. wl-paste --type <primary> --no-newline — fetch raw bytes.
//
// An empty selection (or one with no supported types) returns
// Snapshot{Empty:true} with nil error so callers branch cleanly.
func (r *WaylandClipboardReader) Snapshot(ctx context.Context) (Snapshot, error) {
	if r == nil || r.runner == nil || r.binaryPath == "" {
		return Snapshot{}, ErrNoBackend
	}

	if err := ctx.Err(); err != nil {
		return Snapshot{}, fmt.Errorf("clipboard: %w", err)
	}

	primary, err := r.primaryType(ctx)
	if err != nil {
		return Snapshot{}, err
	}

	if primary == "" {
		return Snapshot{Empty: true}, nil
	}

	data, err := r.runner.RunCapture(ctx, r.binaryPath,
		[]string{wlPasteFlagType, primary, wlPasteFlagNoNewline}, wlPasteTimeout)
	if err != nil {
		if isWlPasteEmpty(err) {
			return Snapshot{Empty: true}, nil
		}

		return Snapshot{}, fmt.Errorf("clipboard: wl-paste type: %w", err)
	}

	r.log.Debug("voice: clipboard snapshot",
		slog.String("mime", primary),
		slog.Int("bytes", len(data)),
	)

	return Snapshot{MIME: primary, Data: data}, nil
}

// primaryType invokes wl-paste --list-types and returns the first
// non-empty type line. Empty clipboard => "" with nil error.
func (r *WaylandClipboardReader) primaryType(ctx context.Context) (string, error) {
	out, err := r.runner.RunCapture(ctx, r.binaryPath,
		[]string{wlPasteFlagListTypes}, wlPasteTimeout)
	if err != nil {
		if isWlPasteEmpty(err) {
			return "", nil
		}

		return "", fmt.Errorf("clipboard: wl-paste list-types: %w", err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			return line, nil
		}
	}

	return "", nil
}

// isWlPasteEmpty checks whether err carries wl-paste's "No selection"
// signal. wl-paste exits non-zero with a stderr line containing the
// marker when the clipboard is empty.
func isWlPasteEmpty(err error) bool {
	if err == nil {
		return false
	}

	return strings.Contains(err.Error(), wlPasteEmptyMarker)
}

// execReadRunner is the production ReadRunner backing both Wayland and
// X11 readers. Lives in this file because it is linux-only — both
// readers depend on it and there is no portable alternative.
type execReadRunner struct{}

func (execReadRunner) LookPath(name string) (string, error) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("clipboard read: %w", err)
	}

	return p, nil
}

const readRunnerStderrTruncLen = 200

// isAllowedReadBinary gates RunCapture to known read tools. Mirrors the
// allowlist pattern from execCopyRunner / execPasteRunner — a function
// (rather than a map global) keeps the package free of mutable globals.
func isAllowedReadBinary(bin string) bool {
	switch bin {
	case wlPasteBin, xclipBin:
		return true
	default:
		return false
	}
}

func (execReadRunner) RunCapture(
	ctx context.Context, name string, args []string, timeout time.Duration,
) ([]byte, error) {
	if timeout <= 0 {
		return nil, errors.New("execReadRunner: read timeout must be positive")
	}

	bin := filepath.Base(name)
	if !isAllowedReadBinary(bin) {
		return nil, fmt.Errorf("clipboard read: command not allowed: %s", bin)
	}

	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("clipboard read: %w", err)
	}

	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(deadlineCtx, name)
	cmd.Args = append(cmd.Args, args...)

	var (
		stdout bytes.Buffer
		stderr bytes.Buffer
	)

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if deadlineCtx.Err() != nil {
			return nil, fmt.Errorf("%s timeout after %s: %w", bin, timeout, deadlineCtx.Err())
		}

		tail := strings.TrimSpace(stderr.String())
		if tail != "" {
			return nil, fmt.Errorf("%s: %w (stderr: %s)",
				bin, err, truncate(tail, readRunnerStderrTruncLen))
		}

		return nil, fmt.Errorf("%s: %w", bin, err)
	}

	return stdout.Bytes(), nil
}
