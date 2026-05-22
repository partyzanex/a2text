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
	// 120s covers a long dictation + slow STT round-trip while still
	// invalidating tokens from a crashed UI.
	injectTokenTTL = 120 * time.Second

	// 10s leaves room for the closer chain without blocking systemd's
	// stop sequence (default TimeoutStopSec=90s).
	shutdownTimeout = 10 * time.Second

	// 0700 keeps the secrets dir invisible to other local users; the
	// file inside is already 0600.
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
	injectMode := mapInjectMode(cfg.Output.Mode, logger)

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

	if pprofErr := startValidatedPprof(signalCtx, cmd.String(FlagPprof), logger); pprofErr != nil {
		return pprofErr
	}

	reader, err := buildHotkeyReader(logger, hotkeyMode, tokens, hub, cfg)
	if err != nil {
		return err
	}

	mgr := buildShutdownChain(driver, grpcSrv, reader)

	return serveUntilDone(signalCtx, logger, grpcSrv, reader, mgr)
}

// startValidatedPprof validates that the pprof bind address is on
// the loopback interface (defense in depth — pprof exposes heap and
// goroutine state) and starts the endpoint. Empty addr is a no-op.
func startValidatedPprof(ctx context.Context, addr string, log *slog.Logger) error {
	if addr == "" {
		return nil
	}

	if err := requireLoopbackAddr(addr); err != nil {
		return fmt.Errorf("bootstrap: validate --pprof: %w", err)
	}

	if err := startPprof(ctx, addr, log); err != nil {
		return fmt.Errorf("bootstrap: start pprof: %w", err)
	}

	return nil
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
	if err != nil && !errors.Is(err, errTLSDisabled) {
		return nil, fmt.Errorf("bootstrap: load mTLS: %w", err)
	}

	listenAddr := cmd.String(FlagListenAddr)
	if err := requireLoopbackAddr(listenAddr); err != nil {
		return nil, fmt.Errorf("bootstrap: validate --listen: %w", err)
	}

	grpcSrv := infragrpc.NewServer(logger, kbSvc, secSvc, tlsConfig)

	if _, lErr := grpcSrv.Listen(signalCtx, listenAddr); lErr != nil {
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
// inject-mode enum.
//
//   - "clipboard_autopaste" → PASTE
//   - "clipboard" / "stdout" → CLIPBOARD (no real injection; the
//     daemon side has no stdout sink)
//   - anything else → CLIPBOARD + warn-log so the operator sees the
//     mismatch instead of silently falling through.
func mapInjectMode(mode string, log *slog.Logger) a2textv1.InjectMode {
	switch mode {
	case config.VoiceOutputModeClipboardAutopaste:
		return a2textv1.InjectMode_INJECT_MODE_PASTE
	case config.VoiceOutputModeClipboard, config.VoiceOutputModeStdout:
		return a2textv1.InjectMode_INJECT_MODE_CLIPBOARD
	default:
		log.Warn("bootstrap: unknown output mode — falling back to CLIPBOARD",
			slog.String("configured_mode", mode),
		)

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

		return tightenDirMode(dir, info.Mode().Perm(), mode)
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

// tightenDirMode chmod's the directory down to want when the
// observed permission set is broader. We never widen permissions —
// if the operator chose 0700 we keep 0700 even when the caller
// asked for 0755. The asymmetry is deliberate: the secrets
// directory is the only caller today and "no looser than caller
// asked" is exactly the desired safety property.
//
// "Broader" is detected via `current &^ want != 0`: any bit set in
// current that is not set in want.
func tightenDirMode(dir string, current, want os.FileMode) error {
	if current&^want == 0 {
		return nil
	}

	// dir is the same operator-supplied / XDG-derived path tightenDirMode
	// got from ensureParentDir. No untrusted user input.
	if err := os.Chmod(dir, want); err != nil { //nolint:gosec // see comment.
		return fmt.Errorf("chmod %q to %#o: %w", dir, want, err)
	}

	return nil
}
