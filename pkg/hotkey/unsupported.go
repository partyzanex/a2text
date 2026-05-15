package hotkey

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// ErrX11HotkeyUnavailable is returned by NewX11Hotkey and Listen on platforms
// that were not built with the linux+x11 tags. Callers can use errors.Is to
// distinguish this from other failures.
var ErrX11HotkeyUnavailable = errors.New("hotkey: X11 hotkey not available")

// X11Hotkey is a compile-time stub for platforms that do not support X11
// hotkey registration (non-linux, or built without the x11 tag).
type X11Hotkey struct{}

// NewX11Hotkey returns ErrX11HotkeyUnavailable on unsupported platforms.
func NewX11Hotkey(_ Handler, _ string, _ uint, _ *slog.Logger) (*X11Hotkey, error) {
	return nil, fmt.Errorf("%w: requires linux + x11 build tag + libX11-dev", ErrX11HotkeyUnavailable)
}

// Listen is a no-op on unsupported platforms.
func (*X11Hotkey) Listen(_ context.Context) error {
	return fmt.Errorf("hotkey: listen: %w", ErrX11HotkeyUnavailable)
}

// Stop is a no-op on unsupported platforms.
func (*X11Hotkey) Stop() error {
	return nil
}

// Modifier constants (zero values on unsupported platforms).
const (
	ModShift   = 0
	ModLock    = 0
	ModControl = 0
	Mod1       = 0
	Mod2       = 0
	Mod3       = 0
	Mod4       = 0
	Mod5       = 0
)
