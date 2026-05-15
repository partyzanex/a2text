package settings

import (
	"context"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/partyzanex/a2text/assets"
	"github.com/partyzanex/a2text/internal/i18n"
	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/ui"
)

const (
	windowWidth  = 700
	windowHeight = 750

	// autoSaveDelay is the debounce window between the last field edit and
	// the actual disk write. Long enough to coalesce a burst of keystrokes
	// in a text field into a single save; short enough that the user does
	// not have to wait noticeably for changes to land.
	autoSaveDelay = 500 * time.Millisecond
)

// Window wraps the Fyne application and settings window lifecycle.
// Create with New; open with Show. Safe to call Show multiple times —
// a second call brings the existing window to front.
type Window struct {
	log      *slog.Logger
	cfg      *config.VoiceConfig
	app      fyne.App
	win      fyne.Window
	stopOnce sync.Once
	saver    *autoSaver

	// downloader is the whisper.cpp model fetcher used by the
	// "Скачать модель" button in the whisper.cpp card. Lazily-initialised
	// on first download so unit tests can swap it via the unexported
	// Downloader field; see SetDownloader in window_internal_test.go.
	downloader     ModelDownloader
	downloadCancel context.CancelFunc
	downloadMu     sync.Mutex

	// onConfigChanged is invoked from persistSave after a successful
	// disk write. The daemon registers a hook here to rebuild the STT
	// transcriber so provider/URL/model changes take effect immediately.
	onConfigChanged func()

	// rootCtxFn, when set via SetRootContext, returns the parent context
	// for any long-running goroutine the window spawns (currently: model
	// download). Cancelling the daemon ctx cancels in-flight downloads.
	// Stored as a function instead of a context.Context field to avoid
	// the containedctx lint rule — Fyne UI callbacks (OnTapped, goroutines
	// launched from them) do not accept context parameters, so the context
	// must be captured at the call site rather than passed through.
	rootCtxFn func() context.Context
}

// SetRootContext links the window's background work to a parent
// context. Typically called by the daemon bootstrap before Run() so
// daemon shutdown cleanly cancels in-flight model downloads. Safe to
// omit — the window falls back to context.Background().
func (w *Window) SetRootContext(ctx context.Context) {
	w.rootCtxFn = func() context.Context { return ctx }
}

// SetOnConfigChanged registers a callback the window fires after every
// successful auto-save. Used by the daemon to rebuild STT-affecting
// dependencies (transcriber, silence gate) without forcing the user to
// restart the process after every settings tweak. Pass nil to detach.
func (w *Window) SetOnConfigChanged(fn func()) {
	w.onConfigChanged = fn
}

// autoSaver coalesces a stream of field-change events into a single
// debounced save. Schedule() (re)arms the timer; the most recent call
// wins. Flush() forces an immediate save and cancels any pending timer
// — used on window close so the user never loses an in-flight edit.
//
// Safe for concurrent use: Schedule may be called from the Fyne
// goroutine while Flush runs from the close handler.
type autoSaver struct {
	mu    sync.Mutex
	timer *time.Timer
	delay time.Duration
	fn    func()
}

func newAutoSaver(delay time.Duration, fn func()) *autoSaver {
	return &autoSaver{delay: delay, fn: fn}
}

// Schedule (re)arms the debounce timer. If a previous Schedule call has
// not yet fired, its timer is stopped and replaced.
func (a *autoSaver) Schedule() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.timer != nil {
		a.timer.Stop()
	}

	a.timer = time.AfterFunc(a.delay, a.fn)
}

// Flush cancels any pending timer and runs the save immediately if a
// save was pending. Idempotent — flushing twice when nothing is pending
// is a no-op.
func (a *autoSaver) Flush() {
	a.mu.Lock()

	timer := a.timer
	a.timer = nil

	a.mu.Unlock()

	if timer == nil {
		return
	}

	if timer.Stop() {
		a.fn()
	}
}

// New creates a Window. cfg is the live config pointer; changes are written
// back to it on Save. A nil log is replaced with a discard handler.
//
// New does NOT start the Fyne event loop. Call Run() from the main goroutine
// to start it. Fyne's GLFW driver requires Run() to be called from the
// process's main goroutine.
func New(cfg *config.VoiceConfig, log *slog.Logger) *Window {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &Window{log: log, cfg: cfg}
}

// Run initialises the Fyne application and starts its event loop.
// MUST be called from the main goroutine — GLFW enforces this.
// Blocks until Stop() is called (or the app is quit by other means).
func (w *Window) Run() {
	runtime.LockOSThread()

	fyneApp := app.NewWithID("io.github.partyzanex.a2text")
	fyneApp.Settings().SetTheme(ui.Theme())
	w.app = fyneApp

	// A hidden stub window keeps the event loop alive between settings opens;
	// without it app.Run() exits when the last visible window is closed.
	// Must be created via fyne.Do so it runs after Run() establishes the
	// Fyne goroutine — calling NewWindow before Run() triggers Fyne's
	// "not on Fyne goroutine" check.
	fyne.Do(func() {
		stub := fyneApp.NewWindow("")
		stub.Hide()
	})

	fyneApp.Run() // blocks until Quit() is called
}

// Stop quits the Fyne event loop, causing Run() to return.
// Safe to call from any goroutine; idempotent.
func (w *Window) Stop() {
	w.stopOnce.Do(func() {
		if w.app != nil {
			fyne.Do(w.app.Quit)
		}
	})
}

// Show opens the settings window. If the window is already open it is brought
// to the front. Safe to call from any goroutine. No-op if Run() has not
// been called yet.
func (w *Window) Show() {
	if w.app == nil {
		return
	}

	// All Fyne UI calls must run on the Fyne goroutine; fyne.Do queues them
	// there from the systray callback goroutine that calls Show.
	fyne.Do(func() {
		if w.win != nil {
			w.win.Show()
			w.win.RequestFocus()

			return
		}

		// Re-initialise i18n with the user's currently saved UI language
		// so a relaunched window picks up a language switch from the
		// previous session without restarting the daemon.
		if err := i18n.Init(w.cfg.UILanguage); err != nil {
			w.log.Warn("settings: i18n init failed, falling back to defaults", slog.Any("err", err))
		}

		w.win = w.app.NewWindow(i18n.T("window.title"))
		w.win.Resize(fyne.NewSize(windowWidth, windowHeight))
		w.win.SetFixedSize(false)
		w.win.SetContent(w.buildContent())
		w.win.SetOnClosed(func() {
			// Flush any pending debounced edit so closing the window
			// without waiting 500ms still persists the last change.
			if w.saver != nil {
				w.saver.Flush()
			}

			w.win = nil
			w.saver = nil
		})
		w.win.Show()
	})
}

// rootCtx returns the stored root context or context.Background() as fallback.
func (w *Window) rootCtx() context.Context {
	if w.rootCtxFn != nil {
		return w.rootCtxFn()
	}

	return context.Background()
}

// formFields carries all the widget references needed to read values on Save.
type formFields struct {
	// STT — general
	provider   *widget.Select
	language   *widget.Select
	uiLanguage *widget.Select

	// go-whisper
	whisperURL          *widget.Entry
	whisperModel        *widget.SelectEntry
	whisperTimeout      *widget.Entry
	whisperAutoDownload *widget.Check
	whisperCheckBtn     *widget.Button
	whisperCheckStatus  *canvas.Text

	// whisper.cpp
	modelPath        *widget.SelectEntry
	modelDownloadBtn *widget.Button
	modelDownloadBar *widget.ProgressBar
	modelDownloadMsg *widget.Label

	// cloud
	cloudProvider *widget.Entry
	cloudAPIKey   *widget.Entry
	cloudBaseURL  *widget.Entry

	// STT retry
	sttRetryEnabled     *widget.Check
	sttRetryInitDelay   *widget.Entry
	sttRetryMaxDelay    *widget.Entry
	sttRetryMaxAttempts *widget.Entry

	// capture
	captureBackend          *widget.Select
	captureSampleRate       *widget.Entry
	captureChannels         *widget.Entry
	captureMaxDuration      *widget.Entry
	captureSilenceThreshold *widget.Entry

	// output
	outputMode       *widget.Select
	autopaste        *widget.Select
	restoreClipboard *widget.Check

	// hotkey
	hotkeyEnabled *widget.Check
	hotkeyBinding *hotkeyCaptureButton
	hotkeyMode    *widget.Select
	hotkeyBackend *widget.Select

	// daemon
	daemonSocketPath  *widget.Entry
	daemonGracePeriod *widget.Entry
	tempDir           *widget.Entry
	convertTimeout    *widget.Entry
	transcribeTimeout *widget.Entry
	logLevel          *widget.Select

	// privacy
	logTranscript   *widget.Check
	keepAudio       *widget.Check
	keepAudioDir    *widget.Entry
	keepAudioFormat *widget.Select

	// Provider-specific STT cards. Tracked here so changing the
	// "Провайдер STT" select can show only the matching card and hide
	// the irrelevant ones. Populated by buildSTTTab; nil before then.
	goWhisperCard  fyne.CanvasObject
	whisperCppCard fyne.CanvasObject
	cloudCard      fyne.CanvasObject
}

func (w *Window) buildContent() fyne.CanvasObject {
	ff := w.buildFields()

	// Auto-save: every field change re-arms a debounce timer; once the
	// user has been quiet for autoSaveDelay the latest values are flushed
	// to disk. Replaces the explicit Save/Cancel buttons.
	w.saver = newAutoSaver(autoSaveDelay, func() { w.save(ff) })
	attachAutoSave(ff, w.saver.Schedule)

	tabs := container.NewAppTabs(
		container.NewTabItemWithIcon(i18n.T("tab.stt"), assets.UIIcon("mic"), w.buildSTTTab(ff)),
		container.NewTabItemWithIcon(
			i18n.T("tab.capture_hotkey"), assets.UIIcon("record"), w.buildCaptureHotkeyTab(ff),
		),
		container.NewTabItemWithIcon(i18n.T("tab.daemon"), assets.UIIcon("server"), w.buildDaemonTab(ff)),
	)

	// Resize the window to fit the active tab so each tab gets a
	// height that matches its content, not the largest. fitWindowToTab
	// reads the inner VBox MinSize from inside the Scroll wrapper.
	tabs.OnSelected = func(t *container.TabItem) { w.fitWindowToTab(t) }

	// Apply initial sizing once the window backs the canvas — running
	// it inline here would race the Show() that hasn't returned yet.
	fyne.Do(func() { w.fitWindowToTab(tabs.Selected()) })

	// Provider-driven card visibility. Run AFTER buildSTTTab (which
	// populates the card refs on ff) and AFTER attachAutoSave (which
	// installed the generic schedule-only OnChanged); the override below
	// chains both behaviours into one handler. Initial apply makes the
	// window show only the relevant card on first open.
	applyProviderVisibility(ff)
	ff.provider.OnChanged = func(string) {
		applyProviderVisibility(ff)
		w.fitWindowToTab(tabs.Selected())
		w.saver.Schedule()
	}

	return tabs
}

// fitWindowToTab resizes the settings window so its body height
// matches the active tab's content height.
func (w *Window) fitWindowToTab(tab *container.TabItem) {
	if w.win == nil || tab == nil {
		return
	}

	// Tab bar + window-frame padding the OS will eat at the top/bottom.
	// Empirically 80dp is enough on GNOME at scale 1.0; smaller values
	// cause the last form row to clip; larger values waste space.
	const tabChromeHeight float32 = 80

	scroll, ok := tab.Content.(*container.Scroll)
	if !ok {
		return
	}

	contentHeight := scroll.Content.MinSize().Height
	w.win.Resize(fyne.NewSize(windowWidth, contentHeight+tabChromeHeight))
}

// attachAutoSave wires the same schedule() callback into the OnChanged
// hook of every editable widget. MUST run AFTER setFieldValues —
// SetSelected/SetChecked invoke OnChanged, which would otherwise trigger
// a spurious save during window construction.
func attachAutoSave(ff *formFields, schedule func()) {
	entries := []*widget.Entry{
		ff.whisperURL, ff.whisperTimeout, ff.keepAudioDir,
		ff.cloudProvider, ff.cloudAPIKey, ff.cloudBaseURL,
		ff.sttRetryInitDelay, ff.sttRetryMaxDelay, ff.sttRetryMaxAttempts,
		ff.captureSampleRate, ff.captureChannels, ff.captureMaxDuration,
		ff.captureSilenceThreshold,
		ff.daemonSocketPath, ff.daemonGracePeriod, ff.tempDir,
		ff.convertTimeout, ff.transcribeTimeout,
	}
	for _, entry := range entries {
		entry.OnChanged = func(string) { schedule() }
	}

	// whisperModel and modelPath are SelectEntry (combobox) rather than
	// plain Entry, so they cannot live in the loop above. Their OnChanged
	// behaves the same.
	ff.whisperModel.OnChanged = func(string) { schedule() }
	ff.modelPath.OnChanged = func(string) { schedule() }

	selects := []*widget.Select{
		ff.provider, ff.language, ff.uiLanguage,
		ff.captureBackend, ff.outputMode, ff.autopaste,
		ff.hotkeyMode, ff.hotkeyBackend, ff.logLevel,
		ff.keepAudioFormat,
	}
	for _, sel := range selects {
		sel.OnChanged = func(string) { schedule() }
	}

	checks := []*widget.Check{
		ff.hotkeyEnabled,
		ff.whisperAutoDownload, ff.sttRetryEnabled,
		ff.logTranscript, ff.keepAudio,
		ff.restoreClipboard,
	}
	for _, chk := range checks {
		chk.OnChanged = func(bool) { schedule() }
	}
}

func (w *Window) buildFields() *formFields {
	ff := w.buildFieldWidgets()
	w.setFieldValues(ff)

	return ff
}

// buildFieldWidgets allocates all form widgets with initial text/placeholder values.
func (w *Window) buildFieldWidgets() *formFields {
	ff := w.buildSTTFieldWidgets()
	w.buildCaptureFieldWidgets(ff)
	w.buildOutputHotkeyDaemonFieldWidgets(ff)

	return ff
}

// setFieldValues initialises select/check widget states from the current config.
func (w *Window) setFieldValues(ff *formFields) {
	hotkeyMode := string(w.cfg.Hotkey.Mode)
	if hotkeyMode == "" {
		hotkeyMode = string(config.VoiceHotkeyModeToggle)
	}

	captureBackend := w.cfg.Capture.Backend
	if captureBackend == "" {
		captureBackend = config.VoiceCaptureBackendAuto
	}

	hotkeyBackend := string(w.cfg.Hotkey.Backend)
	if hotkeyBackend == "" {
		hotkeyBackend = string(config.VoiceHotkeyBackendAuto)
	}

	logLevel := w.cfg.LogLevel
	if logLevel == "" {
		logLevel = config.VoiceLogLevelInfo
	}

	autopaste := w.cfg.Output.AutopasteCommand
	if autopaste == "" {
		autopaste = config.VoiceAutopasteCommandAuto
	}

	ff.provider.SetSelected(w.cfg.Provider)
	ff.language.SetSelected(sttLanguageOrDefault(w.cfg.Language))
	ff.uiLanguage.SetSelected(uiLanguageOrDefault(w.cfg.UILanguage))
	ff.captureBackend.SetSelected(captureBackend)
	ff.outputMode.SetSelected(w.cfg.Output.Mode)
	ff.autopaste.SetSelected(autopaste)
	ff.hotkeyMode.SetSelected(hotkeyMode)
	ff.hotkeyBackend.SetSelected(hotkeyBackend)
	ff.logLevel.SetSelected(logLevel)
	ff.whisperAutoDownload.SetChecked(w.cfg.GoWhisper.AutoDownload)
	ff.sttRetryEnabled.SetChecked(w.cfg.STTRetry.Enabled)
	ff.hotkeyEnabled.SetChecked(w.cfg.Hotkey.Enabled)
	ff.logTranscript.SetChecked(w.cfg.Privacy.LogTranscript)
	ff.keepAudio.SetChecked(w.cfg.Privacy.KeepAudio)
	ff.restoreClipboard.SetChecked(w.cfg.Output.RestoreClipboard)

	keepAudioFormat := w.cfg.Privacy.KeepAudioFormat
	if keepAudioFormat == "" {
		keepAudioFormat = config.VoiceKeepAudioFormatWAV
	}

	ff.keepAudioFormat.SetSelected(keepAudioFormat)
}

// save reads every form field back into the config and writes the
// result to disk via persistSave. Called by the debounced auto-saver.
func (w *Window) save(ff *formFields) {
	w.applySTTFields(ff)
	w.applyCaptureFields(ff)
	w.applyOutputFields(ff)
	w.applyHotkeyFields(ff)
	w.applyDaemonFields(ff)

	w.cfg.Privacy.LogTranscript = ff.logTranscript.Checked
	w.cfg.Privacy.KeepAudio = ff.keepAudio.Checked
	w.cfg.Privacy.KeepAudioDir = ff.keepAudioDir.Text
	w.cfg.Privacy.KeepAudioFormat = ff.keepAudioFormat.Selected

	w.persistSave()
}

// persistSave calls SaveConfig and logs the outcome. Dialog popups were
// removed when the Save/Cancel buttons gave way to auto-save: a dialog on
// every successful keystroke would be intolerable, and dialogs on
// transient parse-in-progress errors (e.g. half-typed durations) would
// be misleading. Operators watching logs see every save attempt.
func (w *Window) persistSave() {
	if err := SaveConfig(w.cfg); err != nil {
		w.log.Warn("settings: auto-save failed", slog.Any("err", err))

		return
	}

	w.log.Debug("settings: config auto-saved")

	if w.onConfigChanged != nil {
		w.onConfigChanged()
	}
}
