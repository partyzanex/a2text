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

func (*WaylandClipboard) CopyTyped(_ context.Context, _ string, _ []byte) error {
	return ErrUnsupportedOS
}

// WaylandClipboardReader is a non-functional stub on non-Linux platforms.
type WaylandClipboardReader struct{}

func NewWaylandClipboardReader(_ *slog.Logger) (*WaylandClipboardReader, error) {
	return nil, ErrUnsupportedOS
}

func (*WaylandClipboardReader) Snapshot(_ context.Context) (Snapshot, error) {
	return Snapshot{}, ErrUnsupportedOS
}

// X11ClipboardReader is a non-functional stub on non-Linux platforms.
type X11ClipboardReader struct{}

func NewX11ClipboardReader(_ *slog.Logger) (*X11ClipboardReader, error) {
	return nil, ErrUnsupportedOS
}

func (*X11ClipboardReader) Snapshot(_ context.Context) (Snapshot, error) {
	return Snapshot{}, ErrUnsupportedOS
}
