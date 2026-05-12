//go:build !linux

package clipboard

import (
	"context"
	"errors"
	"log/slog"
)

// ErrUnsupportedOS is returned by NewWaylandClipboard on non-Linux. The
// Wayland clipboard utilities ship Linux-only; cross-platform support
// belongs in a future stage.
var ErrUnsupportedOS = errors.New("clipboard: Wayland backend is Linux-only")

// WaylandClipboard is a non-functional stub on non-Linux platforms — it
// exists only to keep cross-package references buildable.
type WaylandClipboard struct{}

func NewWaylandClipboard(_ *slog.Logger) (*WaylandClipboard, error) {
	return nil, ErrUnsupportedOS
}

func (*WaylandClipboard) Copy(_ context.Context, _ string) error {
	return ErrUnsupportedOS
}
