package factory

import (
	"context"
	"errors"
	"fmt"

	"github.com/partyzanex/a2text/internal/usecases/transcribe"
)

// LazyErrorTranscriber is a fail-soft placeholder transcribe.Transcriber.
// Construction errors from BuildTranscriber (missing whisper-cpp model,
// unparseable URL, network down at probe time, …) used to abort daemon
// startup. The settings window is the only UI the user has — refusing
// to boot strands them on the CLI with no way to fix the config. This
// stub lets the daemon start, surface the error in the tray's error
// state on every transcribe attempt, and most importantly keep the
// settings window reachable.
//
// All four Transcriber methods return the same wrapped error; Close
// reports success because there is nothing to release.
type LazyErrorTranscriber struct {
	cause error
}

// NewLazyErrorTranscriber wraps an STT construction error so it can be
// passed where a real Transcriber is expected. Production wiring uses
// this from BuildTranscriber's error path; tests can construct one
// directly to assert error-propagation behaviour.
func NewLazyErrorTranscriber(cause error) *LazyErrorTranscriber {
	if cause == nil {
		cause = errors.New("transcriber not configured")
	}

	return &LazyErrorTranscriber{cause: cause}
}

// Cause returns the underlying construction failure. Useful in logs
// that want to attribute every transcribe error back to the original
// startup-time issue rather than printing the wrapped message.
func (l *LazyErrorTranscriber) Cause() error {
	return l.cause
}

// Transcribe always returns the construction error wrapped with phase
// context so the daemon's CycleError mapping classifies it as a
// transcription failure (not a record or deliver issue).
func (l *LazyErrorTranscriber) Transcribe(_ context.Context, _, _ string) (string, error) {
	return "", fmt.Errorf("transcribe: %w", l.cause)
}

// LoadModel returns the wrapped error — the user has not finished
// configuring STT yet, so loading a model can only fail.
func (l *LazyErrorTranscriber) LoadModel(_ string) error {
	return fmt.Errorf("load model: %w", l.cause)
}

// ReloadModel mirrors LoadModel: the lazy stub never holds a model to
// hot-swap, so any reload is a no-op-with-error.
func (l *LazyErrorTranscriber) ReloadModel(_ string) error {
	return fmt.Errorf("reload model: %w", l.cause)
}

// DetectLanguage cannot work without a real backend; surface the
// construction error so callers fall back to the configured language.
func (l *LazyErrorTranscriber) DetectLanguage(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf("detect language: %w", l.cause)
}

// Close is a no-op success. The stub holds no resources; reporting an
// error here would only complicate the daemon's shutdown path without
// surfacing anything actionable.
func (l *LazyErrorTranscriber) Close() error {
	return nil
}

// Static type assertion so refactors of the Transcriber interface
// surface here at build time rather than as a wiring bug at runtime.
var _ transcribe.Transcriber = (*LazyErrorTranscriber)(nil)
