package factory

import (
	"bytes"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/a2text/internal/adapters/output"
	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/pkg/clipboard"
)

type OutputBuilderSuite struct {
	suite.Suite

	logBuf *bytes.Buffer
	log    *slog.Logger
	ctrl   *gomock.Controller
}

func TestOutputBuilderSuite(t *testing.T) {
	suite.Run(t, new(OutputBuilderSuite))
}

func (s *OutputBuilderSuite) SetupTest() {
	s.logBuf = &bytes.Buffer{}
	s.log = slog.New(slog.NewTextHandler(s.logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s.ctrl = gomock.NewController(s.T())
}

func (s *OutputBuilderSuite) TestNilCfg_FallsBackToLog_AndLogsWarn() {
	out, err := buildOutputWith(nil, s.log, s.failingClip(), s.failingAutopaste())

	s.Require().NoError(err)
	s.IsType(&output.LogOutput{}, out)
	s.Contains(s.logBuf.String(), "nil config in BuildOutput")
}

func (s *OutputBuilderSuite) TestNilLog_DoesNotPanic() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = config.VoiceOutputModeStdout

	s.NotPanics(func() {
		_, err := buildOutputWith(cfg, nil, s.failingClip(), s.failingAutopaste())
		s.Require().NoError(err)
	})
}

func (s *OutputBuilderSuite) TestStdoutMode_ReturnsStdout_NoClipboardProbe() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = config.VoiceOutputModeStdout

	out, err := buildOutputWith(cfg, s.log, s.failingClip(), s.failingAutopaste())

	s.Require().NoError(err)
	s.IsType(&output.StdoutOutput{}, out)
}

func (s *OutputBuilderSuite) TestUnknownMode_ReturnsError() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = "telepathy"

	out, err := buildOutputWith(cfg, s.log, s.failingClip(), s.failingAutopaste())

	s.Require().Error(err)
	s.Nil(out)
	s.Contains(err.Error(), "unknown output mode")
}

func (s *OutputBuilderSuite) TestClipboardMode_BuildsClipboardOutput() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = config.VoiceOutputModeClipboard
	clip := NewMockSessionClipboard(s.ctrl)

	out, err := buildOutputWith(cfg, s.log, s.stubClip(clip, nil), s.failingAutopaste())

	s.Require().NoError(err)
	s.IsType(&output.ClipboardOutput{}, out)
}

func (s *OutputBuilderSuite) TestEmptyMode_DefaultsToClipboard() {
	cfg := &config.VoiceConfig{}
	clip := NewMockSessionClipboard(s.ctrl)

	out, err := buildOutputWith(cfg, s.log, s.stubClip(clip, nil), s.failingAutopaste())

	s.Require().NoError(err)
	s.IsType(&output.ClipboardOutput{}, out)
}

func (s *OutputBuilderSuite) TestClipboardMode_BackendErr_FallsBackToLog() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = config.VoiceOutputModeClipboard

	out, err := buildOutputWith(cfg, s.log, s.stubClip(nil, clipboard.ErrNoBackend), s.failingAutopaste())

	s.Require().NoError(err)
	s.IsType(&output.LogOutput{}, out)
	s.Contains(s.logBuf.String(), "no clipboard backend")
}

func (s *OutputBuilderSuite) TestAutopasteMode_BuildsAutopasteOutput() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = config.VoiceOutputModeClipboardAutopaste
	cfg.Output.AutopasteCommand = config.VoiceAutopasteCommandAuto
	clip := NewMockSessionClipboard(s.ctrl)
	paster := NewMockSessionAutopaster(s.ctrl)

	out, err := buildOutputWith(cfg, s.log, s.stubClip(clip, nil), s.stubAutopaste(paster, nil))

	s.Require().NoError(err)
	s.IsType(&output.ClipboardAutopasteOutput{}, out)
}

func (s *OutputBuilderSuite) TestAutopasteMode_UnsupportedBackend_ReturnsError() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = config.VoiceOutputModeClipboardAutopaste
	cfg.Output.AutopasteCommand = "rocket-launcher"
	clip := NewMockSessionClipboard(s.ctrl)
	apErr := clipboard.ErrUnsupportedAutopasteBackend

	out, err := buildOutputWith(cfg, s.log, s.stubClip(clip, nil), s.stubAutopaste(nil, apErr))

	s.Require().Error(err)
	s.Require().ErrorIs(err, clipboard.ErrUnsupportedAutopasteBackend)
	s.Nil(out)
	s.Contains(s.logBuf.String(), "unsupported autopaste backend")
}

func (s *OutputBuilderSuite) TestAutopasteMode_MissingBackend_DegradesToClipboard() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = config.VoiceOutputModeClipboardAutopaste
	cfg.Output.AutopasteCommand = config.VoiceAutopasteCommandAuto
	clip := NewMockSessionClipboard(s.ctrl)

	out, err := buildOutputWith(cfg, s.log, s.stubClip(clip, nil),
		s.stubAutopaste(nil, errors.New("wtype not found")))

	s.Require().NoError(err)
	s.IsType(&output.ClipboardOutput{}, out)
	s.Contains(s.logBuf.String(), "falling back to clipboard-only")
}

func (s *OutputBuilderSuite) TestAutopasteMode_ClipFails_FallsBackToLog_NoAutopasteProbe() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = config.VoiceOutputModeClipboardAutopaste

	out, err := buildOutputWith(cfg, s.log,
		s.stubClip(nil, clipboard.ErrNoBackend), s.failingAutopaste())

	s.Require().NoError(err)
	s.IsType(&output.LogOutput{}, out)
}

func (s *OutputBuilderSuite) TestBuildOutput_DelegatesToBuildOutputWith() {
	// Smoke test: BuildOutput must compile and not panic on a stdout cfg even
	// without injected fakes. Real session probes are not exercised here.
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = config.VoiceOutputModeStdout

	out, err := BuildOutput(cfg, s.log)

	s.Require().NoError(err)
	s.IsType(&output.StdoutOutput{}, out)
}

// --- helpers ---

// stubClip / stubAutopaste are inline builder fns that return a value or error
// without touching real Wayland/X11 detection.
func (s *OutputBuilderSuite) stubClip(clip SessionClipboard, err error) clipboardBuilderFn {
	return func(_ *slog.Logger) (SessionClipboard, error) {
		return clip, err
	}
}

func (s *OutputBuilderSuite) stubAutopaste(paster SessionAutopaster, err error) autopasteBuilderFn {
	return func(_ string, _ *slog.Logger) (SessionAutopaster, error) {
		return paster, err
	}
}

// failingClip / failingAutopaste must never be invoked. Used to assert that
// modes which short-circuit (stdout, error) do not probe clipboard or autopaste.
func (s *OutputBuilderSuite) failingClip() clipboardBuilderFn {
	return func(_ *slog.Logger) (SessionClipboard, error) {
		s.FailNow("clipboard builder must not be called")

		return nil, nil
	}
}

func (s *OutputBuilderSuite) failingAutopaste() autopasteBuilderFn {
	return func(_ string, _ *slog.Logger) (SessionAutopaster, error) {
		s.FailNow("autopaste builder must not be called")

		return nil, nil
	}
}
