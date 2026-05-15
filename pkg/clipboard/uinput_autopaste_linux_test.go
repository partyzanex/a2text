//go:build linux

package clipboard

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/bendahl/uinput"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKeyboard records every KeyDown/KeyUp it receives and optionally
// returns a preconfigured error from any of the five Keyboard methods.
// Method errors are matched by the first event in the recorded sequence
// (e.g. "down:LEFTCTRL") so a test can break the chord at any step.
type fakeKeyboard struct {
	mu      sync.Mutex
	events  []string
	failOn  string // event id that should return failErr; empty = never fail
	failErr error
	closed  bool
}

func (f *fakeKeyboard) KeyDown(key int) error  { return f.record("down", key) }
func (f *fakeKeyboard) KeyUp(key int) error    { return f.record("up", key) }
func (f *fakeKeyboard) KeyPress(key int) error { return f.record("press", key) }

func (f *fakeKeyboard) FetchSyspath() (string, error) {
	return "", nil
}

func (f *fakeKeyboard) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.closed = true

	return nil
}

func (f *fakeKeyboard) record(prefix string, key int) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	id := prefix + ":" + keyName(key)
	f.events = append(f.events, id)

	if f.failOn != "" && f.failOn == id {
		return f.failErr
	}

	return nil
}

func (f *fakeKeyboard) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]string, len(f.events))
	copy(out, f.events)

	return out
}

// keyName maps the small set of uinput key codes used by the autopaster to
// human-readable identifiers for assertions. Extend only when a new key is
// added to the production chord.
func keyName(key int) string {
	switch key {
	case uinput.KeyLeftctrl:
		return "LEFTCTRL"
	case uinput.KeyV:
		return "V"
	default:
		return "OTHER"
	}
}

func TestChord_SequenceIsModDownKeyDownKeyUpModUp(t *testing.T) {
	t.Parallel()

	kb := &fakeKeyboard{}

	err := chord(kb, uinput.KeyLeftctrl, uinput.KeyV, "ctrl", "v")
	require.NoError(t, err)

	assert.Equal(t, []string{
		"down:LEFTCTRL",
		"down:V",
		"up:V",
		"up:LEFTCTRL",
	}, kb.seen())
}

func TestChord_PropagatesErrorAtEveryStep(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		failOn    string
		wantInMsg string
		wantSeen  []string
	}{
		{"mod down", "down:LEFTCTRL", "ctrl down", []string{"down:LEFTCTRL"}},
		{"key down", "down:V", "v down", []string{"down:LEFTCTRL", "down:V"}},
		{"key up", "up:V", "v up", []string{"down:LEFTCTRL", "down:V", "up:V"}},
		{"mod up", "up:LEFTCTRL", "ctrl up", []string{"down:LEFTCTRL", "down:V", "up:V", "up:LEFTCTRL"}},
	}

	sentinel := errors.New("boom")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			kb := &fakeKeyboard{failOn: tc.failOn, failErr: sentinel}

			err := chord(kb, uinput.KeyLeftctrl, uinput.KeyV, "ctrl", "v")
			require.Error(t, err)
			require.ErrorIs(t, err, sentinel)
			assert.Contains(t, err.Error(), tc.wantInMsg)
			assert.Equal(t, tc.wantSeen, kb.seen())
		})
	}
}

func TestUinputAutopaster_Paste_EmitsCtrlV(t *testing.T) {
	t.Parallel()

	kb := &fakeKeyboard{}
	ua := &UinputAutopaster{kb: kb, log: slog.New(slog.DiscardHandler)}

	require.NoError(t, ua.Paste(context.Background()))

	assert.Equal(t, []string{
		"down:LEFTCTRL",
		"down:V",
		"up:V",
		"up:LEFTCTRL",
	}, kb.seen())
}

func TestUinputAutopaster_Paste_WrapsChordError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("boom")
	kb := &fakeKeyboard{failOn: "down:LEFTCTRL", failErr: sentinel}
	ua := &UinputAutopaster{kb: kb, log: slog.New(slog.DiscardHandler)}

	err := ua.Paste(context.Background())
	require.Error(t, err)
	require.ErrorIs(t, err, sentinel)
	assert.Contains(t, err.Error(), "uinput autopaste")
}

func TestUinputAutopaster_Backend_ReturnsUinput(t *testing.T) {
	t.Parallel()

	ua := &UinputAutopaster{}
	assert.Equal(t, autopasteBackendUinput, ua.Backend())
	assert.Equal(t, "uinput", AutopasteBackendUinput)
}

func TestUinputAutopaster_Close_ClosesUnderlyingKeyboard(t *testing.T) {
	t.Parallel()

	kb := &fakeKeyboard{}
	ua := &UinputAutopaster{kb: kb, log: slog.New(slog.DiscardHandler)}

	require.NoError(t, ua.Close())
	assert.True(t, kb.closed)
}

func TestUinputAutopaster_Paste_IsSerialised(t *testing.T) {
	t.Parallel()

	kb := &fakeKeyboard{}
	ua := &UinputAutopaster{kb: kb, log: slog.New(slog.DiscardHandler)}

	const concurrent = 4

	var wg sync.WaitGroup

	wg.Add(concurrent)

	for range concurrent {
		go func() {
			defer wg.Done()

			_ = ua.Paste(context.Background())
		}()
	}

	wg.Wait()

	// Each Paste records 4 events; with the mutex held, no event from one
	// Paste may interleave between events of another. So every group of 4
	// consecutive events must be a complete chord in the canonical order.
	events := kb.seen()
	require.Len(t, events, 4*concurrent)

	for i := 0; i < len(events); i += 4 {
		assert.Equal(t, []string{
			"down:LEFTCTRL",
			"down:V",
			"up:V",
			"up:LEFTCTRL",
		}, events[i:i+4], "chord at offset %d is interleaved", i)
	}
}
