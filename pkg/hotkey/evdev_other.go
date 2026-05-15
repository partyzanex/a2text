//go:build !linux

package hotkey

import (
	"context"
	"errors"
	"log/slog"
)

// ErrEvdevUnsupported is returned by NewEvdevHotkey on non-Linux targets.
// The evdev interface is a Linux kernel feature with no portable equivalent.
var ErrEvdevUnsupported = errors.New("hotkey: evdev backend requires Linux")

// EvdevHotkey is a no-op stub on non-Linux platforms. Methods exist so the
// factory wiring compiles uniformly; Listen returns ErrEvdevUnsupported.
type EvdevHotkey struct{}

// NewEvdevHotkey returns ErrEvdevUnsupported on non-Linux platforms.
func NewEvdevHotkey(_ Handler, _ string, _ []string, _ *slog.Logger) (*EvdevHotkey, error) {
	return nil, ErrEvdevUnsupported
}

// Listen always returns ErrEvdevUnsupported.
func (*EvdevHotkey) Listen(_ context.Context) error {
	return ErrEvdevUnsupported
}

// Stop is a no-op.
func (*EvdevHotkey) Stop() error { return nil }

// Ensure EvdevHotkey satisfies the Listener contract on all platforms.
var _ Listener = (*EvdevHotkey)(nil)
