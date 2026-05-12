//go:build !linux

package clipboard

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// WaylandAutopaster is a non-functional stub on non-Linux platforms — kept
// only to make the type name resolvable in cross-package references.
type WaylandAutopaster struct{}

// NewWaylandAutopaster returns ErrNoAutopasteBackend for known backends
// (auto / wtype / ydotool) and ErrUnsupportedAutopasteBackend for unknown
// ones, matching the Linux adapter contract so cross-platform code can
// errors.Is against the sentinels without build-tag gymnastics.
func NewWaylandAutopaster(backend string, _ *slog.Logger) (*WaylandAutopaster, error) {
	backend = strings.ToLower(strings.TrimSpace(backend))

	switch backend {
	case "", "auto", "wtype", "ydotool":
		return nil, ErrNoAutopasteBackend
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedAutopasteBackend, backend)
	}
}

// Paste always returns ErrNoAutopasteBackend.
func (*WaylandAutopaster) Paste(_ context.Context) error {
	return ErrNoAutopasteBackend
}

// Backend returns "" on non-Linux platforms.
func (*WaylandAutopaster) Backend() string {
	return ""
}
