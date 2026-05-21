//go:build !linux

// Package input non-Linux stub. The /dev/uinput pipeline is
// Linux-specific; macOS and Windows builds will get their own
// concrete drivers (CGEventTap, SendInput) when the cross-platform
// port lands. Until then the constructor surfaces a typed error so
// the daemon refuses to start with a clear message rather than
// crashing on a nil pointer at the first PasteChord call.
package input

import (
	"context"
	"errors"
	"log/slog"
)

// ErrPlatformUnsupported is returned by NewUinputDriver on any
// platform other than Linux. The cross-platform shim will replace
// this once a non-Linux driver lands.
var ErrPlatformUnsupported = errors.New("input: uinput driver is Linux-only")

// UinputDriver is the no-op cross-platform placeholder. It exists
// so callers can reference the type unconditionally; every method
// returns ErrPlatformUnsupported.
type UinputDriver struct{}

// NewUinputDriver always returns ErrPlatformUnsupported on
// non-Linux builds.
func NewUinputDriver(_ *slog.Logger) (*UinputDriver, error) {
	return nil, ErrPlatformUnsupported
}

// PasteChord always returns ErrPlatformUnsupported.
func (u *UinputDriver) PasteChord(_ context.Context) (int32, error) {
	return 0, ErrPlatformUnsupported
}

// Close is a no-op.
func (u *UinputDriver) Close() error {
	return nil
}
