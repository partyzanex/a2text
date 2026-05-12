//go:build !linux

package hotkey

import (
	"context"
	"log/slog"
)

// PortalHotkey is a build-tag stub for non-Linux platforms. xdg-desktop-portal
// is a freedesktop.org spec, so the portal backend exists only on Linux. On
// other platforms the constructor returns ErrPortalUnavailable so the wiring
// layer can either degrade or surface a clear error.
type PortalHotkey struct{}

// NewPortalHotkey always returns ErrPortalUnavailable on non-Linux builds.
func NewPortalHotkey(_ Handler, _ string, _ []string, _ *slog.Logger) (*PortalHotkey, error) {
	return nil, ErrPortalUnavailable
}

// Listen is unreachable but satisfies Listener.
func (*PortalHotkey) Listen(_ context.Context) error { return ErrPortalUnavailable }

// Stop is unreachable but satisfies Listener.
func (*PortalHotkey) Stop() error { return nil }

// IsPortalAvailable always reports false on non-Linux: xdg-desktop-portal
// is a freedesktop spec, no other OS exposes it.
func IsPortalAvailable() bool { return false }

// ErrPortalUnavailable mirrors the linux-side sentinel so callers can
// errors.Is regardless of platform.
var ErrPortalUnavailable = errPortalUnavailable{}

type errPortalUnavailable struct{}

func (errPortalUnavailable) Error() string {
	return "hotkey: portal backend not available on this platform (Linux-only)"
}
