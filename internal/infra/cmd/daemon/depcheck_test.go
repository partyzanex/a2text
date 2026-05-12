package daemon

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/a2text/internal/infra/cmd/depcheck"
	"github.com/partyzanex/a2text/internal/infra/config"
)

type DepCheckSuite struct {
	suite.Suite

	ctrl *gomock.Controller
}

func TestDepCheckSuite(t *testing.T) {
	suite.Run(t, new(DepCheckSuite))
}

func (s *DepCheckSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
}

// baseAutopasteCfg returns a minimal valid config for autopaste-mode testing.
// On Linux, RunDepCheckWith(ModeDaemon) also probes capture and clipboard, so
// the test lookup must supply pw-record and wl-copy to keep those deps clean.
func baseAutopasteCfg(autopasteCmd string) *config.VoiceConfig {
	return &config.VoiceConfig{
		Provider:          config.VoiceProviderGoWhisper,
		Language:          "ru",
		GoWhisper:         config.VoiceGoWhisperConfig{URL: "http://localhost:9081", Timeout: 1},
		ConvertTimeout:    1,
		TranscribeTimeout: 1,
		Output: config.VoiceOutputConfig{
			Mode:             config.VoiceOutputModeClipboardAutopaste,
			AutopasteCommand: autopasteCmd,
		},
	}
}

// filterAutopasteDeps extracts autopaste-group results from a depcheck result list.
func filterAutopasteDeps(results []DepCheckResult) []DepCheckResult {
	var out []DepCheckResult

	for _, res := range results {
		if res.Group == depcheck.GroupAutopaste {
			out = append(out, res)
		}
	}

	return out
}

// --- FileDepCheckMode: WAV vs non-WAV routing ---

func (s *DepCheckSuite) TestFileDepCheckMode_WAV_ReturnsFileWAV() {
	for _, name := range []string{"rec.wav", "rec.WAV", "rec.wave", "REC.WAVE"} {
		s.Equal(depcheck.ModeFileWAV, FileDepCheckMode(name),
			"expected ModeFileWAV for %q", name)
	}
}

func (s *DepCheckSuite) TestFileDepCheckMode_NonWAV_ReturnsFileAudio() {
	for _, name := range []string{"rec.ogg", "rec.mp3", "rec.flac", "rec.m4a", "rec"} {
		s.Equal(depcheck.ModeFileAudio, FileDepCheckMode(name),
			"expected ModeFileAudio for %q", name)
	}
}

// --- RunDepCheckWith mode routing: ModeFileWAV / ModeFileAudio / ModeRecord ---

func (s *DepCheckSuite) TestRunDepCheckWith_ModeFileWAV_NoAudioDep() {
	// WAV input requires no ffmpeg: the only required dep is the STT provider.
	cfg := &config.VoiceConfig{
		Provider:          config.VoiceProviderGoWhisper,
		Language:          "ru",
		GoWhisper:         config.VoiceGoWhisperConfig{URL: "http://localhost:9081", Timeout: 1},
		ConvertTimeout:    1,
		TranscribeTimeout: 1,
	}

	results, _ := RunDepCheckWith(s.T().Context(), depcheck.ModeFileWAV, cfg, s.lookup(nil), nil, nil)

	for _, res := range results {
		s.NotEqual(depcheck.GroupAudio, res.Group,
			"ModeFileWAV must not include audio (capture/ffmpeg) deps")
	}
}

func (s *DepCheckSuite) TestRunDepCheckWith_ModeFileAudio_RequiresFFmpeg() {
	// Non-WAV input requires ffmpeg conversion before STT.
	cfg := &config.VoiceConfig{
		Provider:          config.VoiceProviderGoWhisper,
		Language:          "ru",
		GoWhisper:         config.VoiceGoWhisperConfig{URL: "http://localhost:9081", Timeout: 1},
		ConvertTimeout:    1,
		TranscribeTimeout: 1,
	}

	results, _ := RunDepCheckWith(s.T().Context(), depcheck.ModeFileAudio, cfg, s.lookup(nil), nil, nil)

	var ffmpegFound bool

	for _, res := range results {
		if res.Name == "ffmpeg" && res.Group == depcheck.GroupAudio {
			ffmpegFound = true
		}
	}

	s.True(ffmpegFound, "ModeFileAudio must check for ffmpeg (needed to convert non-WAV input)")
}

func (s *DepCheckSuite) TestRunDepCheckWith_ModeRecord_RequiresCapture() {
	// Record mode must check for pw-record/parecord regardless of STT provider.
	cfg := &config.VoiceConfig{
		Provider:          config.VoiceProviderGoWhisper,
		Language:          "ru",
		GoWhisper:         config.VoiceGoWhisperConfig{URL: "http://localhost:9081", Timeout: 1},
		ConvertTimeout:    1,
		TranscribeTimeout: 1,
	}

	results, _ := RunDepCheckWith(s.T().Context(), depcheck.ModeRecord, cfg, s.lookup(nil), nil, nil)

	var captureFound bool

	for _, res := range results {
		if res.Group == depcheck.GroupAudio && res.Name == "capture" {
			captureFound = true
		}
	}

	s.True(captureFound, "ModeRecord must check for capture (pw-record/parecord)")
}

// --- autopaste deps: only fires for clipboard_autopaste mode ---

func (s *DepCheckSuite) TestAutopasteDeps_ClipboardMode_NotProbed() {
	cfg := &config.VoiceConfig{
		Provider:          config.VoiceProviderGoWhisper,
		Language:          "ru",
		GoWhisper:         config.VoiceGoWhisperConfig{URL: "http://localhost:9081", Timeout: 1},
		ConvertTimeout:    1,
		TranscribeTimeout: 1,
		Output: config.VoiceOutputConfig{
			Mode:             config.VoiceOutputModeClipboard,
			AutopasteCommand: config.VoiceAutopasteCommandAuto,
		},
	}

	results, _ := RunDepCheckWith(s.T().Context(), depcheck.ModeDaemon, cfg, s.lookup(nil), nil, nil)
	s.Nil(filterAutopasteDeps(results),
		"plain clipboard mode must not produce autopaste deps")
}

// --- auto resolution ---

func (s *DepCheckSuite) TestAutopasteDeps_Auto_PrefersWtype() {
	cfg := baseAutopasteCfg(config.VoiceAutopasteCommandAuto)
	lookup := s.lookup(map[string]string{
		"wtype":   "/usr/bin/wtype",
		"ydotool": "/usr/bin/ydotool",
	})

	results, _ := RunDepCheckWith(s.T().Context(), depcheck.ModeDaemon, cfg, lookup, nil, nil)
	autoResults := filterAutopasteDeps(results)
	s.Require().Len(autoResults, 1)
	s.True(autoResults[0].Found)
	s.Contains(autoResults[0].Detail, "wtype",
		"wtype must win against ydotool when both are present")
}

func (s *DepCheckSuite) TestAutopasteDeps_Auto_FallsBackToYdotool() {
	cfg := baseAutopasteCfg(config.VoiceAutopasteCommandAuto)
	lookup := s.lookup(map[string]string{
		"ydotool": "/usr/bin/ydotool",
	})

	results, _ := RunDepCheckWith(s.T().Context(), depcheck.ModeDaemon, cfg, lookup, nil, nil)
	autoResults := filterAutopasteDeps(results)
	s.Require().Len(autoResults, 1)
	s.True(autoResults[0].Found)
	s.Contains(autoResults[0].Detail, "ydotool")
}

func (s *DepCheckSuite) TestAutopasteDeps_Auto_NoCandidates_OptionalMissing() {
	cfg := baseAutopasteCfg(config.VoiceAutopasteCommandAuto)

	results, _ := RunDepCheckWith(s.T().Context(), depcheck.ModeDaemon, cfg, s.lookup(nil), nil, nil)
	autoResults := filterAutopasteDeps(results)
	s.Require().Len(autoResults, 1)
	s.False(autoResults[0].Found)
	s.True(autoResults[0].Optional, "auto + no backend must degrade (optional), not fail-closed")
	s.Contains(autoResults[0].InstallTip, "wtype")
}

// --- explicit backends ---

func (s *DepCheckSuite) TestAutopasteDeps_ExplicitWtype_MissingButYdotoolPresent_NoFallback() {
	// Mirrors the adapter-level contract: explicit "wtype" must not silently
	// downgrade to ydotool just because depcheck saw it in PATH.
	cfg := baseAutopasteCfg(config.VoiceAutopasteCommandWtype)
	lookup := s.lookup(map[string]string{
		"ydotool": "/usr/bin/ydotool",
	})

	results, _ := RunDepCheckWith(s.T().Context(), depcheck.ModeDaemon, cfg, lookup, nil, nil)
	autoResults := filterAutopasteDeps(results)
	s.Require().Len(autoResults, 1)
	s.Equal(config.VoiceAutopasteCommandWtype, autoResults[0].Name)
	s.False(autoResults[0].Found, "explicit wtype must report wtype missing, not switch to ydotool")
}

// --- unsupported autopaste_command surfaces as config error, not 'auto' ---

func (s *DepCheckSuite) TestAutopasteDeps_UnsupportedCommand_FailsClosed() {
	cfg := baseAutopasteCfg("xdotool")

	results, fatal := RunDepCheckWith(s.T().Context(), depcheck.ModeDaemon, cfg, s.lookup(nil), nil, nil)
	autoResults := filterAutopasteDeps(results)
	s.Require().Len(autoResults, 1)
	s.False(autoResults[0].Found)
	s.False(autoResults[0].Optional,
		"unsupported autopaste_command must be fatal — silently treating it as auto would mask the typo")
	s.Contains(autoResults[0].InstallTip, "unsupported autopaste_command")
	s.Contains(autoResults[0].InstallTip, "xdotool")
	s.True(fatal)
}

// --- normalisation matches adapter (trim + lowercase) ---

func (s *DepCheckSuite) TestAutopasteDeps_TrimAndCaseFold() {
	cfg := baseAutopasteCfg("  WTYPE  ")
	lookup := s.lookup(map[string]string{
		"wtype": "/usr/bin/wtype",
	})

	results, _ := RunDepCheckWith(s.T().Context(), depcheck.ModeDaemon, cfg, lookup, nil, nil)
	autoResults := filterAutopasteDeps(results)
	s.Require().Len(autoResults, 1)
	s.Equal(config.VoiceAutopasteCommandWtype, autoResults[0].Name,
		"depcheck must normalise the command name like the adapter does — otherwise the two layers disagree")
	s.True(autoResults[0].Found)
}

// --- RunDepCheckWith defensive guards ---

func (s *DepCheckSuite) TestRunDepCheckWith_NilCfg_FatalMissing_NoPanic() {
	s.NotPanics(func() {
		results, fatal := RunDepCheckWith(s.T().Context(), depcheck.ModeDaemon, nil, s.lookup(nil), nil, nil)
		s.True(fatal, "nil cfg is a wiring bug — depcheck must fail-closed, not silently report ok")
		s.Require().Len(results, 1)
		s.Contains(results[0].InstallTip, "nil voice config")
	})
}

func (s *DepCheckSuite) TestRunDepCheckWith_NilLookup_DoesNotPanic() {
	// nil lookup is a test-seam misuse; the guard substitutes the real exec.LookPath
	// so the rest of the pipeline runs without dereferencing nil. We don't assert
	// results — they depend on host PATH — only that nothing panics.
	cfg := &config.VoiceConfig{
		Provider:          config.VoiceProviderGoWhisper,
		Language:          "ru",
		GoWhisper:         config.VoiceGoWhisperConfig{URL: "http://localhost:9081", Timeout: 1},
		ConvertTimeout:    1,
		TranscribeTimeout: 1,
		Output: config.VoiceOutputConfig{
			Mode:             config.VoiceOutputModeClipboard,
			AutopasteCommand: config.VoiceAutopasteCommandAuto,
		},
	}

	s.NotPanics(func() {
		RunDepCheckWith(s.T().Context(), depcheck.ModeDaemon, cfg, nil, nil, nil)
	})
}

func (s *DepCheckSuite) lookup(paths map[string]string) *MockPathLookuper {
	lookup := NewMockPathLookuper(s.ctrl)
	lookup.EXPECT().LookPath(gomock.Any()).DoAndReturn(func(name string) (string, error) {
		if path, ok := paths[name]; ok {
			return path, nil
		}

		return "", errors.New("not found in PATH: " + name)
	}).AnyTimes()

	return lookup
}
