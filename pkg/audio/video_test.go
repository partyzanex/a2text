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

// VideoSuite содержит тесты для ExtractAudioFromVideo, ProbeDuration и ValidateAudioFormat.
type VideoSuite struct {
	suite.Suite

	conv        *FFmpegConverter
	fixturesDir string
	tmpDir      string

	// тестовые фикстуры (пусто если ffmpeg/lavfi недоступен)
	testMP4 string
	testMKV string
	testAVI string
}

func TestVideoSuite(t *testing.T) {
	suite.Run(t, new(VideoSuite))
}

// SetupSuite генерирует видео-фикстуры один раз; пропускает suite если ffmpeg не найден.
func (s *VideoSuite) SetupSuite() {
	// Проверяем наличие ffmpeg
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		s.T().Skip("ffmpeg не найден — пропускаем video suite")
	}

	var err error

	s.fixturesDir, err = os.MkdirTemp("", "video-fixtures-*")
	s.Require().NoError(err)

	// 0.5-секундная синусоида + чёрный прямоугольник → MP4 (h264+aac)
	s.testMP4 = s.makeVideo("test.mp4", "libx264", "aac")

	// → MKV (mpeg4+mp3)
	s.testMKV = s.makeVideo("test.mkv", "mpeg4", "libmp3lame")

	// → AVI (mpeg4+pcm)
	s.testAVI = s.makeVideo("test.avi", "mpeg4", "pcm_s16le")
}

func (s *VideoSuite) TearDownSuite() {
	_ = os.RemoveAll(s.fixturesDir)
}

func (s *VideoSuite) SetupTest() {
	s.tmpDir = s.T().TempDir()
	s.Require().NoError(os.Chmod(s.tmpDir, 0o700))

	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	s.conv = NewFFmpegConverter(10*time.Second, s.tmpDir, log)
}

// --- ExtractAudioFromVideo: happy path ---

func (s *VideoSuite) TestExtractAudioFromVideo_MP4_ReturnsWAVPath() {
	if s.testMP4 == "" {
		s.T().Skip("MP4 фикстура недоступна")
	}

	wavPath, dur, err := s.conv.ExtractAudioFromVideo(context.Background(), s.testMP4)

	s.Require().NoError(err)
	s.Equal(".wav", filepath.Ext(wavPath))
	s.Equal(s.tmpDir, filepath.Dir(wavPath))
	s.Greater(dur, time.Duration(0))
}

func (s *VideoSuite) TestExtractAudioFromVideo_MP4_OutputIsValidWAV() {
	if s.testMP4 == "" {
		s.T().Skip("MP4 фикстура недоступна")
	}

	wavPath, _, err := s.conv.ExtractAudioFromVideo(context.Background(), s.testMP4)
	s.Require().NoError(err)

	data, err := os.ReadFile(wavPath)
	s.Require().NoError(err)
	s.Require().GreaterOrEqual(len(data), 12)
	s.Equal("RIFF", string(data[0:4]))
	s.Equal("WAVE", string(data[8:12]))
}

func (s *VideoSuite) TestExtractAudioFromVideo_MP4_ValidatesFormat() {
	if s.testMP4 == "" {
		s.T().Skip("MP4 фикстура недоступна")
	}

	wavPath, _, err := s.conv.ExtractAudioFromVideo(context.Background(), s.testMP4)
	s.Require().NoError(err)

	s.NoError(ValidateAudioFormat(wavPath))
}

func (s *VideoSuite) TestExtractAudioFromVideo_MKV_ProducesValidWAV() {
	if s.testMKV == "" {
		s.T().Skip("MKV фикстура недоступна")
	}

	wavPath, dur, err := s.conv.ExtractAudioFromVideo(context.Background(), s.testMKV)

	s.Require().NoError(err)
	s.Greater(dur, time.Duration(0))

	data, err := os.ReadFile(wavPath)
	s.Require().NoError(err)
	s.Equal("RIFF", string(data[0:4]))
}

func (s *VideoSuite) TestExtractAudioFromVideo_AVI_ProducesValidWAV() {
	if s.testAVI == "" {
		s.T().Skip("AVI фикстура недоступна")
	}

	wavPath, dur, err := s.conv.ExtractAudioFromVideo(context.Background(), s.testAVI)

	s.Require().NoError(err)
	s.Greater(dur, time.Duration(0))

	data, err := os.ReadFile(wavPath)
	s.Require().NoError(err)
	s.Equal("RIFF", string(data[0:4]))
}

// --- ExtractAudioFromVideo: error cases ---

func (s *VideoSuite) TestExtractAudioFromVideo_UnsupportedExt_ReturnsConversionFailed() {
	_, _, err := s.conv.ExtractAudioFromVideo(context.Background(), "audio.ogg")

	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrConversionFailed)
}

func (s *VideoSuite) TestExtractAudioFromVideo_NonExistentFile_ReturnsConversionFailed() {
	_, _, err := s.conv.ExtractAudioFromVideo(context.Background(), "/nonexistent/video.mp4")

	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrConversionFailed)
}

func (s *VideoSuite) TestExtractAudioFromVideo_InvalidContent_ReturnsConversionFailed() {
	badFile := filepath.Join(s.tmpDir, "fake.mp4")
	s.Require().NoError(os.WriteFile(badFile, []byte("not-a-video"), 0o600))

	_, _, err := s.conv.ExtractAudioFromVideo(context.Background(), badFile)

	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrConversionFailed)
}

func (s *VideoSuite) TestExtractAudioFromVideo_CancelledContext_ReturnsConversionFailed() {
	if s.testMP4 == "" {
		s.T().Skip("MP4 фикстура недоступна")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := s.conv.ExtractAudioFromVideo(ctx, s.testMP4)

	s.Require().Error(err)
	s.ErrorIs(err, sttx.ErrConversionFailed)
}

// --- ProbeDuration ---

func (s *VideoSuite) TestProbeDuration_MP4_ReturnsDuration() {
	if s.testMP4 == "" {
		s.T().Skip("MP4 фикстура недоступна")
	}

	dur, err := ProbeDuration(context.Background(), s.testMP4)

	s.Require().NoError(err)
	s.Greater(dur, time.Duration(0))
}

func (s *VideoSuite) TestProbeDuration_WAVFile_ReturnsDuration() {
	if s.testMP4 == "" {
		s.T().Skip("MP4 фикстура недоступна")
	}

	// Сначала конвертируем в WAV, затем пробируем
	wavPath, err := s.conv.ToWAVFromFile(context.Background(), s.testMP4)
	s.Require().NoError(err)

	dur, err := ProbeDuration(context.Background(), wavPath)

	s.Require().NoError(err)
	s.Greater(dur, time.Duration(0))
}

func (s *VideoSuite) TestProbeDuration_NonExistentFile_ReturnsError() {
	_, err := ProbeDuration(context.Background(), "/nonexistent/file.mp4")

	s.Require().Error(err)
}

func (s *VideoSuite) TestProbeDuration_InvalidFile_ReturnsError() {
	badFile := filepath.Join(s.tmpDir, "invalid.mp4")
	s.Require().NoError(os.WriteFile(badFile, []byte("garbage"), 0o600))

	_, err := ProbeDuration(context.Background(), badFile)

	s.Require().Error(err)
}

// --- ValidateAudioFormat ---

func (s *VideoSuite) TestValidateAudioFormat_ValidWAV_ReturnsNil() {
	if s.testMP4 == "" {
		s.T().Skip("MP4 фикстура недоступна")
	}

	wavPath, err := s.conv.ToWAVFromFile(context.Background(), s.testMP4)
	s.Require().NoError(err)

	s.NoError(ValidateAudioFormat(wavPath))
}

func (s *VideoSuite) TestValidateAudioFormat_NonWAVFile_ReturnsError() {
	if s.testMP4 == "" {
		s.T().Skip("MP4 фикстура недоступна")
	}

	// Передаём видеофайл — он не является WAV
	err := ValidateAudioFormat(s.testMP4)

	s.Require().Error(err)
}

func (s *VideoSuite) TestValidateAudioFormat_NonExistentFile_ReturnsError() {
	err := ValidateAudioFormat("/nonexistent/audio.wav")

	s.Require().Error(err)
}

func (s *VideoSuite) TestValidateAudioFormat_WrongSampleRate_ReturnsError() {
	// Создаём WAV с 44100 Hz через ffmpeg — не соответствует требованиям Whisper
	badWAV := filepath.Join(s.tmpDir, "wrong_rate.wav")

	if s.testMP4 == "" {
		s.T().Skip("MP4 фикстура недоступна")
	}

	cmd := exec.Command("ffmpeg", "-y",
		"-i", s.testMP4,
		"-vn",
		"-ar", "44100",
		"-ac", "1",
		"-c:a", "pcm_s16le",
		badWAV,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		s.T().Skipf("не удалось создать WAV с неверным sample rate: %v\n%s", err, out)
	}

	err := ValidateAudioFormat(badWAV)
	s.Require().Error(err)
}

// makeVideo creates a test video file using ffmpeg lavfi.
// Returns the path to the file, or "" if the codec is unavailable (test will be skipped at runtime).
func (s *VideoSuite) makeVideo(name, vcodec, acodec string) string {
	path := filepath.Join(s.fixturesDir, name)

	cmd := exec.Command("ffmpeg", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=0.5",
		"-f", "lavfi", "-i", "color=c=black:size=64x64:rate=10:duration=0.5",
		"-c:a", acodec,
		"-c:v", vcodec,
		"-shortest",
		path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		s.T().Logf("fixture %s unavailable (%s/%s): %v\n%s", name, vcodec, acodec, err, out)

		return ""
	}

	return path
}
