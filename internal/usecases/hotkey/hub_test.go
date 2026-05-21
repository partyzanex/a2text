package hotkey_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/a2text/internal/domain"
	"github.com/partyzanex/a2text/internal/usecases/hotkey"
	a2textv1 "github.com/partyzanex/a2text/pkg/proto/a2text/v1"
)

// HubSuite covers Subscribe / Start / End behaviour of hotkey.Hub:
// the initial-state snapshot, the single-subscriber invariant, and
// the kind / token / sequence ordering of the cycle-start and
// cycle-end events. Audio capture is intentionally out of scope —
// the UI starts and stops the microphone in reaction to the
// broadcasted events.
type HubSuite struct {
	suite.Suite

	hub *hotkey.Hub
}

// SetupTest builds a fresh hub per test case in TOGGLE mode so test
// ordering and shared state never bleed across cases.
func (s *HubSuite) SetupTest() {
	s.hub = hotkey.New(slog.New(slog.DiscardHandler), a2textv1.HotkeyMode_HOTKEY_MODE_TOGGLE)
}

// TestSubscribe_ReturnsIdleInitialState verifies the very first
// frame the daemon would push on a fresh Hub: state IDLE, mode set
// to the constructor argument, no active token.
func (s *HubSuite) TestSubscribe_ReturnsIdleInitialState() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial, ch, err := s.hub.Subscribe(ctx)

	s.Require().NoError(err)
	s.Require().NotNil(initial)
	s.Require().NotNil(ch)
	s.Equal(a2textv1.HotkeyState_HOTKEY_STATE_IDLE, initial.GetState())
	s.Equal(a2textv1.HotkeyMode_HOTKEY_MODE_TOGGLE, initial.GetMode())
	s.Empty(initial.GetActiveInjectToken())
}

// TestSubscribe_NewModeIsPropagated verifies the mode the
// constructor was called with shows up in the InitialState snapshot.
func (s *HubSuite) TestSubscribe_NewModeIsPropagated() {
	hub := hotkey.New(slog.New(slog.DiscardHandler), a2textv1.HotkeyMode_HOTKEY_MODE_HOLD)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial, _, err := hub.Subscribe(ctx)
	s.Require().NoError(err)
	s.Equal(a2textv1.HotkeyMode_HOTKEY_MODE_HOLD, initial.GetMode())
}

// TestSubscribe_RejectsSecondCall verifies the single-subscriber
// invariant: a second Subscribe while the slot is occupied gets
// ErrAlreadySubscribed.
func (s *HubSuite) TestSubscribe_RejectsSecondCall() {
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	_, _, err := s.hub.Subscribe(ctx1)
	s.Require().NoError(err)

	_, _, err = s.hub.Subscribe(ctx2)
	s.Require().ErrorIs(err, hotkey.ErrAlreadySubscribed)
}

// TestSubscribe_ChannelClosedOnCtxCancel verifies the cleanup
// goroutine closes the channel once the subscriber context is
// cancelled.
func (s *HubSuite) TestSubscribe_ChannelClosedOnCtxCancel() {
	ctx, cancel := context.WithCancel(context.Background())

	_, ch, err := s.hub.Subscribe(ctx)
	s.Require().NoError(err)

	cancel()

	waitChannelClosed(s.T(), ch)
}

// TestStart_TogglePublishesToggleEvent verifies the cycle-start
// event the Hub emits in TOGGLE mode carries kind=TOGGLE, the
// caller-supplied token, and sequence=1.
func (s *HubSuite) TestStart_TogglePublishesToggleEvent() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, ch, err := s.hub.Subscribe(ctx)
	s.Require().NoError(err)

	s.Require().NoError(s.hub.Start(ctx, "tok-1"))

	ev := receiveEvent(s.T(), ch)
	s.Equal(a2textv1.HotkeyEventKind_HOTKEY_EVENT_KIND_TOGGLE, ev.GetKind())
	s.Equal("tok-1", ev.GetInjectToken())
	s.Equal(uint64(1), ev.GetSequence())
}

// TestStart_HoldPublishesPressEvent verifies the cycle-start event
// in HOLD mode is PRESS, not TOGGLE.
func (s *HubSuite) TestStart_HoldPublishesPressEvent() {
	hub := hotkey.New(slog.New(slog.DiscardHandler), a2textv1.HotkeyMode_HOTKEY_MODE_HOLD)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, ch, err := hub.Subscribe(ctx)
	s.Require().NoError(err)

	s.Require().NoError(hub.Start(ctx, "tok-h"))

	ev := receiveEvent(s.T(), ch)
	s.Equal(a2textv1.HotkeyEventKind_HOTKEY_EVENT_KIND_PRESS, ev.GetKind())
	s.Equal("tok-h", ev.GetInjectToken())
}

// TestStart_DoubleStartReturnsErrCycleInFlight verifies a second
// Start while a cycle is already running is rejected.
func (s *HubSuite) TestStart_DoubleStartReturnsErrCycleInFlight() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.Require().NoError(s.hub.Start(ctx, "tok-1"))

	err := s.hub.Start(ctx, "tok-2")
	s.Require().ErrorIs(err, domain.ErrCycleInFlight)
}

// TestEnd_PublishesEndEventAndClearsState verifies End emits the
// cycle-end event with the same token and transitions state to
// IDLE.
func (s *HubSuite) TestEnd_PublishesEndEventAndClearsState() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, ch, err := s.hub.Subscribe(ctx)
	s.Require().NoError(err)

	s.Require().NoError(s.hub.Start(ctx, "tok-end"))
	receiveEvent(s.T(), ch) // drain start event

	s.hub.End()

	ev := receiveEvent(s.T(), ch)
	s.Equal(a2textv1.HotkeyEventKind_HOTKEY_EVENT_KIND_TOGGLE, ev.GetKind())
	s.Equal("tok-end", ev.GetInjectToken())
	s.Equal(uint64(2), ev.GetSequence())
}

// TestEnd_HoldEmitsRelease verifies End emits RELEASE (not TOGGLE)
// in HOLD mode.
func (s *HubSuite) TestEnd_HoldEmitsRelease() {
	hub := hotkey.New(slog.New(slog.DiscardHandler), a2textv1.HotkeyMode_HOTKEY_MODE_HOLD)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, ch, err := hub.Subscribe(ctx)
	s.Require().NoError(err)

	s.Require().NoError(hub.Start(ctx, "tok-h"))
	receiveEvent(s.T(), ch)

	hub.End()

	ev := receiveEvent(s.T(), ch)
	s.Equal(a2textv1.HotkeyEventKind_HOTKEY_EVENT_KIND_RELEASE, ev.GetKind())
}

// TestEnd_IdleIsNoOp verifies End on an IDLE Hub does nothing — no
// event published.
func (s *HubSuite) TestEnd_IdleIsNoOp() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, ch, err := s.hub.Subscribe(ctx)
	s.Require().NoError(err)

	s.hub.End()

	select {
	case <-ch:
		s.FailNow("unexpected event on idle End")
	case <-time.After(20 * time.Millisecond):
		// no event — pass.
	}
}

// TestEnd_AllowsNextStart verifies that after End, state is cleared
// and the next Start succeeds.
func (s *HubSuite) TestEnd_AllowsNextStart() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, ch, err := s.hub.Subscribe(ctx)
	s.Require().NoError(err)

	s.Require().NoError(s.hub.Start(ctx, "tok-1"))
	receiveEvent(s.T(), ch)

	s.hub.End()
	receiveEvent(s.T(), ch)

	s.Require().NoError(s.hub.Start(ctx, "tok-2"))

	ev := receiveEvent(s.T(), ch)
	s.Equal("tok-2", ev.GetInjectToken())
}

// receiveEvent reads a single event from ch under a short timeout
// so a deadlock in the test fails fast.
func receiveEvent(t *testing.T, ch <-chan *a2textv1.HotkeyEvent) *a2textv1.HotkeyEvent {
	t.Helper()

	select {
	case ev, ok := <-ch:
		require.True(t, ok, "channel must be open")
		require.NotNil(t, ev, "event must not be nil")

		return ev
	case <-time.After(time.Second):
		require.FailNow(t, "timeout waiting for event")
	}

	return nil
}

// waitChannelClosed blocks until ch is closed or the deadline
// fires. Used to synchronise with the Hub's cleanup goroutine.
func waitChannelClosed(t *testing.T, ch <-chan *a2textv1.HotkeyEvent) {
	t.Helper()

	select {
	case _, ok := <-ch:
		require.False(t, ok, "channel must be closed")
	case <-time.After(time.Second):
		require.FailNow(t, "timeout waiting for channel close")
	}
}

// TestHubSuite is the standard testify entry point.
func TestHubSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(HubSuite))
}
