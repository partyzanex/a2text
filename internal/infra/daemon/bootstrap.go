package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/partyzanex/a2text/internal/adapters/ipc"
	"github.com/partyzanex/a2text/internal/adapters/settings"
	"github.com/partyzanex/a2text/internal/adapters/tray"
	"github.com/partyzanex/a2text/internal/domain"
	"github.com/partyzanex/a2text/internal/i18n"
	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/infra/factory"
	"github.com/partyzanex/a2text/internal/infra/setup"
	"github.com/partyzanex/a2text/internal/infra/sysd"
	"github.com/partyzanex/a2text/internal/usecases/transcribe"
	"github.com/partyzanex/a2text/internal/usecases/voice"
)

// ErrDepcheckFailed is returned by runDaemon when depcheck reports at least
// one missing required dependency.
var ErrDepcheckFailed = errors.New("voice: depcheck found missing required dependencies")

// pingTimeout caps the bootstrap-time ping RTT. If a daemon is up it
// answers in microseconds; 3s is well over any reasonable real-world
// figure and short enough that users do not feel a hang on cold start.
const pingTimeout = 3 * time.Second

// raceRetryInterval is how often we re-ping the daemon when we lose the
// lock race (another process won AcquireDaemonLock and is in the middle
// of binding the socket). 50ms keeps the dialog feeling instant for
// the vast majority of races.
const raceRetryInterval = 50 * time.Millisecond

// raceRetryDeadline caps the total wait for the winner's daemon to come
// online. Equal to pingTimeout — if we still cannot reach the socket after
// that, something is genuinely wrong, surface it.
const raceRetryDeadline = pingTimeout

// RunBootstrap is the no-mode-flag entry point. It implements the
// self-bootstrap pattern:
//
//  1. Send Toggle to the daemon socket. If it answers → done, exit.
//  2. If Toggle returned domain.ErrDaemonNotRunning → try to acquire the daemon
//     lock and become the daemon ourselves.
//  3. If another process won the lock race → re-toggle (with a brief retry
//     loop) and forward the result instead of giving up.
//  4. Any other error from the daemon (version mismatch, malformed) →
//     surface to the user without becoming a daemon.
//
// stdout is the destination for one-line operator-visible output (e.g. the
// toggle result). Tests inject io.Discard.
//
// Socket path: stage I.2 always uses DefaultSocketPath. VoiceConfig does
// NOT yet carry a daemon.socket_path field; when it lands (see TODO I.7
// nested config sections) plumb cfg through here. Until then both the
// caller and the daemon agree on the same default, so the absence of a
// config knob is invisible to users.
func RunBootstrap(ctx context.Context, cfg *config.VoiceConfig, log *slog.Logger, stdout io.Writer) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	_ = stdout // reserved for future operator-visible output; currently unused in daemon-only mode

	// Initialise i18n before any UI surface (tray, settings) reads strings.
	// A bad language code is non-fatal: i18n falls back to the default locale.
	if err := i18n.Init(cfg.UILanguage); err != nil {
		log.Warn("bootstrap: i18n init failed; using fallbacks", slog.Any("err", err))
	}

	if err := sysd.EnsureRuntimeDir(); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	socketPath := sysd.DefaultSocketPath()
	if cfg.Daemon.SocketPath != "" {
		socketPath = cfg.Daemon.SocketPath
	}

	client := ipc.NewClient(socketPath, pingTimeout)

	if resp, err := tryToggle(ctx, client); err == nil {
		logToggleResult(ctx, log, &resp)

		return nil
	} else if !errors.Is(err, domain.ErrDaemonNotRunning) {
		return fmt.Errorf("daemon ipc failed: %w", err)
	}

	// No daemon at the socket. Try to take the lock and become one.
	return acquireLockAndServe(ctx, cfg, log, socketPath, client)
}

// acquireLockAndServe attempts to acquire the daemon lock. If another
// process holds it, waits briefly and toggles the winner. On success,
// starts the daemon and sends the initial Toggle.
func acquireLockAndServe(
	ctx context.Context,
	cfg *config.VoiceConfig,
	log *slog.Logger,
	socketPath string,
	client *ipc.Client,
) error {
	lock, err := sysd.AcquireDaemonLock(sysd.DefaultLockPath())
	if err != nil {
		if !errors.Is(err, sysd.ErrDaemonAlreadyRunning) {
			return fmt.Errorf("bootstrap: %w", err)
		}

		resp, retryErr := waitAndToggle(ctx, client)
		if retryErr != nil {
			return fmt.Errorf("lock taken but toggle still failed: %w", retryErr)
		}

		logToggleResult(ctx, log, &resp)

		return nil
	}

	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			log.Warn("voice: release daemon lock failed", slog.Any("err", releaseErr))
		}
	}()

	return runDaemon(ctx, cfg, log, socketPath)
}

// tryToggle issues one Toggle. The single round-trip serves both as
// liveness probe AND as the action the user wanted: a Toggle to a daemon
// that does not exist returns domain.ErrDaemonNotRunning, which the caller
// converts into "become the daemon". A Toggle to an alive daemon advances
// its state machine — exactly what the user invoked us for.
//
// Calling Ping() and then Toggle() would double the round-trips for the
// common case (daemon is up) without buying anything: Toggle already
// reports the post-toggle state, and ping's only edge case (version
// mismatch) is also covered by Toggle's response.
//
// Non-nil errors are already typed by ipc.Client (domain.ErrBusy,
// domain.ErrDaemonNotRunning, ErrIPCVersionMismatch, …) — caller can errors.Is
// to distinguish them.
func tryToggle(ctx context.Context, client *ipc.Client) (ipc.Response, error) {
	resp, err := client.Toggle(ctx)
	if err != nil {
		return resp, fmt.Errorf("bootstrap: %w", err)
	}

	return resp, nil
}

// waitAndToggle retries tryToggle every raceRetryInterval until either
// success, a non-domain.ErrDaemonNotRunning error, or raceRetryDeadline elapses.
// Uses context.WithTimeout instead of a manual deadline so the loop
// correctly handles ctx cancellation and timer cleanup.
func waitAndToggle(ctx context.Context, client *ipc.Client) (ipc.Response, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, raceRetryDeadline)
	defer cancel()

	// Fire immediately on the first iteration, then back off.
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-timeoutCtx.Done():
			return ipc.Response{}, fmt.Errorf("bootstrap: %w", timeoutCtx.Err())
		case <-timer.C:
		}

		resp, err := tryToggle(timeoutCtx, client)
		if err == nil {
			return resp, nil
		}

		if !errors.Is(err, domain.ErrDaemonNotRunning) {
			return resp, err
		}

		timer.Reset(raceRetryInterval)
	}
}

// logToggleResult emits the daemon's reply as a single structured log line.
// In production every voice command must be greppable from the journal —
// printing to stdout in plain text breaks log shippers and forces operators
// to parse two formats. Keep the message stable ("voice: toggle result") so
// callers can `journalctl ... -g 'voice: toggle result'`.
func logToggleResult(ctx context.Context, log *slog.Logger, resp *ipc.Response) {
	attrs := []slog.Attr{slog.String("state", resp.State)}

	if resp.Message != "" {
		attrs = append(attrs, slog.String("daemon_message", resp.Message))
	}

	log.LogAttrs(ctx, slog.LevelInfo, "voice: toggle result", attrs...)
}

// checkDaemonDeps runs depcheck and logs any missing dependencies.
// Missing required deps are logged but do NOT abort startup: the daemon
// brings up the tray + settings UI in a degraded state so the user can
// configure a working provider. The transcriber path returns a lazy error
// until deps are satisfied and the daemon is restarted.
func checkDaemonDeps(ctx context.Context, cfg *config.VoiceConfig, log *slog.Logger) {
	results, fatal := RunDepCheck(ctx, cfg, io.Discard, log)
	if !fatal {
		return
	}

	for i := range results {
		res := &results[i]
		if !res.Found && !res.Optional {
			log.Error("voice: missing required dependency",
				slog.String("group", res.Group),
				slog.String("name", res.Name),
				slog.String("install_tip", res.InstallTip),
			)
		}
	}

	log.Warn("voice: starting in degraded mode — fix deps via settings UI and restart")
}

// runDaemon performs depcheck, builds the daemon, and serves until SIGTERM.
//
// Ownership of the transcriber is transferred to the Daemon on success:
// the daemon's Shutdown calls Close. If anything before NewDaemon fails,
// runDaemon closes the transcriber itself — see the explicit defer + flag
// pattern below.
func runDaemon(ctx context.Context, cfg *config.VoiceConfig, log *slog.Logger, socketPath string) error {
	if cfg == nil {
		return errors.New("daemon: nil config")
	}

	checkDaemonDeps(ctx, cfg, log)

	CleanOrphanDirs(cfg.TempDir, log)

	transcriber := buildTranscriberOrLazyStub(ctx, cfg, log)

	var ownedByDaemon bool

	defer func() {
		if ownedByDaemon {
			return
		}

		if closeErr := transcriber.Close(); closeErr != nil {
			log.Warn("voice: transcriber close (early-fail path) failed",
				slog.Any("err", closeErr),
			)
		}
	}()

	recorder, err := factory.BuildRecorder(log)
	if err != nil {
		return fmt.Errorf("daemon: build recorder: %w", err)
	}

	voiceOutput, err := factory.BuildOutput(cfg, log)
	if err != nil {
		return fmt.Errorf("daemon: build output: %w", err)
	}

	daemon, ownedByDaemon := buildDaemon(cfg, log, transcriber, recorder, voiceOutput)

	hotkeyListener, hkErr := factory.BuildHotkey(cfg, log, daemon.HotkeyHandler())
	if hkErr != nil {
		return fmt.Errorf("daemon: build hotkey: %w", hkErr)
	}

	if hotkeyListener != nil {
		daemon.AttachHotkey(hotkeyListener)
	}

	signalCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	registerHotkey(signalCtx, cfg, log)
	attachTray(signalCtx, cfg, log, daemon, stop)

	return daemon.Serve(signalCtx, socketPath)
}

// registerHotkey attempts to register the global keyboard shortcut in the
// current desktop environment using the key/modifiers from cfg. Failures are
// logged at WARN and never propagate — a missing or broken hotkey must not
// prevent the daemon from starting. Non-GNOME sessions and headless
// environments are silently skipped.
func registerHotkey(ctx context.Context, cfg *config.VoiceConfig, log *slog.Logger) {
	if cfg.Hotkey.Key == "" {
		return
	}

	if err := setup.RunSetup(ctx, cfg, log); err != nil {
		if !errors.Is(err, setup.ErrDesktopUnsupported) {
			log.Warn("voice: hotkey auto-register failed",
				slog.String("key", cfg.Hotkey.Key),
				slog.Any("err", err),
			)
		}
	}
}

// buildTranscriberOrLazyStub builds the STT transcriber and falls back
// to a lazy-error stub when construction fails. Keeping the fallback
// out of runDaemon keeps that function under the funlen budget; the
// fail-soft policy itself is documented inline below.
func buildTranscriberOrLazyStub(
	ctx context.Context,
	cfg *config.VoiceConfig,
	log *slog.Logger,
) transcribe.Transcriber {
	transcriber, err := factory.BuildTranscriber(ctx, cfg, log)
	if err == nil {
		return transcriber
	}

	// Fail-soft: a misconfigured STT must not strand the user on the
	// CLI. Log the construction error and substitute a stub that
	// surfaces the same error on every transcribe attempt. The
	// settings window stays reachable so the user can fix model_path
	// / URL / API key and restart the daemon.
	log.Error("voice: STT init failed; running with lazy-error transcriber",
		slog.String("provider", cfg.Provider),
		slog.Any("err", err),
	)

	return factory.NewLazyErrorTranscriber(err)
}

// attachTray wires the settings window and system-tray icon to the daemon.
func attachTray(ctx context.Context, cfg *config.VoiceConfig, log *slog.Logger, daemon *Daemon, stop func()) {
	settingsWin := settings.New(cfg, log)
	settingsWin.SetRootContext(ctx)
	settingsWin.SetOnConfigChanged(func() { daemon.ReloadConfig(ctx) })

	// settingsWin.Show is invoked via a Fyne tray callback (nullary
	// signature) and threads daemon-scoped ctx through SetRootContext
	// rather than as a function parameter — invisible to contextcheck.
	showSettings := func() { settingsWin.Show() } //nolint:contextcheck // see comment above

	trayInst := tray.New(log,
		func() { daemon.Toggle(ctx) },
		showSettings,
		stop,
	)
	daemon.AttachTray(trayInst)
	daemon.AttachSettings(settingsWin)
}

// buildDaemon constructs the Daemon and returns it along with the
// ownership flag for the deferred transcriber close.
func buildDaemon(
	cfg *config.VoiceConfig,
	log *slog.Logger,
	transcriber transcribe.Transcriber,
	recorder voice.Recorder,
	voiceOutput voice.Output,
) (*Daemon, bool) {
	// Apply the silence-gate decorator on top of the STT chain. Keeps the
	// underlying transcriber as Closer (Close() lives on the rich type)
	// while exposing only the slim voice.Transcriber surface to the cycle.
	gatedTranscriber := factory.WrapWithSilenceGate(
		transcriber, cfg.Capture.SilenceThresholdDBFS, log,
	)

	// Kept-audio archiver: reads Privacy flags from cfg at every cycle
	// so toggling "Сохранять аудио" in the settings UI takes effect on
	// the next recording without a daemon restart. Uses the convert
	// timeout as the per-encode cap — the WAV is tiny so even a
	// pessimistic ffmpeg run finishes well inside it.
	keptArchiver := factory.NewKeptAudioArchiver(cfg, cfg.ConvertTimeout, log)

	daemon := NewDaemon(&DaemonDeps{
		Cfg:         cfg,
		Log:         log,
		Recorder:    recorder,
		Transcriber: gatedTranscriber,
		Closer:      transcriber,
		Output:      voiceOutput,
		Archiver:    keptArchiver,
	})

	return daemon, true
}

// StdoutWriter returns os.Stdout. Wrapped as a function so command.go
// can substitute io.Discard in tests without depending on os.Stdout
// behaviour during the test run.
func StdoutWriter() *os.File {
	return os.Stdout
}

// RunDaemonOnly is the explicit daemon entry point used by the systemd
// unit. Unlike RunBootstrap, it never sends a Toggle and never retries:
//
//   - if the lock is free, the process becomes the daemon;
//   - if the lock is taken, the call returns an error so systemd marks the
//     unit failed instead of exiting cleanly (which would look like
//     "service started successfully" while doing nothing).
//
// Auto-toggle is the wrong default for systemd's ExecStart=: a service
// that exits 0 the moment it sees an existing daemon is indistinguishable
// from a service that ran and finished, breaking restart-on-failure logic.
func RunDaemonOnly(ctx context.Context, cfg *config.VoiceConfig, log *slog.Logger, stdout io.Writer) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	_ = stdout // reserved for future operator-visible output; currently unused in daemon-only mode

	if err := sysd.EnsureRuntimeDir(); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	socketPath := sysd.DefaultSocketPath()

	lock, err := sysd.AcquireDaemonLock(sysd.DefaultLockPath())
	if err != nil {
		return fmt.Errorf("daemon-only mode requires an exclusive lock: %w", err)
	}

	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			log.Warn("voice: release daemon lock failed", slog.Any("err", releaseErr))
		}
	}()

	return runDaemon(ctx, cfg, log, socketPath)
}
