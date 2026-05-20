// Package clid hosts the CLI wiring for cmd/a2textd. cmd/a2textd/main.go
// is a thin shell that calls NewCommand().Run(...).
//
// a2textd is the system-side daemon for the split-user architecture: it
// reads evdev hotkeys, captures audio, runs STT, drives uinput autopaste,
// owns provider credentials, and exposes a gRPC + mTLS control plane on
// the loopback interface for the user-side `a2text` UI binary. It does
// not draw UI and does not require a graphical session.
//
// Wire choices fixed by ADR-0001 (split-user) and ADR-0002 (loopback
// gRPC control plane): TCP 127.0.0.1, kernel-assigned port published via
// a discovery file, mTLS with pinned self-signed certificates. Browsers
// cannot speak gRPC, so JS-driven DNS-rebinding attacks against the
// listener are architecturally impossible. Plain HTTP and Unix-domain
// sockets are explicitly out of scope.
//
// Until the gRPC server and per-user audio bridge land, this binary
// delegates to the existing single-process daemon entry point
// (daemon.RunDaemonOnly) so the scaffold is runnable end to end on a
// developer machine.
package clid

import (
	"context"
	"fmt"
	"log/slog"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v3"

	cli2text "github.com/partyzanex/a2text/internal/infra/cli"
	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/infra/daemon"
)

const (
	// appName is the binary name reported by `a2textd --help` and used
	// in the systemd unit and package metadata.
	appName = "a2textd"

	// configErrorExitCode mirrors the a2text CLI: distinct exit code so
	// service managers can differentiate config errors from runtime
	// failures.
	configErrorExitCode = 2

	// runtimeErrorExitCode is returned when the daemon itself fails
	// after a successful config load.
	runtimeErrorExitCode = 1

	// defaultListenAddr binds the gRPC server to the IPv4 loopback with
	// a kernel-assigned port. The actual port is written to the
	// discovery file so the UI can find it without hard-coding.
	defaultListenAddr = "127.0.0.1:0"
)

// NewCommand returns the root cli.Command for a2textd. Constructors
// return concrete types (per architecture rules).
func NewCommand() *cli.Command {
	return &cli.Command{
		Name:    appName,
		Usage:   "a2text system daemon (hotkey + audio + STT + autopaste, headless, gRPC/mTLS control plane)",
		Version: buildVersion(),
		Flags:   rootFlags(),
		Action:  action,
	}
}

// buildVersion reuses the ldflags-set globals from internal/infra/cli so
// both binaries share the same version stamp without duplicating the
// build-info plumbing.
func buildVersion() string {
	return fmt.Sprintf("%s (%s)", cli2text.Version, cli2text.Commit)
}

func rootFlags() []cli.Flag {
	return append(controlPlaneFlags(), operationalFlags()...)
}

// controlPlaneFlags groups the gRPC listener + TLS material flags.
// Extracted so rootFlags stays under the funlen threshold.
func controlPlaneFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    FlagListenAddr,
			Usage:   "loopback bind address for the gRPC control plane (default 127.0.0.1:0 — kernel-assigned port)",
			Value:   defaultListenAddr,
			Sources: cli.EnvVars("A2TEXTD_LISTEN"),
		},
		&cli.StringFlag{
			Name:    FlagPortFile,
			Usage:   "path to write the kernel-assigned port for UI discovery",
			Sources: cli.EnvVars("A2TEXTD_PORT_FILE"),
		},
		&cli.StringFlag{
			Name:    FlagCertFile,
			Usage:   "PEM file with the daemon's TLS server certificate",
			Sources: cli.EnvVars("A2TEXTD_CERT"),
		},
		&cli.StringFlag{
			Name:    FlagKeyFile,
			Usage:   "PEM file with the daemon's TLS private key (mode 0600)",
			Sources: cli.EnvVars("A2TEXTD_KEY"),
		},
		&cli.StringFlag{
			Name:    FlagClientCAFile,
			Usage:   "PEM bundle of client certificates accepted via mTLS",
			Sources: cli.EnvVars("A2TEXTD_CLIENT_CA"),
		},
	}
}

// operationalFlags groups the non-control-plane flags (config, logging,
// pprof). Extracted for the same reason as controlPlaneFlags.
func operationalFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    FlagConfig,
			Aliases: []string{"c"},
			Usage: "path to config file (default: /etc/a2text/system.yaml for system " +
				"installs, per-user XDG config for user installs)",
			Sources: cli.EnvVars("A2TEXTD_CONFIG"),
		},
		&cli.StringFlag{
			Name:    FlagLogLevel,
			Usage:   "log level: debug | info | warn | error",
			Sources: cli.EnvVars("A2TEXTD_LOG_LEVEL"),
		},
		&cli.StringFlag{
			Name: FlagPprof,
			Usage: "enable pprof endpoint on host:port (e.g. 127.0.0.1:6060); " +
				"empty = disabled. Loopback only.",
			Sources: cli.EnvVars("A2TEXTD_PPROF"),
		},
	}
}

func action(ctx context.Context, cmd *cli.Command) error {
	cfg, err := config.LoadVoice(cmd.String(FlagConfig))
	if err != nil {
		return cli.Exit(fmt.Errorf("load config: %w", err), configErrorExitCode)
	}

	applyFlagOverrides(cmd, cfg)

	if err := config.ValidateVoice(cfg); err != nil {
		return cli.Exit(fmt.Errorf("invalid config after CLI overrides: %w", err), configErrorExitCode)
	}

	signalCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := cli2text.CreateLogger(cfg.LogLevel)
	logger.Info("a2textd starting",
		slog.String("provider", cfg.Provider),
		slog.String("language", cfg.Language),
		slog.String("log_level", cfg.LogLevel),
	)

	warnUnimplementedFlags(cmd, logger)

	// Scaffold delegates to the existing single-process daemon entry
	// point. The gRPC control plane, mTLS handshake, secrets store, and
	// privilege drop will replace this body when ADR-0001 / ADR-0002
	// land.
	if err := daemon.RunDaemonOnly(signalCtx, cfg, logger, daemon.StdoutWriter()); err != nil {
		return cli.Exit(fmt.Errorf("daemon: %w", err), runtimeErrorExitCode)
	}

	return nil
}

// warnUnimplementedFlags emits a single warn-level record per flag that
// the scaffold accepts but does not yet honour. Surfacing this in the
// log keeps the contract honest: callers see at startup that --listen
// or --cert is being ignored, instead of silently inheriting whatever
// the embedded RunDaemonOnly path does.
func warnUnimplementedFlags(cmd *cli.Command, logger *slog.Logger) {
	const reasonGRPCNotWired = "gRPC control plane not wired"

	unimplemented := map[string]string{
		FlagListenAddr:   reasonGRPCNotWired,
		FlagPortFile:     reasonGRPCNotWired,
		FlagCertFile:     reasonGRPCNotWired,
		FlagKeyFile:      reasonGRPCNotWired,
		FlagClientCAFile: reasonGRPCNotWired,
		FlagPprof:        "pprof endpoint not wired",
	}

	for name, reason := range unimplemented {
		val := cmd.String(name)
		if val == "" || val == defaultListenAddr {
			continue
		}

		logger.Warn("flag accepted but not yet honoured",
			slog.String("flag", name),
			slog.String("value", val),
			slog.String("reason", reason),
		)
	}
}

// applyFlagOverrides mutates cfg in-place with CLI flag values. Caller
// must re-validate afterwards: a flag combination can move the config
// into an invalid state (e.g. setting log-level to an unknown token).
func applyFlagOverrides(cmd *cli.Command, cfg *config.VoiceConfig) {
	if val := cmd.String(FlagLogLevel); val != "" {
		cfg.LogLevel = val
	}
}
