package voice

import (
	"context"
	"encoding/binary"
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

type VoiceUseCaseSuite struct {
	suite.Suite

	ctrl        *gomock.Controller
	recorder    *MockRecorder
	transcriber *MockTranscriber
	output      *MockOutput
}

func TestVoiceUseCaseSuite(t *testing.T) {
	suite.Run(t, new(VoiceUseCaseSuite))
}

func (s *VoiceUseCaseSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.recorder = NewMockRecorder(s.ctrl)
	s.transcriber = NewMockTranscriber(s.ctrl)
	s.output = NewMockOutput(s.ctrl)
}

func (s *VoiceUseCaseSuite) TearDownTest() {
	s.ctrl.Finish()
}

// --- Constructor contract ---

func (s *VoiceUseCaseSuite) TestNew_NilLog_DoesNotPanic() {
	s.NotPanics(func() {
		uc := NewVoiceUseCase(s.recorder, s.transcriber, s.output, nil)
		s.NotNil(uc, "nil log must produce a valid use case with a discard logger")
	})
}

func (s *VoiceUseCaseSuite) TestNew_NilRecorder_Panics() {
	s.Panics(func() {
		NewVoiceUseCase(nil, s.transcriber, s.output, nil)
	})
}

func (s *VoiceUseCaseSuite) TestNew_NilTranscriber_Panics() {
	s.Panics(func() {
		NewVoiceUseCase(s.recorder, nil, s.output, nil)
	})
}

func (s *VoiceUseCaseSuite) TestNew_NilOutput_Panics() {
	s.Panics(func() {
		NewVoiceUseCase(s.recorder, s.transcriber, nil, nil)
	})
}

// --- Nil receiver guard ---

func (s *VoiceUseCaseSuite) TestCycle_NilReceiver_ReturnsError() {
	var uc *VoiceUseCase

	_, err := uc.Cycle(context.Background(), context.Background(), domain.RecordOpts{MaxDuration: time.Second}, "ru")
	s.Require().Error(err)
	s.Contains(err.Error(), "nil")
}

// --- Nil context guard ---

func (s *VoiceUseCaseSuite) TestCycle_NilCtx_ReturnsError() {
	uc := s.newUseCase()

	_, err := uc.Cycle(nil, context.Background(), domain.RecordOpts{MaxDuration: time.Second}, "ru")
	s.Require().Error(err)
	s.Contains(err.Error(), "nil")
}

func (s *VoiceUseCaseSuite) TestCycle_NilRecordCtx_ReturnsError() {
	uc := s.newUseCase()

	_, err := uc.Cycle(context.Background(), nil, domain.RecordOpts{MaxDuration: time.Second}, "ru")
	s.Require().Error(err)
	s.Contains(err.Error(), "nil")
}

// --- MaxDuration guard ---

func (s *VoiceUseCaseSuite) TestCycle_ZeroMaxDuration_ReturnsError() {
	uc := s.newUseCase()

	// recorder must NOT be called
	_, err := uc.Cycle(context.Background(), context.Background(), domain.RecordOpts{MaxDuration: 0}, "ru")
	s.Require().Error(err)
	s.Contains(err.Error(), "MaxDuration")
}

func (s *VoiceUseCaseSuite) TestCycle_NegativeMaxDuration_ReturnsError() {
	uc := s.newUseCase()

	_, err := uc.Cycle(context.Background(), context.Background(), domain.RecordOpts{MaxDuration: -time.Second}, "ru")
	s.Require().Error(err)
}

// --- Whitespace-only lang ---

func (s *VoiceUseCaseSuite) TestCycle_WhitespaceOnlyLang_ReturnsError() {
	uc := s.newUseCase()

	// recorder must NOT be called
	_, err := uc.Cycle(context.Background(), context.Background(), domain.RecordOpts{MaxDuration: time.Second}, "   ")
	s.Require().Error(err)
}

// --- Happy path: record → transcribe → deliver ---

func (s *VoiceUseCaseSuite) TestCycle_HappyPath_CleansUpRecorderFile() {
	wavPath := makeWAV(s.T(), 32000)

	opts := domain.RecordOpts{MaxDuration: time.Second}

	s.recorder.EXPECT().
		RecordToFile(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, got RecordOptions) (string, error) {
			s.Equal(opts.MaxDuration, got.Duration)
			s.Equal(16000, got.SampleRate)
			s.Equal(1, got.Channels)

			return wavPath, nil
		})
	s.transcriber.EXPECT().
		Transcribe(gomock.Any(), wavPath, "ru").
		Return("привет", nil)
	s.output.EXPECT().
		Deliver(gomock.Any(), "привет").
		Return(nil)

	uc := s.newUseCase()
	result, err := uc.Cycle(context.Background(), context.Background(), opts, "ru")
	s.Require().NoError(err)
	s.Equal("привет", result.Text)
	s.Positive(result.AudioDuration, "payload > wavHeaderBytes so duration must be > 0")

	_, statErr := os.Stat(wavPath)
	s.Require().ErrorIs(statErr, os.ErrNotExist, "Cycle must delete the recorder's temp file")
}

// --- Lang is trimmed ---

func (s *VoiceUseCaseSuite) TestCycle_LangIsTrimmed() {
	wavPath := makeWAV(s.T(), 32000)

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(wavPath, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("ok", nil)
	s.output.EXPECT().Deliver(gomock.Any(), "ok").Return(nil)

	uc := s.newUseCase()
	_, err := uc.Cycle(context.Background(), context.Background(), domain.RecordOpts{MaxDuration: time.Second}, "  ru  ")
	s.Require().NoError(err)
}

// --- Recorder returns empty path ---

func (s *VoiceUseCaseSuite) TestCycle_RecorderReturnsEmptyPath_PhaseRecord() {
	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return("", nil)

	uc := s.newUseCase()
	_, err := uc.Cycle(context.Background(), context.Background(), domain.RecordOpts{MaxDuration: time.Second}, "ru")
	s.Require().Error(err)

	var ce *domain.CycleError
	s.Require().ErrorAs(err, &ce)
	s.Equal(domain.PhaseRecord, ce.Phase)
}

// --- Recorder error ---

func (s *VoiceUseCaseSuite) TestCycle_RecorderError_PhaseRecord() {
	recErr := errors.New("mic not found")
	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return("", recErr)

	uc := s.newUseCase()
	_, err := uc.Cycle(context.Background(), context.Background(), domain.RecordOpts{MaxDuration: time.Second}, "ru")
	s.Require().ErrorIs(err, recErr)

	var ce *domain.CycleError
	s.Require().ErrorAs(err, &ce)
	s.Equal(domain.PhaseRecord, ce.Phase)
}

// --- Recorder returns a directory (non-regular file) ---

func (s *VoiceUseCaseSuite) TestCycle_RecorderReturnsDir_PhaseRecord() {
	dirPath := s.T().TempDir()

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(dirPath, nil)

	uc := s.newUseCase()
	_, err := uc.Cycle(context.Background(), context.Background(), domain.RecordOpts{MaxDuration: time.Second}, "ru")
	s.Require().Error(err)

	var ce *domain.CycleError
	s.Require().ErrorAs(err, &ce)
	s.Equal(domain.PhaseRecord, ce.Phase)
	// transcriber must not be called (gomock controller will catch unexpected calls)
}

// --- Transcriber error ---

func (s *VoiceUseCaseSuite) TestCycle_TranscriberError_PhaseTranscribe() {
	wavPath := makeWAV(s.T(), 32000)
	sttErr := errors.New("stt failed")

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(wavPath, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("", sttErr)

	uc := s.newUseCase()
	_, err := uc.Cycle(context.Background(), context.Background(), domain.RecordOpts{MaxDuration: time.Second}, "ru")
	s.Require().ErrorIs(err, sttErr)

	var ce *domain.CycleError
	s.Require().ErrorAs(err, &ce)
	s.Equal(domain.PhaseTranscribe, ce.Phase)
}

// --- Transcriber error cleans up temp file ---

func (s *VoiceUseCaseSuite) TestCycle_TranscriberError_CleansUpFile() {
	wavPath := makeWAV(s.T(), 32000)

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(wavPath, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("", errors.New("stt down"))

	uc := s.newUseCase()
	_, _ = uc.Cycle(context.Background(), context.Background(), domain.RecordOpts{MaxDuration: time.Second}, "ru")

	_, statErr := os.Stat(wavPath)
	s.Require().ErrorIs(statErr, os.ErrNotExist, "temp file must be cleaned up even when transcriber fails")
}

// --- Empty text wraps domain.ErrEmptyResult with domain.PhaseTranscribe ---

func (s *VoiceUseCaseSuite) TestCycle_EmptyText_ErrEmptyResultWithPhaseTranscribe() {
	wavPath := makeWAV(s.T(), 32000)

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(wavPath, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("   ", nil)

	uc := s.newUseCase()
	_, err := uc.Cycle(context.Background(), context.Background(), domain.RecordOpts{MaxDuration: time.Second}, "ru")
	s.Require().ErrorIs(err, domain.ErrEmptyResult)

	var ce *domain.CycleError
	s.Require().ErrorAs(err, &ce)
	s.Equal(domain.PhaseTranscribe, ce.Phase, "empty result must be tagged domain.PhaseTranscribe")
}

// --- Empty text cleans up temp file ---

func (s *VoiceUseCaseSuite) TestCycle_EmptyText_CleansUpFile() {
	wavPath := makeWAV(s.T(), 32000)

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(wavPath, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("", nil)

	uc := s.newUseCase()
	_, _ = uc.Cycle(context.Background(), context.Background(), domain.RecordOpts{MaxDuration: time.Second}, "ru")

	_, statErr := os.Stat(wavPath)
	s.Require().ErrorIs(statErr, os.ErrNotExist, "temp file must be cleaned up even when result is empty")
}

// --- Output error ---

func (s *VoiceUseCaseSuite) TestCycle_OutputError_PhaseDeliver() {
	wavPath := makeWAV(s.T(), 32000)
	delivErr := errors.New("clipboard died")

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(wavPath, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("hello", nil)
	s.output.EXPECT().Deliver(gomock.Any(), "hello").Return(delivErr)

	uc := s.newUseCase()
	_, err := uc.Cycle(context.Background(), context.Background(), domain.RecordOpts{MaxDuration: time.Second}, "ru")
	s.Require().ErrorIs(err, delivErr)

	var ce *domain.CycleError
	s.Require().ErrorAs(err, &ce)
	s.Equal(domain.PhaseDeliver, ce.Phase)
}

// --- Output error cleans up temp file ---

func (s *VoiceUseCaseSuite) TestCycle_OutputError_CleansUpFile() {
	wavPath := makeWAV(s.T(), 32000)

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(wavPath, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("hello", nil)
	s.output.EXPECT().Deliver(gomock.Any(), "hello").Return(errors.New("output down"))

	uc := s.newUseCase()
	_, _ = uc.Cycle(context.Background(), context.Background(), domain.RecordOpts{MaxDuration: time.Second}, "ru")

	_, statErr := os.Stat(wavPath)
	s.Require().ErrorIs(statErr, os.ErrNotExist)
}

// --- recordCtx cancel: transcribe/deliver must still run ---
//
// The daemon cancels recordCtx to stop recording (toggle-off). Cycle must
// continue to transcription with whatever audio was captured.

func (s *VoiceUseCaseSuite) TestCycle_RecordCtxCancel_TranscribeAndDeliverContinue() {
	wavPath := makeWAV(s.T(), 32000)

	cycleCtx := context.Background()
	recordCtx, cancelRecord := context.WithCancel(cycleCtx)

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ RecordOptions) (string, error) {
			cancelRecord() // user pressed stop

			return wavPath, nil
		})
	// transcriber and output MUST still be called even though recordCtx was cancelled
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("text", nil)
	s.output.EXPECT().Deliver(gomock.Any(), "text").Return(nil)

	uc := s.newUseCase()
	result, err := uc.Cycle(cycleCtx, recordCtx, domain.RecordOpts{MaxDuration: time.Second}, "ru")
	s.Require().NoError(err)
	s.Equal("text", result.Text)
}

// --- cycleCtx cancel: transcribe and deliver must be skipped ---
//
// The daemon cancels cycleCtx for "discard" (throw away the recording).
// Cycle must NOT transcribe or deliver.

func (s *VoiceUseCaseSuite) TestCycle_CycleCtxCancel_SkipsTranscribeAndDeliver() {
	wavPath := makeWAV(s.T(), 32000)

	cycleCtx, cancelCycle := context.WithCancel(context.Background())

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ RecordOptions) (string, error) {
			cancelCycle() // discard: kill the whole cycle

			return wavPath, nil
		})
	// transcriber and output must NOT be called

	uc := s.newUseCase()
	_, err := uc.Cycle(cycleCtx, cycleCtx, domain.RecordOpts{MaxDuration: time.Second}, "ru")
	s.Require().ErrorIs(err, context.Canceled)

	var ce *domain.CycleError
	s.Require().ErrorAs(err, &ce)
	s.Equal(domain.PhaseTranscribe, ce.Phase, "ctx-cancelled after record must be tagged domain.PhaseTranscribe")
}

// --- cycleCtx cancel cleans up file ---

func (s *VoiceUseCaseSuite) TestCycle_CycleCtxCancel_CleansUpFile() {
	wavPath := makeWAV(s.T(), 32000)

	cycleCtx, cancelCycle := context.WithCancel(context.Background())

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ RecordOptions) (string, error) {
			cancelCycle()

			return wavPath, nil
		})

	uc := s.newUseCase()
	_, _ = uc.Cycle(cycleCtx, cycleCtx, domain.RecordOpts{MaxDuration: time.Second}, "ru")

	_, statErr := os.Stat(wavPath)
	s.Require().ErrorIs(statErr, os.ErrNotExist)
}

// --- AudioDuration is non-zero for sufficiently large WAV ---

func (s *VoiceUseCaseSuite) TestCycle_AudioDuration_1SecondPayload() {
	wavPath := makeWAV(s.T(), 32000) // 32000 bytes payload / 32000 B/s = 1 s

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(wavPath, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("ok", nil)
	s.output.EXPECT().Deliver(gomock.Any(), "ok").Return(nil)

	uc := s.newUseCase()
	opts := domain.RecordOpts{MaxDuration: 5 * time.Second}
	result, err := uc.Cycle(context.Background(), context.Background(), opts, "ru")
	s.Require().NoError(err)
	s.Equal(time.Second, result.AudioDuration, "32000 payload bytes / 32000 B/s = 1 s")
}

func (s *VoiceUseCaseSuite) TestCycle_AudioDuration_HalfSecondPayload() {
	wavPath := makeWAV(s.T(), 16000) // 16000 bytes payload / 32000 B/s = 500 ms

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(wavPath, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("ok", nil)
	s.output.EXPECT().Deliver(gomock.Any(), "ok").Return(nil)

	uc := s.newUseCase()
	opts := domain.RecordOpts{MaxDuration: 5 * time.Second}
	result, err := uc.Cycle(context.Background(), context.Background(), opts, "ru")
	s.Require().NoError(err)
	s.Equal(500*time.Millisecond, result.AudioDuration, "16000 payload bytes / 32000 B/s = 500 ms")
}

func (s *VoiceUseCaseSuite) TestCycle_AudioDuration_WAVHeaderNotCountedInPayload() {
	// File with exactly wavHeaderBytes (44) bytes → payload = 0 → duration = 0.
	// Verifies the header subtraction is applied correctly.
	path := filepath.Join(s.T().TempDir(), "tiny.wav")
	s.Require().NoError(os.WriteFile(path, make([]byte, 44), 0o600))

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(path, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), path, "ru").Return("ok", nil)
	s.output.EXPECT().Deliver(gomock.Any(), "ok").Return(nil)

	uc := s.newUseCase()
	result, err := uc.Cycle(context.Background(), context.Background(), domain.RecordOpts{MaxDuration: time.Second}, "ru")
	s.Require().NoError(err)
	s.Zero(result.AudioDuration, "file with only header bytes and no payload must give 0 duration")
}

// --- domain.CycleError.Error nil safety ---

func (s *VoiceUseCaseSuite) TestCycleError_NilReceiver_DoesNotPanic() {
	var ce *domain.CycleError

	s.NotPanics(func() {
		msg := ce.Error()
		s.NotEmpty(msg)
	})
}

func (s *VoiceUseCaseSuite) TestCycleError_NilErr_DoesNotPanic() {
	ce := &domain.CycleError{Phase: domain.PhaseRecord, Err: nil}

	s.NotPanics(func() {
		msg := ce.Error()
		s.Contains(msg, "record")
	})
}

// --- helpers ---

func (s *VoiceUseCaseSuite) newUseCase() *VoiceUseCase {
	log := slog.New(slog.DiscardHandler)

	return NewVoiceUseCase(s.recorder, s.transcriber, s.output, log)
}

// makeWAV writes a minimal but structurally valid PCM WAV file with payloadBytes
// bytes of audio data and returns its path.
//
// Format: 16kHz, mono, 16-bit signed LE (32000 bytes/s).
func makeWAV(tb testing.TB, payloadBytes int) string {
	tb.Helper()

	const (
		sampleRate    = 16000
		numChannels   = 1
		bitsPerSample = 16
		byteRate      = sampleRate * numChannels * bitsPerSample / 8
		blockAlign    = numChannels * bitsPerSample / 8
		fmtChunkSize  = 16
		wavHeaderSize = 44
	)

	buf := make([]byte, wavHeaderSize+payloadBytes)
	put := func(off int, val uint32) { binary.LittleEndian.PutUint32(buf[off:], val) }
	put16 := func(off int, val uint16) { binary.LittleEndian.PutUint16(buf[off:], val) }

	// RIFF chunk.
	copy(buf[0:], "RIFF")
	put(4, uint32(36+payloadBytes))
	copy(buf[8:], "WAVE")

	// fmt sub-chunk.
	copy(buf[12:], "fmt ")
	put(16, fmtChunkSize)
	put16(20, 1) // PCM
	put16(22, numChannels)
	put(24, sampleRate)
	put(28, byteRate)
	put16(32, blockAlign)
	put16(34, bitsPerSample)

	// data sub-chunk (audio payload stays zero — silence).
	copy(buf[36:], "data")
	put(40, uint32(payloadBytes))

	path := filepath.Join(tb.TempDir(), "rec.wav")

	if err := os.WriteFile(path, buf, 0o600); err != nil {
		tb.Fatalf("makeWAV: %v", err)
	}

	return path
}
