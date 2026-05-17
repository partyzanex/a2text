package voice

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
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

	//nolint:SA1012 // intentional: testing nil context validation
	_, err := uc.Cycle(nil, context.Background(), domain.RecordOpts{MaxDuration: time.Second}, "ru")
	s.Require().Error(err)
	s.Contains(err.Error(), "nil")
}

func (s *VoiceUseCaseSuite) TestCycle_NilRecordCtx_ReturnsError() {
	uc := s.newUseCase()

	//nolint:SA1012 // intentional: testing nil context validation
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

// --- SwapOutput: atomic output replacement ---

// TestSwapOutput_NilReceiver_DoesNotPanic guards that SwapOutput is safe
// to call on a nil receiver (defensive).
func (s *VoiceUseCaseSuite) TestSwapOutput_NilReceiver_DoesNotPanic() {
	var uc *VoiceUseCase

	s.NotPanics(func() {
		uc.SwapOutput(s.output)
	})
}

// TestSwapOutput_NilNext_DoesNotPanic guards that passing a nil output
// is safe (no-op, keeps the previous output).
func (s *VoiceUseCaseSuite) TestSwapOutput_NilNext_DoesNotPanic() {
	uc := s.newUseCase()

	s.NotPanics(func() {
		uc.SwapOutput(nil)
	})
}

// TestSwapOutput_Swaps verifies that SwapOutput replaces the current output.
func (s *VoiceUseCaseSuite) TestSwapOutput_Swaps() {
	uc := s.newUseCase()

	newOutput := NewMockOutput(s.ctrl)
	uc.SwapOutput(newOutput)

	// Verify the swap took effect by expecting the new output to be called.
	newOutput.EXPECT().Deliver(gomock.Any(), "test").Return(nil)

	wavPath := makeWAV(s.T(), 32000)
	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(wavPath, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "en").Return("test", nil)

	_, err := uc.Cycle(context.Background(), context.Background(), domain.RecordOpts{MaxDuration: time.Second}, "en")
	s.Require().NoError(err, "new output should be used after swap")
}

// --- helpers ---

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

// --- Hot-swap (atomic Transcriber / Output) ---

// TestSwapTranscriber_NextCycleUsesNew verifies that after SwapTranscriber
// the next Cycle dispatches Transcribe to the replacement and ignores the
// original. The atomic Pointer makes this swap visible immediately to any
// subsequent cycle; in-flight cycles keep their previous reference (covered
// by the concurrency test below).
func (s *VoiceUseCaseSuite) TestSwapTranscriber_NextCycleUsesNew() {
	wavPath := makeWAV(s.T(), 32000)

	next := NewMockTranscriber(s.ctrl)

	// Original must NOT be called after swap — gomock's strict mode will
	// fail the test if it is. Note s.transcriber gets zero expectations.
	next.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("свопнули", nil)
	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(wavPath, nil)
	s.output.EXPECT().Deliver(gomock.Any(), "свопнули").Return(nil)

	uc := s.newUseCase()
	uc.SwapTranscriber(next)

	result, err := uc.Cycle(
		context.Background(), context.Background(),
		domain.RecordOpts{MaxDuration: time.Second}, "ru",
	)
	s.Require().NoError(err)
	s.Equal("свопнули", result.Text)
}

// TestSwapOutput_NextCycleUsesNew mirrors the transcriber check for Output.
func (s *VoiceUseCaseSuite) TestSwapOutput_NextCycleUsesNew() {
	wavPath := makeWAV(s.T(), 32000)

	next := NewMockOutput(s.ctrl)

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(wavPath, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("новый аутпут", nil)
	next.EXPECT().Deliver(gomock.Any(), "новый аутпут").Return(nil)

	uc := s.newUseCase()
	uc.SwapOutput(next)

	_, err := uc.Cycle(
		context.Background(), context.Background(),
		domain.RecordOpts{MaxDuration: time.Second}, "ru",
	)
	s.Require().NoError(err)
}

// TestSwapTranscriber_NilArg_KeepsCurrent confirms a nil swap is a no-op:
// the existing transcriber continues to serve cycles. Matches the guard
// in SwapTranscriber that silently ignores nil to avoid breaking a working
// pipeline with a bad config reload.
func (s *VoiceUseCaseSuite) TestSwapTranscriber_NilArg_KeepsCurrent() {
	wavPath := makeWAV(s.T(), 32000)

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(wavPath, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("оригинал", nil)
	s.output.EXPECT().Deliver(gomock.Any(), "оригинал").Return(nil)

	uc := s.newUseCase()
	uc.SwapTranscriber(nil)

	_, err := uc.Cycle(
		context.Background(), context.Background(),
		domain.RecordOpts{MaxDuration: time.Second}, "ru",
	)
	s.Require().NoError(err)
}

// TestSwapOutput_NilArg_KeepsCurrent mirrors the nil-guard check for Output.
func (s *VoiceUseCaseSuite) TestSwapOutput_NilArg_KeepsCurrent() {
	wavPath := makeWAV(s.T(), 32000)

	s.recorder.EXPECT().RecordToFile(gomock.Any(), gomock.Any()).Return(wavPath, nil)
	s.transcriber.EXPECT().Transcribe(gomock.Any(), wavPath, "ru").Return("ok", nil)
	s.output.EXPECT().Deliver(gomock.Any(), "ok").Return(nil)

	uc := s.newUseCase()
	uc.SwapOutput(nil)

	_, err := uc.Cycle(
		context.Background(), context.Background(),
		domain.RecordOpts{MaxDuration: time.Second}, "ru",
	)
	s.Require().NoError(err)
}

// TestSwap_NilReceiver_NoPanic covers the *VoiceUseCase == nil guard.
// Daemon wiring constructs the use case before any swap fires, but the
// guard exists so a wiring bug returns nil rather than crashing.
func (s *VoiceUseCaseSuite) TestSwap_NilReceiver_NoPanic() {
	var uc *VoiceUseCase

	s.NotPanics(func() { uc.SwapTranscriber(s.transcriber) })
	s.NotPanics(func() { uc.SwapOutput(s.output) })
}

// TestSwap_ConcurrentWithCycle_NoRace exercises the atomic.Pointer swap
// under -race: many concurrent cycles racing with continuous Swap calls
// must complete without the race detector firing or any panic.
// Replaces the pre-atomic implementation which had a classic
// store-while-read data race on the transcriber/output fields.
func (s *VoiceUseCaseSuite) TestSwap_ConcurrentWithCycle_NoRace() {
	wavPath := makeWAV(s.T(), 32000)

	s.expectPermissiveCycle(wavPath)
	altT, altO := s.buildSwapAlternates(3)

	uc := s.newUseCase()

	const (
		cycleWorkers = 4
		swapWorkers  = 2
		iterations   = 50
	)

	done := make(chan struct{})

	var wg sync.WaitGroup

	// Cycle workers: hammer Cycle() back-to-back until done is closed.
	for range cycleWorkers {
		wg.Go(func() {
			for {
				select {
				case <-done:
					return
				default:
				}

				_, _ = uc.Cycle(
					context.Background(), context.Background(),
					domain.RecordOpts{MaxDuration: time.Second}, "ru",
				)
			}
		})
	}

	// Swap workers: rotate transcriber + output references continuously.
	// This is the writer side of the race that atomic.Pointer fixes.
	for w := range swapWorkers {
		wg.Go(func() {
			for i := range iterations {
				idx := (w + i) % len(altT)
				uc.SwapTranscriber(altT[idx])
				uc.SwapOutput(altO[idx])
			}
		})
	}

	// Let workers run for a bounded slice of time. Long enough for the
	// race detector to observe meaningful interleaving; short enough to
	// keep the test fast.
	time.Sleep(50 * time.Millisecond)
	close(done)
	wg.Wait()
}

// newUseCase wires the use case with the suite's mocks and a discard
// logger. Reused by every Cycle test in this file.
func (s *VoiceUseCaseSuite) newUseCase() *VoiceUseCase {
	log := slog.New(slog.DiscardHandler)

	return NewVoiceUseCase(s.recorder, s.transcriber, s.output, log)
}

// expectPermissiveCycle wires AnyTimes() expectations on the suite's
// mocks so a goroutine running Cycle() in a loop never fails on call
// counts. Used by the race-detector concurrency test, where what
// matters is the absence of a data race rather than precise gomock
// accounting.
func (s *VoiceUseCaseSuite) expectPermissiveCycle(wavPath string) {
	s.recorder.EXPECT().
		RecordToFile(gomock.Any(), gomock.Any()).
		Return(wavPath, nil).
		AnyTimes()
	s.transcriber.EXPECT().
		Transcribe(gomock.Any(), gomock.Any(), gomock.Any()).
		Return("ok", nil).
		AnyTimes()
	s.output.EXPECT().
		Deliver(gomock.Any(), gomock.Any()).
		Return(nil).
		AnyTimes()
}

// buildSwapAlternates returns matching slices of Transcriber/Output
// mocks the swap workers rotate through. The first slot is the suite's
// own mocks, then n additional fresh mocks. All mocks must be created
// on the calling (test) goroutine — gomock's Controller is not safe
// for concurrent NewMock* construction.
func (s *VoiceUseCaseSuite) buildSwapAlternates(n int) ([]Transcriber, []Output) {
	transcribers := []Transcriber{s.transcriber}
	outputs := []Output{s.output}

	for range n {
		nextT := NewMockTranscriber(s.ctrl)
		nextT.EXPECT().Transcribe(gomock.Any(), gomock.Any(), gomock.Any()).Return("ok", nil).AnyTimes()
		transcribers = append(transcribers, nextT)

		nextO := NewMockOutput(s.ctrl)
		nextO.EXPECT().Deliver(gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		outputs = append(outputs, nextO)
	}

	return transcribers, outputs
}
