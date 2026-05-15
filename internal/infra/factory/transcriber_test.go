package factory_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	factory "github.com/partyzanex/a2text/internal/infra/factory"
	"github.com/partyzanex/a2text/internal/infra/config"
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

func (s *BuildersSuite) TestBuildConverter_Cloud_PassthroughBehaviour() {
	cfg := s.validCfg(config.VoiceProviderCloud)

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

func (s *BuildersSuite) TestBuildTranscriber_WhisperCpp_NotEnabledInStageI0() {
	cfg := s.validCfg(config.VoiceProviderWhisperCpp)

	tr, err := factory.BuildTranscriber(bgContext(), cfg, s.log)
	s.Require().Error(err)
	s.Nil(tr)
	s.Contains(err.Error(), "-tags whisper")
}

func (s *BuildersSuite) TestBuildTranscriber_UnknownProvider_ReturnsError() {
	cfg := s.validCfg("banana-stt")

	tr, err := factory.BuildTranscriber(bgContext(), cfg, s.log)
	s.Require().Error(err)
	s.Nil(tr)
	s.Contains(err.Error(), `unknown provider "banana-stt"`)
}

func (s *BuildersSuite) TestBuildTranscriber_Cloud_OpenAI_OK() {
	cfg := s.validCfg(config.VoiceProviderCloud)
	cfg.CloudProvider = config.VoiceCloudProviderOpenAI
	cfg.CloudAPIKey = "sk-test"

	tr, err := factory.BuildTranscriber(bgContext(), cfg, s.log)
	s.Require().NoError(err)
	s.NotNil(tr)
}

func (s *BuildersSuite) TestBuildTranscriber_Cloud_Deepgram_OK() {
	cfg := s.validCfg(config.VoiceProviderCloud)
	cfg.CloudProvider = config.VoiceCloudProviderDeepgram
	cfg.CloudAPIKey = "dg-test"

	tr, err := factory.BuildTranscriber(bgContext(), cfg, s.log)
	s.Require().NoError(err)
	s.NotNil(tr)
}

func (s *BuildersSuite) TestBuildTranscriber_Cloud_EmptyCloudProvider_ReturnsError() {
	// Direct call bypasses config.ValidateVoice — buildCloud must still
	// reject empty CloudProvider rather than silently picking openai.
	// Set a non-empty API key so we reach the provider switch, not the key check.
	cfg := s.validCfg(config.VoiceProviderCloud)
	cfg.CloudProvider = ""
	cfg.CloudAPIKey = "test-key"

	tr, err := factory.BuildTranscriber(bgContext(), cfg, s.log)
	s.Require().Error(err)
	s.Nil(tr)
	s.Contains(err.Error(), "unknown cloud provider")
}

func (s *BuildersSuite) TestBuildTranscriber_Cloud_UnknownCloudProvider_ReturnsError() {
	cfg := s.validCfg(config.VoiceProviderCloud)
	cfg.CloudProvider = "lol-enterprise-ai-9000"
	cfg.CloudAPIKey = "x" // must be non-empty to reach the provider switch

	tr, err := factory.BuildTranscriber(bgContext(), cfg, s.log)
	s.Require().Error(err)
	s.Nil(tr)
	s.Contains(err.Error(), "unknown cloud provider")
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

func (s *BuildersSuite) TestBuildTranscriber_Cloud_EmptyAPIKey_ReturnsError() {
	cfg := s.validCfg(config.VoiceProviderCloud)
	cfg.CloudProvider = config.VoiceCloudProviderOpenAI
	cfg.CloudAPIKey = "" // explicitly empty

	tr, err := factory.BuildTranscriber(bgContext(), cfg, s.log)
	s.Require().Error(err)
	s.Nil(tr)
	s.Contains(err.Error(), "cloud_api_key")
}

func (s *BuildersSuite) TestBuildTranscriber_WhisperCpp_ErrorMessageNoStageReference() {
	cfg := s.validCfg(config.VoiceProviderWhisperCpp)

	_, err := factory.BuildTranscriber(bgContext(), cfg, s.log)
	s.Require().Error(err)
	// Error must mention the build tag but must not carry stale stage references.
	s.Contains(err.Error(), "-tags whisper")
	s.NotContains(err.Error(), "stage I.0")
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
