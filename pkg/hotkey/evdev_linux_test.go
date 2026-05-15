//go:build linux

package hotkey

import (
	"context"
	"encoding/binary"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bendahl/uinput"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makePipe returns a read/write pair of *os.File for feeding synthetic
// input_event packets to readDeviceLoop. The test must close the writer
// to terminate the reader; t.Cleanup closes the reader as a safety net.
func makePipe(t *testing.T) (*os.File, *os.File, error) {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}

	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})

	return r, w, nil
}

// assertEventWithin asserts that the channel delivers `want` within timeout.
// Spelled out (no testify channel helper) to keep the failure message clear:
// "expected Press within 1s, got nothing" is more useful than a generic
// assert.Equal failure on a zero-value Event.
func assertEventWithin(t *testing.T, ch <-chan Event, want Event, timeout time.Duration) {
	t.Helper()

	select {
	case got := <-ch:
		assert.Equal(t, want, got)
	case <-time.After(timeout):
		t.Fatalf("expected event %v within %s, got nothing", want, timeout)
	}
}

// makeEvent packs (type, code, value) into the on-wire 24-byte input_event
// layout. timeval is left zeroed — the production reader ignores it.
func makeEvent(t *testing.T, evType, code uint16, value int32) []byte {
	t.Helper()

	buf := make([]byte, inputEventSize)
	binary.LittleEndian.PutUint16(buf[inputEventTypeOffset:], evType)
	binary.LittleEndian.PutUint16(buf[inputEventCodeOffset:], code)
	binary.LittleEndian.PutUint32(buf[inputEventValueOffset:], uint32(value))

	return buf
}

func TestNewEvdevHotkey_RejectsEmptyKey(t *testing.T) {
	t.Parallel()

	_, err := NewEvdevHotkey(func(context.Context, Event) {}, "", nil, nil)
	require.ErrorIs(t, err, ErrKeyEmpty)
}

func TestNewEvdevHotkey_RejectsNilHandler(t *testing.T) {
	t.Parallel()

	_, err := NewEvdevHotkey(nil, "F4", nil, nil)
	require.ErrorIs(t, err, ErrHandlerNil)
}

func TestNewEvdevHotkey_RejectsUnknownKey(t *testing.T) {
	t.Parallel()

	_, err := NewEvdevHotkey(func(context.Context, Event) {}, "BLAH", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported key")
}

func TestNewEvdevHotkey_RejectsUnknownModifier(t *testing.T) {
	t.Parallel()

	_, err := NewEvdevHotkey(func(context.Context, Event) {}, "F4", []string{"hyper"}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown modifier")
}

func TestNewEvdevHotkey_NormalisesModifierAliases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		alias    string
		wantKey1 uint16
		wantKey2 uint16
	}{
		{"control", uint16(uinput.KeyLeftctrl), uint16(uinput.KeyRightctrl)},
		{"mod1", uint16(uinput.KeyLeftalt), uint16(uinput.KeyRightalt)},
		{"mod4", uint16(uinput.KeyLeftmeta), uint16(uinput.KeyRightmeta)},
		{"win", uint16(uinput.KeyLeftmeta), uint16(uinput.KeyRightmeta)},
	}

	for _, tc := range cases {
		t.Run(tc.alias, func(t *testing.T) {
			t.Parallel()

			hk, err := NewEvdevHotkey(func(context.Context, Event) {}, "F4", []string{tc.alias}, nil)
			require.NoError(t, err)
			require.Len(t, hk.modGroups, 1)
			assert.ElementsMatch(t, []uint16{tc.wantKey1, tc.wantKey2}, hk.modGroups[0])
		})
	}
}

func TestHandleKey_PressReleaseDispatchedInOrder(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		events []Event
	)

	handler := func(_ context.Context, evt Event) {
		mu.Lock()
		defer mu.Unlock()

		events = append(events, evt)
	}

	hk, err := NewEvdevHotkey(handler, "F4", nil, nil)
	require.NoError(t, err)

	hk.handleKey(context.Background(), uint16(uinput.KeyF4), keyValuePress)
	hk.handleKey(context.Background(), uint16(uinput.KeyF4), keyValueRelease)

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, []Event{Press, Release}, events)
}

func TestHandleKey_RepeatsIgnored(t *testing.T) {
	t.Parallel()

	var count atomic.Int32

	hk, err := NewEvdevHotkey(func(context.Context, Event) { count.Add(1) }, "F4", nil, nil)
	require.NoError(t, err)

	hk.handleKey(context.Background(), uint16(uinput.KeyF4), keyValuePress)
	hk.handleKey(context.Background(), uint16(uinput.KeyF4), keyValueRepeat)
	hk.handleKey(context.Background(), uint16(uinput.KeyF4), keyValueRepeat)
	hk.handleKey(context.Background(), uint16(uinput.KeyF4), keyValueRelease)

	assert.EqualValues(t, 2, count.Load(), "repeat events must not dispatch")
}

func TestHandleKey_DuplicatePressFromMultipleDevicesDeduplicated(t *testing.T) {
	t.Parallel()

	var count atomic.Int32

	hk, err := NewEvdevHotkey(func(context.Context, Event) { count.Add(1) }, "F4", nil, nil)
	require.NoError(t, err)

	// Two devices both reporting Press without an intervening Release —
	// can happen with overlapping uinput / real keyboard. Must dispatch once.
	hk.handleKey(context.Background(), uint16(uinput.KeyF4), keyValuePress)
	hk.handleKey(context.Background(), uint16(uinput.KeyF4), keyValuePress)
	hk.handleKey(context.Background(), uint16(uinput.KeyF4), keyValueRelease)
	hk.handleKey(context.Background(), uint16(uinput.KeyF4), keyValueRelease)

	assert.EqualValues(t, 2, count.Load(), "duplicate edges must collapse")
}

func TestHandleKey_ModifierRequiredButNotHeld_NoDispatch(t *testing.T) {
	t.Parallel()

	var count atomic.Int32

	hk, err := NewEvdevHotkey(
		func(context.Context, Event) { count.Add(1) },
		"F4", []string{"ctrl"}, nil,
	)
	require.NoError(t, err)

	hk.handleKey(context.Background(), uint16(uinput.KeyF4), keyValuePress)
	hk.handleKey(context.Background(), uint16(uinput.KeyF4), keyValueRelease)

	assert.EqualValues(t, 0, count.Load(),
		"main-key edge without held modifier must not dispatch")
}

func TestHandleKey_ModifierHeld_DispatchesPressAndRelease(t *testing.T) {
	t.Parallel()

	var (
		mu     sync.Mutex
		events []Event
	)

	hk, err := NewEvdevHotkey(
		func(_ context.Context, evt Event) {
			mu.Lock()
			defer mu.Unlock()

			events = append(events, evt)
		},
		"F4", []string{"ctrl"}, nil,
	)
	require.NoError(t, err)

	// LeftCtrl satisfies the "ctrl" group; RightCtrl would also work.
	hk.handleKey(context.Background(), uint16(uinput.KeyLeftctrl), keyValuePress)
	hk.handleKey(context.Background(), uint16(uinput.KeyF4), keyValuePress)
	hk.handleKey(context.Background(), uint16(uinput.KeyF4), keyValueRelease)
	hk.handleKey(context.Background(), uint16(uinput.KeyLeftctrl), keyValueRelease)

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, []Event{Press, Release}, events)
}

func TestHandleKey_ReleaseAfterModifierGone_StillDispatches(t *testing.T) {
	t.Parallel()

	// Real-world: user presses Ctrl+F4, releases Ctrl FIRST, then F4. The
	// daemon must still see Release so hold-mode does not strand in recording.

	var (
		mu     sync.Mutex
		events []Event
	)

	hk, err := NewEvdevHotkey(
		func(_ context.Context, evt Event) {
			mu.Lock()
			defer mu.Unlock()

			events = append(events, evt)
		},
		"F4", []string{"ctrl"}, nil,
	)
	require.NoError(t, err)

	hk.handleKey(context.Background(), uint16(uinput.KeyLeftctrl), keyValuePress)
	hk.handleKey(context.Background(), uint16(uinput.KeyF4), keyValuePress)
	hk.handleKey(context.Background(), uint16(uinput.KeyLeftctrl), keyValueRelease)
	hk.handleKey(context.Background(), uint16(uinput.KeyF4), keyValueRelease)

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, []Event{Press, Release}, events)
}

func TestReadDeviceLoop_DecodesEventsAndDispatches(t *testing.T) {
	t.Parallel()

	// End-to-end on the parser path: pipe synthetic input_event bytes
	// through readDeviceLoop and observe handler dispatch.

	r, w, err := makePipe(t)
	require.NoError(t, err)

	pressCh := make(chan Event, 4)

	hk, err := NewEvdevHotkey(
		func(_ context.Context, evt Event) { pressCh <- evt },
		"F4", nil, nil,
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	wg.Add(1)

	go hk.readDeviceLoop(ctx, r, &wg)

	// Press and release F4 via the wire format.
	_, err = w.Write(makeEvent(t, 1 /* EV_KEY */, uint16(uinput.KeyF4), keyValuePress))
	require.NoError(t, err)
	_, err = w.Write(makeEvent(t, 1, uint16(uinput.KeyF4), keyValueRelease))
	require.NoError(t, err)

	assertEventWithin(t, pressCh, Press, time.Second)
	assertEventWithin(t, pressCh, Release, time.Second)

	require.NoError(t, w.Close())
	cancel()
	wg.Wait()
}

func TestReadDeviceLoop_IgnoresNonKeyEvents(t *testing.T) {
	t.Parallel()

	r, w, err := makePipe(t)
	require.NoError(t, err)

	var count atomic.Int32

	hk, err := NewEvdevHotkey(
		func(context.Context, Event) { count.Add(1) },
		"F4", nil, nil,
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	wg.Add(1)

	go hk.readDeviceLoop(ctx, r, &wg)

	// EV_SYN (0), EV_REL (2), EV_ABS (3) — none should reach the handler.
	for _, evType := range []uint16{0, 2, 3} {
		_, err := w.Write(makeEvent(t, evType, uint16(uinput.KeyF4), keyValuePress))
		require.NoError(t, err)
	}

	// Give the reader a beat to process — synchronous on the goroutine.
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, w.Close())
	cancel()
	wg.Wait()

	assert.EqualValues(t, 0, count.Load())
}

func TestEvdevHotkey_Stop_Idempotent(t *testing.T) {
	t.Parallel()

	hk, err := NewEvdevHotkey(func(context.Context, Event) {}, "F4", nil, nil)
	require.NoError(t, err)

	require.NoError(t, hk.Stop())
	require.NoError(t, hk.Stop())
}
