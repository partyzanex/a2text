//go:build !linux || !x11

package factory

import (
	"errors"
	"log/slog"

	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/usecases/voice"
)

// errX11BackendUnavailable is returned by buildX11Hotkey when this binary
// was compiled without the `x11` build tag (or for a non-Linux target).
// Hotkey factory wraps it with backend-selection context: when the user
// explicitly asked for `backend: x11` this propagates and fails daemon
// startup; under `backend: auto` the factory swallows it with a warn.
var errX11BackendUnavailable = errors.New("cmd: X11 hotkey backend not in this build (rebuild with -tags=x11)")

// buildX11Hotkey is the no-CGo stub. Returns errX11BackendUnavailable so
// the caller can branch on backend-vs-build mismatch.
//
//nolint:ireturn // matches the x11-tagged variant
func buildX11Hotkey(_ *config.VoiceConfig, _ *slog.Logger, _ voice.Handler) (voice.HotkeyListener, error) {
	return nil, errX11BackendUnavailable
}

// parseHotkeyModifiers is intentionally NOT exported on this build path:
// the portal backend has its own modifier vocabulary (formatAccelerator
// inside the adapter), and the X11-bitmask helper would be dead code on
// the Wayland build. Keep the shape symmetric so x11-tag and no-tag
// builds present the same exported surface to wiring.
