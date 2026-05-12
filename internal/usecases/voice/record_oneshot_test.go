package voice

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/partyzanex/a2text/internal/domain"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"
)

type RecordOneshotSuite struct {
	suite.Suite

	ctrl        *gomock.Controller
	recorder    *MockRecorder
	transcriber *MockTranscriber
	output      *MockOutput
	log         *slog.Logger
}

func TestRecordOneshotSuite(t *testing.T) {
	suite.Run(t, new(RecordOneshotSuite))
}

func (s *RecordOneshotSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.recorder = NewMockRecorder(s.ctrl)
	s.transcriber = NewMockTranscriber(s.ctrl)
	s.output = NewMockOutput(s.ctrl)
	s.log = slog.New(slog.DiscardHandler)
}

func (s *RecordOneshotSuite) TearDownTest() {
	s.ctrl.Finish()
}

// --- Happy path ---

func (s *RecordOneshotSuite) TestRun_RecordTranscribeDeliver_CleansUpFile() {
	wavPath := filepath.Join(s.T().TempDir(), "rec.wav")
	s.Require().NoError(os.WriteFile(wavPath, []byte("riff"), 0o600))

	s.recorder.EXPECT().
		RecordToFile(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, opts RecordOptions) (string, error) {
			s.Equal(3*time.Second, opts.Duration)
			s.Equal(16000, opts.SampleRate)
			s.Equal(1, opts.Channels)

			return wavPath, nil
		})
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("привет", nil)
	s.output.EXPECT().Deliver(gomock.Any(), "привет").Return(nil)

	err := s.newUseCase().Run(context.Background(), 3*time.Second, "ru")
	s.Require().NoError(err)

	_, statErr := os.Stat(wavPath)
	s.Require().ErrorIs(statErr, os.ErrNotExist, "use case must remove the recorder's temp file")
}

func (s *RecordOneshotSuite) TestRun_TrimsWhitespace_BeforeDelivery() {
	wavPath := filepath.Join(s.T().TempDir(), "rec.wav")
	s.Require().NoError(os.WriteFile(wavPath, []byte("riff"), 0o600))

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(wavPath, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("  hello\n", nil)
	s.output.EXPECT().Deliver(gomock.Any(), "hello").Return(nil)

	err := s.newUseCase().Run(context.Background(), 1*time.Second, "ru")
	s.Require().NoError(err)
}

// --- Validation ---

func (s *RecordOneshotSuite) TestRun_ZeroDuration_RejectedBeforeRecording() {
	// Recorder must NOT be called.
	err := s.newUseCase().Run(context.Background(), 0, "ru")
	s.Require().Error(err)
	s.Contains(err.Error(), "duration must be positive")
}

func (s *RecordOneshotSuite) TestRun_NegativeDuration_Rejected() {
	err := s.newUseCase().Run(context.Background(), -1*time.Second, "ru")
	s.Require().Error(err)
}

// --- Error propagation ---

func (s *RecordOneshotSuite) TestRun_RecorderError_Propagated_NoTranscribeNoDeliver() {
	recErr := errors.New("pw-record died")
	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return("", recErr)
	// Transcriber and Output must NOT be called.

	err := s.newUseCase().Run(context.Background(), 1*time.Second, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, recErr)
}

func (s *RecordOneshotSuite) TestRun_TranscriberError_StillCleansUpFile() {
	wavPath := filepath.Join(s.T().TempDir(), "rec.wav")
	s.Require().NoError(os.WriteFile(wavPath, []byte("riff"), 0o600))

	sttErr := errors.New("whisper unavailable")

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(wavPath, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("", sttErr)

	err := s.newUseCase().Run(context.Background(), 1*time.Second, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, sttErr)

	_, statErr := os.Stat(wavPath)
	s.Require().ErrorIs(statErr, os.ErrNotExist, "cleanup must run on transcriber error")
}

func (s *RecordOneshotSuite) TestRun_OutputError_Propagated() {
	wavPath := filepath.Join(s.T().TempDir(), "rec.wav")
	s.Require().NoError(os.WriteFile(wavPath, []byte("riff"), 0o600))

	outErr := errors.New("clipboard closed")

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(wavPath, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("ok", nil)
	s.output.EXPECT().Deliver(gomock.Any(), "ok").Return(outErr)

	err := s.newUseCase().Run(context.Background(), 1*time.Second, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, outErr)
}

// --- Empty result ---

func (s *RecordOneshotSuite) TestRun_EmptyTranscription_ReturnsErrEmptyResult() {
	wavPath := filepath.Join(s.T().TempDir(), "rec.wav")
	s.Require().NoError(os.WriteFile(wavPath, []byte("riff"), 0o600))

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(wavPath, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("  \t\n  ", nil)

	err := s.newUseCase().Run(context.Background(), 1*time.Second, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, domain.ErrEmptyResult)
}

// --- Boundary checks on Recorder return value ---

func (s *RecordOneshotSuite) TestRun_RecorderReturnsEmptyPath_Rejected() {
	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return("", nil)
	// Transcriber and Output must NOT be called.

	err := s.newUseCase().Run(context.Background(), 1*time.Second, "ru")
	s.Require().Error(err)
	s.Contains(err.Error(), "empty audio path")
}

func (s *RecordOneshotSuite) TestRun_RecorderReturnsDirectory_Rejected() {
	dir := s.T().TempDir()

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(dir, nil)

	err := s.newUseCase().Run(context.Background(), 1*time.Second, "ru")
	s.Require().Error(err)
	s.Contains(err.Error(), "not a regular file")
}

func (s *RecordOneshotSuite) TestRun_RecorderReturnsMissingFile_Rejected() {
	s.recorder.EXPECT().
		RecordToFile(gomock.Any(), gomock.Any()).
		Return("/nonexistent/a2text-rec.wav", nil)

	err := s.newUseCase().Run(context.Background(), 1*time.Second, "ru")
	s.Require().Error(err)
	s.Require().ErrorIs(err, os.ErrNotExist)
}

// --- Constructor sanity ---

func (s *RecordOneshotSuite) TestNewUseCase_PanicsOnNilDependency() {
	s.Panics(func() {
		NewRecordOneshotUseCase(nil, s.transcriber, s.output, s.log)
	})
}

// --- Helpers ---

func (s *RecordOneshotSuite) newUseCase() *RecordOneshotUseCase {
	return NewRecordOneshotUseCase(s.recorder, s.transcriber, s.output, s.log)
}
