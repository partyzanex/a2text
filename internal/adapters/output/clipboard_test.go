package output

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"
)

type ClipboardOutputSuite struct {
	suite.Suite

	ctrl *gomock.Controller
}

func TestClipboardOutputSuite(t *testing.T) {
	suite.Run(t, new(ClipboardOutputSuite))
}

func (s *ClipboardOutputSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
}

// --- Happy path: primary succeeds, fallback never invoked ---

func (s *ClipboardOutputSuite) TestDeliver_PrimaryOK_NoFallback() {
	primary := NewMockClipboardCopier(s.ctrl)
	primary.EXPECT().Copy(gomock.Any(), "hello").Return(nil)

	var buf bytes.Buffer

	out := NewClipboardOutput(
		primary,
		NewStdoutOutputWithWriter(&buf),
		slog.New(slog.DiscardHandler),
	)

	err := out.Deliver(context.Background(), "hello")
	s.Require().NoError(err)
	s.Empty(buf.String(), "fallback must NOT receive text on success")
}

// --- Degraded: primary fails, stdout fallback succeeds ---

func (s *ClipboardOutputSuite) TestDeliver_PrimaryFails_StdoutFallback() {
	primary := NewMockClipboardCopier(s.ctrl)
	primary.EXPECT().Copy(gomock.Any(), "hello").Return(errors.New("compositor offline"))

	var buf bytes.Buffer

	out := NewClipboardOutput(
		primary,
		NewStdoutOutputWithWriter(&buf),
		slog.New(slog.DiscardHandler),
	)

	err := out.Deliver(context.Background(), "hello")
	s.Require().NoError(err, "degraded path is still success from caller's POV")
	s.Equal("hello\n", buf.String())
}

// --- Both fail: error wraps both causes ---

func (s *ClipboardOutputSuite) TestDeliver_BothFail_ErrorWrapsBoth() {
	primary := NewMockClipboardCopier(s.ctrl)
	primary.EXPECT().Copy(gomock.Any(), "hello").Return(errors.New("compositor offline"))
	// Closed pipe: writes will error with io.ErrClosedPipe.
	r, w := io.Pipe()
	_ = r.Close()
	_ = w.Close()

	out := NewClipboardOutput(
		primary,
		NewStdoutOutputWithWriter(w),
		slog.New(slog.DiscardHandler),
	)

	err := out.Deliver(context.Background(), "hello")
	s.Require().Error(err)
	s.Contains(err.Error(), "clipboard")
	s.Contains(err.Error(), "stdout")
}

// --- Cancellation honoured up-front ---

func (s *ClipboardOutputSuite) TestDeliver_CancelledContext_NotInvoked() {
	primary := NewMockClipboardCopier(s.ctrl)

	var buf bytes.Buffer

	out := NewClipboardOutput(
		primary,
		NewStdoutOutputWithWriter(&buf),
		slog.New(slog.DiscardHandler),
	)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := out.Deliver(ctx, "hello")
	s.Require().ErrorIs(err, context.Canceled)
	s.Empty(buf.String())
}

// --- Context cancellation between clipboard and fallback ---

func (s *ClipboardOutputSuite) TestDeliver_CtxCancelledDuringPrimary_DoesNotInvokeFallback() {
	// Simulates "primary takes a long time and the caller's deadline elapses
	// during the call". By the time primary returns its error, ctx is dead.
	// The second ctx.Err() check inside Deliver must catch this and skip
	// the fallback rather than blindly writing past the deadline.
	ctx, cancel := context.WithCancel(context.Background())

	primary := NewMockClipboardCopier(s.ctrl)
	primary.EXPECT().Copy(ctx, "hello").DoAndReturn(func(context.Context, string) error {
		cancel()

		return errors.New("compositor offline (after timeout)")
	})

	var buf bytes.Buffer

	out := NewClipboardOutput(
		primary,
		NewStdoutOutputWithWriter(&buf),
		slog.New(slog.DiscardHandler),
	)

	err := out.Deliver(ctx, "hello")
	s.Require().ErrorIs(err, context.Canceled)
	s.Empty(buf.String(), "fallback must NOT run when ctx died mid-call")
}

// --- Constructor sanity ---

func (s *ClipboardOutputSuite) TestConstructor_PanicsOnNilPrimary() {
	s.Panics(func() {
		NewClipboardOutput(nil, NewStdoutOutput(), nil)
	})
}

func (s *ClipboardOutputSuite) TestConstructor_PanicsOnNilFallback() {
	primary := NewMockClipboardCopier(s.ctrl)
	s.Panics(func() {
		NewClipboardOutput(primary, nil, nil)
	})
}
