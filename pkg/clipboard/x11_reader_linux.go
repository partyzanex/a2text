//go:build linux

package clipboard

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
)

// X11ClipboardReader snapshots the X11 clipboard via xclip. Reuses the
// xclip binary the writer already depends on, so there is no extra
// install hint to surface in depcheck.
type X11ClipboardReader struct {
	runner     ReadRunner
	log        *slog.Logger
	binaryPath string
}

// NewX11ClipboardReader binds to the xclip binary in PATH. Returns
// ErrNoBackend when xclip is missing.
func NewX11ClipboardReader(log *slog.Logger) (*X11ClipboardReader, error) {
	return newX11ClipboardReader(execReadRunner{}, log)
}

func newX11ClipboardReader(runner ReadRunner, log *slog.Logger) (*X11ClipboardReader, error) {
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

	log.Info("voice: clipboard reader selected",
		slog.String("backend", xclipBin),
		slog.String("binary", filepath.Base(path)),
	)

	return &X11ClipboardReader{
		runner:     runner,
		log:        log,
		binaryPath: path,
	}, nil
}

// isX11MetaTarget reports whether name is an ICCCM/EWMH meta-TARGETS
// entry that describes the selection itself rather than carrying a
// payload. Skipped when picking the primary MIME type.
func isX11MetaTarget(name string) bool {
	switch name {
	case xclipTargetsTarget, "MULTIPLE", "TIMESTAMP", "SAVE_TARGETS", "DELETE",
		"INSERT_SELECTION", "INSERT_PROPERTY", "INCR", "_SAVE_TARGETS",
		"ATOM", "ATOM_PAIR":
		return true
	default:
		return false
	}
}

// Snapshot captures the primary payload from the X11 clipboard.
// Strategy mirrors the Wayland reader: xclip -t TARGETS -o lists
// supported types; the first non-meta line is the primary; one
// follow-up xclip -t <primary> -o fetches the bytes.
//
// An empty selection (xclip exits non-zero with no data) returns
// Snapshot{Empty:true} and nil error.
func (r *X11ClipboardReader) Snapshot(ctx context.Context) (Snapshot, error) {
	if r == nil || r.runner == nil || r.binaryPath == "" {
		return Snapshot{}, ErrNoBackend
	}

	if err := ctx.Err(); err != nil {
		return Snapshot{}, fmt.Errorf("clipboard: %w", err)
	}

	primary, ok, err := r.primaryTarget(ctx)
	if err != nil {
		return Snapshot{}, err
	}

	if !ok {
		return Snapshot{Empty: true}, nil
	}

	data, err := r.runner.RunCapture(ctx, r.binaryPath,
		[]string{xclipFlagSelection, xclipSelectionClipboard, xclipFlagTarget, primary, xclipFlagOutput}, xclipTimeout)
	if err != nil {
		// xclip prints to stderr and exits non-zero on empty selection;
		// treat that as Empty rather than propagating.
		if isXclipEmpty(err) {
			return Snapshot{Empty: true}, nil
		}

		return Snapshot{}, fmt.Errorf("clipboard: xclip read: %w", err)
	}

	r.log.Debug("voice: clipboard snapshot",
		slog.String("mime", primary),
		slog.Int("bytes", len(data)),
	)

	return Snapshot{MIME: primary, Data: data}, nil
}

// primaryTarget returns the first non-meta TARGETS line. ok==false +
// nil error means "clipboard is empty / has no payload target".
func (r *X11ClipboardReader) primaryTarget(ctx context.Context) (target string, found bool, err error) {
	out, err := r.runner.RunCapture(
		ctx,
		r.binaryPath,
		[]string{
			xclipFlagSelection,
			xclipSelectionClipboard,
			xclipFlagTarget,
			xclipTargetsTarget,
			xclipFlagOutput,
		},
		xclipTimeout)
	if err != nil {
		if isXclipEmpty(err) {
			return "", false, nil
		}

		return "", false, fmt.Errorf("clipboard: xclip targets: %w", err)
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if isX11MetaTarget(line) {
			continue
		}

		return line, true, nil
	}

	return "", false, nil
}

// isXclipEmpty matches xclip's "Error: target … not available" / "no
// owner" stderr lines that indicate an empty or unsupported selection.
// xclip's messages differ slightly across builds, so we look for any of
// the common substrings rather than an exact match.
func isXclipEmpty(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()

	return strings.Contains(msg, "not available") ||
		strings.Contains(msg, "no owner") ||
		strings.Contains(msg, "Error: target")
}
