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

type WaylandClipboardReaderSuite struct {
	suite.Suite

	log  *slog.Logger
	ctrl *gomock.Controller
}

func TestWaylandClipboardReaderSuite(t *testing.T) {
	suite.Run(t, new(WaylandClipboardReaderSuite))
}

func (s *WaylandClipboardReaderSuite) SetupTest() {
	s.log = slog.New(slog.DiscardHandler)
	s.ctrl = gomock.NewController(s.T())
}

// --- Construction ---

func (s *WaylandClipboardReaderSuite) TestNew_NoWlPaste_ReturnsErrNoBackend() {
	runner := NewMockReadRunner(s.ctrl)
	runner.EXPECT().LookPath(wlPasteBin).Return("", errors.New("not found"))

	r, err := newWaylandClipboardReader(runner, s.log)
	s.Require().ErrorIs(err, ErrNoBackend)
	s.Nil(r)
}

func (s *WaylandClipboardReaderSuite) TestNew_NilRunner_ReturnsErrNoBackend() {
	r, err := newWaylandClipboardReader(nil, s.log)
	s.Require().ErrorIs(err, ErrNoBackend)
	s.Nil(r)
}

// --- Snapshot happy path ---

func (s *WaylandClipboardReaderSuite) TestSnapshot_PicksFirstNonEmptyType() {
	runner := NewMockReadRunner(s.ctrl)
	runner.EXPECT().LookPath(wlPasteBin).Return("/usr/bin/wl-paste", nil)
	runner.EXPECT().RunCapture(gomock.Any(), "/usr/bin/wl-paste",
		[]string{"--list-types"}, wlPasteTimeout).
		Return([]byte("\ntext/plain\ntext/html\n"), nil)
	runner.EXPECT().RunCapture(gomock.Any(), "/usr/bin/wl-paste",
		[]string{"--type", "text/plain", "--no-newline"}, wlPasteTimeout).
		Return([]byte("hello"), nil)

	r, err := newWaylandClipboardReader(runner, s.log)
	s.Require().NoError(err)

	snap, err := r.Snapshot(context.Background())
	s.Require().NoError(err)
	s.Equal("text/plain", snap.MIME)
	s.Equal([]byte("hello"), snap.Data)
	s.False(snap.Empty)
}

// --- Empty selection: list-types returns the "No selection" marker ---

func (s *WaylandClipboardReaderSuite) TestSnapshot_EmptyClipboard_ListTypesMarker() {
	runner := NewMockReadRunner(s.ctrl)
	runner.EXPECT().LookPath(wlPasteBin).Return("/usr/bin/wl-paste", nil)
	runner.EXPECT().RunCapture(gomock.Any(), "/usr/bin/wl-paste",
		[]string{"--list-types"}, wlPasteTimeout).
		Return(nil, errors.New("wl-paste: No selection"))

	r, err := newWaylandClipboardReader(runner, s.log)
	s.Require().NoError(err)

	snap, err := r.Snapshot(context.Background())
	s.Require().NoError(err)
	s.True(snap.Empty)
	s.Empty(snap.MIME)
	s.Empty(snap.Data)
}

// --- Empty list (no error but blank stdout) ---

func (s *WaylandClipboardReaderSuite) TestSnapshot_BlankListTypes_ReturnsEmpty() {
	runner := NewMockReadRunner(s.ctrl)
	runner.EXPECT().LookPath(wlPasteBin).Return("/usr/bin/wl-paste", nil)
	runner.EXPECT().RunCapture(gomock.Any(), "/usr/bin/wl-paste",
		[]string{"--list-types"}, wlPasteTimeout).
		Return([]byte("\n\n"), nil)

	r, err := newWaylandClipboardReader(runner, s.log)
	s.Require().NoError(err)

	snap, err := r.Snapshot(context.Background())
	s.Require().NoError(err)
	s.True(snap.Empty)
}

// --- list-types error propagates (non-empty cause) ---

func (s *WaylandClipboardReaderSuite) TestSnapshot_ListTypesError_Propagates() {
	runner := NewMockReadRunner(s.ctrl)
	runner.EXPECT().LookPath(wlPasteBin).Return("/usr/bin/wl-paste", nil)
	runner.EXPECT().RunCapture(gomock.Any(), "/usr/bin/wl-paste",
		[]string{"--list-types"}, wlPasteTimeout).
		Return(nil, errors.New("wl-paste: compositor wedged"))

	r, err := newWaylandClipboardReader(runner, s.log)
	s.Require().NoError(err)

	snap, err := r.Snapshot(context.Background())
	s.Require().Error(err)
	s.Empty(snap.MIME)
}

// --- Fetch step empty marker → Empty=true ---

func (s *WaylandClipboardReaderSuite) TestSnapshot_FetchEmptyMarker_ReturnsEmpty() {
	runner := NewMockReadRunner(s.ctrl)
	runner.EXPECT().LookPath(wlPasteBin).Return("/usr/bin/wl-paste", nil)
	runner.EXPECT().RunCapture(gomock.Any(), "/usr/bin/wl-paste",
		[]string{"--list-types"}, wlPasteTimeout).
		Return([]byte("text/plain\n"), nil)
	runner.EXPECT().RunCapture(gomock.Any(), "/usr/bin/wl-paste",
		[]string{"--type", "text/plain", "--no-newline"}, wlPasteTimeout).
		Return(nil, errors.New("wl-paste: No selection"))

	r, err := newWaylandClipboardReader(runner, s.log)
	s.Require().NoError(err)

	snap, err := r.Snapshot(context.Background())
	s.Require().NoError(err)
	s.True(snap.Empty)
}

// --- ctx already cancelled is fail-fast ---

func (s *WaylandClipboardReaderSuite) TestSnapshot_CancelledCtx_ReturnsError() {
	runner := NewMockReadRunner(s.ctrl)
	runner.EXPECT().LookPath(wlPasteBin).Return("/usr/bin/wl-paste", nil)

	r, err := newWaylandClipboardReader(runner, s.log)
	s.Require().NoError(err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = r.Snapshot(ctx)
	s.Require().ErrorIs(err, context.Canceled)
}
