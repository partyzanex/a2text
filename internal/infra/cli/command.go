// Package cmd hosts the CLI/wiring for cmd/a2text. cmd/a2text/main.go is a
// thin shell that calls NewCommand().Run(...).
//
// `a2text` (no flags) starts the dictation daemon under a PID lock. If
// another daemon already holds the lock, the second invocation exits
// cleanly without disturbing it. `a2text --daemon` is the explicit
// systemd entry point: it returns an error on lock contention so the
// unit goes Failed instead of looking like a no-op success.
package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v3"

	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/infra/daemon"
)

const configErrorExitCode = 2

// buildVersion returns the version string injected at build time.
// Format: "v1.2.3 (abc1234)" or "dev (unknown)" for local builds.
func buildVersion() string {
	return fmt.Sprintf("%s (%s)", Version, Commit)
}

const appName = "a2text"

func NewCommand() *cli.Command {
	return &cli.Command{
		Name:    appName,
		Usage:   "voice dictation daemon (push-to-talk via evdev hotkey, autopaste, tray UI)",
		Version: buildVersion(),
		Flags:   rootFlags(),
		Action:  action,
	}
}

// rootFlags returns the cli.Flag set for the root command. Extracted out
// of NewCommand so the latter stays under the funlen threshold.
func rootFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    FlagConfig,
			Aliases: []string{"c"},
			Usage:   "path to config file (default: per-user config dir, then ./config.yaml)",
			Sources: cli.EnvVars("A2TEXT_CONFIG"),
		},
		&cli.BoolFlag{
			Name:    FlagDaemon,
			Usage:   "run as the dictation daemon (used by the systemd unit; fails if another daemon holds the lock)",
			Sources: cli.EnvVars("A2TEXT_DAEMON"),
		},
		&cli.StringFlag{
			Name:  FlagProvider,
			Usage: "override stt provider: go-whisper | whisper-cpp | openai | deepgram",
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
		&cli.StringFlag{
			Name: FlagPprof,
			Usage: "enable pprof endpoint on host:port (e.g. 127.0.0.1:6060); " +
				"empty = disabled. Loopback only unless you really mean to expose it.",
			Sources: cli.EnvVars("A2TEXT_PPROF"),
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
	// invalid state (e.g. --provider=openai without openai.api_key).
	if err := config.ValidateVoice(cfg); err != nil {
		return cli.Exit(fmt.Errorf("invalid config after CLI overrides: %w", err), configErrorExitCode)
	}

	signalCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := CreateLogger(cfg.LogLevel)

	if err := startPprof(signalCtx, cmd.String(FlagPprof), logger); err != nil {
		return cli.Exit(fmt.Errorf("pprof: %w", err), 1)
	}

	if cmd.Bool(FlagDaemon) {
		return runDaemonMode(signalCtx, cfg, logger)
	}

	return runBootstrapMode(signalCtx, cfg, logger)
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
	logger.Info("a2text starting",
		slog.String("provider", cfg.Provider),
		slog.String("language", cfg.Language),
		slog.String("log_level", cfg.LogLevel),
	)

	if err := daemon.RunBootstrap(ctx, cfg, logger, daemon.StdoutWriter()); err != nil {
		return cli.Exit(fmt.Errorf("daemon: %w", err), 1)
	}

	return nil
}

// applyFlagOverrides mutates cfg in-place with CLI flag values; caller must re-validate after.
func applyFlagOverrides(cmd *cli.Command, cfg *config.VoiceConfig) {
	if val := cmd.String(FlagProvider); val != "" {
		cfg.Provider = val
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
