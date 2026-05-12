package stt

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/a2text/pkg/sttx"
)

type FallbackSuite struct {
	suite.Suite

	ctrl *gomock.Controller
}

func TestFallbackSuite(t *testing.T) {
	suite.Run(t, new(FallbackSuite))
}

func (s *FallbackSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
}

func newTestFallback(primary, secondary STTBackend) *FallbackTranscriber {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	return NewFallbackTranscriber(primary, secondary, log)
}

// --- Transcribe ---

func (s *FallbackSuite) TestTranscribe_PrimarySucceeds_ReturnsPrimaryResult() {
	primary := NewMockSTTBackend(s.ctrl)
	secondary := NewMockSTTBackend(s.ctrl)

	primary.EXPECT().Transcribe(gomock.Any(), "x.wav", "ru").Return("primary result", nil)

	f := newTestFallback(primary, secondary)
	result, err := f.Transcribe(context.Background(), "x.wav", "ru")
	s.Require().NoError(err)
	s.Equal("primary result", result)
}

func (s *FallbackSuite) TestTranscribe_PrimaryFails_SecondarySucceeds_ReturnsSecondaryResult() {
	primary := NewMockSTTBackend(s.ctrl)
	secondary := NewMockSTTBackend(s.ctrl)

	primary.EXPECT().Transcribe(gomock.Any(), "x.wav", "ru").Return("", errors.New("primary error"))
	secondary.EXPECT().Transcribe(gomock.Any(), "x.wav", "ru").Return("from cloud", nil)

	f := newTestFallback(primary, secondary)
	result, err := f.Transcribe(context.Background(), "x.wav", "ru")
	s.Require().NoError(err)
	s.Equal("from cloud", result)
}

func (s *FallbackSuite) TestTranscribe_BothFail_ReturnsTranscribeFailed() {
	primary := NewMockSTTBackend(s.ctrl)
	secondary := NewMockSTTBackend(s.ctrl)

	primary.EXPECT().Transcribe(gomock.Any(), "x.wav", "ru").Return("", errors.New("primary error"))
	secondary.EXPECT().Transcribe(gomock.Any(), "x.wav", "ru").Return("", errors.New("secondary error"))

	f := newTestFallback(primary, secondary)
	_, err := f.Transcribe(context.Background(), "x.wav", "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
	s.Contains(err.Error(), "primary error")
	s.Contains(err.Error(), "secondary error")
}

func (s *FallbackSuite) TestTranscribe_PrimaryFails_ContextCanceled_NoFallback() {
	primary := NewMockSTTBackend(s.ctrl)
	secondary := NewMockSTTBackend(s.ctrl)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	primary.EXPECT().Transcribe(ctx, "x.wav", "ru").Return("", sttx.ErrTranscribeFailed)

	f := newTestFallback(primary, secondary)
	_, err := f.Transcribe(ctx, "x.wav", "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrTranscribeFailed)
}

// --- LoadModel ---

func (s *FallbackSuite) TestLoadModel_DelegatesToPrimaryOnly() {
	loadErr := errors.New("load failed")
	primary := NewMockSTTBackend(s.ctrl)
	secondary := NewMockSTTBackend(s.ctrl)

	primary.EXPECT().LoadModel("/model.bin").Return(loadErr)

	f := newTestFallback(primary, secondary)
	err := f.LoadModel("/model.bin")
	s.Require().Error(err)
	s.ErrorIs(err, loadErr)
}

func (s *FallbackSuite) TestLoadModel_PrimarySuccess_ReturnsNil() {
	primary := NewMockSTTBackend(s.ctrl)
	secondary := NewMockSTTBackend(s.ctrl)

	primary.EXPECT().LoadModel("/model.bin").Return(nil)

	f := newTestFallback(primary, secondary)
	s.Require().NoError(f.LoadModel("/model.bin"))
}

// --- Close ---

func (s *FallbackSuite) TestClose_BothSucceed_ReturnsNil() {
	primary := NewMockSTTBackend(s.ctrl)
	secondary := NewMockSTTBackend(s.ctrl)

	primary.EXPECT().Close().Return(nil)
	secondary.EXPECT().Close().Return(nil)

	f := newTestFallback(primary, secondary)
	s.Require().NoError(f.Close())
}

func (s *FallbackSuite) TestClose_JoinsBothErrors() {
	primary := NewMockSTTBackend(s.ctrl)
	secondary := NewMockSTTBackend(s.ctrl)

	primary.EXPECT().Close().Return(errors.New("primary close error"))
	secondary.EXPECT().Close().Return(errors.New("secondary close error"))

	f := newTestFallback(primary, secondary)
	err := f.Close()
	s.Require().Error(err)
	s.Contains(err.Error(), "primary close error")
	s.Contains(err.Error(), "secondary close error")
}

func (s *FallbackSuite) TestClose_OnlyPrimaryFails_ReturnsError() {
	primary := NewMockSTTBackend(s.ctrl)
	secondary := NewMockSTTBackend(s.ctrl)

	primary.EXPECT().Close().Return(errors.New("primary close error"))
	secondary.EXPECT().Close().Return(nil)

	f := newTestFallback(primary, secondary)
	err := f.Close()
	s.Require().Error(err)
	s.Contains(err.Error(), "primary close error")
}
