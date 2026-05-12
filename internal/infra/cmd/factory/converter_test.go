package factory

// White-box + black-box tests for converters (passthroughConverter,
// ffmpegConverter) and BuildConverter wiring. Package cmd (not
// cmd_test) gives access to unexported identifiers used by the
// ffmpegConverter tests.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/a2text/internal/infra/config"
)

// ---------------------------------------------------------------------------
// WAV fixtures (shared between suite and flat tests)
// ---------------------------------------------------------------------------

// minimalWAV writes a minimal valid 16kHz mono 16-bit PCM WAV file (one
// silent sample) and returns its path. The file passes audio.ValidateAudioFormat.
func minimalWAV(t *testing.T) string {
	t.Helper()

	// Byte layout is little-endian throughout.
	// RIFF chunk size = 38 (fmt 24 + data header 8 + 2 sample bytes - 8 RIFF overhead)
	data := []byte{
		// RIFF chunk descriptor
		'R', 'I', 'F', 'F',
		38, 0, 0, 0, // chunk size = 38
		'W', 'A', 'V', 'E',
		// fmt sub-chunk (PCM = 16 bytes)
		'f', 'm', 't', ' ',
		16, 0, 0, 0, // sub-chunk size
		1, 0, // AudioFormat = PCM
		1, 0, // NumChannels = 1 (mono)
		0x80, 0x3E, 0x00, 0x00, // SampleRate = 16000
		0x00, 0x7D, 0x00, 0x00, // ByteRate = 32000
		2, 0, // BlockAlign = 2
		16, 0, // BitsPerSample = 16
		// data sub-chunk
		'd', 'a', 't', 'a',
		2, 0, 0, 0, // data size = 2 (one silent 16-bit sample)
		0, 0, // silence
	}

	path := filepath.Join(t.TempDir(), "silence.wav")
	require.NoError(t, os.WriteFile(path, data, 0o600))

	return path
}

// nonWAVFile writes a small file that is not a valid WAV and returns its path.
// ffmpegConverter.ToWAV will reach the inner converter for such files.
func nonWAVFile(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "recording.ogg")
	require.NoError(t, os.WriteFile(path, []byte("not a wav file"), 0o600))

	return path
}

// ---------------------------------------------------------------------------
// ConverterSuite — BuildConverter wiring + passthroughConverter (black-box)
// ---------------------------------------------------------------------------

type ConverterSuite struct {
	suite.Suite
}

func TestConverterSuite(t *testing.T) {
	suite.Run(t, new(ConverterSuite))
}

// --- passthroughConverter (via BuildConverter) ---

func (s *ConverterSuite) TestPassthroughConverter_EmptyPath_ReturnsError() {
	cfg := &config.VoiceConfig{Provider: config.VoiceProviderGoWhisper}

	conv, err := BuildConverter(cfg, "", nil)
	s.Require().NoError(err)

	_, _, err = conv.ToWAV(context.Background(), "")
	s.Require().ErrorContains(err, "empty input path")
}

func (s *ConverterSuite) TestPassthroughConverter_CleanupDoesNotDeleteInput() {
	cfg := &config.VoiceConfig{Provider: config.VoiceProviderGoWhisper}

	conv, err := BuildConverter(cfg, "", nil)
	s.Require().NoError(err)

	dir := s.T().TempDir()
	src := filepath.Join(dir, "audio.ogg")
	s.Require().NoError(os.WriteFile(src, []byte("audio-data"), 0o600))

	out, cleanup, err := conv.ToWAV(context.Background(), src)
	s.Require().NoError(err)
	s.Equal(src, out, "passthrough must return input path verbatim")
	s.NotNil(cleanup)

	cleanup()

	_, statErr := os.Stat(src)
	s.Require().NoError(statErr, "passthrough cleanup must NOT delete the input file")
}

// --- BuildConverter nil/invalid guards ---

func (s *ConverterSuite) TestBuildConverter_NilConfig_ReturnsError() {
	s.NotPanics(func() {
		conv, err := BuildConverter(nil, "", nil)
		s.Require().ErrorContains(err, "nil config")
		s.Nil(conv)
	})
}

func (s *ConverterSuite) TestBuildConverter_NilLog_NoPanic() {
	cfg := &config.VoiceConfig{Provider: config.VoiceProviderGoWhisper}

	s.NotPanics(func() {
		conv, err := BuildConverter(cfg, "", nil)
		s.Require().NoError(err)
		s.NotNil(conv)
	})
}

// --- ffmpegConverter (whisper-cpp path) ---

func (s *ConverterSuite) TestBuildConverter_WhisperCpp_EmptyTempDir_FallsBackToOsTempDir() {
	cfg := &config.VoiceConfig{
		Provider:       config.VoiceProviderWhisperCpp,
		ConvertTimeout: 30 * time.Second,
	}

	// Construction must succeed; actual WAV output dir is verified in internal tests.
	conv, err := BuildConverter(cfg, "", nil)
	s.Require().NoError(err)
	s.NotNil(conv)
}

func (s *ConverterSuite) TestBuildConverter_WhisperCpp_ZeroConvertTimeout_ReturnsError() {
	cfg := &config.VoiceConfig{
		Provider:       config.VoiceProviderWhisperCpp,
		ConvertTimeout: 0,
	}

	conv, err := BuildConverter(cfg, "", nil)
	s.Require().ErrorContains(err, "convert_timeout")
	s.Nil(conv)
}

// ---------------------------------------------------------------------------
// ffmpegConverter white-box tests — access to unexported types
// ---------------------------------------------------------------------------

// --- nil / zero-value receiver ---

func TestFFmpegConverter_NilReceiver_ReturnsError(t *testing.T) {
	var conv *ffmpegConverter

	_, _, err := conv.ToWAV(context.Background(), "input.wav")
	require.ErrorContains(t, err, "nil receiver")
}

func TestFFmpegConverter_NilInner_ReturnsError(t *testing.T) {
	_, err := newFfmpegConverter(nil, slog.New(slog.DiscardHandler))
	require.ErrorContains(t, err, "nil")
}

// --- nil log: no panic ---

func TestFFmpegConverter_NilLog_NoPanic(t *testing.T) {
	ctrl := gomock.NewController(t)
	dir := t.TempDir()
	src := nonWAVFile(t)

	converted := filepath.Join(dir, "out.wav")
	require.NoError(t, os.WriteFile(converted, []byte("wav data"), 0o600))

	inner := NewMockFFmpegInner(ctrl)
	inner.EXPECT().ToWAV(gomock.Any(), src).Return(converted, nil)

	// nil log is accepted by the constructor — a discard handler is substituted.
	conv, err := newFfmpegConverter(inner, nil)
	require.NoError(t, err)

	require.NotPanics(t, func() {
		_, cleanup, callErr := conv.ToWAV(context.Background(), src)
		require.NoError(t, callErr)

		cleanup()
	})
}

// --- input validation ---

func TestFFmpegConverter_EmptyInputPath_ReturnsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	conv := &ffmpegConverter{
		inner: NewMockFFmpegInner(ctrl),
		log:   slog.New(slog.DiscardHandler),
	}

	_, _, err := conv.ToWAV(context.Background(), "")
	require.ErrorContains(t, err, "empty input path")
}

// --- converted == inputPath ownership guard ---

func TestFFmpegConverter_ConvertedEqualsInput_ReturnsError(t *testing.T) {
	ctrl := gomock.NewController(t)
	src := nonWAVFile(t)
	inner := NewMockFFmpegInner(ctrl)
	inner.EXPECT().ToWAV(gomock.Any(), src).Return(src, nil)

	conv := &ffmpegConverter{
		inner: inner,
		log:   slog.New(slog.DiscardHandler),
	}

	_, _, err := conv.ToWAV(context.Background(), src)
	require.ErrorContains(t, err, "input path as output")
}

// --- inner error wrapping ---

func TestFFmpegConverter_InnerError_IsWrapped(t *testing.T) {
	ctrl := gomock.NewController(t)
	src := nonWAVFile(t)
	inner := NewMockFFmpegInner(ctrl)
	inner.EXPECT().ToWAV(gomock.Any(), src).Return("", errors.New("ffmpeg dead"))

	conv := &ffmpegConverter{
		inner: inner,
		log:   slog.New(slog.DiscardHandler),
	}

	_, _, err := conv.ToWAV(context.Background(), src)
	require.ErrorContains(t, err, "ffmpeg ToWAV")
	require.ErrorContains(t, err, "ffmpeg dead")
}

// --- WAV fast-path ---

func TestFFmpegConverter_ValidWAVFastPath_ReturnsInputUnchanged(t *testing.T) {
	ctrl := gomock.NewController(t)
	wavPath := minimalWAV(t)

	inner := NewMockFFmpegInner(ctrl)
	conv := &ffmpegConverter{inner: inner, log: slog.New(slog.DiscardHandler)}

	out, cleanup, err := conv.ToWAV(context.Background(), wavPath)
	require.NoError(t, err)
	require.Equal(t, wavPath, out, "fast-path must return the input path verbatim")
	require.NotNil(t, cleanup)

	cleanup()

	_, statErr := os.Stat(wavPath)
	require.NoError(t, statErr, "fast-path cleanup must NOT delete the input file")
}

// --- conversion output path and cleanup ownership ---

func TestFFmpegConverter_ConversionOutputPath_MatchesInnerReturn(t *testing.T) {
	ctrl := gomock.NewController(t)
	outputDir := t.TempDir()
	src := nonWAVFile(t)

	converted := filepath.Join(outputDir, "out.wav")
	require.NoError(t, os.WriteFile(converted, []byte("wav data"), 0o600))

	inner := NewMockFFmpegInner(ctrl)
	inner.EXPECT().ToWAV(gomock.Any(), src).Return(converted, nil)

	conv := &ffmpegConverter{
		inner: inner,
		log:   slog.New(slog.DiscardHandler),
	}

	out, _, err := conv.ToWAV(context.Background(), src)
	require.NoError(t, err)
	require.Equal(t, converted, out)
}

func TestFFmpegConverter_Cleanup_RemovesConvertedNotInput(t *testing.T) {
	ctrl := gomock.NewController(t)
	dir := t.TempDir()
	src := nonWAVFile(t)

	converted := filepath.Join(dir, "out.wav")
	require.NoError(t, os.WriteFile(converted, []byte("wav data"), 0o600))

	inner := NewMockFFmpegInner(ctrl)
	inner.EXPECT().ToWAV(gomock.Any(), src).Return(converted, nil)

	conv := &ffmpegConverter{
		inner: inner,
		log:   slog.New(slog.DiscardHandler),
	}

	_, cleanup, err := conv.ToWAV(context.Background(), src)
	require.NoError(t, err)
	require.NotNil(t, cleanup)

	cleanup()

	_, statErr := os.Stat(converted)
	require.ErrorIs(t, statErr, os.ErrNotExist, "cleanup must remove the converted WAV")

	_, statErr = os.Stat(src)
	require.NoError(t, statErr, "cleanup must NOT remove the original input file")
}
