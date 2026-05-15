//go:build !linux

package clipboard

import (
	"context"
	"errors"
	"log/slog"
)

const (
	autopasteBackendUinput = "uinput"
	AutopasteBackendUinput = autopasteBackendUinput
)

// UinputAutopaster is a stub on non-Linux platforms.
type UinputAutopaster struct{}

// NewUinputAutopaster always returns an error on non-Linux platforms.
func NewUinputAutopaster(_ *slog.Logger) (*UinputAutopaster, error) {
	return nil, errors.New("uinput autopaster: not supported on this platform")
}

// Backend returns "uinput".
func (*UinputAutopaster) Backend() string { return autopasteBackendUinput }

// Close is a no-op stub.
func (*UinputAutopaster) Close() error { return nil }

// Paste always returns an error on non-Linux platforms.
func (*UinputAutopaster) Paste(_ context.Context) error {
	return errors.New("uinput autopaster: not supported on this platform")
}
