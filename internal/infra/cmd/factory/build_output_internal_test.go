package factory

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/pkg/clipboard"
)

func clipOK(ctrl *gomock.Controller) clipboardBuilderFn {
	return func(_ *slog.Logger) (SessionClipboard, error) {
		clip := NewMockSessionClipboard(ctrl)
		clip.EXPECT().Copy(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

		return clip, nil
	}
}

func clipFail(err error) clipboardBuilderFn {
	return func(_ *slog.Logger) (SessionClipboard, error) {
		return nil, err
	}
}

func autopasteOK(ctrl *gomock.Controller) autopasteBuilderFn {
	return func(_ string, _ *slog.Logger) (SessionAutopaster, error) {
		paster := NewMockSessionAutopaster(ctrl)
		paster.EXPECT().Paste(gomock.Any()).Return(nil).AnyTimes()

		return paster, nil
	}
}

func autopasteNoBackend() autopasteBuilderFn {
	return func(_ string, _ *slog.Logger) (SessionAutopaster, error) {
		return nil, clipboard.ErrNoAutopasteBackend
	}
}

func autopasteUnsupported() autopasteBuilderFn {
	return func(cmd string, _ *slog.Logger) (SessionAutopaster, error) {
		return nil, errors.Join(clipboard.ErrUnsupportedAutopasteBackend, errors.New(cmd))
	}
}

func autopasteCapture(ctrl *gomock.Controller, got *string) autopasteBuilderFn {
	return func(cmd string, _ *slog.Logger) (SessionAutopaster, error) {
		*got = cmd
		paster := NewMockSessionAutopaster(ctrl)
		paster.EXPECT().Paste(gomock.Any()).Return(nil).AnyTimes()

		return paster, nil
	}
}

// --- suite ---

type BuildOutputSuite struct {
	suite.Suite

	ctrl *gomock.Controller
}

func TestBuildOutputSuite(t *testing.T) {
	suite.Run(t, new(BuildOutputSuite))
}

func (s *BuildOutputSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
}

// --- nil cfg ---

func (s *BuildOutputSuite) TestNilConfig_ReturnsStdout() {
	out, err := buildOutputWith(nil, nil, clipOK(s.ctrl), autopasteOK(s.ctrl))
	s.Require().NoError(err)
	s.Require().NotNil(out)
	s.NoError(out.Deliver(s.T().Context(), "test"))
}

// --- stdout mode ---

func (s *BuildOutputSuite) TestStdoutMode_NestedField() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = config.VoiceOutputModeStdout

	out, err := buildOutputWith(cfg, nil, clipOK(s.ctrl), autopasteOK(s.ctrl))
	s.Require().NoError(err)
	s.NoError(out.Deliver(s.T().Context(), "test"))
}

// --- unknown mode ---

func (s *BuildOutputSuite) TestUnknownMode_ReturnsError() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = "garbage"

	_, err := buildOutputWith(cfg, nil, clipOK(s.ctrl), autopasteOK(s.ctrl))
	s.Require().Error(err)
	s.Contains(err.Error(), "garbage", "error must mention the unknown mode value")
}

// --- clipboard mode ---

func (s *BuildOutputSuite) TestClipboardMode_NoBackend_FallsBackToStdout() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = config.VoiceOutputModeClipboard

	out, err := buildOutputWith(cfg, nil, clipFail(clipboard.ErrNoBackend), autopasteOK(s.ctrl))
	s.Require().NoError(err)
	s.NoError(out.Deliver(s.T().Context(), "test"))
}

func (s *BuildOutputSuite) TestClipboardMode_DoesNotCallAutopaste() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = config.VoiceOutputModeClipboard

	autopasteCalled := false
	_, err := buildOutputWith(cfg, nil, clipOK(s.ctrl), func(_ string, _ *slog.Logger) (SessionAutopaster, error) {
		autopasteCalled = true

		return NewMockSessionAutopaster(s.ctrl), nil
	})
	s.Require().NoError(err)
	s.False(autopasteCalled, "autopaste must not be called for plain clipboard mode")
}

// --- clipboard_autopaste mode ---

func (s *BuildOutputSuite) TestAutopasteMode_PassesCmdToBuilder() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = config.VoiceOutputModeClipboardAutopaste
	cfg.Output.AutopasteCommand = config.VoiceAutopasteCommandWtype

	var gotCmd string

	out, err := buildOutputWith(cfg, nil, clipOK(s.ctrl), autopasteCapture(s.ctrl, &gotCmd))
	s.Require().NoError(err)
	s.Equal(config.VoiceAutopasteCommandWtype, gotCmd)
	s.NoError(out.Deliver(s.T().Context(), "test"))
}

func (s *BuildOutputSuite) TestAutopasteMode_NoBinary_FallsBackToClipboard() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = config.VoiceOutputModeClipboardAutopaste
	cfg.Output.AutopasteCommand = config.VoiceAutopasteCommandAuto

	out, err := buildOutputWith(cfg, nil, clipOK(s.ctrl), autopasteNoBackend())
	s.Require().NoError(err)
	s.NoError(out.Deliver(s.T().Context(), "test"))
}

func (s *BuildOutputSuite) TestAutopasteMode_UnsupportedBackend_ReturnsError() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = config.VoiceOutputModeClipboardAutopaste
	cfg.Output.AutopasteCommand = "xdotool"

	_, err := buildOutputWith(cfg, nil, clipOK(s.ctrl), autopasteUnsupported())
	s.Require().Error(err)
	s.ErrorIs(err, clipboard.ErrUnsupportedAutopasteBackend)
}

func (s *BuildOutputSuite) TestAutopasteMode_ClipboardFails_FallsBackToStdout() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = config.VoiceOutputModeClipboardAutopaste

	out, err := buildOutputWith(cfg, nil, clipFail(clipboard.ErrNoBackend), autopasteOK(s.ctrl))
	s.Require().NoError(err)
	s.NoError(out.Deliver(s.T().Context(), "test"))
}

// --- empty mode defaults to clipboard ---

func (s *BuildOutputSuite) TestEmptyMode_ClipboardBuilderCalled() {
	cfg := &config.VoiceConfig{}

	clipCalled := false
	_, err := buildOutputWith(cfg, nil,
		func(_ *slog.Logger) (SessionClipboard, error) {
			clipCalled = true
			clip := NewMockSessionClipboard(s.ctrl)
			clip.EXPECT().Copy(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

			return clip, nil
		},
		autopasteOK(s.ctrl),
	)
	s.Require().NoError(err)
	s.True(clipCalled, "clipboard builder must be called when mode is empty (default)")
}

// --- nil logger ---

func (s *BuildOutputSuite) TestNilLogger_StdoutMode_DoesNotPanic() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = config.VoiceOutputModeStdout

	s.NotPanics(func() {
		out, err := buildOutputWith(cfg, nil, clipOK(s.ctrl), autopasteOK(s.ctrl))
		s.Require().NoError(err)
		s.Require().NotNil(out)
	})
}

func (s *BuildOutputSuite) TestNilLogger_ClipboardMode_DoesNotPanic() {
	cfg := &config.VoiceConfig{}
	cfg.Output.Mode = config.VoiceOutputModeClipboard

	s.NotPanics(func() {
		_, _ = buildOutputWith(cfg, nil, clipFail(clipboard.ErrNoBackend), autopasteNoBackend())
	})
}
