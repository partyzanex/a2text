package audio

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/a2text/pkg/sttx"
)

type FFmpegConverterSuite struct {
	suite.Suite

	conv        *FFmpegConverter
	tmpDir      string
	fixturesDir string // общая директория с тестовыми аудиофайлами (живёт всё время suite)
	testOGG     string // путь к сгенерированному test.ogg
	testAVI     string // путь к сгенерированному test.avi (видео+аудио); пуст если ffmpeg не поддерживает
}

func TestFFmpegConverterSuite(t *testing.T) {
	suite.Run(t, new(FFmpegConverterSuite))
}

// SetupSuite генерирует тестовые аудиофайлы один раз для всего suite.
// Использует ffmpeg lavfi (виртуальный источник) — никаких внешних файлов.
func (s *FFmpegConverterSuite) SetupSuite() {
	var err error

	s.fixturesDir, err = os.MkdirTemp("", "audio-fixtures-*")
	s.Require().NoError(err)

	s.testOGG = filepath.Join(s.fixturesDir, "test.ogg")

	// 0.5-секундная синусоида → OGG/Opus
	cmd := exec.Command("ffmpeg", "-y",
		"-f", "lavfi",
		"-i", "sine=frequency=440:duration=0.5",
		"-c:a", "libopus",
		s.testOGG,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		s.T().Skipf("ffmpeg с поддержкой lavfi+opus не найден: %v\n%s", err, out)
	}

	// 0.5-секундное видео (color+sine) → AVI (mpeg4 video + pcm audio).
	// Используем только встроенные кодеки ffmpeg, без внешних библиотек.
	// Если не удалось — тест пропускается ниже по флагу testAVI == "".
	s.testAVI = filepath.Join(s.fixturesDir, "test.avi")

	videoCmd := exec.Command("ffmpeg", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=0.5",
		"-f", "lavfi", "-i", "color=c=black:size=64x64:rate=10:duration=0.5",
		"-c:a", "pcm_s16le",
		"-c:v", "mpeg4",
		"-shortest",
		s.testAVI,
	)
	if out, err := videoCmd.CombinedOutput(); err != nil {
		s.T().Logf("видео-фикстура недоступна (пропускаем видео-тест): %v\n%s", err, out)
		s.testAVI = ""
	}
}

func (s *FFmpegConverterSuite) TearDownSuite() {
	_ = os.RemoveAll(s.fixturesDir)
}

func (s *FFmpegConverterSuite) SetupTest() {
	s.tmpDir = s.T().TempDir()
	s.Require().NoError(os.Chmod(s.tmpDir, 0o700))

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	s.conv = NewFFmpegConverter(10*time.Second, s.tmpDir, log)
}

// --- Happy path ---

func (s *FFmpegConverterSuite) TestToWAV_OGG_ReturnsWAVPath() {
	wavPath, err := s.conv.ToWAVFromFile(context.Background(), s.testOGG)

	s.Require().NoError(err)
	s.Equal("test.wav", filepath.Base(wavPath))
	s.Equal(s.tmpDir, filepath.Dir(wavPath))
}

func (s *FFmpegConverterSuite) TestToWAV_OGG_OutputFileExists() {
	wavPath, err := s.conv.ToWAVFromFile(context.Background(), s.testOGG)

	s.Require().NoError(err)
	info, err := os.Stat(wavPath)
	s.Require().NoError(err)
	s.Greater(info.Size(), int64(44)) // WAV header ≥ 44 байт
}

func (s *FFmpegConverterSuite) TestToWAV_OGG_OutputIsValidWAV() {
	wavPath, err := s.conv.ToWAVFromFile(context.Background(), s.testOGG)
	s.Require().NoError(err)

	data, err := os.ReadFile(wavPath)
	s.Require().NoError(err)
	s.Require().GreaterOrEqual(len(data), 12, "файл слишком мал для WAV заголовка")

	// RIFF/WAVE magic
	s.Equal("RIFF", string(data[0:4]))
	s.Equal("WAVE", string(data[8:12]))
}

func (s *FFmpegConverterSuite) TestToWAV_OGG_Idempotent() {
	// Два вызова с одним входом возвращают одинаковый путь и оба успешны
	path1, err := s.conv.ToWAVFromFile(context.Background(), s.testOGG)
	s.Require().NoError(err)

	path2, err := s.conv.ToWAVFromFile(context.Background(), s.testOGG)
	s.Require().NoError(err)

	s.Equal(path1, path2) // тот же выходной путь
}

// --- Video input ---

// TestToWAV_VideoAVI_ProducesValidWAV verifies that a video file (AVI with mpeg4+pcm)
// is accepted by ToWAV and produces a valid RIFF/WAVE output.
// The -vn flag in the ffmpeg command ensures video streams are explicitly discarded.
func (s *FFmpegConverterSuite) TestToWAV_VideoAVI_ProducesValidWAV() {
	if s.testAVI == "" {
		s.T().Skip("видео-фикстура недоступна")
	}

	wavPath, err := s.conv.ToWAVFromFile(context.Background(), s.testAVI)
	s.Require().NoError(err)

	data, err := os.ReadFile(wavPath)
	s.Require().NoError(err)
	s.Require().GreaterOrEqual(len(data), 12)
	s.Equal("RIFF", string(data[0:4]))
	s.Equal("WAVE", string(data[8:12]))
}

// --- Error cases ---

func (s *FFmpegConverterSuite) TestToWAV_NonExistentFile_ReturnsConversionFailed() {
	_, err := s.conv.ToWAVFromFile(context.Background(), "/nonexistent/file.ogg")

	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrConversionFailed)
}

func (s *FFmpegConverterSuite) TestToWAV_InvalidAudioContent_ReturnsConversionFailed() {
	inputPath := filepath.Join(s.tmpDir, "test.ogg")
	s.Require().NoError(os.WriteFile(inputPath, []byte("not-audio"), 0o600))

	_, err := s.conv.ToWAVFromFile(context.Background(), inputPath)

	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrConversionFailed)
}

func (s *FFmpegConverterSuite) TestToWAV_CancelledContext_ReturnsConversionFailed() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // отменяем до вызова — ffmpeg сразу получит сигнал завершения

	_, err := s.conv.ToWAVFromFile(ctx, s.testOGG)

	s.Require().Error(err)
	s.Require().ErrorIs(err, sttx.ErrConversionFailed)
}

// --- Partial output cleanup ---

// TestToWAV_FailedConversion_RemovesPartialOutput проверяет, что при ошибке
// конвертации частично созданный выходной файл удаляется.
func (s *FFmpegConverterSuite) TestToWAV_FailedConversion_RemovesPartialOutput() {
	inputPath := filepath.Join(s.tmpDir, "test.ogg")
	s.Require().NoError(os.WriteFile(inputPath, []byte("not-audio"), 0o600))

	// Заранее создаём файл по тому же пути, куда ffmpeg пишет вывод,
	// чтобы проверить что os.Remove вызывается и возвращает nil (файл существует).
	partialWAV := filepath.Join(s.tmpDir, "test.wav")
	s.Require().NoError(os.WriteFile(partialWAV, []byte("partial"), 0o600))

	_, err := s.conv.ToWAVFromFile(context.Background(), inputPath)
	s.Require().Error(err)

	_, statErr := os.Stat(partialWAV)
	s.True(os.IsNotExist(statErr), "частичный вывод должен быть удалён")
}
