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

	"github.com/partyzanex/a2text/internal/adapters/settings"
	"github.com/partyzanex/a2text/internal/adapters/tray"
	"github.com/partyzanex/a2text/internal/domain"
	"github.com/partyzanex/a2text/internal/i18n"
	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/infra/factory"
	"github.com/partyzanex/a2text/internal/infra/sysd"
	"github.com/partyzanex/a2text/internal/usecases/transcribe"
	"github.com/partyzanex/a2text/internal/usecases/voice"
	"github.com/partyzanex/a2text/pkg/stt"
)

// ErrDepcheckFailed is returned by runDaemon when depcheck reports at least
// one missing required dependency.
var ErrDepcheckFailed = errors.New("voice: depcheck found missing required dependencies")

// RunBootstrap is the no-mode-flag entry point. With the IPC/DE-shortcut
// layers removed, the only thing `a2text` does is start the daemon. The
// PID lock guarantees single-instance: if another daemon is already up,
// the second invocation exits cleanly without disturbing it.
//
// stdout is the destination for one-line operator-visible output. It is
// currently unused but kept in the signature so smoke tests can swap in
// io.Discard.
func RunBootstrap(ctx context.Context, cfg *config.VoiceConfig, log *slog.Logger, stdout io.Writer) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	_ = stdout // reserved for future operator-visible output

	// Initialise i18n before any UI surface (tray, settings) reads strings.
	// A bad language code is non-fatal: i18n falls back to the default locale.
	if err := i18n.Init(cfg.UILanguage); err != nil {
		log.Warn("bootstrap: i18n init failed; using fallbacks", slog.Any("err", err))
	}

	if err := sysd.EnsureRuntimeDir(); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	lock, err := sysd.AcquireDaemonLock(sysd.DefaultLockPath())
	if err != nil {
		if errors.Is(err, sysd.ErrDaemonAlreadyRunning) {
			// Surface the conflict as a non-zero exit + a stderr line so
			// the user notices it without grepping the JSON log. Two
			// concurrent daemons would fight over the microphone, evdev
			// fds and the autopaste uinput device — refuse loudly.
			fmt.Fprintf(os.Stderr,
				"a2text: another daemon is already running (lock %s held).\n"+
					"  Stop it first: `pkill a2text` or `systemctl --user stop a2text-voice`.\n",
				sysd.DefaultLockPath(),
			)
			log.Warn("voice: another daemon already holds the lock — refusing to start")

			return fmt.Errorf("bootstrap: %w", err)
		}

		return fmt.Errorf("bootstrap: %w", err)
	}

	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			log.Warn("voice: release daemon lock failed", slog.Any("err", releaseErr))
		}
	}()

	return runDaemon(ctx, cfg, log)
}

// RunDaemonOnly is the explicit daemon entry point used by the systemd
// unit. Returns an error if another daemon already holds the lock so
// systemd marks the unit failed instead of exiting cleanly (which would
// look like "service started successfully" while doing nothing).
func RunDaemonOnly(ctx context.Context, cfg *config.VoiceConfig, log *slog.Logger, stdout io.Writer) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	_ = stdout // reserved for future operator-visible output

	if err := sysd.EnsureRuntimeDir(); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	lock, err := sysd.AcquireDaemonLock(sysd.DefaultLockPath())
	if err != nil {
		return fmt.Errorf("daemon-only mode requires an exclusive lock: %w", err)
	}

	defer func() {
		if releaseErr := lock.Release(); releaseErr != nil {
			log.Warn("voice: release daemon lock failed", slog.Any("err", releaseErr))
		}
	}()

	return runDaemon(ctx, cfg, log)
}

// StdoutWriter returns os.Stdout. Wrapped as a function so command.go can
// substitute io.Discard in tests without depending on os.Stdout behaviour
// during the test run.
func StdoutWriter() *os.File { return os.Stdout }

// openAuditOrNoop opens the JSON-lines audit log on disk. Returns the
// logger plus a close-helper that runDaemon defers. On open error the
// returned logger is a no-op and the close-helper is a no-op; the daemon
// must keep running even when audit cannot be persisted.
func openAuditOrNoop(log *slog.Logger) (auditLogger stt.AuditLogger, closer func()) {
	file, err := sysd.OpenAuditLog()
	if err != nil {
		log.Warn("voice: open audit log failed; cloud STT will not be audited",
			slog.Any("err", err),
		)

		return stt.NoopAuditLogger{}, func() {}
	}

	path, pathErr := sysd.AuditLogPath()
	if pathErr != nil {
		path = "<unknown>"
	}

	log.Info("voice: audit log open", slog.String("path", path))

	closer = func() {
		if closeErr := file.Close(); closeErr != nil {
			log.Warn("voice: close audit log failed", slog.Any("err", closeErr))
		}
	}

	return stt.NewJSONLAuditLogger(file), closer
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
func runDaemon(ctx context.Context, cfg *config.VoiceConfig, log *slog.Logger) error {
	if cfg == nil {
		return errors.New("daemon: nil config")
	}

	checkDaemonDeps(ctx, cfg, log)

	CleanOrphanDirs(cfg.TempDir, log)

	// First-launch ergonomic: on a fresh install (provider=whisper-cpp,
	// no model file picked yet) drop ggml-small.bin into the XDG models
	// dir before the transcriber factory probes for it. Idempotent —
	// stat-only on every subsequent start.
	EnsureWhisperCppModel(ctx, cfg, log)

	auditLogger, closeAudit := openAuditOrNoop(log)
	defer closeAudit()

	transcriber := buildTranscriberOrLazyStub(ctx, cfg, auditLogger, log)

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
	daemon.AttachAudit(auditLogger)

	hotkeyListener, hkErr := factory.BuildHotkey(cfg, log, daemon.HotkeyHandler())
	if hkErr != nil {
		return fmt.Errorf("daemon: build hotkey: %w", hkErr)
	}

	if hotkeyListener != nil {
		daemon.AttachHotkey(hotkeyListener)
	}

	signalCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	attachTray(signalCtx, cfg, log, daemon, stop)

	return daemon.Serve(signalCtx)
}

// buildTranscriberOrLazyStub builds the STT transcriber and falls back
// to a lazy-error stub when construction fails. Keeping the fallback
// out of runDaemon keeps that function under the funlen budget; the
// fail-soft policy itself is documented inline below.
func buildTranscriberOrLazyStub(
	ctx context.Context,
	cfg *config.VoiceConfig,
	audit stt.AuditLogger,
	log *slog.Logger,
) transcribe.Transcriber {
	transcriber, err := factory.BuildTranscriberWithAudit(ctx, cfg, audit, log)
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
	showSettings := func() { settingsWin.Show() } //nolint:contextcheck // settingsWin already holds ctx via SetRootContext

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

// Ensure domain stays in the import surface even when bootstrap doesn't
// directly reference it (avoids churn if a future tweak adds a domain
// type reference back here).
var _ = domain.EventToggle
