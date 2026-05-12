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

const maxAutoPasteStderrLen = 200

// Supported autopaste backends. autopasteBackendAuto selects the first
// available binary at construction time (wtype > ydotool).
const (
	autopasteBackendAuto    = "auto"
	autopasteBackendWtype   = "wtype"
	autopasteBackendYdotool = "ydotool"
)

// WaylandAutopaster sends a Ctrl+V keystroke via wtype or ydotool to the
// currently focused application, simulating a paste of whatever the
// system clipboard holds. Use AFTER copying text to the clipboard.
//
// wtype is the preferred backend — it is a small, modern tool that
// speaks the Wayland input protocol directly and needs no privileged
// helper. ydotool covers environments where wtype is not packaged but
// requires ydotoold + /dev/uinput permissions; we treat it as a fallback.
type WaylandAutopaster struct {
	runner     PasteRunner
	log        *slog.Logger
	backend    string // resolved binary name (wtype/ydotool)
	binaryPath string
}

type execPasteRunner struct{}

func (execPasteRunner) LookPath(name string) (string, error) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("autopaste: %w", err)
	}

	return p, nil
}

func (execPasteRunner) Run(
	ctx context.Context, name string, args []string, timeout time.Duration,
) error {
	if timeout <= 0 {
		return errors.New("execPasteRunner: paste timeout must be positive")
	}

	// Allowlist permitted autopaste binaries to prevent command injection.
	bin := filepath.Base(name)
	if bin != autopasteBackendWtype && bin != autopasteBackendYdotool {
		return fmt.Errorf("autopaste: command not allowed: %s", bin)
	}

	// Short-circuit before allocating a timeout context: if the caller is
	// already cancelled there is no reason to fork a process we'd just kill.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("autopaste: %w", err)
	}

	deadlineCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Safe: binary is allowlisted above (wtype or ydotool), args are caller-controlled.
	cmd := exec.CommandContext(deadlineCtx, name, args...) //nolint:gosec // binary allowlisted

	var stderr bytes.Buffer

	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if deadlineCtx.Err() != nil {
			return fmt.Errorf("%s timeout after %s: %w", bin, timeout, deadlineCtx.Err())
		}

		tail := strings.TrimSpace(stderr.String())
		if tail != "" {
			return fmt.Errorf("%s: %w (stderr: %s)", bin, err, truncate(tail, maxAutoPasteStderrLen))
		}

		return fmt.Errorf("%s: %w", bin, err)
	}

	return nil
}

// pasteTimeout caps how long a single Ctrl+V invocation may take. wtype
// and ydotool both return sub-millisecond on a healthy system; 2s is
// generous and protects against a wedged ydotoold.
const pasteTimeout = 2 * time.Second

// NewWaylandAutopaster picks an autopaste backend.
//
// backendName values:
//   - ""/"auto": prefer wtype, fall back to ydotool.
//   - "wtype" / "ydotool": force a specific binary; ErrNoAutopasteBackend
//     if the requested one is missing.
//
// Returns ErrNoAutopasteBackend if no candidate is in PATH.
func NewWaylandAutopaster(backendName string, log *slog.Logger) (*WaylandAutopaster, error) {
	return newWaylandAutopaster(execPasteRunner{}, backendName, log)
}

func newWaylandAutopaster(runner PasteRunner, backendName string, log *slog.Logger) (*WaylandAutopaster, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	if runner == nil {
		return nil, ErrNoAutopasteBackend
	}

	backend, path, err := resolveAutopasteBackend(runner, backendName)
	if err != nil {
		return nil, err
	}

	log.Info("voice: autopaste backend selected",
		slog.String("backend", backend),
		slog.String("binary", filepath.Base(path)),
	)

	return &WaylandAutopaster{
		runner:     runner,
		log:        log,
		backend:    backend,
		binaryPath: path,
	}, nil
}

// resolveAutopasteBackend implements the backendName selection rules in
// one place so NewWaylandAutopaster stays linear. Returns the resolved
// backend name (always "wtype" or "ydotool" on success) and its full path.
//
// Input is normalised before matching: whitespace trimmed, case folded.
// A user writing `autopaste_command: " WTYPE "` in yaml must get the
// same behaviour as `autopaste_command: wtype` — both come from the same
// human intent.
//
// runner must not be nil; returns ErrNoAutopasteBackend immediately if it is.
func resolveAutopasteBackend(runner PasteRunner, backendName string) (backend, path string, err error) {
	if runner == nil {
		return "", "", ErrNoAutopasteBackend
	}

	backendName = strings.ToLower(strings.TrimSpace(backendName))

	// Normalise: empty or "auto" means probe in preference order.
	if backendName == "" {
		backendName = autopasteBackendAuto
	}

	if backendName == autopasteBackendAuto {
		for _, candidate := range []string{autopasteBackendWtype, autopasteBackendYdotool} {
			if candidatePath, lookErr := runner.LookPath(candidate); lookErr == nil {
				return candidate, candidatePath, nil
			}
		}

		return "", "", ErrNoAutopasteBackend
	}

	// Explicit backend request — only the known names are valid. This is a
	// config error, not a missing dependency, so a distinct sentinel.
	if backendName != autopasteBackendWtype && backendName != autopasteBackendYdotool {
		return "", "", fmt.Errorf("%w: %q", ErrUnsupportedAutopasteBackend, backendName)
	}

	path, err = runner.LookPath(backendName)
	if err != nil {
		return "", "", fmt.Errorf("%w: %s", ErrNoAutopasteBackend, backendName)
	}

	return backendName, path, nil
}

// Paste sends Ctrl+V to the focused window via the selected backend.
// Nil-safe: returns ErrNoAutopasteBackend if the receiver was never
// properly initialised, or ErrUnsupportedAutopasteBackend if a caller
// hand-built the struct with an unknown backend string.
//
// The backend field is NOT normalised at Paste time — the constructor
// (NewWaylandAutopaster / newWaylandAutopaster) is the only place where
// whitespace and case folding happen. A hand-built struct with " WTYPE "
// will fail with ErrUnsupportedAutopasteBackend; use the constructor.
func (a *WaylandAutopaster) Paste(ctx context.Context) error {
	if a == nil || a.runner == nil || a.binaryPath == "" {
		return ErrNoAutopasteBackend
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("autopaste: %w", err)
	}

	args, err := pasteArgs(a.backend)
	if err != nil {
		return fmt.Errorf("autopaste: %w", err)
	}

	if err := a.runner.Run(ctx, a.binaryPath, args, pasteTimeout); err != nil {
		return fmt.Errorf("autopaste: %w", err)
	}

	a.logger().Debug("voice: autopaste fired", slog.String("backend", a.backend))

	return nil
}

// Backend reports the resolved backend name ("wtype" / "ydotool"). The value
// is always normalised when the struct was produced by the constructor; a
// hand-built struct returns whatever was stored in the field verbatim.
func (a *WaylandAutopaster) Backend() string {
	if a == nil {
		return ""
	}

	return a.backend
}

// wtypeCtrlModifier is the wtype modifier argument for "Left Ctrl" — used
// both to press and release the modifier around the 'v' keystroke.
const wtypeCtrlModifier = "ctrl"

// ydotoolKeyCmd is the ydotool subcommand for sending key events.
const ydotoolKeyCmd = "key"

// pasteArgs returns the per-backend argument vector to simulate Ctrl+V.
//
//   - wtype: "-M ctrl v -m ctrl" — press LeftCtrl, type 'v', release LeftCtrl.
//     wtype interprets a single character argument as a key to type while
//     the modifier set by -M is active.
//
//   - ydotool: "key 29:1 47:1 47:0 29:0" — raw Linux input event codes.
//     29 = KEY_LEFTCTRL, 47 = KEY_V; trailing :1 is "down", :0 is "up".
//     Documented in linux/input-event-codes.h; ydotool key reference confirms.
//
// The default branch returns ErrUnsupportedAutopasteBackend instead of
// silently producing empty args. A hand-built `&WaylandAutopaster{backend:
// "bad"}` would otherwise spawn the wrong binary with no flags and either
// hang or paste nothing — both worse than a clear error at Paste time.
func pasteArgs(backend string) ([]string, error) {
	switch backend {
	case autopasteBackendWtype:
		return []string{"-M", wtypeCtrlModifier, "v", "-m", wtypeCtrlModifier}, nil
	case autopasteBackendYdotool:
		return []string{ydotoolKeyCmd, "29:1", "47:1", "47:0", "29:0"}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedAutopasteBackend, backend)
	}
}

// logger returns a non-nil *slog.Logger even if the field was never set.
func (a *WaylandAutopaster) logger() *slog.Logger {
	if a != nil && a.log != nil {
		return a.log
	}

	return slog.New(slog.DiscardHandler)
}
