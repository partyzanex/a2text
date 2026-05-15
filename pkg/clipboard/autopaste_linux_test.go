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

type WaylandAutopasterSuite struct {
	suite.Suite

	log  *slog.Logger
	ctrl *gomock.Controller
}

func TestWaylandAutopasterSuite(t *testing.T) {
	suite.Run(t, new(WaylandAutopasterSuite))
}

func (s *WaylandAutopasterSuite) SetupTest() {
	s.log = slog.New(slog.DiscardHandler)
	s.ctrl = gomock.NewController(s.T())
}

// --- Backend selection ---

func (s *WaylandAutopasterSuite) TestNew_Auto_PrefersWtype() {
	runner := NewMockPasteRunner(s.ctrl)
	runner.EXPECT().LookPath("wtype").Return("/usr/bin/wtype", nil)

	a, err := newWaylandAutopaster(runner, "auto", s.log)
	s.Require().NoError(err)
	s.Equal("wtype", a.Backend(),
		"wtype must win against ydotool — simpler, no /dev/uinput dependency")
}

func (s *WaylandAutopasterSuite) TestNew_Auto_FallsBackToYdotoolWhenWtypeMissing() {
	runner := NewMockPasteRunner(s.ctrl)
	runner.EXPECT().LookPath("wtype").Return("", errors.New("not found"))
	runner.EXPECT().LookPath("ydotool").Return("/usr/bin/ydotool", nil)

	a, err := newWaylandAutopaster(runner, "auto", s.log)
	s.Require().NoError(err)
	s.Equal("ydotool", a.Backend())
}

func (s *WaylandAutopasterSuite) TestNew_Empty_TreatedAsAuto() {
	runner := NewMockPasteRunner(s.ctrl)
	runner.EXPECT().LookPath("wtype").Return("/usr/bin/wtype", nil)

	a, err := newWaylandAutopaster(runner, "", s.log)
	s.Require().NoError(err)
	s.Equal("wtype", a.Backend())
}

func (s *WaylandAutopasterSuite) TestNew_Auto_NoCandidates_ReturnsErrNoBackend() {
	runner := NewMockPasteRunner(s.ctrl)
	runner.EXPECT().LookPath("wtype").Return("", errors.New("not found"))
	runner.EXPECT().LookPath("ydotool").Return("", errors.New("not found"))
	runner.EXPECT().LookPath("xdotool").Return("", errors.New("not found"))

	a, err := newWaylandAutopaster(runner, "auto", s.log)

	s.Require().Error(err)
	s.Require().ErrorIs(err, ErrNoAutopasteBackend)
	s.Nil(a)
}

func (s *WaylandAutopasterSuite) TestNew_ExplicitBackend_Found() {
	runner := NewMockPasteRunner(s.ctrl)
	runner.EXPECT().LookPath("ydotool").Return("/usr/bin/ydotool", nil)

	paster, err := newWaylandAutopaster(runner, "ydotool", s.log)
	s.Require().NoError(err)
	s.Equal("ydotool", paster.Backend(),
		"explicit request must override the auto preference order")
}

func (s *WaylandAutopasterSuite) TestNew_ExplicitBackend_Missing_ReturnsErrNoBackend() {
	runner := NewMockPasteRunner(s.ctrl)
	runner.EXPECT().LookPath("ydotool").Return("", errors.New("not found"))

	paster, err := newWaylandAutopaster(runner, "ydotool", s.log)

	s.Require().Error(err)
	s.Require().ErrorIs(err, ErrNoAutopasteBackend)
	s.Require().ErrorContains(err, "ydotool",
		"error must name the missing binary so the operator knows what to install")
	s.Nil(paster, "explicit request must NOT silently fall back to a different binary")
}

func (s *WaylandAutopasterSuite) TestNew_UnsupportedBackend_RejectedBeforePathLookup() {
	runner := NewMockPasteRunner(s.ctrl)

	paster, err := newWaylandAutopaster(runner, "banana", s.log)

	s.Require().Error(err)
	s.Require().ErrorIs(err, ErrUnsupportedAutopasteBackend,
		"config typo must surface as a config error, NOT as 'missing dependency' — "+
			"depcheck/install hints would be misleading otherwise")
	s.Require().NotErrorIs(err, ErrNoAutopasteBackend)
	s.Nil(paster)
}

func (s *WaylandAutopasterSuite) TestNew_ExplicitXdotool_Found() {
	runner := NewMockPasteRunner(s.ctrl)
	runner.EXPECT().LookPath("xdotool").Return("/usr/bin/xdotool", nil)

	paster, err := newWaylandAutopaster(runner, "xdotool", s.log)
	s.Require().NoError(err)
	s.Equal("xdotool", paster.Backend())
}

func (s *WaylandAutopasterSuite) TestNew_Auto_FallsBackToXdotool() {
	runner := NewMockPasteRunner(s.ctrl)
	runner.EXPECT().LookPath("wtype").Return("", errors.New("not found"))
	runner.EXPECT().LookPath("ydotool").Return("", errors.New("not found"))
	runner.EXPECT().LookPath("xdotool").Return("/usr/bin/xdotool", nil)

	paster, err := newWaylandAutopaster(runner, "auto", s.log)
	s.Require().NoError(err)
	s.Equal("xdotool", paster.Backend())
}

func (s *WaylandAutopasterSuite) TestNew_ExplicitWtype_MissingButYdotoolPresent_NoFallback() {
	// Explicit "wtype" must NOT silently downgrade to ydotool even when
	// ydotool is in PATH. That fallback is reserved for "auto"; honouring
	// it for an explicit request would mislead operators who picked wtype
	// for a reason (e.g. avoiding the /dev/uinput dependency chain).
	runner := NewMockPasteRunner(s.ctrl)
	runner.EXPECT().LookPath("wtype").Return("", errors.New("not found"))

	paster, err := newWaylandAutopaster(runner, "wtype", s.log)
	s.Require().Error(err)
	s.Require().ErrorIs(err, ErrNoAutopasteBackend)
	s.Require().ErrorContains(err, "wtype",
		"error must name the missing binary so the operator knows what to install")
	s.Nil(paster)
}

func (s *WaylandAutopasterSuite) TestNew_BackendName_TrimmedAndCaseFolded() {
	// Yaml writers occasionally insert stray whitespace or capitalisation.
	// Treat " WTYPE " the same as "wtype" — the user's intent is identical.
	runner := NewMockPasteRunner(s.ctrl)
	runner.EXPECT().LookPath("wtype").Return("/usr/bin/wtype", nil)

	a, err := newWaylandAutopaster(runner, " WTYPE ", s.log)
	s.Require().NoError(err)
	s.Equal("wtype", a.Backend(),
		"backend name must be normalised before matching against the supported set")
}

func (s *WaylandAutopasterSuite) TestNew_NilRunner_ReturnsErrNoBackend() {
	a, err := newWaylandAutopaster(nil, "auto", s.log)

	s.Require().Error(err)
	s.Require().ErrorIs(err, ErrNoAutopasteBackend)
	s.Nil(a)
}

func (s *WaylandAutopasterSuite) TestNew_NilLog_DoesNotPanic() {
	runner := NewMockPasteRunner(s.ctrl)
	runner.EXPECT().LookPath("wtype").Return("/usr/bin/wtype", nil)

	a, err := newWaylandAutopaster(runner, "auto", nil)
	s.Require().NoError(err)
	s.NotNil(a)
}

// --- Paste argv per backend ---

func (s *WaylandAutopasterSuite) TestPaste_Wtype_SendsCtrlVArgv() {
	runner := NewMockPasteRunner(s.ctrl)
	runner.EXPECT().LookPath("wtype").Return("/usr/bin/wtype", nil)
	runner.EXPECT().
		Run(gomock.Any(), "/usr/bin/wtype", []string{"-M", "ctrl", "v", "-m", "ctrl"}, pasteTimeout).
		Return(nil)
	a := s.autopaster(runner, "wtype")

	s.Require().NoError(a.Paste(context.Background()))
}

func (s *WaylandAutopasterSuite) TestPaste_Ydotool_SendsKeycodes() {
	runner := NewMockPasteRunner(s.ctrl)
	runner.EXPECT().LookPath("ydotool").Return("/usr/bin/ydotool", nil)
	runner.EXPECT().
		Run(gomock.Any(), "/usr/bin/ydotool", []string{"key", "--delay", "300", "ctrl+v"}, pasteTimeout).
		Return(nil)
	a := s.autopaster(runner, "ydotool")

	s.Require().NoError(a.Paste(context.Background()))
}

// --- Paste edge cases ---

func (s *WaylandAutopasterSuite) TestPaste_CancelledContext_NoSubprocess() {
	runner := NewMockPasteRunner(s.ctrl)
	runner.EXPECT().LookPath("wtype").Return("/usr/bin/wtype", nil)
	a := s.autopaster(runner, "wtype")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := a.Paste(ctx)
	s.Require().Error(err)
	s.Require().ErrorIs(err, context.Canceled)
}

func (s *WaylandAutopasterSuite) TestPaste_SubprocessError_WrappedWithAutopastePrefix() {
	runner := NewMockPasteRunner(s.ctrl)
	runner.EXPECT().LookPath("wtype").Return("/usr/bin/wtype", nil)
	runner.EXPECT().
		Run(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(errors.New("compositor unreachable"))
	a := s.autopaster(runner, "wtype")

	err := a.Paste(context.Background())
	s.Require().Error(err)
	s.Require().ErrorContains(err, "autopaste:")
	s.Require().ErrorContains(err, "compositor unreachable")
}

func (s *WaylandAutopasterSuite) TestPaste_PassesPasteTimeoutToRunner() {
	runner := NewMockPasteRunner(s.ctrl)
	runner.EXPECT().LookPath("wtype").Return("/usr/bin/wtype", nil)
	runner.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any(), pasteTimeout).Return(nil)
	a := s.autopaster(runner, "wtype")

	s.Require().NoError(a.Paste(context.Background()))
}

// --- Defensive paths ---

func (s *WaylandAutopasterSuite) TestPaste_NilReceiver_ReturnsErrNoBackend() {
	var a *WaylandAutopaster

	err := a.Paste(context.Background())
	s.Require().ErrorIs(err, ErrNoAutopasteBackend)
}

func (s *WaylandAutopasterSuite) TestPaste_ZeroValue_ReturnsErrNoBackend() {
	a := &WaylandAutopaster{}

	err := a.Paste(context.Background())
	s.Require().ErrorIs(err, ErrNoAutopasteBackend)
}

func (s *WaylandAutopasterSuite) TestPaste_HandBuiltWithBadBackend_ReturnsUnsupported() {
	// A caller could skip NewWaylandAutopaster and assemble the struct
	// manually. If they set runner+binaryPath but a backend the daemon
	// doesn't speak, Paste must refuse rather than fork the binary with
	// no args (which would silently type nothing or hang the keystroke).
	runner := NewMockPasteRunner(s.ctrl)

	handBuilt := &WaylandAutopaster{
		runner:     runner,
		binaryPath: "/usr/bin/wtype",
		backend:    "bad",
	}

	err := handBuilt.Paste(context.Background())
	s.Require().Error(err)
	s.Require().ErrorIs(err, ErrUnsupportedAutopasteBackend)
}

func (s *WaylandAutopasterSuite) TestPaste_HandBuiltWithNilLog_NoPanic() {
	// Production callers always set log via the constructor, but a refactor
	// that exposes the struct's zero value (or a test) shouldn't crash the
	// Debug line at the end of the happy path.
	runner := NewMockPasteRunner(s.ctrl)
	runner.EXPECT().
		Run(gomock.Any(), "/usr/bin/wtype", []string{"-M", "ctrl", "v", "-m", "ctrl"}, pasteTimeout).
		Return(nil)

	handBuilt := &WaylandAutopaster{
		runner:     runner,
		binaryPath: "/usr/bin/wtype",
		backend:    autopasteBackendWtype,
		log:        nil,
	}

	s.Require().NoError(handBuilt.Paste(context.Background()))
}

func (s *WaylandAutopasterSuite) TestBackend_NilReceiver_ReturnsEmpty() {
	var a *WaylandAutopaster

	s.Empty(a.Backend())
}

// --- resolveAutopasteBackend white-box ---

func (s *WaylandAutopasterSuite) TestResolveBackend_NilRunner_ReturnsErrNoBackend() {
	_, _, err := resolveAutopasteBackend(nil, "auto")

	s.Require().ErrorIs(err, ErrNoAutopasteBackend,
		"resolveAutopasteBackend must not panic on nil runner — callers outside the constructor path must get a clean error")
}

// --- execPasteRunner white-box ---

func (s *WaylandAutopasterSuite) TestExecPasteRunner_ZeroTimeout_ReturnsError() {
	// Guard fires before any exec.Command, so no subprocess is spawned.
	// This keeps the production runner contract explicit: non-positive
	// timeouts are rejected before any subprocess is spawned.
	err := execPasteRunner{}.Run(context.Background(), "wtype", nil, 0)

	s.Require().Error(err)
	s.Require().ErrorContains(err, "timeout must be positive")
}

// --- Helpers ---

func (s *WaylandAutopasterSuite) autopaster(runner PasteRunner, backend string) *WaylandAutopaster {
	a, err := newWaylandAutopaster(runner, backend, s.log)
	s.Require().NoError(err)

	return a
}
