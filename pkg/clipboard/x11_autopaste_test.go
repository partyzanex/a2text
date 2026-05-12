//go:build linux

package clipboard

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"
)

type X11AutopasteSuite struct {
	suite.Suite

	ctrl *gomock.Controller
	mock *MockPasteRunner
}

func TestX11AutopasteSuite(t *testing.T) {
	suite.Run(t, new(X11AutopasteSuite))
}

func (s *X11AutopasteSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.mock = NewMockPasteRunner(s.ctrl)
}

// --- Construction ---

func (s *X11AutopasteSuite) TestNewX11Autopaster_NoXdotool_ReturnsErrNoAutopasteBackend() {
	s.mock.EXPECT().LookPath(xdotoolBackend).Return("", errors.New("not found"))

	_, err := newX11Autopaster(s.mock, "auto", nil)
	s.Require().ErrorIs(err, ErrNoAutopasteBackend)
}

func (s *X11AutopasteSuite) TestNewX11Autopaster_XdotoolFound_Success() {
	s.mock.EXPECT().LookPath(xdotoolBackend).Return("/usr/bin/xdotool", nil)

	paster, err := newX11Autopaster(s.mock, "auto", nil)
	s.Require().NoError(err)
	s.NotNil(paster)
	s.Equal(xdotoolBackend, paster.Backend())
}

func (s *X11AutopasteSuite) TestNewX11Autopaster_ExplicitXdotool_Success() {
	s.mock.EXPECT().LookPath(xdotoolBackend).Return("/usr/bin/xdotool", nil)

	paster, err := newX11Autopaster(s.mock, xdotoolBackend, nil)
	s.Require().NoError(err)
	s.NotNil(paster)
}

func (s *X11AutopasteSuite) TestNewX11Autopaster_WtypeCmd_ReturnsErrUnsupported() {
	// LookPath not called: validation fails before binary resolution.
	_, err := newX11Autopaster(s.mock, "wtype", nil)
	s.Require().ErrorIs(err, ErrUnsupportedAutopasteBackend)
}

// --- Paste ---

func (s *X11AutopasteSuite) TestPaste_HappyPath() {
	ctx := s.T().Context()

	s.mock.EXPECT().LookPath(xdotoolBackend).Return("/usr/bin/xdotool", nil)
	s.mock.EXPECT().
		Run(gomock.Any(), "/usr/bin/xdotool", gomock.Eq([]string{xdotoolKeyCmd, xdotoolCtrlV}), xdotoolTimeout).
		Return(nil)

	paster, err := newX11Autopaster(s.mock, "auto", nil)
	s.Require().NoError(err)

	s.Require().NoError(paster.Paste(ctx))
}

func (s *X11AutopasteSuite) TestPaste_CtxCancelled_ReturnsError() {
	ctx, cancel := context.WithCancel(s.T().Context())
	cancel()

	s.mock.EXPECT().LookPath(xdotoolBackend).Return("/usr/bin/xdotool", nil)

	paster, err := newX11Autopaster(s.mock, "auto", nil)
	s.Require().NoError(err)

	s.Require().ErrorIs(paster.Paste(ctx), context.Canceled)
}

func (s *X11AutopasteSuite) TestPaste_RunnerError_Wraps() {
	ctx := s.T().Context()

	s.mock.EXPECT().LookPath(xdotoolBackend).Return("/usr/bin/xdotool", nil)
	s.mock.EXPECT().
		Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(errors.New("xdotool crashed"))

	paster, err := newX11Autopaster(s.mock, "auto", nil)
	s.Require().NoError(err)

	err = paster.Paste(ctx)
	s.Require().ErrorContains(err, "autopaste:")
	s.Require().ErrorContains(err, "xdotool crashed")
}
