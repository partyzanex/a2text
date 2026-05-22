package hotkeyreader

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/a2text/internal/infra/cycletoken"
	"github.com/partyzanex/a2text/internal/usecases/hotkey"
	pkghotkey "github.com/partyzanex/a2text/pkg/hotkey"
	a2textv1 "github.com/partyzanex/a2text/pkg/proto/a2text/v1"
)

func newTestReader(t *testing.T, mode a2textv1.HotkeyMode) *Reader {
	t.Helper()

	return &Reader{
		log:    slog.New(slog.DiscardHandler),
		mode:   mode,
		tokens: cycletoken.New(5*time.Second, time.Now),
		hub:    hotkey.New(slog.New(slog.DiscardHandler), mode),
		// evdev intentionally nil — tests drive handleHold /
		// handleToggle / startCycle directly.
	}
}

func TestStartCycle_TransitionsHubToRecording(t *testing.T) {
	t.Parallel()

	r := newTestReader(t, a2textv1.HotkeyMode_HOTKEY_MODE_HOLD)

	r.startCycle(context.Background())

	require.True(t, r.hub.IsRecording())
}

func TestStartCycle_TokenStaysActiveAfterStart(t *testing.T) {
	t.Parallel()

	r := newTestReader(t, a2textv1.HotkeyMode_HOTKEY_MODE_HOLD)

	r.startCycle(context.Background())

	// Slot is occupied — second Issue must report ErrAlreadyActive.
	_, _, err := r.tokens.Issue()
	require.ErrorIs(t, err, cycletoken.ErrAlreadyActive)
}

func TestStartCycle_RollsBackTokenOnHubFailure(t *testing.T) {
	t.Parallel()

	r := newTestReader(t, a2textv1.HotkeyMode_HOTKEY_MODE_HOLD)

	// Force hub.Start to fail by putting the hub into RECORDING
	// before the reader starts a cycle.
	require.NoError(t, r.hub.Start(context.Background(), "pre-existing"))

	r.startCycle(context.Background())

	// hub.Start fails on the second call (ErrCycleInFlight); the
	// reader's rollback must Consume the token so the slot is free
	// for the next attempt once the in-flight cycle ends.
	r.hub.End()

	_, _, err := r.tokens.Issue()
	require.NoError(t, err, "rollback must release the token slot")
}

func TestHandleHold_PressStartsCycle(t *testing.T) {
	t.Parallel()

	r := newTestReader(t, a2textv1.HotkeyMode_HOTKEY_MODE_HOLD)

	r.handleHold(context.Background(), pkghotkey.Press)

	require.True(t, r.hub.IsRecording())
}

func TestHandleHold_ReleaseEndsCycle(t *testing.T) {
	t.Parallel()

	r := newTestReader(t, a2textv1.HotkeyMode_HOTKEY_MODE_HOLD)

	r.handleHold(context.Background(), pkghotkey.Press)
	require.True(t, r.hub.IsRecording())

	r.handleHold(context.Background(), pkghotkey.Release)
	require.False(t, r.hub.IsRecording())
}

func TestHandleToggle_FirstPressStarts(t *testing.T) {
	t.Parallel()

	r := newTestReader(t, a2textv1.HotkeyMode_HOTKEY_MODE_TOGGLE)

	r.handleToggle(context.Background(), pkghotkey.Press)

	require.True(t, r.hub.IsRecording())
}

func TestHandleToggle_SecondPressEnds(t *testing.T) {
	t.Parallel()

	r := newTestReader(t, a2textv1.HotkeyMode_HOTKEY_MODE_TOGGLE)

	r.handleToggle(context.Background(), pkghotkey.Press)
	require.True(t, r.hub.IsRecording())

	r.handleToggle(context.Background(), pkghotkey.Press)
	require.False(t, r.hub.IsRecording(),
		"second press must End the in-flight cycle")
}

func TestHandleToggle_ReleaseIgnored(t *testing.T) {
	t.Parallel()

	r := newTestReader(t, a2textv1.HotkeyMode_HOTKEY_MODE_TOGGLE)

	r.handleToggle(context.Background(), pkghotkey.Press)
	require.True(t, r.hub.IsRecording())

	r.handleToggle(context.Background(), pkghotkey.Release)
	require.True(t, r.hub.IsRecording(),
		"TOGGLE mode must not react to Release edges")
}

// TestHandleToggle_StopUsesIsRecordingProbe verifies the M2 fix:
// when Hub is RECORDING, a Press correctly ends the cycle even when
// the token store could have transiently returned an unrelated
// error. The probe path bypasses Issue entirely on the stop branch,
// so it cannot be confused by transient Issue failures.
func TestHandleToggle_StopUsesIsRecordingProbe(t *testing.T) {
	t.Parallel()

	r := newTestReader(t, a2textv1.HotkeyMode_HOTKEY_MODE_TOGGLE)

	require.NoError(t, r.hub.Start(context.Background(), "external-start"))
	require.True(t, r.hub.IsRecording())

	r.handleToggle(context.Background(), pkghotkey.Press)
	require.False(t, r.hub.IsRecording())
}
