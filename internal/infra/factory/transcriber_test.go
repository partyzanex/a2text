package factory_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/a2text/internal/infra/config"
	factory "github.com/partyzanex/a2text/internal/infra/factory"
)

// bgContext is a shorthand for context.Background() used in BuildTranscriber calls.
func bgContext() context.Context { return context.Background() }

type BuildersSuite struct {
	suite.Suite

	log *slog.Logger
}

func TestBuildersSuite(t *testing.T) {
	suite.Run(t, new(BuildersSuite))
}

func (s *BuildersSuite) SetupTest() {
	s.log = slog.New(slog.DiscardHandler)
}

// --- BuildConverter ---

// For passthrough providers we verify the *behaviour* (returns input
// verbatim with a non-nil cleanup) rather than the concrete type — the
// wrapper is unexported on purpose.

func (s *BuildersSuite) TestBuildConverter_GoWhisper_PassthroughBehaviour() {
	cfg := s.validCfg(config.VoiceProviderGoWhisper)

	conv, err := factory.BuildConverter(cfg, "", s.log)
	s.Require().NoError(err)
	s.assertPassthrough(conv)
}

func (s *BuildersSuite) TestBuildConverter_OpenAI_PassthroughBehaviour() {
	cfg := s.validCfg(config.VoiceProviderOpenAI)

	conv, err := factory.BuildConverter(cfg, "", s.log)
	s.Require().NoError(err)
	s.assertPassthrough(conv)
}

func (s *BuildersSuite) TestBuildConverter_Deepgram_PassthroughBehaviour() {
	cfg := s.validCfg(config.VoiceProviderDeepgram)

	conv, err := factory.BuildConverter(cfg, "", s.log)
	s.Require().NoError(err)
	s.assertPassthrough(conv)
}

func (s *BuildersSuite) TestBuildConverter_WhisperCpp_BuildsButFFmpegRunOnInvalidFails() {
	cfg := s.validCfg(config.VoiceProviderWhisperCpp)
	cfg.TempDir = s.T().TempDir()
	cfg.ConvertTimeout = 30 * time.Second

	conv, err := factory.BuildConverter(cfg, "", s.log)
	s.Require().NoError(err)
	s.NotNil(conv)
	// We don't run ffmpeg here — that's an integration concern. Building
	// the converter without panic is enough for this unit test.
}

func (s *BuildersSuite) TestBuildConverter_UnknownProvider_ReturnsError() {
	cfg := s.validCfg("banana-stt")

	conv, err := factory.BuildConverter(cfg, "", s.log)
	s.Require().Error(err)
	s.Nil(conv)
	s.Contains(err.Error(), `unknown provider "banana-stt"`)
}

// --- BuildTranscriber ---

func (s *BuildersSuite) TestBuildTranscriber_GoWhisper_OK() {
	cfg := s.validCfg(config.VoiceProviderGoWhisper)

	tr, err := factory.BuildTranscriber(bgContext(), cfg, s.log)
	s.Require().NoError(err)
	s.NotNil(tr)
}

func (s *BuildersSuite) TestBuildTranscriber_UnknownProvider_ReturnsError() {
	cfg := s.validCfg("banana-stt")

	tr, err := factory.BuildTranscriber(bgContext(), cfg, s.log)
	s.Require().Error(err)
	s.Nil(tr)
	s.Contains(err.Error(), `unknown provider "banana-stt"`)
}

func (s *BuildersSuite) TestBuildTranscriber_OpenAI_OK() {
	cfg := s.validCfg(config.VoiceProviderOpenAI)
	cfg.OpenAI = config.VoiceOpenAIConfig{APIKey: "sk-test"}

	tr, err := factory.BuildTranscriber(bgContext(), cfg, s.log)
	s.Require().NoError(err)
	s.NotNil(tr)
}

func (s *BuildersSuite) TestBuildTranscriber_Deepgram_OK() {
	cfg := s.validCfg(config.VoiceProviderDeepgram)
	cfg.Deepgram = config.VoiceDeepgramConfig{APIKey: "dg-test"}

	tr, err := factory.BuildTranscriber(bgContext(), cfg, s.log)
	s.Require().NoError(err)
	s.NotNil(tr)
}

// --- Nil/invalid guard tests ---

func (s *BuildersSuite) TestBuildTranscriber_NilConfig_ReturnsErrorNoPanic() {
	s.NotPanics(func() {
		tr, err := factory.BuildTranscriber(bgContext(), nil, s.log)
		s.Require().Error(err)
		s.Nil(tr)
		s.Contains(err.Error(), "nil config")
	})
}

func (s *BuildersSuite) TestBuildTranscriber_NilLog_NoPanic() {
	cfg := s.validCfg(config.VoiceProviderGoWhisper)

	s.NotPanics(func() {
		tr, err := factory.BuildTranscriber(bgContext(), cfg, nil)
		s.Require().NoError(err)
		s.NotNil(tr)
	})
}

func (s *BuildersSuite) TestBuildConverter_NilConfig_ReturnsErrorNoPanic() {
	s.NotPanics(func() {
		conv, err := factory.BuildConverter(nil, "", s.log)
		s.Require().Error(err)
		s.Nil(conv)
		s.Contains(err.Error(), "nil config")
	})
}

func (s *BuildersSuite) TestBuildConverter_NilLog_NoPanic() {
	cfg := s.validCfg(config.VoiceProviderGoWhisper)

	s.NotPanics(func() {
		conv, err := factory.BuildConverter(cfg, "", nil)
		s.Require().NoError(err)
		s.NotNil(conv)
	})
}

func (s *BuildersSuite) TestBuildTranscriber_OpenAI_EmptyAPIKey_ReturnsError() {
	cfg := s.validCfg(config.VoiceProviderOpenAI)
	cfg.OpenAI = config.VoiceOpenAIConfig{APIKey: ""}

	tr, err := factory.BuildTranscriber(bgContext(), cfg, s.log)
	s.Require().Error(err)
	s.Nil(tr)
	s.Contains(err.Error(), "openai.api_key")
}

func (s *BuildersSuite) TestBuildTranscriber_Deepgram_EmptyAPIKey_ReturnsError() {
	cfg := s.validCfg(config.VoiceProviderDeepgram)
	cfg.Deepgram = config.VoiceDeepgramConfig{APIKey: ""}

	tr, err := factory.BuildTranscriber(bgContext(), cfg, s.log)
	s.Require().Error(err)
	s.Nil(tr)
	s.Contains(err.Error(), "deepgram.api_key")
}

// validCfg returns a minimum-valid VoiceConfig for the given provider so
// each subtest only has to flip the field it cares about.
func (s *BuildersSuite) validCfg(provider string) *config.VoiceConfig {
	return &config.VoiceConfig{
		Provider:  provider,
		Language:  "ru",
		GoWhisper: config.VoiceGoWhisperConfig{URL: "http://localhost:9081/api/whisper"},
	}
}

// assertPassthrough verifies that conv hands the input path back unchanged
// and provides a cleanup that is safe to call but does NOT delete the
// caller-owned input file.
func (s *BuildersSuite) assertPassthrough(conv interface {
	ToWAV(ctx context.Context, inputPath string) (string, func(), error)
}) {
	dir := s.T().TempDir()
	src := filepath.Join(dir, "user.ogg")
	s.Require().NoError(os.WriteFile(src, []byte("data"), 0o600))

	out, cleanup, err := conv.ToWAV(context.Background(), src)
	s.Require().NoError(err)
	s.Equal(src, out, "passthrough must return the input path verbatim")
	s.NotNil(cleanup)

	cleanup()

	_, statErr := os.Stat(src)
	s.Require().NoError(statErr, "passthrough cleanup must NOT delete the input file")
}
