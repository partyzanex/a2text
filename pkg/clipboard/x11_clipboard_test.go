//go:build linux

package clipboard

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"
)

type X11ClipboardSuite struct {
	suite.Suite

	ctrl *gomock.Controller
	mock *MockCopyRunner
}

func TestX11ClipboardSuite(t *testing.T) {
	suite.Run(t, new(X11ClipboardSuite))
}

func (s *X11ClipboardSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.mock = NewMockCopyRunner(s.ctrl)
}

// --- Construction ---

func (s *X11ClipboardSuite) TestNewX11Clipboard_NoXclip_ReturnsErrNoBackend() {
	s.mock.EXPECT().LookPath(xclipBin).Return("", errors.New("not found"))

	_, err := newX11Clipboard(s.mock, nil)
	s.Require().ErrorIs(err, ErrNoBackend)
}

func (s *X11ClipboardSuite) TestNewX11Clipboard_XclipFound_Success() {
	s.mock.EXPECT().LookPath(xclipBin).Return("/usr/bin/xclip", nil)

	c, err := newX11Clipboard(s.mock, nil)
	s.Require().NoError(err)
	s.NotNil(c)
}

// --- Copy ---

func (s *X11ClipboardSuite) TestCopy_HappyPath() {
	ctx := s.T().Context()

	s.mock.EXPECT().LookPath(xclipBin).Return("/usr/bin/xclip", nil)
	s.mock.EXPECT().
		Run(
			gomock.Any(), "/usr/bin/xclip",
			gomock.Eq([]string{"-selection", "clipboard"}),
			[]byte("hello x11"), xclipTimeout,
		).
		Return(nil)

	c, err := newX11Clipboard(s.mock, nil)
	s.Require().NoError(err)

	s.Require().NoError(c.Copy(ctx, "hello x11"))
}

func (s *X11ClipboardSuite) TestCopy_EmptyText_NoOp() {
	ctx := s.T().Context()

	s.mock.EXPECT().LookPath(xclipBin).Return("/usr/bin/xclip", nil)
	// Run not called: empty text is a no-op.

	c, err := newX11Clipboard(s.mock, nil)
	s.Require().NoError(err)

	s.Require().NoError(c.Copy(ctx, ""))
}

func (s *X11ClipboardSuite) TestCopy_CtxCancelled_ReturnsError() {
	ctx, cancel := context.WithCancel(s.T().Context())
	cancel()

	s.mock.EXPECT().LookPath(xclipBin).Return("/usr/bin/xclip", nil)

	c, err := newX11Clipboard(s.mock, nil)
	s.Require().NoError(err)

	s.Require().ErrorIs(c.Copy(ctx, "text"), context.Canceled)
}

func (s *X11ClipboardSuite) TestCopy_RunnerError_Wraps() {
	ctx := s.T().Context()

	s.mock.EXPECT().LookPath(xclipBin).Return("/usr/bin/xclip", nil)
	s.mock.EXPECT().
		Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(errors.New("xclip crashed"))

	c, err := newX11Clipboard(s.mock, nil)
	s.Require().NoError(err)

	err = c.Copy(ctx, "text")
	s.Require().ErrorContains(err, "clipboard:")
	s.Require().ErrorContains(err, "xclip crashed")
}
