package voice

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/partyzanex/a2text/internal/domain"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"
)

type TranscribeFileSuite struct {
	suite.Suite

	ctrl        *gomock.Controller
	transcriber *MockTranscriber
	converter   *MockConverter
	output      *MockOutput
	log         *slog.Logger
}

func TestTranscribeFileSuite(t *testing.T) {
	suite.Run(t, new(TranscribeFileSuite))
}

func (s *TranscribeFileSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.transcriber = NewMockTranscriber(s.ctrl)
	s.converter = NewMockConverter(s.ctrl)
	s.output = NewMockOutput(s.ctrl)
	s.log = slog.New(slog.DiscardHandler)
}

func (s *TranscribeFileSuite) TearDownTest() {
	s.ctrl.Finish()
}

// --- Happy path ---

func (s *TranscribeFileSuite) TestRun_ConverterReturnsTempFile_TranscribeAndCleanup() {
	src := s.writeFile("irrelevant")
	wavOut := filepath.Join(s.T().TempDir(), "converted.wav")
	s.Require().NoError(os.WriteFile(wavOut, []byte("converted"), 0o600))

	cleanupCalled := atomic.Bool{}
	cleanup := func() {
		cleanupCalled.Store(true)

		_ = os.Remove(wavOut)
	}

	s.converter.EXPECT().ToWAV(gomock.Any(), src).Return(wavOut, cleanup, nil)
	s.transcriber.EXPECT().
		Transcribe(gomock.Any(), wavOut, "en").
		Return("transcribed", nil)
	s.output.EXPECT().Deliver(gomock.Any(), "transcribed").Return(nil)

	err := s.newUseCase().Run(context.Background(), src, "en")
	s.Require().NoError(err)

	s.True(cleanupCalled.Load(), "use case must invoke converter cleanup")

	_, statErr := os.Stat(wavOut)
	s.Require().ErrorIs(statErr, os.ErrNotExist, "cleanup should remove the converted file")
}

// --- BLOCKER regression: passthrough converter must not delete source ---

func (s *TranscribeFileSuite) TestRun_PassthroughConverter_DoesNotDeleteSource() {
	// Passthrough returns the input path verbatim with a no-op cleanup.
	// The use case must NEVER end up deleting the original file.
	src := s.writeFile("user-precious-audio")

	s.converter.EXPECT().
		ToWAV(gomock.Any(), src).
		Return(src, func() { /* no-op */ }, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), src, "ru").Return("ok", nil)
	s.output.EXPECT().Deliver(gomock.Any(), "ok").Return(nil)

	err := s.newUseCase().Run(context.Background(), src, "ru")
	s.Require().NoError(err)

	_, statErr := os.Stat(src)
	s.Require().NoError(statErr, "source file must survive a passthrough converter")
}

// --- Input validation ---

func (s *TranscribeFileSuite) TestRun_FileNotFound() {
	err := s.newUseCase().Run(context.Background(), "/tmp/definitely-missing.wav", "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, domain.ErrFileNotFound)
}

func (s *TranscribeFileSuite) TestRun_DirectoryInput_Rejected() {
	dir := s.T().TempDir()

	// Converter and transcriber must NOT be called.
	err := s.newUseCase().Run(context.Background(), dir, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, domain.ErrNotRegularFile)
}

// --- Error propagation ---

func (s *TranscribeFileSuite) TestRun_ConverterError_PropagatesAsWrappedError() {
	src := s.writeFile("x")

	convErr := errors.New("ffmpeg crashed")

	s.converter.EXPECT().ToWAV(gomock.Any(), src).Return("", nil, convErr)
	// Transcriber and Output must not be called.

	err := s.newUseCase().Run(context.Background(), src, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, convErr)
}

func (s *TranscribeFileSuite) TestRun_TranscriberError_CleanupStillRuns() {
	src := s.writeFile("x")
	wavOut := filepath.Join(s.T().TempDir(), "converted.wav")
	cleanupCalled := atomic.Bool{}
	sttErr := errors.New("whisper service unavailable")

	s.converter.EXPECT().ToWAV(gomock.Any(), src).Return(wavOut, func() { cleanupCalled.Store(true) }, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavOut, "ru").Return("", sttErr)

	err := s.newUseCase().Run(context.Background(), src, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttErr)
	s.True(cleanupCalled.Load(), "cleanup must run even on transcriber error")
}

func (s *TranscribeFileSuite) TestRun_OutputError_Propagates() {
	src := s.writeFile("x")
	outErr := errors.New("writer closed")

	s.converter.EXPECT().ToWAV(gomock.Any(), src).Return(src, func() {}, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), src, "ru").Return("ok", nil)
	s.output.EXPECT().Deliver(gomock.Any(), "ok").Return(outErr)

	err := s.newUseCase().Run(context.Background(), src, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, outErr)
}

// --- Empty / whitespace-only result ---

func (s *TranscribeFileSuite) TestRun_EmptyResult_ReturnsErrEmptyResult() {
	src := s.writeFile("x")

	s.converter.EXPECT().ToWAV(gomock.Any(), src).Return(src, func() {}, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), src, "ru").Return("", nil)

	err := s.newUseCase().Run(context.Background(), src, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, domain.ErrEmptyResult)
}

func (s *TranscribeFileSuite) TestRun_WhitespaceOnlyResult_ReturnsErrEmptyResult() {
	src := s.writeFile("x")

	s.converter.EXPECT().ToWAV(gomock.Any(), src).Return(src, func() {}, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), src, "ru").Return("  \n\t  ", nil)

	err := s.newUseCase().Run(context.Background(), src, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, domain.ErrEmptyResult)
}

func (s *TranscribeFileSuite) TestRun_TrimsLeadingTrailingWhitespace_FromDeliveredText() {
	src := s.writeFile("x")

	s.converter.EXPECT().ToWAV(gomock.Any(), src).Return(src, func() {}, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), src, "ru").Return("  hello\n", nil)
	s.output.EXPECT().Deliver(gomock.Any(), "hello").Return(nil)

	err := s.newUseCase().Run(context.Background(), src, "ru")
	s.Require().NoError(err)
}

// --- Context propagation ---

func (s *TranscribeFileSuite) TestRun_ContextCancel_PropagatesToTranscriber() {
	src := s.writeFile("x")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s.converter.EXPECT().ToWAV(gomock.Any(), src).Return(src, func() {}, nil)
	s.transcriber.EXPECT().
		Transcribe(gomock.Any(), src, "ru").
		DoAndReturn(func(callCtx context.Context, _ string, _ string) (string, error) {
			s.Require().ErrorIs(callCtx.Err(), context.Canceled)

			return "", callCtx.Err()
		})

	err := s.newUseCase().Run(ctx, src, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, context.Canceled)
}

// --- Helpers ---

// writeFile writes content to a fresh temp dir and returns its path.
// The file is auto-cleaned by t.TempDir() teardown. The filename is fixed
// because no test cares about the actual name.
func (s *TranscribeFileSuite) writeFile(content string) string {
	dir := s.T().TempDir()
	path := filepath.Join(dir, "audio.ogg")
	s.Require().NoError(os.WriteFile(path, []byte(content), 0o600))

	return path
}

func (s *TranscribeFileSuite) newUseCase() *TranscribeFileUseCase {
	return NewTranscribeFileUseCase(s.transcriber, s.converter, s.output, s.log)
}
