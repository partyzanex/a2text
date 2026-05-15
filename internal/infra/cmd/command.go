// Package cmd hosts the CLI/wiring for cmd/a2text. cmd/a2text/main.go is
// a thin shell that calls NewCommand().Run(...).
//
// As of stage I.2 invoking `a2text` with no mode flags is the canonical
// dictation flow: it pings the daemon socket and either sends Toggle or
// becomes the daemon itself (self-bootstrap). The hidden --file/--record
// flags remain as dev/smoke single-shot modes.
package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/partyzanex/a2text/internal/infra/cmd/daemon"
	"github.com/partyzanex/a2text/internal/infra/cmd/setup"
	"github.com/partyzanex/a2text/internal/infra/config"
)

const configErrorExitCode = 2

// buildVersion returns the version string injected at build time.
// Format: "v1.2.3 (abc1234)" or "dev (unknown)" for local builds.
func buildVersion() string {
	return fmt.Sprintf("%s (%s)", Version, Commit)
}

// NewCommand returns the root CLI command for a2text.
//
// Env vars for fields owned by VoiceConfig (provider, language, log_level,
// cloud_*, model_path, etc.) are read by viper inside config.LoadVoice via
// the A2TEXT_ prefix. CLI flags here intentionally do NOT register
// cli.EnvVars sources for those fields — having two pulpits reading the
// same variable just complicates reasoning. Only A2TEXT_CONFIG and
// A2TEXT_FILE (which are not part of the config struct) read env at this
// layer.
const appName = "a2text"

func NewCommand() *cli.Command {
	return &cli.Command{
		Name:    appName,
		Usage:   "voice CLI: file transcription and microphone dictation (one-shot smoke modes)",
		Version: buildVersion(),
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    FlagConfig,
				Aliases: []string{"c"},
				Usage:   "path to config file (default: ./config.yaml or ./app/config.yaml)",
				Sources: cli.EnvVars("A2TEXT_CONFIG"),
			},
			&cli.StringFlag{
				Name:    FlagFile,
				Usage:   "transcribe an audio file and print the result to stdout (dev/smoke)",
				Hidden:  true,
				Sources: cli.EnvVars("A2TEXT_FILE"),
			},
			&cli.DurationFlag{
				Name:    FlagRecord,
				Usage:   "record from the default mic for DURATION, transcribe, print to stdout (dev/smoke)",
				Hidden:  true,
				Sources: cli.EnvVars("A2TEXT_RECORD"),
			},
			&cli.BoolFlag{
				Name:    FlagDaemon,
				Usage:   "run as the dictation daemon (used by the systemd unit; fails if another daemon holds the lock)",
				Sources: cli.EnvVars("A2TEXT_DAEMON"),
			},
			&cli.StringFlag{
				Name:  FlagProvider,
				Usage: "override stt provider: go-whisper | whisper-cpp | cloud",
			},
			&cli.StringFlag{
				Name: FlagCloudProvider,
				Usage: "override cloud stt provider: openai | deepgram " +
					"(requires A2TEXT_CLOUD_API_KEY env var or cloud_api_key in config)",
			},
			&cli.StringFlag{
				Name:  FlagModelPath,
				Usage: "override path to local whisper model (only with --provider=whisper-cpp)",
			},
			&cli.StringFlag{
				Name:  FlagLanguage,
				Usage: "override language hint (e.g. ru, en)",
			},
			&cli.StringFlag{
				Name:  FlagLogLevel,
				Usage: "override log level: debug | info | warn | error",
			},
		},
		Action: action,
		Commands: []*cli.Command{
			setupCommand(),
		},
	}
}

// setupCommand returns the `a2text setup` subcommand. It registers (or
// removes) the global keyboard shortcut in the current desktop environment.
func setupCommand() *cli.Command {
	return &cli.Command{
		Name:  "setup",
		Usage: "register a global keyboard shortcut for voice dictation",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:  "undo",
				Usage: "remove the keyboard shortcut registered by `a2text setup`",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			cfg, err := config.LoadVoice(cmd.Root().String(FlagConfig))
			if err != nil {
				return cli.Exit(fmt.Errorf("failed to load config: %w", err), configErrorExitCode)
			}

			logger := CreateLogger(cfg.LogLevel)

			if cmd.Bool("undo") {
				if err := setup.RunUnsetup(ctx, logger); err != nil {
					return cli.Exit(fmt.Errorf("setup undo: %w", err), 1)
				}

				return nil
			}

			if err := setup.RunSetup(ctx, cfg, logger); err != nil {
				return cli.Exit(fmt.Errorf("setup: %w", err), 1)
			}

			return nil
		},
	}
}

func action(ctx context.Context, cmd *cli.Command) error {
	cfg, err := config.LoadVoice(cmd.String(FlagConfig))
	if err != nil {
		return cli.Exit(fmt.Errorf("failed to load config: %w", err), configErrorExitCode)
	}

	applyFlagOverrides(cmd, cfg)

	// Re-validate after overrides — flags can move the config into an
	// invalid state (e.g. --provider=cloud without cloud_provider/cloud_api_key).
	if err := config.ValidateVoice(cfg); err != nil {
		return cli.Exit(fmt.Errorf("invalid config after CLI overrides: %w", err), configErrorExitCode)
	}

	filePath := cmd.String(FlagFile)
	recordDuration := cmd.Duration(FlagRecord)
	daemonMode := cmd.Bool(FlagDaemon)

	if err := validateModeFlags(cmd, filePath, recordDuration, daemonMode); err != nil {
		return err
	}

	signalCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := CreateLogger(cfg.LogLevel)

	return dispatchMode(signalCtx, cfg, logger, filePath, recordDuration, daemonMode)
}

// dispatchMode routes to the appropriate handler based on the mode flags.
func dispatchMode(
	ctx context.Context,
	cfg *config.VoiceConfig,
	logger *slog.Logger,
	filePath string,
	recordDuration time.Duration,
	daemonMode bool,
) error {
	if filePath != "" {
		return runFileMode(ctx, cfg, logger, filePath)
	}

	if recordDuration > 0 {
		return runRecordMode(ctx, cfg, logger, recordDuration)
	}

	if daemonMode {
		return runDaemonMode(ctx, cfg, logger)
	}

	return runBootstrapMode(ctx, cfg, logger)
}

func runFileMode(ctx context.Context, cfg *config.VoiceConfig, logger *slog.Logger, filePath string) error {
	logger.Info("a2text file mode starting",
		slog.String("provider", cfg.Provider),
		slog.String("language", cfg.Language),
	)

	if err := RunFile(ctx, cfg, logger, filePath, daemon.StdoutWriter()); err != nil {
		return cli.Exit(fmt.Errorf("transcription failed: %w", err), 1)
	}

	return nil
}

func runRecordMode(
	ctx context.Context, cfg *config.VoiceConfig, logger *slog.Logger, recordDuration time.Duration,
) error {
	logger.Info("a2text record mode starting",
		slog.String("provider", cfg.Provider),
		slog.String("language", cfg.Language),
		slog.Duration("duration", recordDuration),
	)

	if err := RunRecord(ctx, cfg, logger, recordDuration, daemon.StdoutWriter()); err != nil {
		return cli.Exit(fmt.Errorf("recording failed: %w", err), 1)
	}

	return nil
}

func runDaemonMode(ctx context.Context, cfg *config.VoiceConfig, logger *slog.Logger) error {
	logger.Info("a2text starting in daemon mode",
		slog.String("provider", cfg.Provider),
		slog.String("language", cfg.Language),
		slog.String("log_level", cfg.LogLevel),
	)

	if err := daemon.RunDaemonOnly(ctx, cfg, logger, daemon.StdoutWriter()); err != nil {
		return cli.Exit(fmt.Errorf("daemon: %w", err), 1)
	}

	return nil
}

func runBootstrapMode(ctx context.Context, cfg *config.VoiceConfig, logger *slog.Logger) error {
	logger.Info("a2text starting in daemon-or-toggle mode",
		slog.String("provider", cfg.Provider),
		slog.String("language", cfg.Language),
		slog.String("log_level", cfg.LogLevel),
	)

	if err := daemon.RunBootstrap(ctx, cfg, logger, daemon.StdoutWriter()); err != nil {
		return cli.Exit(fmt.Errorf("daemon: %w", err), 1)
	}

	return nil
}

// validateModeFlags rejects ambiguous flag combinations before any real
// work starts. Extracted from action() so the dispatcher stays linear and
// the conflict matrix can grow without re-tripping the cyclomatic limit.
//
// Rules:
//   - --file and --record are mutually exclusive smoke modes.
//   - --daemon is a service entrypoint; combining it with smoke modes
//     would silently pick one branch and waste a debugging session.
//   - --record with a non-positive duration is reported up front rather
//     than masked as "daemon not implemented".
func validateModeFlags(cmd *cli.Command, filePath string, recordDuration time.Duration, daemonMode bool) error {
	if filePath != "" && cmd.IsSet(FlagRecord) {
		return cli.Exit("use either --file or --record, not both", configErrorExitCode)
	}

	if daemonMode && (filePath != "" || cmd.IsSet(FlagRecord)) {
		return cli.Exit("--daemon cannot be combined with --file or --record", configErrorExitCode)
	}

	if cmd.IsSet(FlagRecord) && recordDuration <= 0 {
		return cli.Exit("--record duration must be positive (got "+recordDuration.String()+")", configErrorExitCode)
	}

	return nil
}

// applyFlagOverrides mutates cfg in-place with CLI flag values; caller must re-validate after.
func applyFlagOverrides(cmd *cli.Command, cfg *config.VoiceConfig) {
	if val := cmd.String(FlagProvider); val != "" {
		cfg.Provider = val
	}

	if val := cmd.String(FlagCloudProvider); val != "" {
		cfg.CloudProvider = val
	}

	if val := cmd.String(FlagModelPath); val != "" {
		cfg.ModelPath = val
	}

	if val := cmd.String(FlagLanguage); val != "" {
		cfg.Language = val
	}

	if val := cmd.String(FlagLogLevel); val != "" {
		cfg.LogLevel = val
	}
}
