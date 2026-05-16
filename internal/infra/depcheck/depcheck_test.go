package depcheck_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/infra/depcheck"
)

type DepCheckSuite struct {
	suite.Suite
}

func TestDepCheckSuite(t *testing.T) {
	suite.Run(t, new(DepCheckSuite))
}

// --- helpers ---

func testEnv(paths map[string]string) depcheck.Env {
	return depcheck.Env{
		LookPath: func(name string) (string, error) {
			if path, ok := paths[name]; ok {
				return path, nil
			}

			return "", errors.New("not found in PATH: " + name)
		},
		StatFile: func(name string) (os.FileInfo, error) {
			return nil, errors.New("stat: not found: " + name)
		},
		HTTPHead:            func(_ context.Context, _ string) (int, error) { return 0, errors.New("no network in tests") },
		WhisperCppAvailable: func() bool { return false },
	}
}

// testEnvWithStat returns an Env where StatFile succeeds for files listed in statFiles.
// Uses os.Stat(".") to produce a real (non-nil) FileInfo so the nilnil linter is satisfied.
func testEnvWithStat(paths map[string]string, statFiles map[string]bool) depcheck.Env {
	env := testEnv(paths)

	env.StatFile = func(name string) (os.FileInfo, error) {
		if statFiles[name] {
			return os.Stat(".") // always valid; we only need a non-nil FileInfo
		}

		return nil, errors.New("stat: not found: " + name)
	}

	return env
}

func filterGroup(deps []depcheck.Dependency, group string) []depcheck.Dependency {
	var out []depcheck.Dependency

	for _, dep := range deps {
		if dep.Group == group {
			out = append(out, dep)
		}
	}

	return out
}

func missingNames(missing []depcheck.MissingDep) []string {
	names := make([]string, 0, len(missing))

	for _, md := range missing {
		names = append(names, md.Dep.Name)
	}

	return names
}

func baseGoWhisperCfg() *config.VoiceConfig {
	return &config.VoiceConfig{
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
}

// --- CheckMode: nil cfg ---

func (s *DepCheckSuite) TestCheckMode_NilCfg_OneRequiredMissingDep() {
	allDeps, missing := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, nil, testEnv(nil))
	s.Require().Len(allDeps, 1)
	s.Require().Len(missing, 1)
	s.False(missing[0].Dep.Optional, "nil cfg must be fatal, not optional")
	s.Contains(missing[0].Dep.InstallHint, "nil voice config")
}

// --- autopaste deps: only for clipboard_autopaste ---

func (s *DepCheckSuite) TestCheckMode_ClipboardMode_NoAutopasteDep() {
	cfg := baseGoWhisperCfg()
	cfg.Output.Mode = config.VoiceOutputModeClipboard

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(map[string]string{
		"pw-record": "/usr/bin/pw-record",
	}))
	autoDeps := filterGroup(allDeps, depcheck.GroupAutopaste)
	s.Empty(autoDeps, "plain clipboard mode must not include autopaste deps")
}

// --- autopaste: auto resolution ---

func (s *DepCheckSuite) TestCheckMode_AutopasteDep_Auto_PrefersWtype() {
	cfg := baseGoWhisperCfg()
	cfg.Output.Mode = config.VoiceOutputModeClipboardAutopaste
	cfg.Output.AutopasteCommand = config.VoiceAutopasteCommandAuto

	allDeps, missing := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(map[string]string{
		"pw-record": "/usr/bin/pw-record",
		"wl-copy":   "/usr/bin/wl-copy",
		"wtype":     "/usr/bin/wtype",
		"ydotool":   "/usr/bin/ydotool",
	}))

	autoDeps := filterGroup(allDeps, depcheck.GroupAutopaste)
	s.Require().Len(autoDeps, 1, "one autopaste dep expected")

	res := autoDeps[0].Check(s.T().Context(), testEnv(map[string]string{
		"wtype":   "/usr/bin/wtype",
		"ydotool": "/usr/bin/ydotool",
	}))
	s.True(res.Found)
	s.Contains(res.Detail, "wtype", "wtype must win against ydotool when both are present")

	for _, md := range missing {
		s.NotEqual(depcheck.GroupAutopaste, md.Dep.Group)
	}
}

func (s *DepCheckSuite) TestCheckMode_AutopasteDep_Auto_FallsBackToYdotool() {
	cfg := baseGoWhisperCfg()
	cfg.Output.Mode = config.VoiceOutputModeClipboardAutopaste
	cfg.Output.AutopasteCommand = config.VoiceAutopasteCommandAuto

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(nil))
	autoDeps := filterGroup(allDeps, depcheck.GroupAutopaste)
	s.Require().Len(autoDeps, 1)

	res := autoDeps[0].Check(s.T().Context(), testEnv(map[string]string{"ydotool": "/usr/bin/ydotool"}))
	s.True(res.Found)
	s.Contains(res.Detail, "ydotool")
}

func (s *DepCheckSuite) TestCheckMode_AutopasteDep_Auto_NoCandidates_OptionalMissing() {
	cfg := baseGoWhisperCfg()
	cfg.Output.Mode = config.VoiceOutputModeClipboardAutopaste
	cfg.Output.AutopasteCommand = config.VoiceAutopasteCommandAuto

	allDeps, missing := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(map[string]string{
		"pw-record": "/usr/bin/pw-record",
	}))
	autoDeps := filterGroup(allDeps, depcheck.GroupAutopaste)
	s.Require().Len(autoDeps, 1)
	s.True(autoDeps[0].Optional, "auto + no backend must degrade (optional), not fail-closed")

	var foundMissing bool

	for _, md := range missing {
		if md.Dep.Group == depcheck.GroupAutopaste {
			foundMissing = true

			s.True(md.Dep.Optional)
		}
	}

	s.True(foundMissing, "missing optional dep should appear in MissingDep list")
}

// --- autopaste: explicit backend ---

func (s *DepCheckSuite) TestCheckMode_AutopasteDep_ExplicitWtype_NoFallback() {
	cfg := baseGoWhisperCfg()
	cfg.Output.Mode = config.VoiceOutputModeClipboardAutopaste
	cfg.Output.AutopasteCommand = config.VoiceAutopasteCommandWtype

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(nil))
	autoDeps := filterGroup(allDeps, depcheck.GroupAutopaste)
	s.Require().Len(autoDeps, 1)
	s.Equal(config.VoiceAutopasteCommandWtype, autoDeps[0].Name)

	res := autoDeps[0].Check(s.T().Context(), testEnv(map[string]string{"ydotool": "/usr/bin/ydotool"}))
	s.False(res.Found, "explicit wtype must report wtype missing, not switch to ydotool")
}

// --- autopaste: unknown command is fatal ---

func (s *DepCheckSuite) TestCheckMode_AutopasteDep_UnsupportedCommand_FailsClosed() {
	cfg := baseGoWhisperCfg()
	cfg.Output.Mode = config.VoiceOutputModeClipboardAutopaste
	cfg.Output.AutopasteCommand = "banana" // bypasses ValidateVoice in test

	allDeps, missing := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(map[string]string{
		"pw-record": "/usr/bin/pw-record",
	}))
	autoDeps := filterGroup(allDeps, depcheck.GroupAutopaste)
	s.Require().Len(autoDeps, 1)
	s.False(autoDeps[0].Optional, "unsupported command must be fatal, not optional")
	s.Contains(autoDeps[0].InstallHint, "unsupported autopaste_command")
	s.Contains(autoDeps[0].InstallHint, "banana")

	var foundFatal bool

	for _, md := range missing {
		if md.Dep.Group == depcheck.GroupAutopaste {
			foundFatal = true

			s.False(md.Dep.Optional)
		}
	}

	s.True(foundFatal)
}

// --- autopaste: normalisation (trim + case fold) ---

func (s *DepCheckSuite) TestCheckMode_AutopasteDep_TrimAndCaseFold() {
	cfg := baseGoWhisperCfg()
	cfg.Output.Mode = config.VoiceOutputModeClipboardAutopaste
	cfg.Output.AutopasteCommand = "  WTYPE  "

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(nil))
	autoDeps := filterGroup(allDeps, depcheck.GroupAutopaste)
	s.Require().Len(autoDeps, 1)
	s.Equal(config.VoiceAutopasteCommandWtype, autoDeps[0].Name,
		"depcheck must normalise the command like the adapter does")
}

// --- ModeFileWAV: only STT ---

func (s *DepCheckSuite) TestCheckMode_ModeFileWAV_GoWhisper_NoCaptureNoFFmpegDeps() {
	cfg := baseGoWhisperCfg()

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeFileWAV, cfg, testEnv(nil))

	captureDeps := filterGroup(allDeps, depcheck.GroupAudio)
	s.Empty(captureDeps, "WAV file mode must not check audio capture or conversion")

	sttDeps := filterGroup(allDeps, depcheck.GroupSTT)
	s.NotEmpty(sttDeps)
}

// --- ModeFileAudio: ffmpeg + STT ---

func (s *DepCheckSuite) TestCheckMode_ModeFileAudio_GoWhisper_RequiresFFmpeg() {
	cfg := baseGoWhisperCfg()

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeFileAudio, cfg, testEnv(nil))

	audioDeps := filterGroup(allDeps, depcheck.GroupAudio)
	s.Require().NotEmpty(audioDeps)

	var ffmpegFound bool

	for _, dep := range audioDeps {
		if dep.Name == "ffmpeg" {
			ffmpegFound = true

			s.False(dep.Optional, "ffmpeg must be required for audio conversion")
		}
	}

	s.True(ffmpegFound)
}

// --- ModeFileWAV: whisper-cpp, no ffmpeg ---

func (s *DepCheckSuite) TestCheckMode_ModeFileWAV_WhisperCpp_NoFFmpegDep() {
	cfg := baseGoWhisperCfg()
	cfg.Provider = config.VoiceProviderWhisperCpp
	cfg.ModelPath = "/models/ggml-small.bin"

	env := testEnvWithStat(nil, map[string]bool{"/models/ggml-small.bin": true})
	env.WhisperCppAvailable = func() bool { return true }

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeFileWAV, cfg, env)

	for _, dep := range allDeps {
		s.NotEqual("ffmpeg", dep.Name, "WAV file + whisper-cpp must not require ffmpeg")
	}
}

// --- ModeFileAudio: whisper-cpp, ffmpeg required ---

func (s *DepCheckSuite) TestCheckMode_ModeFileAudio_WhisperCpp_FFmpegRequired() {
	cfg := baseGoWhisperCfg()
	cfg.Provider = config.VoiceProviderWhisperCpp
	cfg.ModelPath = "/models/ggml-small.bin"

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeFileAudio, cfg, testEnv(nil))

	var ffmpegFound bool

	for _, dep := range allDeps {
		if dep.Name == "ffmpeg" {
			ffmpegFound = true
		}
	}

	s.True(ffmpegFound, "non-WAV file + whisper-cpp must require ffmpeg")
}

// --- ModeDaemon: whisper-cpp includes ffmpeg ---

func (s *DepCheckSuite) TestCheckMode_ModeDaemon_WhisperCpp_IncludesFFmpeg() {
	cfg := baseGoWhisperCfg()
	cfg.Provider = config.VoiceProviderWhisperCpp
	cfg.ModelPath = "/models/ggml-small.bin"

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(nil))

	var ffmpegFound bool

	for _, dep := range allDeps {
		if dep.Name == "ffmpeg" {
			ffmpegFound = true
		}
	}

	s.True(ffmpegFound, "daemon with whisper-cpp must check for ffmpeg (needed for audio pipeline)")
}

// --- whisper-cpp: os.Stat probe for model file ---

// TestCheckMode_WhisperCpp_ModelMissing_BootTolerant verifies that a
// linked whisper-cpp binary with a misconfigured (missing on disk)
// model_path does NOT block daemon startup. The dep reports Found
// with a Detail flagging the issue — surfaced in logs — but no
// missing-dep error is raised. Rationale: the user needs to be able
// to open the settings window and either fix the path or download a
// new model; refusing to boot strands them on the CLI.
func (s *DepCheckSuite) TestCheckMode_WhisperCpp_ModelMissing_BootTolerant() {
	cfg := baseGoWhisperCfg()
	cfg.Provider = config.VoiceProviderWhisperCpp
	cfg.ModelPath = "/models/ggml-small.bin"

	env := testEnv(nil)
	env.WhisperCppAvailable = func() bool { return true }

	allDeps, missing := depcheck.CheckMode(s.T().Context(), depcheck.ModeFileWAV, cfg, env)
	sttDeps := filterGroup(allDeps, depcheck.GroupSTT)
	s.Require().NotEmpty(sttDeps)

	names := missingNames(missing)
	s.NotContains(names, "whisper-cpp",
		"misconfigured model_path must NOT fail-hard depcheck — daemon must still boot so settings can fix it")

	// And the Detail line must communicate the actual problem so the
	// user (or operator scanning the log) can act on it. Re-run the
	// Check via the dep's closure — that's how every other test in
	// this file inspects Detail fields.
	var sttDetail string

	for _, dep := range sttDeps {
		if dep.Name == "whisper-cpp" {
			sttDetail = dep.Check(s.T().Context(), env).Detail

			break
		}
	}

	s.Contains(sttDetail, "missing on disk")
}

func (s *DepCheckSuite) TestCheckMode_WhisperCpp_ModelPresent_Found() {
	cfg := baseGoWhisperCfg()
	cfg.Provider = config.VoiceProviderWhisperCpp
	cfg.ModelPath = "/models/ggml-small.bin"

	env := testEnvWithStat(nil, map[string]bool{"/models/ggml-small.bin": true})
	env.WhisperCppAvailable = func() bool { return true }

	_, missing := depcheck.CheckMode(s.T().Context(), depcheck.ModeFileWAV, cfg, env)

	for _, md := range missing {
		s.NotEqual("whisper-cpp", md.Dep.Name,
			"whisper-cpp must be found when binary is linked and model exists")
	}
}

// --- Detail sanitization: no absolute paths or credentials in dep output ---

func (s *DepCheckSuite) TestCheckMode_GoWhisper_Detail_OmitsUserinfo() {
	cfg := baseGoWhisperCfg()
	cfg.GoWhisper.URL = "http://user:secret@localhost:9081"

	env := testEnv(nil)
	env.HTTPHead = func(_ context.Context, _ string) (int, error) { return 200, nil }

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeFileWAV, cfg, env)

	for _, dep := range allDeps {
		if dep.Group == depcheck.GroupSTT {
			res := dep.Check(s.T().Context(), env)
			s.True(res.Found, "reachable go-whisper must be found")
			s.NotContains(res.Detail, "secret",
				"STT detail must not expose credentials from the config URL")
		}
	}
}

func (s *DepCheckSuite) TestCheckMode_WhisperCpp_Detail_BasenameOnly() {
	cfg := baseGoWhisperCfg()
	cfg.Provider = config.VoiceProviderWhisperCpp
	cfg.ModelPath = "/home/user/.local/share/whisper/ggml-small.bin"

	env := testEnvWithStat(nil, map[string]bool{cfg.ModelPath: true})
	env.WhisperCppAvailable = func() bool { return true }

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeFileWAV, cfg, env)

	for _, dep := range allDeps {
		if dep.Group == depcheck.GroupSTT {
			res := dep.Check(s.T().Context(), env)
			s.True(res.Found)
			s.NotContains(res.Detail, "/home",
				"whisper-cpp model detail must use basename, not the full path")
			s.Contains(res.Detail, "ggml-small.bin")
		}
	}
}

func (s *DepCheckSuite) TestCheckMode_AudioDep_Detail_NoAbsolutePath() {
	cfg := baseGoWhisperCfg()

	pathEnv := testEnv(map[string]string{"ffmpeg": "/usr/local/bin/ffmpeg"})

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeFileAudio, cfg, pathEnv)

	for _, dep := range allDeps {
		if dep.Group == depcheck.GroupAudio {
			res := dep.Check(s.T().Context(), pathEnv)
			if res.Found {
				s.NotContains(res.Detail, "/usr",
					"audio dep detail must not expose the full binary path")
			}
		}
	}
}

// --- unknownProviderDep: empty string gets a non-empty name ---

func (s *DepCheckSuite) TestCheckMode_UnknownProvider_EmptyString_HasNonEmptyName() {
	cfg := baseGoWhisperCfg()
	cfg.Provider = "" // bypasses ValidateVoice in test

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(nil))

	sttDeps := filterGroup(allDeps, depcheck.GroupSTT)
	s.Require().NotEmpty(sttDeps)
	s.NotEmpty(sttDeps[0].Name, "empty provider must produce a dep with a non-empty name")
}

// --- buildDeps: unknown mode is a fatal dep, not an empty list ---

func (s *DepCheckSuite) TestCheckMode_UnknownMode_ReturnsFatalDep() {
	cfg := baseGoWhisperCfg()

	allDeps, missing := depcheck.CheckMode(s.T().Context(), depcheck.CLIMode(999), cfg, testEnv(nil))
	s.Require().NotEmpty(allDeps, "unknown mode must return a fatal dep, not an empty list")
	s.Require().NotEmpty(missing)

	sysDeps := filterGroup(allDeps, depcheck.GroupSystem)
	s.Require().NotEmpty(sysDeps)
	s.False(sysDeps[0].Optional, "unknown-mode dep must be required, not optional")
	s.Contains(sysDeps[0].InstallHint, "internal error")
}

// --- sanitizeURL: strips query params ---

func (s *DepCheckSuite) TestCheckMode_GoWhisper_Detail_StripsQueryParams() {
	cfg := baseGoWhisperCfg()
	cfg.GoWhisper.URL = "http://localhost:9081/api?token=supersecret"

	env := testEnv(nil)
	env.HTTPHead = func(_ context.Context, _ string) (int, error) { return 200, nil }

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeFileWAV, cfg, env)

	for _, dep := range allDeps {
		if dep.Group == depcheck.GroupSTT {
			res := dep.Check(s.T().Context(), env)
			s.True(res.Found)
			s.NotContains(res.Detail, "supersecret",
				"query params must be stripped from URL detail to avoid token leakage")
			s.NotContains(res.Detail, "token")
		}
	}
}

// --- goWhisperDeps: HTTP reachability probe ---

func (s *DepCheckSuite) TestCheckMode_GoWhisper_ServiceReachable_Found() {
	cfg := baseGoWhisperCfg()

	env := testEnv(nil)
	env.HTTPHead = func(_ context.Context, _ string) (int, error) { return 200, nil }

	_, missing := depcheck.CheckMode(s.T().Context(), depcheck.ModeFileWAV, cfg, env)

	names := missingNames(missing)
	s.NotContains(names, config.VoiceProviderGoWhisper,
		"go-whisper dep must be found when the service responds to HTTP HEAD")
}

func (s *DepCheckSuite) TestCheckMode_GoWhisper_ServiceUnreachable_NotFound() {
	cfg := baseGoWhisperCfg()

	// HTTPHead returns error (network failure, service down, etc.)
	env := testEnv(nil) // already returns error from HTTPHead

	_, missing := depcheck.CheckMode(s.T().Context(), depcheck.ModeFileWAV, cfg, env)

	names := missingNames(missing)
	s.Contains(names, config.VoiceProviderGoWhisper,
		"go-whisper dep must be missing when HTTP HEAD fails")
}

func (s *DepCheckSuite) TestCheckMode_GoWhisper_AnyStatusCode_CountsAsReachable() {
	cfg := baseGoWhisperCfg()

	// 404 still means the server is up — model list endpoint may not exist but
	// the service is reachable. Only a network error means "not found".
	env := testEnv(nil)
	env.HTTPHead = func(_ context.Context, _ string) (int, error) { return 404, nil }

	_, missing := depcheck.CheckMode(s.T().Context(), depcheck.ModeFileWAV, cfg, env)

	names := missingNames(missing)
	s.NotContains(names, config.VoiceProviderGoWhisper,
		"any HTTP status code (including 4xx) must count as reachable")
}

func (s *DepCheckSuite) TestCheckMode_GoWhisper_ProbesModelEndpoint() {
	cfg := baseGoWhisperCfg()
	cfg.GoWhisper.URL = "http://localhost:9081/api/whisper"

	var probed string

	env := testEnv(nil)
	env.HTTPHead = func(_ context.Context, url string) (int, error) {
		probed = url

		return 200, nil
	}

	depcheck.CheckMode(s.T().Context(), depcheck.ModeFileWAV, cfg, env)

	s.Equal("http://localhost:9081/api/whisper/model", probed,
		"HTTP probe must target <go_whisper.url><go_whisper.prefix>/model")
}

func (s *DepCheckSuite) TestCheckMode_GoWhisper_EmptyURL_NotFound() {
	cfg := baseGoWhisperCfg()
	cfg.GoWhisper.URL = ""

	env := testEnv(nil)
	env.HTTPHead = func(_ context.Context, _ string) (int, error) { return 200, nil }

	_, missing := depcheck.CheckMode(s.T().Context(), depcheck.ModeFileWAV, cfg, env)

	names := missingNames(missing)
	s.Contains(names, config.VoiceProviderGoWhisper,
		"empty go_whisper.url must be missing regardless of HTTPHead")
}

// --- unknownProviderDep: newline in provider name is sanitized ---

func (s *DepCheckSuite) TestCheckMode_UnknownProvider_Newline_SanitizedInName() {
	cfg := baseGoWhisperCfg()
	cfg.Provider = "bad\nprovider\nwith\nnewlines"

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(nil))

	sttDeps := filterGroup(allDeps, depcheck.GroupSTT)
	s.Require().NotEmpty(sttDeps)
	s.NotContains(sttDeps[0].Name, "\n",
		"provider name with embedded newlines must be sanitized before appearing in dep output")
}

// --- openAI provider with empty key surfaces a dep error, not a panic ---

func (s *DepCheckSuite) TestCheckMode_OpenAI_NoKey_ReportsMissing() {
	cfg := baseGoWhisperCfg()
	cfg.Provider = config.VoiceProviderOpenAI
	cfg.OpenAI = config.VoiceOpenAIConfig{APIKey: ""}

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeFileWAV, cfg, testEnv(nil))
	sttDeps := filterGroup(allDeps, depcheck.GroupSTT)
	s.Require().NotEmpty(sttDeps)
}

// --- autopasteDeps: Output.Mode with spaces and caps is normalised ---

func (s *DepCheckSuite) TestCheckMode_OutputMode_SpacesAndCaps_Normalised() {
	cfg := baseGoWhisperCfg()
	cfg.Output.Mode = "  CLIPBOARD_AUTOPASTE  "
	cfg.Output.AutopasteCommand = config.VoiceAutopasteCommandAuto

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(map[string]string{
		"pw-record": "/usr/bin/pw-record",
	}))
	autoDeps := filterGroup(allDeps, depcheck.GroupAutopaste)
	s.NotEmpty(autoDeps, "output mode with surrounding spaces and caps must still trigger autopaste deps")
}

// --- MissingDependencyError ---

func (s *DepCheckSuite) TestMissingDependencyError_Format() {
	err := &depcheck.MissingDependencyError{
		Name:        "ffmpeg",
		RequiredFor: "audio conversion to WAV",
		InstallHint: "sudo apt install ffmpeg",
	}
	s.Equal(
		"missing dependency: ffmpeg\n  required for: audio conversion to WAV\n  install: sudo apt install ffmpeg",
		err.Error(),
	)
}

// --- ModeDaemon: platform dep always present and found ---

func (s *DepCheckSuite) TestCheckMode_ModeDaemon_PlatformDepAlwaysFound() {
	cfg := baseGoWhisperCfg()
	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(nil))

	var platformFound bool

	for _, dep := range allDeps {
		if dep.Name == "platform" && dep.Group == depcheck.GroupSystem {
			res := dep.Check(s.T().Context(), testEnv(nil))
			s.True(res.Found)
			s.NotEmpty(res.Detail)

			platformFound = true
		}
	}

	s.True(platformFound)
}

// --- Clipboard dep: probes wl-copy first, xclip as fallback ---

// TestCheckMode_ClipboardDep_WlCopyFound verifies that wl-copy in PATH is
// reported as found and surfaces "wl-copy" in the result detail.
func (s *DepCheckSuite) TestCheckMode_ClipboardDep_WlCopyFound() {
	cfg := baseGoWhisperCfg()

	env := testEnv(map[string]string{
		"pw-record": "/usr/bin/pw-record",
		"wl-copy":   "/usr/bin/wl-copy",
	})

	allDeps, missing := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, env)

	clipDeps := filterGroup(allDeps, depcheck.GroupClipboard)
	s.Require().Len(clipDeps, 1)

	res := clipDeps[0].Check(s.T().Context(), env)
	s.True(res.Found, "wl-copy present: clipboard dep must be found")
	s.Equal("wl-copy", res.Detail)

	for _, md := range missing {
		s.NotEqual(depcheck.GroupClipboard, md.Dep.Group,
			"clipboard must not appear in missing list when wl-copy is present")
	}
}

// TestCheckMode_ClipboardDep_XclipFound_NoWlCopy verifies that xclip is used
// as a fallback when wl-copy is absent, keeping the dep from appearing in the
// missing list.
func (s *DepCheckSuite) TestCheckMode_ClipboardDep_XclipFound_NoWlCopy() {
	cfg := baseGoWhisperCfg()

	env := testEnv(map[string]string{
		"pw-record": "/usr/bin/pw-record",
		"xclip":     "/usr/bin/xclip",
	})

	allDeps, missing := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, env)

	clipDeps := filterGroup(allDeps, depcheck.GroupClipboard)
	s.Require().Len(clipDeps, 1)

	res := clipDeps[0].Check(s.T().Context(), env)
	s.True(res.Found, "xclip present, wl-copy absent: clipboard dep must still be found")
	s.Equal("xclip", res.Detail)

	for _, md := range missing {
		s.NotEqual(depcheck.GroupClipboard, md.Dep.Group,
			"clipboard must not appear in missing list when xclip is present")
	}
}

// TestCheckMode_ClipboardDep_NeitherFound_OptionalMissing verifies that when
// neither wl-copy nor xclip are in PATH the dep is reported as optional-missing
// (not fatal — users can run in stdout mode).
func (s *DepCheckSuite) TestCheckMode_ClipboardDep_NeitherFound_OptionalMissing() {
	cfg := baseGoWhisperCfg()

	env := testEnv(map[string]string{"pw-record": "/usr/bin/pw-record"})

	allDeps, missing := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, env)

	clipDeps := filterGroup(allDeps, depcheck.GroupClipboard)
	s.Require().Len(clipDeps, 1)
	s.True(clipDeps[0].Optional, "missing clipboard must be optional, not fatal")

	res := clipDeps[0].Check(s.T().Context(), env)
	s.False(res.Found)

	var foundMissing bool

	for _, md := range missing {
		if md.Dep.Group == depcheck.GroupClipboard {
			foundMissing = true

			s.True(md.Dep.Optional, "clipboard missing dep must carry Optional=true")
		}
	}

	s.True(foundMissing, "clipboard dep must appear in missing list when neither wl-copy nor xclip found")
}

// TestCheckMode_ClipboardDep_NameIncludesBothAlternatives verifies that the dep
// name communicates both Wayland and X11 alternatives to the operator.
func (s *DepCheckSuite) TestCheckMode_ClipboardDep_NameIncludesBothAlternatives() {
	cfg := baseGoWhisperCfg()

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(nil))

	clipDeps := filterGroup(allDeps, depcheck.GroupClipboard)
	s.Require().Len(clipDeps, 1)
	s.Contains(clipDeps[0].Name, "wl-copy", "dep name must mention wl-copy")
	s.Contains(clipDeps[0].Name, "xclip", "dep name must mention xclip")
}

// --- ModeRecord: no platform dep ---

func (s *DepCheckSuite) TestCheckMode_ModeRecord_NoPlatformDep() {
	cfg := baseGoWhisperCfg()
	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeRecord, cfg, testEnv(nil))

	for _, dep := range allDeps {
		s.NotEqual("platform", dep.Name, "ModeRecord must not include the platform info dep")
	}
}
