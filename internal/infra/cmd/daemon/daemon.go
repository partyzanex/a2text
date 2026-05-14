package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/partyzanex/shutdown"

	"github.com/partyzanex/a2text/internal/adapters/ipc"
	"github.com/partyzanex/a2text/internal/domain"
	"github.com/partyzanex/a2text/internal/infra/cmd/sysd"
	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/infra/tray"
	"github.com/partyzanex/a2text/internal/usecases/voice"
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

// Daemon ties together state machine, IPC, sd_notify, voice use case, and
// the recording/transcription/output adapters into the long-running
// dictation service.
//
// One daemon per process. Mutual exclusion against parallel daemon starts
// is enforced by the file-lock in StartDaemonLocked (see lock.go) — Daemon
// itself assumes it has the field clear.
//
// Stage I.3 work: X11 hotkey (voice.HotkeyListener / adapters/hotkey.X11Hotkey)
// is not yet wired here. The current entry point is CLI self-bootstrap
// (RunBootstrap), which toggles via IPC. Hotkey wiring lands in stage I.3
// when the daemon manages the full hotkey lifecycle directly.
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

	// notifyCh carries state-machine transitions to any interested in-process
	// consumer (currently the tray). Sends are non-blocking — a lagging
	// consumer causes drops, not stalls.
	notifyCh chan domain.State

	// hotkeyMode caches cfg.Hotkey.Mode so HotkeyHandler doesn't read
	// through the config pointer on the hot path. Default "" maps to toggle.
	hotkeyMode config.VoiceHotkeyMode

	// serverMu protects the server pointer set by Serve and read by Shutdown.
	// Without it we'd race when Shutdown is called from another goroutine
	// during Serve startup.
	serverMu sync.Mutex
	server   *ipc.Server

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
}

// transcribeCloser is the subset of stt.Transcriber the daemon needs to
// call Close on shutdown. We accept any type with a Close method to avoid
// importing the whole stt package here.
type transcribeCloser interface {
	Close() error
}

// DaemonDeps groups everything NewDaemon needs from the wiring layer.
// Keeps the signature flat instead of a 7-arg constructor.
//
// The socket path is intentionally NOT here: Daemon.Serve takes it as an
// argument so callers can pass DefaultSocketPath() (production) or a
// per-test temp path (integration tests) without a separate construction
// path.
type DaemonDeps struct {
	Cfg         *config.VoiceConfig
	Log         *slog.Logger
	Recorder    voice.Recorder
	Transcriber voice.Transcriber
	Closer      transcribeCloser // typically same value as Transcriber
	Output      voice.Output
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

	return &Daemon{
		cfg:         deps.Cfg,
		log:         deps.Log,
		machine:     machine,
		notifier:    notifier,
		useCase:     voice.NewVoiceUseCase(deps.Recorder, deps.Transcriber, deps.Output, deps.Log),
		transcriber: closer,
		maxRecord:   pickMaxRecord(deps.Cfg),
		hotkeyMode:  deps.Cfg.Hotkey.Mode,
		notifyCh:    notifyCh,
	}
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

// Serve binds the IPC socket and runs the accept loop until ctx is done
// or the listener is closed (typically by Shutdown). On a clean shutdown
// the returned error is nil.
//

func (d *Daemon) Serve(ctx context.Context, socketPath string) error {
	if socketPath == "" {
		return errors.New("daemon: Serve: socket path must not be empty")
	}

	server, err := ipc.NewServer(ctx, socketPath, ipc.HandlerFunc(d.handleIPC), d.log)
	if err != nil {
		return fmt.Errorf("daemon: bind ipc: %w", err)
	}

	d.serverMu.Lock()
	d.server = server
	d.serverMu.Unlock()

	d.notifier.Ready("idle")
	d.log.Info("voice: daemon ready",
		slog.String("socket", filepath.Base(server.SocketPath())),
		slog.String("provider", d.cfg.Provider),
	)

	if d.hotkey != nil {
		go d.runHotkey(ctx)
	}

	if d.tray != nil {
		go d.tray.Run(ctx)
	}

	go func() {
		<-ctx.Done()

		// Initiate shutdown when the surrounding context cancels.
		if shutdownErr := d.Shutdown(ctx); shutdownErr != nil {
			d.log.Warn("voice: daemon shutdown reported error",
				slog.Any("err", shutdownErr),
			)
		}
	}()

	if serveErr := server.Serve(ctx); serveErr != nil {
		return fmt.Errorf("daemon: serve: %w", serveErr)
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
		// server registered second (closed first — stops new commands before
		// the model/connection is released).
		mgr := shutdown.NewLIFO()

		if d.transcriber != nil {
			mgr.Append(shutdown.Fn(d.transcriber.Close))
		}

		d.serverMu.Lock()
		srv := d.server
		d.serverMu.Unlock()

		if srv != nil {
			mgr.Append(shutdown.Fn(srv.Shutdown))
		}

		// Hotkey appended last so it Stops first under LIFO — no new toggles
		// arrive while server.Shutdown drains in-flight IPC commands and the
		// transcriber releases its model.
		if d.hotkey != nil {
			mgr.Append(shutdown.Fn(d.hotkey.Stop))
		}

		shutdownCtx, cancel := context.WithTimeout(ctx, shutdownTimeout)
		defer cancel()

		d.shutdownErr = mgr.CloseContext(shutdownCtx)
	})

	return d.shutdownErr
}

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

// handleIPC dispatches one IPC request → Response. Called from per-conn
// goroutines inside ipc.Server; safe for concurrency thanks to the
// machine's internal lock and cycleMu.
//
// Every Response carries Version and ID — the server layer also enforces
// this (see server.go), but populating here keeps the contract local to
// each handler return path so a future direct test of handleIPC sees the
// correct shape.
func (d *Daemon) handleIPC(ctx context.Context, req ipc.Request) ipc.Response {
	state := d.machine.State()

	if req.Command == ipc.CmdPing {
		resp := ipc.NewResponseFor(req, string(state))
		resp.OK = true
		resp.LastError = d.machine.LastError()

		return resp
	}

	event, evOk := commandToEvent(req.Command)
	if !evOk {
		resp := ipc.NewResponseFor(req, string(state))
		resp.OK = false
		resp.ErrorCode = ipc.ErrCodeUnknownCommand
		resp.Message = fmt.Sprintf("daemon does not know command %q", req.Command)

		return resp
	}

	if event == domain.EventToggle && !d.acceptToggle() {
		resp := ipc.NewResponseFor(req, string(state))
		resp.OK = false
		resp.ErrorCode = ipc.ErrCodeBusy
		resp.Message = "toggle debounced: too frequent"

		return resp
	}

	newState, action, err := d.machine.Apply(event)
	if err != nil {
		// Only domain.ErrBusy maps to the "busy" error code. Any other rejection
		// (unknown transition, post-shutdown, etc.) gets an empty code so
		// the client doesn't treat all failures as transient backpressure.
		code := ""
		if errors.Is(err, domain.ErrBusy) {
			code = ipc.ErrCodeBusy
		}

		resp := ipc.NewResponseFor(req, string(newState))
		resp.OK = false
		resp.ErrorCode = code
		resp.Message = err.Error()

		return resp
	}

	d.dispatch(ctx, action)

	resp := ipc.NewResponseFor(req, string(newState))
	resp.OK = true

	return resp
}

// commandToEvent maps an IPC command to a state-machine event. Returns
// ok=false for commands that have no event mapping (Ping, plus any future
// command that lands here without a switch arm) so the caller can reject
// rather than silently toggling — a default of domain.EventToggle was a footgun
// for unknown commands.
func commandToEvent(cmd ipc.Command) (domain.Event, bool) {
	switch cmd {
	case ipc.CmdToggle:
		return domain.EventToggle, true
	case ipc.CmdStart:
		return domain.EventStart, true
	case ipc.CmdStop:
		return domain.EventStop, true
	case ipc.CmdPing:
		// Ping has no event mapping — handled before dispatch in handleIPC.
		return "", false
	}

	return "", false
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

// startCycle kicks off a new dictation cycle in the background. It runs
// record→transcribe→deliver as a single op and feeds completion events
// into the state machine.
//
// A daemon-side timer enforces the recording cap independently of the
// recorder: even if the recorder honours MaxDuration and returns naturally,
// the SM stays in domain.StateRecording until something fires domain.EventTimeout. Without
// this timer a successfully-completed natural-finish cycle would try to
// Apply(domain.EventTranscribeDone) from domain.StateRecording and be rejected, leaving
// the daemon stuck in "recording" forever.
//
// Refuses to start if a cycle is already in flight (cycleCancel != nil) —
// the state machine should already prevent this via domain.ErrBusy, but a cheap
// guard here avoids leaking a goroutine if the SM ever has a regression.
func (d *Daemon) startCycle(parent context.Context) {
	cycleCtx, cycleCancel := context.WithCancel(parent)
	recordCtx, recordCancel := context.WithCancel(cycleCtx)

	d.cycleMu.Lock()

	if d.cycleCancel != nil {
		d.cycleMu.Unlock()
		cycleCancel()
		recordCancel()
		d.log.Warn("voice: startCycle invoked while cycle already running — state machine bug?")

		return
	}

	d.cycleCancel = cycleCancel
	d.recordingCancel = recordCancel

	d.cycleMu.Unlock()

	// Defensive: a misconfigured pickMaxRecord (or future config flag set
	// to 0/negative) would fire AfterFunc immediately and stop the user's
	// recording before it began. Normalise here so the timer always has a
	// sane positive duration regardless of how maxRecord was computed.
	maxRecord := d.maxRecord
	if maxRecord <= 0 {
		maxRecord = defaultMaxRecord
	}

	// time.AfterFunc fires in its own goroutine. The state machine
	// serialises Apply, so timer-vs-manual-toggle is decided at the SM
	// level: whoever calls Apply(domain.EventTimeout)/Apply(domain.EventToggle) first
	// transitions Recording→Transcribing; the other gets domain.ErrBusy.
	timeoutTimer := time.AfterFunc(maxRecord, func() {
		if _, _, err := d.machine.Apply(domain.EventTimeout); err != nil {
			// domain.State already moved out of Recording — a manual toggle
			// reached the SM before us. Nothing to do.
			return
		}

		// We won the race. Cancel the recording phase so the recorder
		// finalises its WAV; transcribe + deliver continue on cycleCtx.
		d.cancelRecordingPhase()
	})

	go func() {
		defer func() {
			// Stop is safe to call after the timer fired — returns false.
			// Doing it inside the goroutine that owns the cycle keeps the
			// timer's lifetime bounded by the cycle's lifetime.
			timeoutTimer.Stop()

			d.cycleMu.Lock()
			d.cycleCancel = nil
			d.recordingCancel = nil
			d.cycleMu.Unlock()

			recordCancel()
			cycleCancel()
		}()

		result, cycleErr := d.useCase.Cycle(
			cycleCtx, recordCtx,
			domain.RecordOpts{MaxDuration: maxRecord},
			d.cfg.Language,
		)
		if cycleErr != nil {
			d.handleCycleError(cycleErr)

			return
		}

		d.advanceCycleSuccess(result)
	}()
}

// advanceCycleSuccess is the success-path continuation after Cycle returns.
// It logs the transcript when configured, bridges out of domain.StateRecording if
// necessary, then advances the state machine through TranscribeDone →
// DeliverDone. Errors on state machine transitions are logged and the method
// returns early — the goroutine that called this has nothing useful to do if
// the SM rejects a post-cycle event.
func (d *Daemon) advanceCycleSuccess(result domain.CycleResult) {
	var textLen []slog.Attr
	if d.cfg.Privacy.LogTranscript && result.Text != "" {
		// text_len is gated on LogTranscript: even the length of a transcription
		// can be sensitive in strict-privacy deployments.
		textLen = []slog.Attr{slog.Int("text_len", len(result.Text))}
	}

	d.log.Info("voice: cycle completed",
		voice.CycleAttrs(result, textLen...),
		slog.String("provider", d.cfg.Provider),
	)

	if d.cfg.Privacy.LogTranscript && result.Text != "" {
		// Emit the full transcript at DEBUG so it appears in dev logs without
		// polluting INFO-level journal entries.
		d.log.Debug("voice: transcript",
			slog.String("model", d.cfg.GoWhisper.Model),
			slog.String("text", result.Text),
		)
	}

	// Bridge: if the recorder finished naturally at MaxDuration before
	// either the daemon timer or a manual toggle moved the SM, we are
	// still in domain.StateRecording. Drive the SM through domain.EventTimeout so
	// the upcoming domain.EventTranscribeDone is valid.
	//
	// Do NOT pre-check State() — it and Apply are not atomic. Apply
	// itself validates the transition; domain.ErrBusy means the SM already
	// moved (timer or manual toggle won the race), which is fine.
	if _, _, applyErr := d.machine.Apply(domain.EventTimeout); applyErr != nil && !errors.Is(applyErr, domain.ErrBusy) {
		d.log.Warn("voice: post-cycle bridge to transcribing rejected",
			slog.Any("err", applyErr),
		)

		return
	}

	if _, _, applyErr := d.machine.Apply(domain.EventTranscribeDone); applyErr != nil {
		d.log.Warn("voice: cycle done but state rejected",
			slog.Any("err", applyErr),
		)

		return
	}

	// Output already happened inside Cycle; jump straight to delivered.
	if _, _, applyErr := d.machine.Apply(domain.EventDeliverDone); applyErr != nil {
		d.log.Warn("voice: deliver-done bookkeeping rejected",
			slog.Any("err", applyErr),
		)
	}
}

// cancelRecordingPhase cancels the recording sub-context only. Transcribe
// and deliver continue with the surviving cycle ctx.
func (d *Daemon) cancelRecordingPhase() {
	d.cycleMu.Lock()
	defer d.cycleMu.Unlock()

	if d.recordingCancel != nil {
		d.recordingCancel()
	}
}

func (d *Daemon) cancelCycle() {
	d.cycleMu.Lock()
	defer d.cycleMu.Unlock()

	if d.cycleCancel != nil {
		d.cycleCancel()
	}
}

func (d *Daemon) handleCycleError(err error) {
	if errors.Is(err, domain.ErrEmptyResult) {
		d.log.Info("voice: cycle produced empty transcription")

		// Bridge past domain.StateRecording first: Cycle errors short-circuit
		// before the success-path bridge runs, so the SM is typically
		// still in domain.StateRecording even though the recording phase is over.
		// domain.EventEmptyResult is only valid from domain.StateTranscribing — without
		// this bridge the daemon would stay in "recording" until the next
		// manual toggle.
		d.bridgeOutOfRecording("empty-result")

		// Empty result is not a failure: STT succeeded, the audio simply
		// had no speech. Skip domain.StateDelivering entirely via domain.EventEmptyResult
		// — going through delivering with no real text would mislead
		// sd_notify/IPC about what the daemon is doing.
		if _, _, applyErr := d.machine.Apply(domain.EventEmptyResult); applyErr != nil {
			d.log.Warn("voice: empty-result event rejected",
				slog.Any("err", applyErr),
			)
		}

		return
	}

	// Phase-aware logging: which step actually failed matters for triage.
	var cycleErr *domain.CycleError
	if errors.As(err, &cycleErr) {
		d.log.Warn("voice: cycle failed",
			slog.String("phase", string(cycleErr.Phase)),
			slog.Any("err", cycleErr.Err),
		)
	} else {
		d.log.Warn("voice: cycle failed", slog.Any("err", err))
	}

	// Phase-aware SM routing. Cycle errors short-circuit before the success
	// path's bridge runs, so the SM is still in domain.StateRecording regardless of
	// which phase actually failed:
	//
	//   - domain.PhaseRecord  → domain.EventRecordFailed: Recording → Error directly.
	//   - other phases → domain.EventRecordFailed semantically wrong (we DID get
	//     audio); bridge through domain.EventTimeout to Transcribing, then
	//     domain.EventTranscribeFailed → Error.
	//
	// Without this routing, ApplyWithError(domain.EventTranscribeFailed) from
	// domain.StateRecording is treated as a late/stale event and rejected,
	// leaving the daemon wedged in "recording" forever.
	if cycleErr != nil && cycleErr.Phase == domain.PhaseRecord {
		if _, _, applyErr := d.machine.ApplyWithError(domain.EventRecordFailed, err.Error()); applyErr != nil {
			d.log.Warn("voice: failed to record capture failure in state machine",
				slog.Any("err", applyErr),
			)
		}

		return
	}

	// Non-record-phase failure: bridge past domain.StateRecording first.
	d.bridgeOutOfRecording("transcribe/deliver-failure")

	if _, _, applyErr := d.machine.ApplyWithError(domain.EventTranscribeFailed, err.Error()); applyErr != nil {
		d.log.Warn("voice: failed to record cycle error in state machine",
			slog.Any("err", applyErr),
		)
	}
}

// bridgeOutOfRecording advances the SM out of domain.StateRecording via
// domain.EventTimeout when an error-path event arrives before the success-path
// bridge ran. Cycle errors short-circuit `startCycle`'s goroutine before
// the post-cycle Apply(domain.EventTimeout), so the SM may still be in Recording
// when handleCycleError fires; without this bridge the follow-up
// domain.EventTranscribeFailed / domain.EventEmptyResult would be rejected as
// "known event invalid for current state" and the daemon would wedge.
//
// `domain.ErrBusy` is expected here: a parallel manual toggle (or the
// daemon-side timer) may have already moved the SM. Log only non-busy
// rejections so the journal stays clean during normal races.
func (d *Daemon) bridgeOutOfRecording(reason string) {
	// Do NOT pre-check State() — it and Apply are not atomic.
	// domain.ErrBusy from Apply means the SM already moved; that is the expected
	// race outcome and must not be logged as an error.
	_, _, applyErr := d.machine.Apply(domain.EventTimeout)
	if applyErr == nil || errors.Is(applyErr, domain.ErrBusy) {
		return
	}

	d.log.Warn("voice: bridge to transcribing rejected",
		slog.String("reason", reason),
		slog.Any("err", applyErr),
	)
}

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

// Output construction (factory.BuildOutput, session-aware clipboard/autopaste factories,
// and the factory.SessionClipboard / factory.SessionAutopaster seam interfaces) lives in
// output_builder.go to keep this file focused on daemon lifecycle and SM wiring.

//go:generate go run go.uber.org/mock/mockgen@latest -package=cmd -destination=daemon_mocks_test.go -source=daemon.go transcribeCloser
