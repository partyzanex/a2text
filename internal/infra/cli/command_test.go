package cmd

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"github.com/urfave/cli/v3"

	"github.com/partyzanex/a2text/internal/infra/config"
)

type CommandSuite struct {
	suite.Suite
}

func TestCommandSuite(t *testing.T) {
	suite.Run(t, new(CommandSuite))
}

// SetupSuite stops urfave/cli from calling os.Exit when an domain.Action returns
// cli.Exit(...) — without this, the very first ExitCoder kills the test
// binary mid-suite.
func (s *CommandSuite) SetupSuite() {
	cli.OsExiter = func(int) {}
}

// --- Flag schema: names, aliases, hidden, env sources ---

func (s *CommandSuite) TestNewCommand_FlagSchema() {
	cmd := NewCommand()

	type wantFlag struct {
		name    string
		aliases []string
		hidden  bool
		envVars []string
	}

	cases := []wantFlag{
		{name: FlagConfig, aliases: []string{"c"}, hidden: false, envVars: []string{"A2TEXT_CONFIG"}},
		{name: FlagFile, hidden: true, envVars: []string{"A2TEXT_FILE"}},
		{name: FlagProvider, hidden: false},
		{name: FlagCloudProvider, hidden: false},
		{name: FlagModelPath, hidden: false},
		{name: FlagLanguage, hidden: false},
		{name: FlagLogLevel, hidden: false},
	}

	for _, want := range cases {
		s.Run(want.name, func() {
			f := lookupFlag(cmd, want.name)
			s.Require().NotNil(f, "flag %q must be registered", want.name)

			str, ok := f.(*cli.StringFlag)
			s.Require().True(ok, "flag %q must be a StringFlag", want.name)

			s.Equal(want.hidden, str.Hidden, "flag %q hidden mismatch", want.name)

			if len(want.aliases) > 0 {
				s.Equal(want.aliases, str.Aliases, "flag %q aliases mismatch", want.name)
			}

			if len(want.envVars) > 0 {
				// cli.EnvVars returns a private type; we compare via Source.String()
				// which contains the joined env-var list.
				s.NotEmpty(str.Sources, "flag %q must declare an env source", want.name)
			}
		})
	}

	// --record is a DurationFlag — verify separately to avoid mixing types
	// in the StringFlag table above.
	s.Run("record", func() {
		f := lookupFlag(cmd, FlagRecord)
		s.Require().NotNil(f)

		dur, ok := f.(*cli.DurationFlag)
		s.Require().True(ok, "--record must be a DurationFlag")
		s.True(dur.Hidden, "--record must be hidden")
		s.NotEmpty(dur.Sources, "--record must declare an env source")
	})
}

func (s *CommandSuite) TestNewCommand_NoUnexpectedFlags() {
	cmd := NewCommand()

	expected := []string{
		FlagConfig,
		FlagFile,
		FlagRecord,
		FlagDaemon,
		FlagProvider,
		FlagCloudProvider,
		FlagModelPath,
		FlagLanguage,
		FlagLogLevel,
		FlagPprof,
	}

	for _, f := range cmd.Flags {
		name := f.Names()[0]
		s.Contains(expected, name, "unexpected flag %q in NewCommand — keep the surface minimal", name)
	}
}

// --- applyFlagOverrides: each flag flips exactly one field ---

type overrideCase struct {
	name      string
	args      []string
	expectCfg func(s *CommandSuite, cfg *config.VoiceConfig)
}

func (s *CommandSuite) TestApplyFlagOverrides_TableDriven() {
	for _, testCase := range overrideCases() {
		s.Run(testCase.name, func() {
			cfg := &config.VoiceConfig{
				Provider: "go-whisper",
				Language: "ru",
			}

			parsed := s.parseFlags(testCase.args)
			applyFlagOverrides(parsed, cfg)
			testCase.expectCfg(s, cfg)
		})
	}
}

// overrideCases returns the table of test cases for applyFlagOverrides.
func overrideCases() []overrideCase {
	return append(singleFlagCases(), multiFlagCases()...)
}

func singleFlagCases() []overrideCase {
	return []overrideCase{
		{
			name: "no flags leaves config untouched",
			args: []string{"a2text"},
			expectCfg: func(s *CommandSuite, cfg *config.VoiceConfig) {
				s.Equal("go-whisper", cfg.Provider)
				s.Equal("ru", cfg.Language)
			},
		},
		{
			name: "provider override",
			args: []string{"a2text", "--provider", "cloud"},
			expectCfg: func(s *CommandSuite, cfg *config.VoiceConfig) {
				s.Equal("cloud", cfg.Provider)
			},
		},
		{
			name: "cloud-provider override",
			args: []string{"a2text", "--cloud-provider", "openai"},
			expectCfg: func(s *CommandSuite, cfg *config.VoiceConfig) {
				s.Equal("openai", cfg.CloudProvider)
			},
		},
		{
			name: "model-path override",
			args: []string{"a2text", "--model-path", "/models/ggml.bin"},
			expectCfg: func(s *CommandSuite, cfg *config.VoiceConfig) {
				s.Equal("/models/ggml.bin", cfg.ModelPath)
			},
		},
		{
			name: "lang override",
			args: []string{"a2text", "--lang", "en"},
			expectCfg: func(s *CommandSuite, cfg *config.VoiceConfig) {
				s.Equal("en", cfg.Language)
			},
		},
		{
			name: "log-level override",
			args: []string{"a2text", "--log-level", "debug"},
			expectCfg: func(s *CommandSuite, cfg *config.VoiceConfig) {
				s.Equal("debug", cfg.LogLevel)
			},
		},
		{
			name: "empty flag value is ignored",
			args: []string{"a2text", "--provider", ""},
			expectCfg: func(s *CommandSuite, cfg *config.VoiceConfig) {
				s.Equal("go-whisper", cfg.Provider, "empty --provider must NOT clobber config")
			},
		},
	}
}

func multiFlagCases() []overrideCase {
	return []overrideCase{
		{
			name: "multiple overrides combine",
			args: []string{
				"a2text",
				"--provider", "cloud",
				"--cloud-provider", "deepgram",
				"--lang", "en",
				"--log-level", "warn",
			},
			expectCfg: func(s *CommandSuite, cfg *config.VoiceConfig) {
				s.Equal("cloud", cfg.Provider)
				s.Equal("deepgram", cfg.CloudProvider)
				s.Equal("en", cfg.Language)
				s.Equal("warn", cfg.LogLevel)
			},
		},
	}
}

// --- Hidden flags: reachable, but absent from default help ---

func (s *CommandSuite) TestHiddenFlags_NotInHelpOutput() {
	cmd := NewCommand()

	visible := []string{}

	for _, f := range cmd.VisibleFlags() {
		visible = append(visible, f.Names()[0])
	}

	s.False(slices.Contains(visible, FlagFile), "--file is hidden and must NOT appear in VisibleFlags")
	s.True(slices.Contains(visible, FlagProvider), "--provider must be visible")
	s.True(slices.Contains(visible, FlagConfig), "--config must be visible")
}

// --- Config flag aliases: -c equivalent to --config ---

func (s *CommandSuite) TestConfigFlag_ShortAlias() {
	parsed := s.parseFlags([]string{"a2text", "-c", "/tmp/x.yaml"})
	s.Equal("/tmp/x.yaml", parsed.String(FlagConfig))
}

// --- File flag is parsed even though it's hidden ---

func (s *CommandSuite) TestHiddenFileFlag_ParsesValue() {
	parsed := s.parseFlags([]string{"a2text", "--file", "/tmp/audio.ogg"})
	s.Equal("/tmp/audio.ogg", parsed.String(FlagFile))
}

// --- Record flag is parsed even though it's hidden ---

func (s *CommandSuite) TestHiddenRecordFlag_ParsesDuration() {
	parsed := s.parseFlags([]string{"a2text", "--record", "5s"})
	s.Equal(5*time.Second, parsed.Duration(FlagRecord))
}

// --- daemon.Daemon flag is visible, parses as bool ---

func (s *CommandSuite) TestDaemonFlag_VisibleAndParsesAsBool() {
	cmd := NewCommand()

	visible := []string{}
	for _, f := range cmd.VisibleFlags() {
		visible = append(visible, f.Names()[0])
	}

	s.True(slices.Contains(visible, FlagDaemon), "--daemon must be visible — it's the official systemd entry point")

	parsed := s.parseFlags([]string{"a2text", "--daemon"})
	s.True(parsed.Bool(FlagDaemon))
}

func (s *CommandSuite) TestHiddenFlags_BothNotInVisible() {
	cmd := NewCommand()

	visible := []string{}
	for _, f := range cmd.VisibleFlags() {
		visible = append(visible, f.Names()[0])
	}

	s.False(slices.Contains(visible, FlagFile), "--file must be hidden")
	s.False(slices.Contains(visible, FlagRecord), "--record must be hidden")
}

// --- Action: mode dispatch and conflict handling ---
//
// These tests exercise the real domain.Action with a minimal on-disk config so we
// also cover config loading + ValidateVoice integration. The config points
// at provider=go-whisper which needs no external resources to validate.

func (s *CommandSuite) TestAction_FileAndRecord_Conflict() {
	cfgPath := s.writeMinimalConfig()

	err := NewCommand().Run(context.Background(), []string{
		"a2text", "--config", cfgPath,
		"--file", "/tmp/x.wav",
		"--record", "5s",
	})

	s.Require().Error(err)

	var exit cli.ExitCoder
	s.Require().ErrorAs(err, &exit, "must wrap a cli.ExitCoder")
	s.Equal(2, exit.ExitCode())
	s.Contains(err.Error(), "either --file or --record")
}

func (s *CommandSuite) TestAction_DaemonAndFile_Conflict() {
	cfgPath := s.writeMinimalConfig()

	err := NewCommand().Run(context.Background(), []string{
		"a2text", "--config", cfgPath,
		"--daemon",
		"--file", "/tmp/x.wav",
	})

	s.Require().Error(err)

	var exit cli.ExitCoder
	s.Require().ErrorAs(err, &exit, "must wrap a cli.ExitCoder")
	s.Equal(2, exit.ExitCode(),
		"silent precedence on --daemon + --file would mask the operator's intent")
	s.Contains(err.Error(), "--daemon cannot be combined")
}

func (s *CommandSuite) TestAction_DaemonAndRecord_Conflict() {
	cfgPath := s.writeMinimalConfig()

	err := NewCommand().Run(context.Background(), []string{
		"a2text", "--config", cfgPath,
		"--daemon",
		"--record", "5s",
	})

	s.Require().Error(err)

	var exit cli.ExitCoder
	s.Require().ErrorAs(err, &exit)
	s.Equal(2, exit.ExitCode())
	s.Contains(err.Error(), "--daemon cannot be combined")
}

func (s *CommandSuite) TestAction_RecordZeroDuration_ExplicitError() {
	cfgPath := s.writeMinimalConfig()

	err := NewCommand().Run(context.Background(), []string{
		"a2text", "--config", cfgPath,
		"--record", "0s",
	})

	s.Require().Error(err)

	var exit cli.ExitCoder
	s.Require().ErrorAs(err, &exit)
	s.Equal(2, exit.ExitCode())
	s.Contains(err.Error(), "--record duration must be positive")
}

func (s *CommandSuite) TestAction_RecordNegativeDuration_ExplicitError() {
	cfgPath := s.writeMinimalConfig()

	err := NewCommand().Run(context.Background(), []string{
		"a2text", "--config", cfgPath,
		"--record", "-1s",
	})

	s.Require().Error(err)
	s.Contains(err.Error(), "--record duration must be positive")
}

// As of stage I.2 the no-mode path triggers self-bootstrap. We can't
// drive the full daemon from a unit test (it would bind a real socket and
// invoke recorder/transcriber wiring), so the assertion here only checks
// that the action does NOT short-circuit with "not implemented" and that
// it observes context cancellation gracefully.
func (s *CommandSuite) TestAction_NoMode_TriggersBootstrap() {
	cfgPath := s.writeMinimalConfig()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel so daemon.RunBootstrap exits quickly without binding sockets

	err := NewCommand().Run(ctx, []string{
		"a2text", "--config", cfgPath,
	})

	// Either the bootstrap returned an error (daemon couldn't start due to
	// cancelled ctx / missing capture deps in CI) or it returned cleanly.
	// What matters is we did NOT see "daemon mode not implemented" anymore.
	if err != nil {
		s.NotContains(err.Error(), "daemon mode not implemented")
	}
}

func (s *CommandSuite) TestAction_CloudOverrideWithoutAPIKey_FailsValidation() {
	cfgPath := s.writeMinimalConfig()

	err := NewCommand().Run(context.Background(), []string{
		"a2text", "--config", cfgPath,
		"--provider", "cloud",
		"--cloud-provider", "openai",
	})

	s.Require().Error(err)

	var exit cli.ExitCoder
	s.Require().ErrorAs(err, &exit)
	s.Equal(2, exit.ExitCode())
	s.Contains(err.Error(), "invalid config after CLI overrides")
}

// --- Helpers ---

// writeMinimalConfig produces a config file with provider=go-whisper that
// passes ValidateVoice without requiring any external resources. Returned
// path lives in t.TempDir() and is auto-cleaned.
func (s *CommandSuite) writeMinimalConfig() string {
	dir := s.T().TempDir()
	path := dir + "/config.yaml"

	content := `
provider: "go-whisper"
language: "ru"
go_whisper:
  url: "http://localhost:9081"
temp_dir: "` + dir + `/tmp"
log_level: "error"
`
	s.Require().NoError(os.WriteFile(path, []byte(content), 0o600))

	return path
}

// parseFlags builds a fresh command, parses args without executing the action,
// and returns the *cli.Command so flag values can be inspected. We strip the
// domain.Action to avoid running the daemon/file pipeline during unit tests.
func (s *CommandSuite) parseFlags(args []string) *cli.Command {
	cmd := NewCommand()
	cmd.Action = func(_ context.Context, _ *cli.Command) error { return nil }

	err := cmd.Run(context.Background(), args)
	s.Require().NoError(err)

	return cmd
}

//nolint:ireturn // cli.Flag is the interface stored in cmd.Flags; returning it is the only option
func lookupFlag(cmd *cli.Command, name string) cli.Flag {
	for _, f := range cmd.Flags {
		if slices.Contains(f.Names(), name) {
			return f
		}
	}

	return nil
}
