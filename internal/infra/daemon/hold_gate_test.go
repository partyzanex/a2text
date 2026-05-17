package daemon

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHoldGate_ShortTap_DropsDispatch covers the regression that motivated
// the gate: Press followed by a Release inside the min-hold window must not
// invoke the deferred dispatch and must report "don't dispatch Stop".
func TestHoldGate_ShortTap_DropsDispatch(t *testing.T) {
	t.Parallel()

	var gate holdGate

	var fired atomic.Int32

	gate.OnPress(50*time.Millisecond, func() { fired.Add(1) })

	// Release well inside the window.
	time.Sleep(5 * time.Millisecond)

	got := gate.OnRelease()
	assert.False(t, got, "short tap must return false (drop Stop)")

	// Wait past the original window to ensure the cancelled timer never fires.
	time.Sleep(80 * time.Millisecond)
	assert.Equal(t, int32(0), fired.Load(), "deferred Press dispatch must be cancelled")
}

// TestHoldGate_LongPress_DispatchesPressAndAllowsStop covers the happy path:
// Press is held past the min-hold window, the deferred dispatch fires once,
// and the subsequent Release returns true so the caller emits Stop.
func TestHoldGate_LongPress_DispatchesPressAndAllowsStop(t *testing.T) {
	t.Parallel()

	var gate holdGate

	var fired atomic.Int32

	gate.OnPress(20*time.Millisecond, func() { fired.Add(1) })

	require.Eventually(t,
		func() bool { return fired.Load() == 1 },
		200*time.Millisecond, 5*time.Millisecond,
		"deferred Press dispatch must fire after min duration",
	)

	got := gate.OnRelease()
	assert.True(t, got, "Release after dispatch must return true (emit Stop)")
}

// TestHoldGate_SpuriousRelease_ReturnsFalse guards against a Release that
// arrives with no preceding Press (backend desync, focus-loss artefacts).
func TestHoldGate_SpuriousRelease_ReturnsFalse(t *testing.T) {
	t.Parallel()

	var gate holdGate

	got := gate.OnRelease()
	assert.False(t, got, "Release with no pending/started Press must drop")
}

// TestHoldGate_RepeatedPress_Idempotent guards against duplicate Press
// edges (key autorepeat reaching the handler, or backends that emit the
// same edge twice on the same physical press).
func TestHoldGate_RepeatedPress_Idempotent(t *testing.T) {
	t.Parallel()

	var gate holdGate

	var fired atomic.Int32

	dispatch := func() { fired.Add(1) }

	gate.OnPress(20*time.Millisecond, dispatch)
	gate.OnPress(20*time.Millisecond, dispatch)
	gate.OnPress(20*time.Millisecond, dispatch)

	require.Eventually(t,
		func() bool { return fired.Load() >= 1 },
		200*time.Millisecond, 5*time.Millisecond,
	)

	// Give any extra (incorrectly armed) timers a chance to fire.
	time.Sleep(60 * time.Millisecond)
	assert.Equal(t, int32(1), fired.Load(), "duplicate Press edges must dispatch at most once")

	got := gate.OnRelease()
	assert.True(t, got)
}

// TestHoldGate_PressReleasePressRelease_Sequence covers two full
// long-press cycles back-to-back to ensure state resets between them.
func TestHoldGate_PressReleasePressRelease_Sequence(t *testing.T) {
	t.Parallel()

	var gate holdGate

	var fired atomic.Int32

	dispatch := func() { fired.Add(1) }

	// First cycle: long press.
	gate.OnPress(15*time.Millisecond, dispatch)
	require.Eventually(t, func() bool { return fired.Load() == 1 }, 100*time.Millisecond, 2*time.Millisecond)
	assert.True(t, gate.OnRelease())

	// Second cycle: long press again.
	gate.OnPress(15*time.Millisecond, dispatch)
	require.Eventually(t, func() bool { return fired.Load() == 2 }, 100*time.Millisecond, 2*time.Millisecond)
	assert.True(t, gate.OnRelease())
}

// TestHoldGate_TapThenLongPress covers a short tap followed by a real
// long press — the gate must drop the first pair and honour the second.
func TestHoldGate_TapThenLongPress(t *testing.T) {
	t.Parallel()

	var gate holdGate

	var fired atomic.Int32

	dispatch := func() { fired.Add(1) }

	// Tap.
	gate.OnPress(30*time.Millisecond, dispatch)
	time.Sleep(5 * time.Millisecond)
	assert.False(t, gate.OnRelease())

	// Long press.
	gate.OnPress(15*time.Millisecond, dispatch)
	require.Eventually(t, func() bool { return fired.Load() == 1 }, 150*time.Millisecond, 2*time.Millisecond)
	assert.True(t, gate.OnRelease())

	// Confirm the cancelled tap never bled through.
	time.Sleep(60 * time.Millisecond)
	assert.Equal(t, int32(1), fired.Load())
}
