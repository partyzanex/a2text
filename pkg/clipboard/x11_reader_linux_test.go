//go:build linux

package clipboard

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"
)

type X11ClipboardReaderSuite struct {
	suite.Suite

	log  *slog.Logger
	ctrl *gomock.Controller
}

func TestX11ClipboardReaderSuite(t *testing.T) {
	suite.Run(t, new(X11ClipboardReaderSuite))
}

func (s *X11ClipboardReaderSuite) SetupTest() {
	s.log = slog.New(slog.DiscardHandler)
	s.ctrl = gomock.NewController(s.T())
}

func (s *X11ClipboardReaderSuite) TestNew_NoXclip_ReturnsErrNoBackend() {
	runner := NewMockReadRunner(s.ctrl)
	runner.EXPECT().LookPath(xclipBin).Return("", errors.New("not found"))

	r, err := newX11ClipboardReader(runner, s.log)
	s.Require().ErrorIs(err, ErrNoBackend)
	s.Nil(r)
}

// First non-meta TARGETS line is selected as primary; meta lines skipped.
func (s *X11ClipboardReaderSuite) TestSnapshot_SkipsMetaTargets() {
	runner := NewMockReadRunner(s.ctrl)
	runner.EXPECT().LookPath(xclipBin).Return("/usr/bin/xclip", nil)
	runner.EXPECT().RunCapture(gomock.Any(), "/usr/bin/xclip",
		[]string{"-selection", "clipboard", "-t", "TARGETS", "-o"}, xclipTimeout).
		Return([]byte("TARGETS\nTIMESTAMP\nimage/png\ntext/plain\n"), nil)
	runner.EXPECT().RunCapture(gomock.Any(), "/usr/bin/xclip",
		[]string{"-selection", "clipboard", "-t", "image/png", "-o"}, xclipTimeout).
		Return([]byte{0x89, 0x50, 0x4e, 0x47}, nil)

	r, err := newX11ClipboardReader(runner, s.log)
	s.Require().NoError(err)

	snap, err := r.Snapshot(context.Background())
	s.Require().NoError(err)
	s.Equal("image/png", snap.MIME)
	s.Equal([]byte{0x89, 0x50, 0x4e, 0x47}, snap.Data)
}

// xclip "not available" error on TARGETS → empty selection.
func (s *X11ClipboardReaderSuite) TestSnapshot_NotAvailableOnTargets_Empty() {
	runner := NewMockReadRunner(s.ctrl)
	runner.EXPECT().LookPath(xclipBin).Return("/usr/bin/xclip", nil)
	runner.EXPECT().RunCapture(gomock.Any(), "/usr/bin/xclip",
		[]string{"-selection", "clipboard", "-t", "TARGETS", "-o"}, xclipTimeout).
		Return(nil, errors.New("xclip: target STRING not available"))

	r, err := newX11ClipboardReader(runner, s.log)
	s.Require().NoError(err)

	snap, err := r.Snapshot(context.Background())
	s.Require().NoError(err)
	s.True(snap.Empty)
}

// TARGETS-only (no payload targets) → Empty=true.
func (s *X11ClipboardReaderSuite) TestSnapshot_OnlyMetaTargets_Empty() {
	runner := NewMockReadRunner(s.ctrl)
	runner.EXPECT().LookPath(xclipBin).Return("/usr/bin/xclip", nil)
	runner.EXPECT().RunCapture(gomock.Any(), "/usr/bin/xclip",
		[]string{"-selection", "clipboard", "-t", "TARGETS", "-o"}, xclipTimeout).
		Return([]byte("TARGETS\nTIMESTAMP\n"), nil)

	r, err := newX11ClipboardReader(runner, s.log)
	s.Require().NoError(err)

	snap, err := r.Snapshot(context.Background())
	s.Require().NoError(err)
	s.True(snap.Empty)
}
