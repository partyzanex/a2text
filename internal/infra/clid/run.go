package clid

// runDaemon (defined below) is the composition root for the new
// split-user architecture: it constructs every concrete impl
// declared by the internal/{usecases,adapters,infra} packages, wires
// them into the gRPC server, and serves until signalCtx is
// cancelled. Replaces the legacy daemon.RunDaemonOnly path once
// audio capture moves to the UI side.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/partyzanex/shutdown"
	"github.com/urfave/cli/v3"

	grpcserver "github.com/partyzanex/a2text/internal/adapters/grpc/server"
	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/infra/cycletoken"
	infragrpc "github.com/partyzanex/a2text/internal/infra/grpc"
	"github.com/partyzanex/a2text/internal/infra/hotkeyreader"
	"github.com/partyzanex/a2text/internal/infra/input"
	"github.com/partyzanex/a2text/internal/infra/secret"
	"github.com/partyzanex/a2text/internal/usecases/hotkey"
	"github.com/partyzanex/a2text/internal/usecases/inject"
	a2textv1 "github.com/partyzanex/a2text/pkg/proto/a2text/v1"
)

const (
	// injectTokenTTL bounds how long an inject_token issued by
	// StartCycle (or the hotkey reader) stays valid before the
	// cycletoken.Store marks it expired. 30 s comfortably covers
	// the longest plausible dictation + STT round-trip while still
	// invalidating stale tokens from a crashed UI.
	injectTokenTTL = 30 * time.Second

	// shutdownTimeout caps how long the LIFO close chain may run
	// before the daemon force-exits. Keeps a stuck closer from
	// blocking systemd's stop sequence.
	shutdownTimeout = 10 * time.Second

	// secretsDirMode is the permission for the secrets directory.
	// 0700 prevents other users on a multi-user system from listing
	// the daemon's credential file even though the file itself is
	// already 0600.
	secretsDirMode os.FileMode = 0o700
)

// runDaemon is the new composition root. It blocks until signalCtx
// is cancelled (SIGINT/SIGTERM) and then runs the LIFO shutdown
// chain with a bounded timeout.
func runDaemon(
	signalCtx context.Context,
	cmd *cli.Command,
	cfg *config.VoiceConfig,
	logger *slog.Logger,
) error {
	hotkeyMode := mapHotkeyMode(cfg.Hotkey.Mode)
	injectMode := mapInjectMode(cfg.Output.Mode)

	logger.Info("bootstrap: configuration resolved",
		slog.String("hotkey_mode", hotkeyMode.String()),
		slog.String("inject_mode", injectMode.String()),
		slog.String("hotkey_key", cfg.Hotkey.Key),
		slog.Any("hotkey_modifiers", cfg.Hotkey.Modifiers),
	)

	driver, err := input.NewUinputDriver(logger)
	if err != nil {
		return fmt.Errorf("bootstrap: open input driver: %w", err)
	}

	secretsPath, err := resolveSecretsPath(cmd.String(FlagSecretsPath))
	if err != nil {
		return fmt.Errorf("bootstrap: resolve secrets path: %w", err)
	}

	secStore, err := secret.New(secretsPath, time.Now)
	if err != nil {
		return fmt.Errorf("bootstrap: open secret store: %w", err)
	}

	tokens := cycletoken.New(injectTokenTTL, time.Now)
	hub := hotkey.New(logger, hotkeyMode)
	injectSvc := inject.New(logger, injectMode, driver)

	kbSvc := grpcserver.NewKeyboardService(logger, tokens, hub, injectSvc, hub)
	secSvc := grpcserver.NewSecretService(logger, secStore)

	grpcSrv, err := buildGRPCServer(signalCtx, cmd, logger, kbSvc, secSvc)
	if err != nil {
		return err
	}

	if pprofErr := startPprof(signalCtx, cmd.String(FlagPprof), logger); pprofErr != nil {
		return fmt.Errorf("bootstrap: start pprof: %w", pprofErr)
	}

	reader, err := buildHotkeyReader(logger, hotkeyMode, tokens, hub, cfg)
	if err != nil {
		return err
	}

	mgr := buildShutdownChain(driver, grpcSrv, reader)

	return serveUntilDone(signalCtx, logger, grpcSrv, reader, mgr)
}

// buildGRPCServer loads optional mTLS material, constructs the
// transport server, and binds the loopback listener. Extracted from
// runDaemon to keep that function under the funlen threshold.
func buildGRPCServer(
	signalCtx context.Context,
	cmd *cli.Command,
	logger *slog.Logger,
	kbSvc a2textv1.KeyboardServiceServer,
	secSvc a2textv1.SecretServiceServer,
) (*infragrpc.Server, error) {
	tlsConfig, err := loadServerTLS(
		cmd.String(FlagCertFile),
		cmd.String(FlagKeyFile),
		cmd.String(FlagClientCAFile),
	)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: load mTLS: %w", err)
	}

	grpcSrv := infragrpc.NewServer(logger, kbSvc, secSvc, tlsConfig)

	if _, lErr := grpcSrv.Listen(signalCtx, cmd.String(FlagListenAddr)); lErr != nil {
		return nil, fmt.Errorf("bootstrap: listen: %w", lErr)
	}

	return grpcSrv, nil
}

// buildHotkeyReader wraps the evdev reader constructor so runDaemon
// stays under the funlen threshold without obscuring the wiring.
func buildHotkeyReader(
	logger *slog.Logger,
	hotkeyMode a2textv1.HotkeyMode,
	tokens *cycletoken.Store,
	hub *hotkey.Hub,
	cfg *config.VoiceConfig,
) (*hotkeyreader.Reader, error) {
	reader, err := hotkeyreader.New(
		logger,
		hotkeyMode,
		tokens,
		hub,
		cfg.Hotkey.Key,
		cfg.Hotkey.Modifiers,
	)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: hotkey reader: %w", err)
	}

	return reader, nil
}

// buildShutdownChain wires the LIFO closer order. LIFO closes in
// reverse-append order, so reader stops first (no more hub.Start
// calls), then the gRPC server stops accepting RPCs, then the
// driver releases /dev/uinput.
func buildShutdownChain(
	driver *input.UinputDriver,
	grpcSrv *infragrpc.Server,
	reader *hotkeyreader.Reader,
) *shutdown.Lifo {
	mgr := shutdown.NewLIFO()
	mgr.Append(driver)
	mgr.Append(grpcSrv)
	mgr.Append(reader)

	return mgr
}

// serveUntilDone spawns the gRPC serve loop and the evdev listen
// loop, waits for signalCtx, then runs the shutdown chain with a
// bounded timeout. Errors from the two long-running goroutines are
// surfaced even if signalCtx fires concurrently.
func serveUntilDone(
	signalCtx context.Context,
	logger *slog.Logger,
	grpcSrv *infragrpc.Server,
	reader *hotkeyreader.Reader,
	mgr *shutdown.Lifo,
) error {
	serveErr := make(chan error, 1)
	hotkeyErr := make(chan error, 1)

	go func() { serveErr <- grpcSrv.Serve(signalCtx) }()
	go func() { hotkeyErr <- reader.Listen(signalCtx) }()

	var runErr error

	select {
	case <-signalCtx.Done():
		logger.Info("bootstrap: shutdown signal received")
	case err := <-serveErr:
		runErr = fmt.Errorf("bootstrap: grpc serve exited: %w", err)
	case err := <-hotkeyErr:
		runErr = fmt.Errorf("bootstrap: hotkey reader exited: %w", err)
	}

	// signalCtx is cancelled by the time we get here on the normal
	// path; derive a fresh deadline from it via WithoutCancel so the
	// shutdown chain still gets a bounded budget.
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(signalCtx), shutdownTimeout)
	defer cancel()

	if err := mgr.CloseContext(closeCtx); err != nil {
		closeErr := fmt.Errorf("bootstrap: shutdown chain: %w", err)
		if runErr == nil {
			return closeErr
		}

		logger.Error("bootstrap: shutdown chain failed",
			slog.Any("error", closeErr),
		)
	}

	return runErr
}

// mapHotkeyMode converts the config-level string enum to the proto
// enum the Hub and the Reader speak. An unknown value defaults to
// TOGGLE — the safer default because HOLD can leave a cycle stuck
// "on" if the daemon misses a RELEASE edge.
func mapHotkeyMode(mode config.VoiceHotkeyMode) a2textv1.HotkeyMode {
	if mode == config.VoiceHotkeyModeHold {
		return a2textv1.HotkeyMode_HOTKEY_MODE_HOLD
	}

	return a2textv1.HotkeyMode_HOTKEY_MODE_TOGGLE
}

// mapInjectMode converts the config-level output mode to the proto
// inject-mode enum. "stdout" maps to CLIPBOARD (no real injection;
// daemon just records the cycle in audit logs) because the new
// architecture has no stdout sink on the daemon side. "clipboard"
// also maps to CLIPBOARD; "clipboard_autopaste" → PASTE.
func mapInjectMode(mode string) a2textv1.InjectMode {
	switch mode {
	case config.VoiceOutputModeClipboardAutopaste:
		return a2textv1.InjectMode_INJECT_MODE_PASTE
	default:
		return a2textv1.InjectMode_INJECT_MODE_CLIPBOARD
	}
}

// resolveSecretsPath chooses the file the SecretService will read
// from and write to. CLI flag wins; otherwise falls back to
// $XDG_DATA_HOME/a2text/secrets.json (or the documented default
// ~/.local/share/a2text/secrets.json when XDG_DATA_HOME is empty).
// The parent directory is created mode 0700 if missing.
func resolveSecretsPath(flagValue string) (string, error) {
	if flagValue != "" {
		return flagValue, ensureParentDir(flagValue, secretsDirMode)
	}

	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("user home dir: %w", err)
		}

		base = filepath.Join(home, ".local", "share")
	}

	path := filepath.Join(base, "a2text", "secrets.json")

	return path, ensureParentDir(path, secretsDirMode)
}

// ensureParentDir creates the directory holding path (recursively)
// with the supplied mode if it does not exist. An existing directory
// is left alone — its permissions are honoured even if they are more
// permissive than mode (the operator may have a reason).
func ensureParentDir(path string, mode os.FileMode) error {
	dir := filepath.Dir(path)

	// dir comes from operator-supplied config flags / XDG env, not untrusted user input.
	info, err := os.Stat(dir) //nolint:gosec // see comment above.
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("parent %q exists and is not a directory", dir)
		}

		return nil
	}

	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat parent %q: %w", dir, err)
	}

	// Same source-of-truth as the Stat call above (operator config / XDG env).
	if err := os.MkdirAll(dir, mode); err != nil { //nolint:gosec // see comment above.
		return fmt.Errorf("mkdir %q: %w", dir, err)
	}

	return nil
}
