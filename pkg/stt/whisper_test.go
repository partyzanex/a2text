//go:build whisper

package stt

import (
	"bytes"
	"context"
	"encoding/binary"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/a2text/pkg/sttx"
)

// modelPathEnv is the env var pointing to a real GGML model file.
// If unset, all tests in this suite are skipped.
const modelPathEnv = "WHISPER_MODEL_PATH"

type WhisperSuite struct {
	suite.Suite

	w       *WhisperTranscriber
	tempDir string
	ctx     context.Context
}

func TestWhisperSuite(t *testing.T) {
	suite.Run(t, new(WhisperSuite))
}

func (s *WhisperSuite) SetupSuite() {
	modelPath := os.Getenv(modelPathEnv)
	if modelPath == "" {
		s.T().Skipf("set %s to a GGML model path to run whisper integration tests", modelPathEnv)
	}

	s.ctx = context.Background()

	var err error

	s.tempDir, err = os.MkdirTemp("", "whisper-test-*")
	s.Require().NoError(err)

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	s.w = NewWhisperTranscriber(log)
	s.Require().NoError(s.w.LoadModel(modelPath))
}

func (s *WhisperSuite) TearDownSuite() {
	if s.w != nil {
		_ = s.w.Close()
	}

	if s.tempDir != "" {
		_ = os.RemoveAll(s.tempDir)
	}
}

// --- NewWhisperTranscriber ---

// TestNewWhisperTranscriber_NilLoggerFallsBackToDefault covers the nil-log guard.
func (s *WhisperSuite) TestNewWhisperTranscriber_NilLoggerFallsBackToDefault() {
	w := NewWhisperTranscriber(nil)
	s.NotNil(w)
	s.NotNil(w.log)
	_ = w.Close() // must not panic
}

// --- LoadModel ---

// TestLoadModel_EmptyPath covers the empty-path guard.
func (s *WhisperSuite) TestLoadModel_EmptyPath() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	w := NewWhisperTranscriber(log)
	defer func() { _ = w.Close() }()

	err := w.LoadModel("")
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}

// TestLoadModel_FileNotFound covers the os.Stat failure branch.
func (s *WhisperSuite) TestLoadModel_FileNotFound() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	w := NewWhisperTranscriber(log)
	defer func() { _ = w.Close() }()

	err := w.LoadModel(filepath.Join(s.tempDir, "nonexistent.bin"))
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}

// TestLoadModel_NotAModel covers whisper_init_from_file returning nil (invalid file).
func (s *WhisperSuite) TestLoadModel_NotAModel() {
	path := filepath.Join(s.tempDir, "fake.bin")
	s.Require().NoError(os.WriteFile(path, []byte("this is definitely not a ggml model"), 0o600))

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	w := NewWhisperTranscriber(log)
	defer func() { _ = w.Close() }()

	err := w.LoadModel(path)
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}

// TestLoadModel_ReplacesExistingModel covers the whisper_free(w.ctx) branch
// triggered when LoadModel is called while a model is already loaded.
func (s *WhisperSuite) TestLoadModel_ReplacesExistingModel() {
	modelPath := os.Getenv(modelPathEnv)

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	w := NewWhisperTranscriber(log)
	defer func() { _ = w.Close() }()

	s.Require().NoError(w.LoadModel(modelPath))
	// Second load: old ctx must be freed, new one loaded.
	s.Require().NoError(w.LoadModel(modelPath))

	// Should still be usable after reload.
	_, err := w.Transcribe(s.ctx, s.writeSilenceWAV("reload.wav", 0.5), "ru")
	s.Require().NoError(err)
}

// --- Transcribe: model state ---

// TestTranscribe_ModelNotLoaded covers the w.ctx == nil guard inside the mutex.
func (s *WhisperSuite) TestTranscribe_ModelNotLoaded() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWhisperTranscriber(log)

	_, err := w.Transcribe(s.ctx, s.writeSilenceWAV("no-model.wav", 1.0), "ru")
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}

// TestTranscribeAfterClose covers w.ctx == nil after Close.
func (s *WhisperSuite) TestTranscribeAfterClose() {
	modelPath := os.Getenv(modelPathEnv)

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWhisperTranscriber(log)
	s.Require().NoError(w.LoadModel(modelPath))
	s.Require().NoError(w.Close())

	_, err := w.Transcribe(s.ctx, s.writeSilenceWAV("after-close.wav", 0.5), "ru")
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}

// --- Transcribe: audio content ---

// TestTranscribe_SilenceReturnsEmptyOrShort is the baseline happy-path test.
func (s *WhisperSuite) TestTranscribe_SilenceReturnsEmptyOrShort() {
	result, err := s.w.Transcribe(s.ctx, s.writeSilenceWAV("silence.wav", 2.0), "ru")
	s.Require().NoError(err)
	s.T().Logf("silence result: %q", result)
}

// TestTranscribe_EmptyAudio covers the len(samples)==0 guard:
// a valid WAV container with a 0-byte data chunk.
func (s *WhisperSuite) TestTranscribe_EmptyAudio() {
	_, err := s.w.Transcribe(s.ctx, s.writeSilenceWAV("empty.wav", 0.0), "ru")
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}

// TestTranscribe_TruncatedAudio covers the ReadAll error branch:
// the WAV header claims N samples but the file has no actual audio data.
func (s *WhisperSuite) TestTranscribe_TruncatedAudio() {
	_, err := s.w.Transcribe(s.ctx, s.writeTruncatedWAV("truncated.wav"), "ru")
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}

// TestTranscribe_WrongSampleRate covers wav.Open rejection of 44100 Hz audio.
func (s *WhisperSuite) TestTranscribe_WrongSampleRate() {
	_, err := s.w.Transcribe(s.ctx, s.writeWAVWithFormat("wrong-rate.wav", 44100, 1, 44100), "ru")
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}

// TestTranscribe_StereoAudio covers wav.Open rejection of 2-channel audio.
func (s *WhisperSuite) TestTranscribe_StereoAudio() {
	_, err := s.w.Transcribe(s.ctx, s.writeWAVWithFormat("stereo.wav", 16000, 2, 16000), "ru")
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}

// TestTranscribe_FileNotFound covers wav.Open failure on a missing path.
func (s *WhisperSuite) TestTranscribe_FileNotFound() {
	_, err := s.w.Transcribe(s.ctx, filepath.Join(s.tempDir, "nonexistent.wav"), "ru")
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}

// TestTranscribe_NotAWAV covers wav.Open rejection of non-WAV content.
func (s *WhisperSuite) TestTranscribe_NotAWAV() {
	path := filepath.Join(s.tempDir, "bad.wav")
	s.Require().NoError(os.WriteFile(path, []byte("not a wav file"), 0o600))

	_, err := s.w.Transcribe(s.ctx, path, "ru")
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}

// TestTranscribe_WhisperFullError covers the whisper_full != 0 branch via the
// whisperFullHook test seam (a pure-Go *int that replaces the CGo return code).
func (s *WhisperSuite) TestTranscribe_WhisperFullError() {
	code := -1

	whisperFullHook = &code
	defer func() { whisperFullHook = nil }()

	_, err := s.w.Transcribe(s.ctx, s.writeSilenceWAV("whisper-fail.wav", 0.5), "ru")
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}

// --- Transcribe: language ---

// TestTranscribe_AutoLanguage checks that lang="auto" runs without unexpected error.
// Silence input may produce ErrEmptyResult, which is acceptable.
func (s *WhisperSuite) TestTranscribe_AutoLanguage() {
	result, err := s.w.Transcribe(s.ctx, s.writeSilenceWAV("auto.wav", 1.0), "auto")
	if err != nil {
		s.Require().ErrorIs(err, sttx.ErrEmptyResult)
	}

	s.T().Logf("auto-detect result: %q", result)
}

// TestTranscribe_EmptyLangFallsBackToAutoDetect checks that lang="" triggers detection.
// Silence input may produce ErrEmptyResult, which is acceptable.
func (s *WhisperSuite) TestTranscribe_EmptyLangFallsBackToAutoDetect() {
	result, err := s.w.Transcribe(s.ctx, s.writeSilenceWAV("empty-lang.wav", 1.0), "")
	if err != nil {
		s.Require().ErrorIs(err, sttx.ErrEmptyResult)
	}

	s.T().Logf("empty-lang result: %q", result)
}

// --- Transcribe: context ---

// TestTranscribe_ContextAlreadyCanceled covers the early ctx.Err() check
// (context canceled before any IO).
func (s *WhisperSuite) TestTranscribe_ContextAlreadyCanceled() {
	ctx, cancel := context.WithCancel(s.ctx)
	cancel()

	_, err := s.w.Transcribe(ctx, s.writeSilenceWAV("ctx-cancel.wav", 0.5), "ru")
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}

// TestTranscribe_ContextCanceledWhileWaitingForLock covers the second ctx.Err()
// check inside the mutex (context canceled while another goroutine holds the lock).
func (s *WhisperSuite) TestTranscribe_ContextCanceledWhileWaitingForLock() {
	wavPath := s.writeSilenceWAV("ctx-wait.wav", 0.5)

	// Hold the mutex directly to simulate a long-running transcription.
	s.w.mu.Lock()

	ctx, cancel := context.WithCancel(s.ctx)
	errCh := make(chan error, 1)

	go func() {
		_, err := s.w.Transcribe(ctx, wavPath, "ru")
		errCh <- err
	}()

	// Give the goroutine time to pass the early ctx.Err() check and read the WAV
	// (pure file IO: microseconds). 20 ms is generous even on heavily loaded hardware.
	time.Sleep(20 * time.Millisecond)

	// Cancel the context while the goroutine is blocked on mu.Lock().
	cancel()
	s.w.mu.Unlock()

	err := <-errCh
	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrTranscribeFailed)
}

// --- Transcribe: concurrency ---

// TestTranscribe_ConcurrentCallsAreSerialized verifies that N goroutines calling
// Transcribe simultaneously do not race; w.mu serializes whisper_full.
func (s *WhisperSuite) TestTranscribe_ConcurrentCallsAreSerialized() {
	const goroutines = 3

	wavPath := s.writeSilenceWAV("concurrent.wav", 0.5)

	errs := make([]error, goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := range goroutines {
		go func(i int) {
			defer wg.Done()

			_, errs[i] = s.w.Transcribe(s.ctx, wavPath, "ru")
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		s.NoErrorf(err, "goroutine %d failed: %v", i, err)
	}
}

// --- Close ---

// TestClose_BeforeLoadIsNoop checks that Close without LoadModel is safe.
func (s *WhisperSuite) TestClose_BeforeLoadIsNoop() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWhisperTranscriber(log)
	s.Require().NoError(w.Close())
}

// TestClose_IdempotentAfterLoad checks that calling Close twice does not panic.
func (s *WhisperSuite) TestClose_IdempotentAfterLoad() {
	modelPath := os.Getenv(modelPathEnv)

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWhisperTranscriber(log)
	s.Require().NoError(w.LoadModel(modelPath))

	s.Require().NoError(w.Close())
	s.Require().NoError(w.Close())
}

// --- helpers ---

// writeSilenceWAV writes a pcm_s16le 16kHz mono WAV with durationSec seconds of silence.
func (s *WhisperSuite) writeSilenceWAV(name string, durationSec float64) string {
	s.T().Helper()

	numSamples := uint32(float64(16000) * durationSec)

	return s.writeWAVWithFormat(name, 16000, 1, numSamples)
}

// writeTruncatedWAV writes a WAV whose header claims 1000 samples but contains
// no actual audio data. ReadAll will return io.ErrUnexpectedEOF.
func (s *WhisperSuite) writeTruncatedWAV(name string) string {
	s.T().Helper()

	const (
		sampleRate     = uint32(16000)
		numChannels    = uint16(1)
		bitDepth       = uint16(16)
		audioFmt       = uint16(1)
		claimedSamples = uint32(1000)
	)

	dataSize := claimedSamples * uint32(bitDepth/8)
	fmtSize := uint32(16)

	buf := &bytes.Buffer{}
	buf.WriteString("RIFF")
	binary.Write(buf, binary.LittleEndian, 4+8+fmtSize+8+dataSize)
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	binary.Write(buf, binary.LittleEndian, fmtSize)
	binary.Write(buf, binary.LittleEndian, audioFmt)
	binary.Write(buf, binary.LittleEndian, numChannels)
	binary.Write(buf, binary.LittleEndian, sampleRate)
	binary.Write(buf, binary.LittleEndian, sampleRate*uint32(numChannels)*uint32(bitDepth/8))
	binary.Write(buf, binary.LittleEndian, numChannels*bitDepth/8)
	binary.Write(buf, binary.LittleEndian, bitDepth)
	buf.WriteString("data")
	binary.Write(buf, binary.LittleEndian, dataSize)
	// Write exactly 1 byte so io.ReadFull gets (n>0, io.EOF) → io.ErrUnexpectedEOF.
	// Writing 0 bytes would give (0, io.EOF) which ReadAll treats as normal EOF.
	buf.WriteByte(0x00)

	path := filepath.Join(s.tempDir, name)
	s.Require().NoError(os.WriteFile(path, buf.Bytes(), 0o600))

	return path
}

// writeWAVWithFormat writes a pcm_s16le WAV with the given sample rate, channel count,
// and number of silence samples. Used to craft WAVs with invalid formats.
func (s *WhisperSuite) writeWAVWithFormat(name string, sampleRate uint32, numChannels uint16, numSamples uint32) string {
	s.T().Helper()

	const (
		bitDepth = uint16(16)
		audioFmt = uint16(1)
	)

	dataSize := numSamples * uint32(numChannels) * uint32(bitDepth/8)
	fmtSize := uint32(16)

	buf := &bytes.Buffer{}
	buf.WriteString("RIFF")
	binary.Write(buf, binary.LittleEndian, 4+8+fmtSize+8+dataSize)
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	binary.Write(buf, binary.LittleEndian, fmtSize)
	binary.Write(buf, binary.LittleEndian, audioFmt)
	binary.Write(buf, binary.LittleEndian, numChannels)
	binary.Write(buf, binary.LittleEndian, sampleRate)
	binary.Write(buf, binary.LittleEndian, sampleRate*uint32(numChannels)*uint32(bitDepth/8))
	binary.Write(buf, binary.LittleEndian, numChannels*bitDepth/8)
	binary.Write(buf, binary.LittleEndian, bitDepth)
	buf.WriteString("data")
	binary.Write(buf, binary.LittleEndian, dataSize)
	buf.Write(make([]byte, dataSize))

	path := filepath.Join(s.tempDir, name)
	s.Require().NoError(os.WriteFile(path, buf.Bytes(), 0o600))

	return path
}
