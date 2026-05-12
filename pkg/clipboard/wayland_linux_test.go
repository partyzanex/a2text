//go:build linux

package clipboard

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/stretchr/testify/suite"
)

type WaylandClipboardSuite struct {
	suite.Suite

	log  *slog.Logger
	ctrl *gomock.Controller
}

func TestWaylandClipboardSuite(t *testing.T) {
	suite.Run(t, new(WaylandClipboardSuite))
}

func (s *WaylandClipboardSuite) SetupTest() {
	s.log = slog.New(slog.DiscardHandler)
	s.ctrl = gomock.NewController(s.T())
}

// --- Construction ---

func (s *WaylandClipboardSuite) TestNew_NoWlCopy_ReturnsErrNoBackend() {
	runner := NewMockCopyRunner(s.ctrl)
	runner.EXPECT().LookPath(wlCopyBin).Return("", errors.New("not found in PATH"))

	c, err := newWaylandClipboard(runner, s.log)
	s.Require().ErrorIs(err, ErrNoBackend)
	s.Nil(c)
}

func (s *WaylandClipboardSuite) TestNew_NilRunner_ReturnsErrNoBackend() {
	c, err := newWaylandClipboard(nil, s.log)
	s.Require().ErrorIs(err, ErrNoBackend)
	s.Nil(c)
}

func (s *WaylandClipboardSuite) TestNew_NilLog_DoesNotPanic() {
	runner := NewMockCopyRunner(s.ctrl)
	runner.EXPECT().LookPath(wlCopyBin).Return("/usr/bin/wl-copy", nil)

	c, err := newWaylandClipboard(runner, nil)
	s.Require().NoError(err)
	s.NotNil(c)
}

// --- Happy path: text passed to subprocess stdin ---

func (s *WaylandClipboardSuite) TestCopy_PassesTextToWlCopy() {
	runner := NewMockCopyRunner(s.ctrl)
	runner.EXPECT().LookPath(wlCopyBin).Return("/usr/bin/wl-copy", nil)
	runner.EXPECT().Run(gomock.Any(), "/usr/bin/wl-copy", gomock.Nil(), []byte("hello"), copyTimeout).Return(nil)

	c := s.clipboardWith(runner)
	s.Require().NoError(c.Copy(s.T().Context(), "hello"))
}

// --- Empty text is a no-op (no subprocess invoked) ---

func (s *WaylandClipboardSuite) TestCopy_EmptyText_NoSubprocess() {
	runner := NewMockCopyRunner(s.ctrl)
	runner.EXPECT().LookPath(wlCopyBin).Return("/usr/bin/wl-copy", nil)
	// No Run expectation — mock will fail if Run is called unexpectedly.

	c := s.clipboardWith(runner)
	s.Require().NoError(c.Copy(s.T().Context(), ""))
}

// --- Cancelled context short-circuits before spawning the subprocess ---
//
// Order of checks: ctx cancellation → empty-text short-circuit. Both halves:
//   - non-empty text + cancelled ctx → context error, no subprocess.
//   - empty text + cancelled ctx     → context error (NOT silent nil),
//     so a shutting-down caller never gets a misleading "ok" on a no-op delivery.

func (s *WaylandClipboardSuite) TestCopy_CancelledContext_EmptyText_StillReturnsContextErr() {
	runner := NewMockCopyRunner(s.ctrl)
	runner.EXPECT().LookPath(wlCopyBin).Return("/usr/bin/wl-copy", nil)

	c := s.clipboardWith(runner)

	ctx, cancel := context.WithCancel(s.T().Context())
	cancel()

	err := c.Copy(ctx, "")
	s.Require().ErrorIs(err, context.Canceled,
		"empty text with a cancelled ctx must surface the cancellation, not silently return nil")
}

func (s *WaylandClipboardSuite) TestCopy_CancelledContext_NoSubprocess() {
	runner := NewMockCopyRunner(s.ctrl)
	runner.EXPECT().LookPath(wlCopyBin).Return("/usr/bin/wl-copy", nil)

	c := s.clipboardWith(runner)

	ctx, cancel := context.WithCancel(s.T().Context())
	cancel()

	err := c.Copy(ctx, "hello")
	s.Require().ErrorIs(err, context.Canceled)
}

// --- Subprocess errors are wrapped with the 'clipboard:' prefix ---

func (s *WaylandClipboardSuite) TestCopy_SubprocessError_WrappedWithClipboardPrefix() {
	runner := NewMockCopyRunner(s.ctrl)
	runner.EXPECT().LookPath(wlCopyBin).Return("/usr/bin/wl-copy", nil)
	runner.EXPECT().
		Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(errors.New("compositor unreachable"))

	c := s.clipboardWith(runner)

	err := c.Copy(s.T().Context(), "hello")
	s.Require().ErrorContains(err, "clipboard:")
	s.Require().ErrorContains(err, "compositor unreachable")
}

// --- copyTimeout is propagated to the runner ---

func (s *WaylandClipboardSuite) TestCopy_PassesCopyTimeoutToRunner() {
	runner := NewMockCopyRunner(s.ctrl)
	runner.EXPECT().LookPath(wlCopyBin).Return("/usr/bin/wl-copy", nil)
	runner.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), copyTimeout).Return(nil)

	c := s.clipboardWith(runner)
	s.Require().NoError(c.Copy(s.T().Context(), "hello"))
}

// --- truncate handles UTF-8 by rune, not by byte ---

func (s *WaylandClipboardSuite) TestTruncate_UTF8_DoesNotSplitRunes() {
	// "абвгдеж" is 7 runes / 14 bytes. Truncating to 3 runes must yield
	// 3 valid runes plus the marker — byte-slicing would chop a 2-byte
	// sequence and produce invalid UTF-8.
	got := truncate("абвгдеж", 3)
	s.Equal("абв...(truncated)", got)
}

func (s *WaylandClipboardSuite) TestTruncate_ShortString_Untouched() {
	got := truncate("hello", 200)
	s.Equal("hello", got)
}

func (s *WaylandClipboardSuite) TestTruncate_ZeroOrNegative_DoesNotPanic() {
	s.NotPanics(func() { _ = truncate("abc", 0) })
	s.NotPanics(func() { _ = truncate("abc", -1) })

	s.Equal("...(truncated)", truncate("abc", 0))
	s.Equal("...(truncated)", truncate("abc", -1))
}

// --- Helpers ---

func (s *WaylandClipboardSuite) clipboardWith(runner CopyRunner) *WaylandClipboard {
	c, err := newWaylandClipboard(runner, s.log)
	s.Require().NoError(err)

	return c
}
