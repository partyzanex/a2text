package cmd

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/suite"
	"github.com/urfave/cli/v3"
)

type CommandSuite struct {
	suite.Suite
}

func TestCommandSuite(t *testing.T) {
	suite.Run(t, new(CommandSuite))
}

// SetupSuite stops urfave/cli from calling os.Exit when an action returns
// cli.Exit(...) — without this, the very first ExitCoder kills the test
// binary mid-suite.
func (s *CommandSuite) SetupSuite() {
	cli.OsExiter = func(int) {}
}

// --- Flag schema: names, aliases, env sources ---

func (s *CommandSuite) TestNewCommand_FlagSchema() {
	cmd := NewCommand()

	type wantFlag struct {
		name    string
		aliases []string
		envVars []string
	}

	cases := []wantFlag{
		{name: FlagConfig, aliases: []string{"c"}, envVars: []string{"A2TEXT_CONFIG"}},
		{name: FlagProvider},
		{name: FlagModelPath},
		{name: FlagLanguage},
		{name: FlagLogLevel},
	}

	for _, want := range cases {
		s.Run(want.name, func() {
			f := lookupFlag(cmd, want.name)
			s.Require().NotNil(f, "flag %q must be registered", want.name)

			str, ok := f.(*cli.StringFlag)
			s.Require().True(ok, "flag %q must be a StringFlag", want.name)

			if len(want.aliases) > 0 {
				s.Equal(want.aliases, str.Aliases, "flag %q aliases mismatch", want.name)
			}

			if len(want.envVars) > 0 {
				s.NotEmpty(str.Sources, "flag %q must declare an env source", want.name)
			}
		})
	}
}

// TestNewCommand_NoUnexpectedFlags pins the visible flag list so a future
// refactor cannot silently add a flag without test coverage updates.
func (s *CommandSuite) TestNewCommand_NoUnexpectedFlags() {
	cmd := NewCommand()

	expected := []string{
		FlagConfig,
		FlagDaemon,
		FlagProvider,
		FlagModelPath,
		FlagLanguage,
		FlagLogLevel,
		FlagPprof,
	}

	got := make([]string, 0, len(cmd.Flags))
	for _, f := range cmd.Flags {
		got = append(got, f.Names()[0])
	}

	slices.Sort(got)
	slices.Sort(expected)

	s.Equal(expected, got, "flag set drifted — update test if a new flag was added intentionally")
}

func lookupFlag(cmd *cli.Command, name string) cli.Flag {
	for _, f := range cmd.Flags {
		if slices.Contains(f.Names(), name) {
			return f
		}
	}

	return nil
}
