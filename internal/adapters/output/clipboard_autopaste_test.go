package output

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"
)

type ClipboardAutopasteSuite struct {
	suite.Suite

	ctrl *gomock.Controller
}

func TestClipboardAutopasteSuite(t *testing.T) {
	suite.Run(t, new(ClipboardAutopasteSuite))
}

func (s *ClipboardAutopasteSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
}

// --- Constructor contract ---

func (s *ClipboardAutopasteSuite) TestNew_NilDelivery_Panics() {
	paster := NewMockAutopaster(s.ctrl)
	s.Panics(func() {
		NewClipboardAutopasteOutput(nil, paster, 0, nil)
	})
}

func (s *ClipboardAutopasteSuite) TestNew_NilPaster_Panics() {
	delivery := NewMockClipboardDelivery(s.ctrl)
	s.Panics(func() {
		NewClipboardAutopasteOutput(delivery, nil, 0, nil)
	})
}

func (s *ClipboardAutopasteSuite) TestNew_NilLog_DoesNotPanic() {
	delivery := NewMockClipboardDelivery(s.ctrl)
	paster := NewMockAutopaster(s.ctrl)
	s.NotPanics(func() {
		NewClipboardAutopasteOutput(delivery, paster, 0, nil)
	})
}

func (s *ClipboardAutopasteSuite) TestNew_ZeroDelay_DefaultsTo50ms() {
	delivery := NewMockClipboardDelivery(s.ctrl)
	paster := NewMockAutopaster(s.ctrl)
	out := NewClipboardAutopasteOutput(delivery, paster, 0, nil)
	s.Equal(defaultPasteDelay, out.delay,
		"zero / negative delay must default to a known-good value, not disable the sync window")
}

// --- Happy path: copy then paste ---

func (s *ClipboardAutopasteSuite) TestDeliver_HappyPath_CopyThenPaste() {
	delivery := NewMockClipboardDelivery(s.ctrl)
	paster := NewMockAutopaster(s.ctrl)

	delivery.EXPECT().Deliver(gomock.Any(), "hello").Return(nil)
	paster.EXPECT().Paste(gomock.Any()).Return(nil)

	out := NewClipboardAutopasteOutput(delivery, paster, time.Millisecond, slog.New(slog.DiscardHandler))

	s.Require().NoError(out.Deliver(context.Background(), "hello"))
}

// --- Clipboard failure propagates ---

func (s *ClipboardAutopasteSuite) TestDeliver_ClipboardError_Propagates_AndPasteSkipped() {
	delivery := NewMockClipboardDelivery(s.ctrl)
	paster := NewMockAutopaster(s.ctrl)

	delivery.EXPECT().Deliver(gomock.Any(), "hello").Return(errors.New("both clipboard and stdout failed"))

	out := NewClipboardAutopasteOutput(delivery, paster, time.Millisecond, slog.New(slog.DiscardHandler))

	err := out.Deliver(context.Background(), "hello")
	s.Require().Error(err)
	s.Require().ErrorContains(err, "both clipboard and stdout failed",
		"underlying clipboard error must surface verbatim — daemon needs it for the error state")
}

// --- Paste failure is logged but does NOT fail Deliver ---

func (s *ClipboardAutopasteSuite) TestDeliver_PasteError_TextStillDelivered() {
	delivery := NewMockClipboardDelivery(s.ctrl)
	paster := NewMockAutopaster(s.ctrl)

	delivery.EXPECT().Deliver(gomock.Any(), "hello").Return(nil)
	paster.EXPECT().Paste(gomock.Any()).Return(errors.New("ydotoold dead"))

	out := NewClipboardAutopasteOutput(delivery, paster, time.Millisecond, slog.New(slog.DiscardHandler))

	err := out.Deliver(context.Background(), "hello")
	s.Require().NoError(err,
		"paste is convenience-only — clipboard already holds the text, the user can Ctrl+V manually")
}

// --- Autopaster ctx errors must propagate, not be swallowed as convenience ---

func (s *ClipboardAutopasteSuite) TestDeliver_PasteCancelledCtxError_Propagates() {
	delivery := NewMockClipboardDelivery(s.ctrl)
	paster := NewMockAutopaster(s.ctrl)

	delivery.EXPECT().Deliver(gomock.Any(), "hello").Return(nil)
	paster.EXPECT().Paste(gomock.Any()).Return(context.Canceled)

	out := NewClipboardAutopasteOutput(delivery, paster, time.Millisecond, slog.New(slog.DiscardHandler))

	err := out.Deliver(context.Background(), "hello")
	s.Require().Error(err)
	s.Require().ErrorIs(err, context.Canceled,
		"a paste cancellation MUST propagate — swallowing it would mark a shutdown cycle as successful delivery")
}

func (s *ClipboardAutopasteSuite) TestDeliver_PasteDeadlineExceededError_Propagates() {
	delivery := NewMockClipboardDelivery(s.ctrl)
	paster := NewMockAutopaster(s.ctrl)

	delivery.EXPECT().Deliver(gomock.Any(), "hello").Return(nil)
	paster.EXPECT().Paste(gomock.Any()).Return(context.DeadlineExceeded)

	out := NewClipboardAutopasteOutput(delivery, paster, time.Millisecond, slog.New(slog.DiscardHandler))

	err := out.Deliver(context.Background(), "hello")
	s.Require().Error(err)
	s.Require().ErrorIs(err, context.DeadlineExceeded)
}

// --- Empty text short-circuits BEFORE delivery (no wasted clipboard call) ---

func (s *ClipboardAutopasteSuite) TestDeliver_EmptyText_NoDeliveryNoPaste() {
	delivery := NewMockClipboardDelivery(s.ctrl)
	paster := NewMockAutopaster(s.ctrl)

	out := NewClipboardAutopasteOutput(delivery, paster, time.Millisecond, slog.New(slog.DiscardHandler))

	s.Require().NoError(out.Deliver(context.Background(), ""))
}

// --- Cancelled context paths ---

func (s *ClipboardAutopasteSuite) TestDeliver_PassesContextToDelivery() {
	delivery := NewMockClipboardDelivery(s.ctrl)
	paster := NewMockAutopaster(s.ctrl)

	out := NewClipboardAutopasteOutput(delivery, paster, time.Millisecond, slog.New(slog.DiscardHandler))

	ctx := context.Background()
	delivery.EXPECT().Deliver(ctx, "hello").Return(nil)
	paster.EXPECT().Paste(ctx).Return(nil)
	s.Require().NoError(out.Deliver(ctx, "hello"))
}

func (s *ClipboardAutopasteSuite) TestDeliver_CancelledBefore_NoOpReturnsContextErr() {
	delivery := NewMockClipboardDelivery(s.ctrl)
	paster := NewMockAutopaster(s.ctrl)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	out := NewClipboardAutopasteOutput(delivery, paster, time.Millisecond, slog.New(slog.DiscardHandler))

	err := out.Deliver(ctx, "hello")
	s.Require().ErrorIs(err, context.Canceled)
}

func (s *ClipboardAutopasteSuite) TestDeliver_CancelledAfterDelivery_SkipsPaste() {
	// Synchronise on a real signal from the mocked delivery rather than
	// time.Sleep — the latter is flaky on slow CI runners and obscures
	// the intent of the test ("wait until delivery returned").
	done := make(chan struct{})
	delivery := NewMockClipboardDelivery(s.ctrl)
	paster := NewMockAutopaster(s.ctrl)

	delivery.EXPECT().Deliver(gomock.Any(), "hello").DoAndReturn(func(context.Context, string) error {
		close(done)

		return nil
	})

	out := NewClipboardAutopasteOutput(delivery, paster, time.Hour, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() { errCh <- out.Deliver(ctx, "hello") }()

	// Wait for Deliver to have called the clipboard step and entered
	// the sleep window, then cancel mid-sleep.
	select {
	case <-done:
	case <-time.After(time.Second):
		s.FailNow("delivery never ran — test setup wrong")
	}

	cancel()

	select {
	case err := <-errCh:
		s.Require().ErrorIs(err, context.Canceled,
			"a cancellation during the sync window must short-circuit before paste fires")
	case <-time.After(time.Second):
		s.FailNow("Deliver did not return after cancellation — the delay select is not respecting ctx")
	}
}

func (s *ClipboardAutopasteSuite) TestDeliver_DeadlineExceededDuringDelay_SkipsPaste() {
	// The context deadline may fire before delivery, during the clipboard
	// step, or during the delay window. In all cases Deliver must propagate
	// DeadlineExceeded and must never invoke the autopaster.
	delivery := NewMockClipboardDelivery(s.ctrl)
	paster := NewMockAutopaster(s.ctrl)

	delivery.EXPECT().Deliver(gomock.Any(), "hello").Return(nil)

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	out := NewClipboardAutopasteOutput(delivery, paster, time.Hour, slog.New(slog.DiscardHandler))

	err := out.Deliver(ctx, "hello")
	s.Require().ErrorIs(err, context.DeadlineExceeded,
		"deadline expiry must propagate whether it fires before, during, or after the clipboard step")
}

// --- Defensive: nil/zero/hand-built receivers don't panic ---

func (s *ClipboardAutopasteSuite) TestDeliver_NilReceiver_ReturnsErrorNoPanic() {
	var out *ClipboardAutopasteOutput

	err := out.Deliver(context.Background(), "hello")
	s.Require().ErrorIs(err, ErrClipboardAutopasteNotInitialized)
}

func (s *ClipboardAutopasteSuite) TestDeliver_ZeroValue_ReturnsErrorNoPanic() {
	out := &ClipboardAutopasteOutput{}

	err := out.Deliver(context.Background(), "hello")
	s.Require().ErrorIs(err, ErrClipboardAutopasteNotInitialized,
		"hand-built zero value must surface as a wiring error, not panic in delivery")
}

func (s *ClipboardAutopasteSuite) TestDeliver_HandBuiltNilLog_PasteErrorNoPanic() {
	// log==nil + paste error → WARN path would deref nil if logger() were
	// not nil-safe. Ensure both that no panic happens and that the paste
	// error is still swallowed (convenience-only contract).
	out := &ClipboardAutopasteOutput{
		delivery:   NewMockClipboardDelivery(s.ctrl),
		autopaster: NewMockAutopaster(s.ctrl),
		delay:      time.Millisecond,
		log:        nil,
	}

	out.delivery.(*MockClipboardDelivery).EXPECT().Deliver(gomock.Any(), "hello").Return(nil)

	out.autopaster.(*MockAutopaster).EXPECT().Paste(gomock.Any()).Return(errors.New("paste failed"))

	s.Require().NoError(out.Deliver(context.Background(), "hello"))
}

func (s *ClipboardAutopasteSuite) TestDeliver_HandBuiltZeroDelay_UsesDefault() {
	// Constructor normalises zero → default, but a hand-built struct can
	// still carry delay=0. Deliver must also normalise so the sync window
	// is not disabled and pastes don't fire against stale clipboards.
	delivery := NewMockClipboardDelivery(s.ctrl)
	paster := NewMockAutopaster(s.ctrl)

	delivery.EXPECT().Deliver(gomock.Any(), "hello").Return(nil)
	paster.EXPECT().Paste(gomock.Any()).Return(nil)

	out := &ClipboardAutopasteOutput{
		delivery:   delivery,
		autopaster: paster,
		delay:      0,
		log:        slog.New(slog.DiscardHandler),
	}

	s.Require().NoError(out.Deliver(context.Background(), "hello"))
}

// --- logger() nil-safety ---

func (s *ClipboardAutopasteSuite) TestLogger_NilReceiver_ReturnsNonNil() {
	var out *ClipboardAutopasteOutput

	s.NotNil(out.logger(), "logger() must return a usable logger even for a nil receiver — WARN path calls it")
}
