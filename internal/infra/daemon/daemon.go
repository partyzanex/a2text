package daemon

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/partyzanex/shutdown"

	"github.com/partyzanex/a2text/internal/adapters/tray"
	"github.com/partyzanex/a2text/internal/domain"
	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/infra/factory"
	"github.com/partyzanex/a2text/internal/infra/sysd"
	"github.com/partyzanex/a2text/internal/usecases/voice"
	"github.com/partyzanex/a2text/pkg/stt"
)

const (
	shutdownTimeout = 30 * time.Second
	// toggleMinInterval is the minimum time between accepted Toggle events.
	// Prevents GNOME key-repeat from spawning multiple rapid-fire `a2text`
	// invocations that alternate start/stop 30 ms apart.
	toggleMinInterval = 500 * time.Millisecond
	// stateChBufSize is the capacity of the daemon's internal state-change
	// notification channel. Non-blocking sends drop messages when the channel
	// is full; a buffer of 16 is enough to absorb a full recording cycle
	// (idle→recording→transcribing→delivering→idle) several times over
	// without stalling the state machine.
	stateChBufSize = 16
)

// Daemon ties together state machine, sd_notify, voice use case, and the
// recording/transcription/output adapters into the long-running dictation
// service.
//
// One daemon per process. Mutual exclusion against parallel daemon starts
// is enforced by the file-lock in sysd.AcquireDaemonLock — Daemon itself
// assumes it has the field clear.
//
// Hotkey is driven entirely by an in-process evdev listener; there is no
// IPC socket and no DE-shortcut path. The tray and settings window invoke
// daemon methods directly.
type Daemon struct {
	cfg         *config.VoiceConfig
	log         *slog.Logger
	machine     *voice.Machine
	notifier    *sysd.SdNotifier
	useCase     *voice.VoiceUseCase
	transcriber transcribeCloser

	// hotkey is an optional global key listener. When non-nil it is started
	// in Serve and stopped during Shutdown. Wired by callers via AttachHotkey
	// after NewDaemon — keeping it out of NewDaemon avoids pulling X11/CGo
	// types into the constructor signature.
	hotkey voice.HotkeyListener

	// tray is an optional system-tray icon. When non-nil it is started in
	// Serve and exits when ctx is cancelled. Wired by callers via AttachTray.
	tray *tray.Tray

	// settingsWin is an optional Fyne-based settings window. When non-nil,
	// Serve calls settingsWin.Run() on the main goroutine (Fyne requires it)
	// and settingsWin.Stop() during shutdown. Wired via AttachSettings.
	settingsWin fyneRunner

	// notifyCh carries state-machine transitions to any interested in-process
	// consumer (currently the tray). Sends are non-blocking — a lagging
	// consumer causes drops, not stalls.
	notifyCh chan domain.State

	// hotkeyMode caches cfg.Hotkey.Mode so HotkeyHandler doesn't read
	// through the config pointer on the hot path. Default "" maps to toggle.
	hotkeyMode config.VoiceHotkeyMode

	// holdGate debounces hold-mode Press/Release pairs that are too short
	// to be real recording attempts (accidental taps, key bounces).
	holdGate holdGate

	// audit receives one event per cloud STT request. Wired by
	// AttachAudit; nil falls back to a no-op so ReloadTranscriber can
	// always pass a non-nil value into the factory.
	audit stt.AuditLogger

	// Cycle cancellation is split: cycleCancel kills the whole pipeline
	// (used for "discard" and shutdown), recordingCancel kills only the
	// recording phase so transcription can still complete (used for
	// the normal "stop recording" toggle).
	cycleMu         sync.Mutex
	cycleCancel     context.CancelFunc
	recordingCancel context.CancelFunc

	maxRecord time.Duration

	// toggleMu guards lastToggleAt for the debounce check.
	toggleMu     sync.Mutex
	lastToggleAt time.Time

	shutdownOnce sync.Once
	shutdownErr  error

	// reloadMu serialises ReloadTranscriber against itself; the actual
	// swap into useCase is atomic from voice.VoiceUseCase's perspective
	// because it happens between cycles (a cycle holds no reference to
	// the transcriber field that survives method return).
	reloadMu sync.Mutex
}

// transcribeCloser is the subset of stt.Transcriber the daemon needs to
// call Close on shutdown. We accept any type with a Close method to avoid
// importing the whole stt package here.
type transcribeCloser interface {
	Close() error
}

// fyneRunner abstracts the Fyne settings window so daemon.go does not import
// the settings package directly. Run() blocks on the Fyne event loop (must be
// called from the main goroutine). Stop() quits the event loop.
type fyneRunner interface {
	Run()
	Stop()
}

// DaemonDeps groups everything NewDaemon needs from the wiring layer.
// Keeps the signature flat instead of a 7-arg constructor.
type DaemonDeps struct {
	Cfg         *config.VoiceConfig
	Log         *slog.Logger
	Recorder    voice.Recorder
	Transcriber voice.Transcriber
	Closer      transcribeCloser // typically same value as Transcriber
	Output      voice.Output

	// Archiver, when non-nil, takes a copy of every successfully
	// transcribed recording into the configured kept-audio
	// directory. nil disables archiving entirely (legacy behaviour).
	Archiver voice.KeptAudioArchiver
}

// NewDaemon constructs the daemon with all dependencies pre-built. It does
// NOT bind the IPC socket — that happens in Serve, so binding errors are
// surfaced from the same call as the accept loop.
//
// Required deps: Cfg, Recorder, Transcriber, Output. Closer is optional —
// when nil we try Transcriber.(transcribeCloser); if neither is set,
// shutdown will skip transcriber Close (effectively a no-op resource).
// Log is replaced with a discard handler when nil.
func NewDaemon(deps *DaemonDeps) *Daemon {
	if deps.Cfg == nil || deps.Recorder == nil || deps.Transcriber == nil || deps.Output == nil {
		panic("cmd: NewDaemon: cfg, recorder, transcriber and output are required")
	}

	if deps.Log == nil {
		deps.Log = slog.New(slog.DiscardHandler)
	}

	closer := deps.Closer
	if closer == nil {
		if implicitCloser, ok := deps.Transcriber.(transcribeCloser); ok {
			closer = implicitCloser
		}
	}

	notifyCh := make(chan domain.State, stateChBufSize)
	notifier := sysd.NewSdNotifier(deps.Log)
	machine := voice.NewMachine(makeNotifyListener(sysd.MakeStateListener(notifier, deps.Log), notifyCh))

	useCase := voice.NewVoiceUseCase(deps.Recorder, deps.Transcriber, deps.Output, deps.Log)
	if deps.Archiver != nil {
		useCase.AttachArchiver(deps.Archiver)
	}

	return &Daemon{
		cfg:         deps.Cfg,
		log:         deps.Log,
		machine:     machine,
		notifier:    notifier,
		useCase:     useCase,
		transcriber: closer,
		maxRecord:   pickMaxRecord(deps.Cfg),
		hotkeyMode:  deps.Cfg.Hotkey.Mode,
		notifyCh:    notifyCh,
	}
}

// ReloadConfig re-applies every config-driven side-effect the daemon
// owns: the STT chain (via ReloadTranscriber) AND the desktop hotkey
// binding (via setup.RunSetup). Called from the settings window after
// auto-save so the user does not have to restart anything when they
// switch provider or rebind the global hotkey.
//
// ctx is the parent context for building new dependencies. Callers
// should pass the daemon's lifetime context or context.Background()
// for administrative reloads.
func (d *Daemon) ReloadConfig(ctx context.Context) {
	d.ReloadTranscriber(ctx)
	d.ReloadOutput(ctx)
	d.reloadHotkey(ctx)
}

// ReloadTranscriber rebuilds the STT chain from the current config and
// atomically swaps it into the use case. Intended to be called by the
// settings UI after the user changes a provider-affecting field
// (provider, URL, model_path, retry policy, silence threshold) so the
// new values take effect without a daemon restart.
//
// Serialised against itself via reloadMu. The previous transcriber is
// Close()d after the swap; if BuildTranscriber fails, the old chain
// stays in place and the failure is logged — a misconfiguration that
// looks transient is preferable to a daemon that suddenly cannot
// transcribe anything because a typo dropped the URL.
//
// ctx is the parent context for building new dependencies. Callers
// should pass the daemon's lifetime context (captured during Serve)
// or context.Background() for administrative reloads.
func (d *Daemon) ReloadTranscriber(ctx context.Context) {
	if d == nil || d.useCase == nil {
		return
	}

	d.reloadMu.Lock()
	defer d.reloadMu.Unlock()

	newTranscriber, err := factory.BuildTranscriberWithAudit(ctx, d.cfg, d.audit, d.log)
	if err != nil {
		d.log.Warn("voice: reload transcriber failed; keeping current backend",
			slog.String("provider", d.cfg.Provider),
			slog.Any("err", err),
		)

		return
	}

	gated := factory.WrapWithSilenceGate(newTranscriber, d.cfg.Capture.SilenceThresholdDBFS, d.log)
	d.useCase.SwapTranscriber(gated)

	if d.transcriber != nil {
		if closeErr := d.transcriber.Close(); closeErr != nil {
			d.log.Warn("voice: previous transcriber close failed",
				slog.Any("err", closeErr),
			)
		}
	}

	d.transcriber = newTranscriber

	d.log.Info("voice: transcriber reloaded",
		slog.String("provider", d.cfg.Provider),
	)
}

// ReloadOutput rebuilds the Output (clipboard / autopaste / restore) from the
// current config and atomically swaps it into the use case. Called by
// ReloadConfig when output-affecting settings (e.g., restore_clipboard) change.
// Similar safety guarantees as ReloadTranscriber: cycles in flight use the old
// output; only new cycles use the rebuilt one.
func (d *Daemon) ReloadOutput(ctx context.Context) {
	if d == nil || d.useCase == nil {
		return
	}

	d.reloadMu.Lock()
	defer d.reloadMu.Unlock()

	newOutput, err := factory.BuildOutput(d.cfg, d.log)
	if err != nil {
		d.log.Warn("voice: reload output failed; keeping current output",
			slog.Any("err", err),
		)

		return
	}

	d.useCase.SwapOutput(newOutput)
	d.log.Info("voice: output reloaded")
}

// AttachAudit wires the AuditLogger used by cloud STT transcribers.
// Idempotent; passing nil restores the no-op default.
func (d *Daemon) AttachAudit(audit stt.AuditLogger) {
	if audit == nil {
		audit = stt.NoopAuditLogger{}
	}

	d.audit = audit
}

// AttachHotkey wires an optional global hotkey listener. Must be called
// before Serve. Idempotent overwrite: a second call replaces the previous
// listener without stopping it (callers that re-attach are expected to have
// stopped the old one first).
func (d *Daemon) AttachHotkey(hk voice.HotkeyListener) {
	d.hotkey = hk
}

// AttachTray wires an optional system-tray icon. Must be called before
// Serve. The tray receives state-change notifications via notifyCh and its
// Run method is started in a goroutine inside Serve.
func (d *Daemon) AttachTray(tr *tray.Tray) {
	d.tray = tr
	tr.SetInputCh(d.notifyCh)
}

// AttachSettings wires the Fyne settings window. Serve() calls win.Run() on
// the main goroutine and win.Stop() during shutdown. Must be called before
// Serve().
func (d *Daemon) AttachSettings(win fyneRunner) {
	d.settingsWin = win
}

// Toggle advances the state machine by EventToggle and dispatches the
// resulting action. Used by the tray icon to trigger recording without an
// IPC round-trip.
func (d *Daemon) Toggle(ctx context.Context) {
	if !d.acceptToggle() {
		d.log.DebugContext(ctx, "voice: tray toggle debounced")

		return
	}

	_, action, err := d.machine.Apply(domain.EventToggle)
	if err != nil {
		d.log.DebugContext(ctx, "voice: tray toggle rejected", slog.Any("err", err))

		return
	}

	d.dispatch(ctx, action)
}

// HotkeyHandler returns a voice.Handler that maps hotkey edges to state
// machine events. Mapping depends on cfg.Hotkey.Mode (resolved in
// factory.BuildHotkey; the daemon receives the policy decision indirectly via
// which events the listener delivers):
//
//   - toggle mode: only HotkeyPress is observed, fires domain.EventToggle.
//   - hold mode: HotkeyPress fires domain.EventStart, HotkeyRelease fires
//     domain.EventStop. Backends that cannot see release degrade to toggle
//     by delivering Press-only — the domain.EventStart from a stray Press then
//     starts recording, but the SM stays in domain.StateRecording until the next
//     Press (which the SM rejects as domain.ErrBusy → degraded UX, but no wedge).
//
// All event-mapping decisions live in this function so adapters stay
// generic — they just deliver edge events.
func (d *Daemon) HotkeyHandler() voice.Handler {
	return func(ctx context.Context, evt voice.HotkeyEvent) {
		// Hold mode: short Press+Release pairs (<DefaultHoldMinDuration)
		// would otherwise produce a few-frame WAV that either hits the
		// silence gate or makes the STT backend return "no speech",
		// landing the SM in StateError. Debounce by deferring Press
		// dispatch until the user has held long enough.
		if d.hotkeyMode == config.VoiceHotkeyModeHold {
			d.handleHoldEdge(ctx, evt)

			return
		}

		event, ok := hotkeyEventToSM(evt, d.hotkeyMode)
		if !ok {
			d.log.DebugContext(ctx, "voice: hotkey edge ignored for current mode",
				slog.String("edge", hotkeyEventString(evt)),
				slog.String("mode", string(d.hotkeyMode)),
			)

			return
		}

		if event == domain.EventToggle && !d.acceptToggle() {
			d.log.DebugContext(ctx, "voice: hotkey toggle debounced",
				slog.String("edge", hotkeyEventString(evt)),
			)

			return
		}

		d.applyHotkeyEvent(ctx, evt, event)
	}
}

// hotkeyEventToSM maps a (HotkeyEvent, hotkeyMode) tuple to the SM event
// to apply, or returns ok=false if this edge should be ignored.
//
// hold mode: Press → Start, Release → Stop. Anything else: ignore.
// toggle mode: Press → Toggle. Release: ignore.
func hotkeyEventToSM(evt voice.HotkeyEvent, mode config.VoiceHotkeyMode) (domain.Event, bool) {
	switch mode {
	case config.VoiceHotkeyModeHold:
		switch evt {
		case voice.HotkeyPress:
			return domain.EventStart, true
		case voice.HotkeyRelease:
			return domain.EventStop, true
		}
	case config.VoiceHotkeyModeToggle, "":
		if evt == voice.HotkeyPress {
			return domain.EventToggle, true
		}
	}

	return "", false
}

func hotkeyEventString(evt voice.HotkeyEvent) string {
	if evt == voice.HotkeyRelease {
		return "release"
	}

	return "press"
}

// pickMaxRecord chooses the cap on a single recording from the config.
// Falls back to 60s when the config has not been extended with capture
// limits yet, or when cfg is nil.
func pickMaxRecord(cfg *config.VoiceConfig) time.Duration {
	if cfg != nil && cfg.Capture.MaxDuration > 0 {
		return cfg.Capture.MaxDuration
	}

	return defaultMaxRecord
}

const defaultMaxRecord = 60 * time.Second

// Serve runs the daemon's foreground loop. The hotkey listener and tray run
// in background goroutines; the Fyne settings event loop runs on the main
// goroutine (GLFW requirement). Returns nil on a clean ctx-driven shutdown.
func (d *Daemon) Serve(ctx context.Context) error {
	d.notifier.Ready("idle")
	d.log.Info("voice: daemon ready",
		slog.String("provider", d.cfg.Provider),
	)

	if d.hotkey != nil {
		go d.runHotkey(ctx)
	}

	if d.tray != nil {
		go d.tray.Run(ctx)
	}

	// shutdownDone is closed after Daemon.Shutdown completes, giving Serve a
	// clean synchronisation point regardless of whether Fyne is in use.
	shutdownDone := make(chan struct{})

	// Capture context.Background() before the goroutine so gosec does not flag
	// it inside the goroutine. ctx is already cancelled by the time this
	// goroutine runs, so WithTimeout(ctx, ...) inside Shutdown would create an
	// immediately-done context, causing the LIFO manager to skip all resource
	// closers. A fresh background context gives the full shutdown timeout.
	shutdownCtx := context.Background()

	go func() {
		<-ctx.Done()

		shutdownErr := d.Shutdown(shutdownCtx)
		if shutdownErr != nil {
			d.log.Warn("voice: daemon shutdown reported error",
				slog.Any("err", shutdownErr),
			)
		}

		if d.settingsWin != nil {
			d.settingsWin.Stop()
		}

		close(shutdownDone)
	}()

	if d.settingsWin != nil {
		// Run Fyne event loop on main goroutine (GLFW requirement).
		// Blocks until Stop() is called from the shutdown goroutine above.
		d.settingsWin.Run()
		<-shutdownDone // usually already closed; ensures cleanup is complete
	} else {
		<-shutdownDone // headless: wait for shutdown before returning
	}

	return nil
}

// Shutdown closes the listener, cancels in-flight cycles, notifies systemd,
// and closes the transcriber. Idempotent via sync.Once: subsequent calls
// return the cached first-call result. Two paths can call this — the
// ctx-cancel watcher goroutine in Serve and an outside call from Bootstrap —
// so without sync.Once we'd risk double-Close on the transcriber.
//
// Teardown sequence (LIFO via partyzanex/shutdown):
//  1. Notify systemd + advance state machine + cancel in-flight cycle (inline).
//  2. IPC server.Shutdown — stops accepting new commands.
//  3. transcriber.Close — releases model/connection (registered first → closed last).
func (d *Daemon) Shutdown(ctx context.Context) error {
	if d == nil {
		return nil
	}

	d.shutdownOnce.Do(func() {
		d.notifier.Stopping("shutting down")

		// Tell the state machine we're going down so any concurrent IPC
		// handler short-circuits to domain.ErrBusy instead of starting new work.
		// Errors here are uninteresting — we ARE shutting down regardless.
		state, action, err := d.machine.Apply(domain.EventShutdown)
		_ = state
		_ = action
		_ = err

		// Cancel both cycle layers explicitly. recordingCancel is a child
		// of cycleCancel, but firing both keeps the contract independent
		// of the parent/child relationship inside startCycle.
		d.cycleMu.Lock()

		if d.recordingCancel != nil {
			d.recordingCancel()
		}

		if d.cycleCancel != nil {
			d.cycleCancel()
		}

		d.cycleMu.Unlock()

		// LIFO manager: transcriber registered first (closed last),
		// hotkey listener registered second (closed first — no new edges
		// can drive the daemon while the transcriber releases its model).
		mgr := shutdown.NewLIFO()

		if d.transcriber != nil {
			mgr.Append(shutdown.Fn(d.transcriber.Close))
		}

		if d.hotkey != nil {
			mgr.Append(shutdown.Fn(d.hotkey.Stop))
		}

		shutdownCtx, cancel := context.WithTimeout(ctx, shutdownTimeout)
		defer cancel()

		d.shutdownErr = mgr.CloseContext(shutdownCtx)
	})

	return d.shutdownErr
}

// handleHoldEdge routes a hold-mode hotkey edge through the debounce gate.
// Press is deferred by DefaultHoldMinDuration so taps shorter than that drop
// silently; Release after that window dispatches EventStop normally.
func (d *Daemon) handleHoldEdge(ctx context.Context, evt voice.HotkeyEvent) {
	switch evt {
	case voice.HotkeyPress:
		d.holdGate.OnPress(DefaultHoldMinDuration, func() {
			d.applyHotkeyEvent(ctx, evt, domain.EventStart)
		})
	case voice.HotkeyRelease:
		if !d.holdGate.OnRelease() {
			d.log.DebugContext(ctx, "voice: hold tap below minimum duration — dropped",
				slog.Duration("min", DefaultHoldMinDuration),
			)

			return
		}

		d.applyHotkeyEvent(ctx, evt, domain.EventStop)
	}
}

// applyHotkeyEvent feeds a single SM event through the state machine and
// dispatches the resulting action. Shared by all hotkey paths so the
// busy/error logging stays in one place.
func (d *Daemon) applyHotkeyEvent(ctx context.Context, evt voice.HotkeyEvent, event domain.Event) {
	newState, action, err := d.machine.Apply(event)
	if err != nil {
		// domain.ErrBusy is the expected race outcome when a key edge lands
		// mid-cycle (e.g. between domain.EventStop and domain.EventTranscribeDone).
		// Logging it at warn would spam during normal use.
		if errors.Is(err, domain.ErrBusy) {
			d.log.DebugContext(ctx, "voice: hotkey edge rejected (busy)",
				slog.String("edge", hotkeyEventString(evt)),
				slog.String("event", string(event)),
			)

			return
		}

		d.log.WarnContext(ctx, "voice: hotkey edge apply failed",
			slog.String("edge", hotkeyEventString(evt)),
			slog.Any("err", err),
		)

		return
	}

	d.log.DebugContext(ctx, "voice: hotkey edge applied",
		slog.String("edge", hotkeyEventString(evt)),
		slog.String("event", string(event)),
		slog.String("state", string(newState)),
		slog.String("action", string(action)),
	)

	d.dispatch(ctx, action)
}

// reloadHotkey was the GNOME-shortcut re-registration path. With the
// IPC/DE-shortcut layers removed, the only hotkey mechanism is the
// in-process evdev listener — changing the configured key requires a
// daemon restart for now. The method is kept as a no-op so older callers
// in ReloadConfig still compile; the body will be re-introduced when the
// evdev listener supports live re-binding.
func (d *Daemon) reloadHotkey(_ context.Context) {}

// acceptToggle returns true when the Toggle should be processed.
//
// Two cases always pass without touching the timer:
//   - Toggle from domain.StateRecording is a stop-recording intent and must
//     never be rate-limited — the user decides when to stop.
//
// All other states (typically Idle or Error) are subject to the debounce:
// only the first Toggle per toggleMinInterval window is accepted, which
// prevents GNOME key-repeat from spawning back-to-back recording cycles.
func (d *Daemon) acceptToggle() bool {
	if d.machine.State() == domain.StateRecording {
		return true
	}

	d.toggleMu.Lock()
	defer d.toggleMu.Unlock()

	if time.Since(d.lastToggleAt) < toggleMinInterval {
		return false
	}

	d.lastToggleAt = time.Now()

	return true
}

// runHotkey supervises the hotkey listener for the daemon's lifetime. Listen
// returns nil after Stop and ctx.Err() after cancellation; both are clean
// exits. Other errors indicate a real misconfiguration and are logged at WARN.
func (d *Daemon) runHotkey(ctx context.Context) {
	if d.hotkey == nil {
		return
	}

	d.log.InfoContext(ctx, "voice: hotkey listener started")

	err := d.hotkey.Listen(ctx)
	switch {
	case err == nil:
		d.log.DebugContext(ctx, "voice: hotkey listener stopped")
	case errors.Is(err, context.Canceled):
		d.log.DebugContext(ctx, "voice: hotkey listener cancelled")
	default:
		d.log.WarnContext(ctx, "voice: hotkey listener exited with error",
			slog.Any("err", err),
		)
	}
}

// dispatch performs the side effect that corresponds to a state-machine
// action. Long-running actions (record/transcribe/deliver) are launched
// in a background goroutine so the IPC reply returns immediately — the
// state machine guards against overlapping cycles via domain.ErrBusy.
func (d *Daemon) dispatch(ctx context.Context, action domain.Action) {
	switch action {
	case domain.ActionStartRecording:
		d.startCycle(ctx)
	case domain.ActionStopRecording:
		// Stop = "I'm done speaking, please transcribe what I just said".
		// Cancel ONLY the recording context so SubprocessRecorder stops
		// gracefully (SIGINT → finalised WAV). The cycle goroutine continues
		// into the transcribe phase, which uses the cycle ctx (still alive).
		d.cancelRecordingPhase()
	case domain.ActionDiscardAudio:
		// Discard = "abort everything, throw the audio away". Currently the
		// state machine emits this only on domain.EventShutdown from domain.StateRecording;
		// Shutdown() ALSO calls cycleCancel directly, so this is mostly a
		// belt-and-braces dispatch that runs before the explicit Shutdown
		// path takes over.
		d.cancelCycle()
	case domain.ActionShutdownNow:
		// Caller (Shutdown) already does the heavy lifting.
	case domain.ActionNone, domain.ActionFinishCycle:
		// No daemon-side dispatch — these actions are produced by transitions
		// the cycle goroutine drives internally. The use case's Cycle does
		// recording, transcription and delivery in one synchronous pass, so
		// the daemon has nothing to do after domain.EventTranscribeDone fires.
	}
}

// Output construction (factory.BuildOutput, session-aware clipboard/autopaste factories,
// and the factory.SessionClipboard / factory.SessionAutopaster seam interfaces) lives in
// output_builder.go to keep this file focused on daemon lifecycle and SM wiring.

// makeNotifyListener creates a domain.TransitionListener that fans each
// successful transition out to sdListener (sd_notify) and to ch (optional
// in-process consumers such as the tray). The channel send is non-blocking
// — a lagging consumer causes drops, not stalls on the state machine.
func makeNotifyListener(sdListener domain.TransitionListener, ch chan domain.State) domain.TransitionListener {
	return func(state domain.State, action domain.Action) {
		if sdListener != nil {
			sdListener(state, action)
		}

		select {
		case ch <- state:
		default:
		}
	}
}

//go:generate go run go.uber.org/mock/mockgen@latest -write_package_comment=false -package=daemon -destination=daemon_mocks_test.go -source=daemon.go transcribeCloser
