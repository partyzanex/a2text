//go:build !linux

package capture

import (
	"context"
	"errors"
	"log/slog"
)

// ErrUnsupportedOS is returned by NewSubprocessRecorder on non-Linux
// platforms. The voice CLI is Linux-first; this stub exists only so that
// production code referring to *SubprocessRecorder remains buildable on
// darwin/windows for editor tooling and cross-package tests. There is no
// supported way to obtain a working *SubprocessRecorder here.
var ErrUnsupportedOS = errors.New("microphone capture is only implemented on Linux")

// SubprocessRecorder is a non-functional stub on non-Linux platforms — kept
// only to make the type-name resolvable in cross-package references. Methods
// are reachable only through manually-constructed values; the public
// constructor refuses to produce one.
type SubprocessRecorder struct{}

// NewSubprocessRecorder returns ErrUnsupportedOS on non-Linux platforms.
func NewSubprocessRecorder(_ *slog.Logger) (*SubprocessRecorder, error) {
	return nil, ErrUnsupportedOS
}

// RecordToFile always returns ErrUnsupportedOS. Present so the type still
// satisfies Recorder and the cross-platform build does not break on
// interface assertions.
func (*SubprocessRecorder) RecordToFile(_ context.Context, _ Options) (string, error) {
	return "", ErrUnsupportedOS
}

// Backend returns an empty Backend on non-Linux platforms. Present for
// symmetry with the linux build so that callers can probe Backend()
// without a build-tag dance.
func (*SubprocessRecorder) Backend() Backend {
	return ""
}

// Compile-time interface check.
var _ Recorder = (*SubprocessRecorder)(nil)
