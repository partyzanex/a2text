package factory

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubTranscriber records every Transcribe call so the silence-gate
// test can assert pass-through behaviour. Returns canned reply.
type stubTranscriber struct {
	mu    sync.Mutex
	calls int
	reply string
	err   error
	paths []string
}

func (s *stubTranscriber) Transcribe(_ context.Context, audioPath, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls++
	s.paths = append(s.paths, audioPath)

	return s.reply, s.err
}

func TestSilenceGate_BelowThreshold_SkipsInner(t *testing.T) {
	t.Parallel()

	inner := &stubTranscriber{reply: "should not be reached"}
	gate := newSilenceGate(inner, -45, nil)
	gate.rmsFn = func(string) (float64, error) { return -60, nil }

	got, err := gate.Transcribe(context.Background(), "/tmp/quiet.wav", "ru")
	require.NoError(t, err)
	assert.Empty(t, got, "below-threshold RMS must return empty transcript")
	assert.Zero(t, inner.calls, "inner Transcribe must not be called when audio is silent")
}

func TestSilenceGate_AboveThreshold_DelegatesToInner(t *testing.T) {
	t.Parallel()

	inner := &stubTranscriber{reply: "привет"}
	gate := newSilenceGate(inner, -45, nil)
	gate.rmsFn = func(string) (float64, error) { return -20, nil }

	got, err := gate.Transcribe(context.Background(), "/tmp/loud.wav", "ru")
	require.NoError(t, err)
	assert.Equal(t, "привет", got)
	assert.Equal(t, 1, inner.calls)
	assert.Equal(t, []string{"/tmp/loud.wav"}, inner.paths)
}

func TestSilenceGate_ExactlyAtThreshold_DelegatesToInner(t *testing.T) {
	t.Parallel()

	// Threshold check is strict "<" so the exact boundary value should
	// pass through to STT. This guards the boundary semantics from drift
	// to "<=" in future edits.
	inner := &stubTranscriber{reply: "edge"}
	gate := newSilenceGate(inner, -45, nil)
	gate.rmsFn = func(string) (float64, error) { return -45, nil }

	got, err := gate.Transcribe(context.Background(), "/tmp/edge.wav", "ru")
	require.NoError(t, err)
	assert.Equal(t, "edge", got)
	assert.Equal(t, 1, inner.calls)
}

func TestSilenceGate_RMSError_FailsOpen(t *testing.T) {
	t.Parallel()

	// A corrupt WAV or unreadable file must NOT block transcription.
	// The gate logs and lets STT decide what to do with the audio.
	inner := &stubTranscriber{reply: "still tries"}
	gate := newSilenceGate(inner, -45, nil)
	gate.rmsFn = func(string) (float64, error) { return 0, errors.New("bad header") }

	got, err := gate.Transcribe(context.Background(), "/tmp/bad.wav", "ru")
	require.NoError(t, err)
	assert.Equal(t, "still tries", got)
	assert.Equal(t, 1, inner.calls)
}

func TestSilenceGate_InnerError_Propagated(t *testing.T) {
	t.Parallel()

	innerErr := errors.New("upstream STT down")
	inner := &stubTranscriber{err: innerErr}
	gate := newSilenceGate(inner, -45, nil)
	gate.rmsFn = func(string) (float64, error) { return -20, nil }

	_, err := gate.Transcribe(context.Background(), "/tmp/loud.wav", "ru")
	require.ErrorIs(t, err, innerErr)
}

func TestSilenceGate_NilInner_Panics(t *testing.T) {
	t.Parallel()

	assert.Panics(t, func() { _ = newSilenceGate(nil, -45, nil) })
}
