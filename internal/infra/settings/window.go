package settings

import (
	"log/slog"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/partyzanex/a2text/internal/infra/config"
)

const (
	windowWidth   = 700
	windowHeight  = 750
	buttonColumns = 2
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

		w.win = w.app.NewWindow("a2text — Настройки")
		w.win.Resize(fyne.NewSize(windowWidth, windowHeight))
		w.win.SetFixedSize(false)
		w.win.SetContent(w.buildContent())
		w.win.SetOnClosed(func() { w.win = nil })
		w.win.Show()
	})
}

// formFields carries all the widget references needed to read values on Save.
type formFields struct {
	// STT — general
	provider *widget.Select
	language *widget.Entry

	// go-whisper
	whisperURL          *widget.Entry
	whisperPrefix       *widget.Entry
	whisperModel        *widget.Entry
	whisperTimeout      *widget.Entry
	whisperAutoDownload *widget.Check

	// whisper.cpp
	modelPath *widget.Entry

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
	captureBackend     *widget.Select
	captureSampleRate  *widget.Entry
	captureChannels    *widget.Entry
	captureMaxDuration *widget.Entry

	// output
	outputMode *widget.Select
	autopaste  *widget.Entry

	// hotkey
	hotkeyEnabled *widget.Check
	hotkeyKey     *widget.Entry
	hotkeyMods    *widget.Entry
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
	logTranscript *widget.Check
	keepAudio     *widget.Check
}

func (w *Window) buildContent() fyne.CanvasObject {
	ff := w.buildFields()

	saveBtn := widget.NewButton("Сохранить", func() { w.save(ff) })
	saveBtn.Importance = widget.HighImportance

	cancelBtn := widget.NewButton("Отмена", func() {
		if w.win != nil {
			w.win.Close()
		}
	})

	tabs := container.NewAppTabs(
		container.NewTabItem("STT", w.buildSTTTab(ff)),
		container.NewTabItem("Захват", w.buildCaptureTab(ff)),
		container.NewTabItem("Вывод", w.buildOutputTab(ff)),
		container.NewTabItem("Хоткей", w.buildHotkeyTab(ff)),
		container.NewTabItem("Демон", w.buildDaemonTab(ff)),
		container.NewTabItem("Приватность", w.buildPrivacyTab(ff)),
	)

	buttons := container.NewGridWithColumns(buttonColumns, saveBtn, cancelBtn)

	return container.NewBorder(nil, buttons, nil, nil, tabs)
}

// buildSTTTab assembles the STT settings tab with cards for each sub-section.
func (w *Window) buildSTTTab(ff *formFields) fyne.CanvasObject {
	generalForm := widget.NewForm(
		widget.NewFormItem("Провайдер STT", ff.provider),
		widget.NewFormItem("Язык", ff.language),
	)

	goWhisperForm := widget.NewForm(
		widget.NewFormItem("URL", ff.whisperURL),
		widget.NewFormItem("Prefix", ff.whisperPrefix),
		widget.NewFormItem("Модель", ff.whisperModel),
		widget.NewFormItem("Таймаут", ff.whisperTimeout),
		widget.NewFormItem("Авто-загрузка модели", ff.whisperAutoDownload),
	)

	whisperCppForm := widget.NewForm(
		widget.NewFormItem("Путь к модели", ff.modelPath),
	)

	cloudForm := widget.NewForm(
		widget.NewFormItem("Провайдер", ff.cloudProvider),
		widget.NewFormItem("API ключ", ff.cloudAPIKey),
		widget.NewFormItem("Base URL", ff.cloudBaseURL),
	)

	retryForm := widget.NewForm(
		widget.NewFormItem("Включить ретраи", ff.sttRetryEnabled),
		widget.NewFormItem("Начальная задержка", ff.sttRetryInitDelay),
		widget.NewFormItem("Макс. задержка", ff.sttRetryMaxDelay),
		widget.NewFormItem("Макс. попыток", ff.sttRetryMaxAttempts),
	)

	content := container.NewVBox(
		widget.NewCard("Общее", "", generalForm),
		widget.NewCard("go-whisper", "", goWhisperForm),
		widget.NewCard("whisper.cpp", "", whisperCppForm),
		widget.NewCard("Облако", "", cloudForm),
		widget.NewCard("Ретраи STT", "", retryForm),
	)

	return container.NewScroll(content)
}

// buildCaptureTab assembles the audio capture settings tab.
func (w *Window) buildCaptureTab(ff *formFields) fyne.CanvasObject {
	form := widget.NewForm(
		widget.NewFormItem("Бэкенд", ff.captureBackend),
		widget.NewFormItem("Частота дискретизации", ff.captureSampleRate),
		widget.NewFormItem("Каналы", ff.captureChannels),
		widget.NewFormItem("Макс. длительность", ff.captureMaxDuration),
	)

	return container.NewScroll(form)
}

// buildOutputTab assembles the output settings tab.
func (w *Window) buildOutputTab(ff *formFields) fyne.CanvasObject {
	form := widget.NewForm(
		widget.NewFormItem("Режим вывода", ff.outputMode),
		widget.NewFormItem("Команда автовставки", ff.autopaste),
	)

	return container.NewScroll(form)
}

// buildHotkeyTab assembles the hotkey settings tab.
func (w *Window) buildHotkeyTab(ff *formFields) fyne.CanvasObject {
	form := widget.NewForm(
		widget.NewFormItem("Включить встроенный хоткей", ff.hotkeyEnabled),
		widget.NewFormItem("Клавиша", ff.hotkeyKey),
		widget.NewFormItem("Модификаторы (через запятую)", ff.hotkeyMods),
		widget.NewFormItem("Режим", ff.hotkeyMode),
		widget.NewFormItem("Бэкенд", ff.hotkeyBackend),
	)

	return container.NewScroll(form)
}

// buildDaemonTab assembles the daemon settings tab with cards.
func (w *Window) buildDaemonTab(ff *formFields) fyne.CanvasObject {
	ipcForm := widget.NewForm(
		widget.NewFormItem("Путь к сокету", ff.daemonSocketPath),
		widget.NewFormItem("Период завершения", ff.daemonGracePeriod),
	)

	filesForm := widget.NewForm(
		widget.NewFormItem("Временная директория", ff.tempDir),
		widget.NewFormItem("Таймаут конвертации", ff.convertTimeout),
		widget.NewFormItem("Таймаут транскрипции", ff.transcribeTimeout),
	)

	logForm := widget.NewForm(
		widget.NewFormItem("Уровень логов", ff.logLevel),
	)

	content := container.NewVBox(
		widget.NewCard("IPC", "", ipcForm),
		widget.NewCard("Рабочие файлы", "", filesForm),
		widget.NewCard("Логирование", "", logForm),
	)

	return container.NewScroll(content)
}

// buildPrivacyTab assembles the privacy settings tab.
func (w *Window) buildPrivacyTab(ff *formFields) fyne.CanvasObject {
	form := widget.NewForm(
		widget.NewFormItem("Логировать транскрипции", ff.logTranscript),
		widget.NewFormItem("Сохранять аудио", ff.keepAudio),
	)

	return container.NewScroll(form)
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

// buildSTTFieldWidgets creates STT-section widgets and returns an initialized formFields.
func (w *Window) buildSTTFieldWidgets() *formFields {
	apiKeyEntry := widget.NewEntry()
	apiKeyEntry.Password = true
	apiKeyEntry.SetText(w.cfg.CloudAPIKey)

	return &formFields{
		provider: widget.NewSelect(
			[]string{
				config.VoiceProviderGoWhisper,
				config.VoiceProviderWhisperCpp,
				config.VoiceProviderCloud,
			},
			nil,
		),
		language:            entryWithText(w.cfg.Language, "ru"),
		whisperURL:          entryWithText(w.cfg.GoWhisper.URL, "http://localhost:9081"),
		whisperPrefix:       entryWithText(w.cfg.GoWhisper.Prefix, "/api/whisper"),
		whisperModel:        entryWithText(w.cfg.GoWhisper.Model, "ggml-small"),
		whisperTimeout:      entryWithText(formatDuration(w.cfg.GoWhisper.Timeout), "30s"),
		whisperAutoDownload: widget.NewCheck("", nil),
		modelPath:           entryWithText(w.cfg.ModelPath, ""),
		cloudProvider:       entryWithText(w.cfg.CloudProvider, "openai"),
		cloudAPIKey:         apiKeyEntry,
		cloudBaseURL:        entryWithText(w.cfg.CloudBaseURL, ""),
		sttRetryEnabled:     widget.NewCheck("", nil),
		sttRetryInitDelay:   entryWithText(formatDuration(w.cfg.STTRetry.InitialDelay), "200ms"),
		sttRetryMaxDelay:    entryWithText(formatDuration(w.cfg.STTRetry.MaxDelay), "5s"),
		sttRetryMaxAttempts: entryWithText(intOrEmpty(w.cfg.STTRetry.MaxAttempts), "2"),
	}
}

// buildCaptureFieldWidgets fills capture section widgets into ff.
func (w *Window) buildCaptureFieldWidgets(ff *formFields) {
	ff.captureBackend = widget.NewSelect(
		[]string{
			config.VoiceCaptureBackendAuto,
			config.VoiceCaptureBackendPipeWire,
			config.VoiceCaptureBackendPulseAudio,
		},
		nil,
	)
	ff.captureSampleRate = entryWithText(intOrEmpty(w.cfg.Capture.SampleRate), "16000")
	ff.captureChannels = entryWithText(intOrEmpty(w.cfg.Capture.Channels), "1")
	ff.captureMaxDuration = entryWithText(formatDuration(w.cfg.Capture.MaxDuration), "60s")
}

// buildOutputHotkeyDaemonFieldWidgets fills output, hotkey, daemon and privacy widgets into ff.
func (w *Window) buildOutputHotkeyDaemonFieldWidgets(ff *formFields) {
	ff.outputMode = widget.NewSelect(
		[]string{
			config.VoiceOutputModeStdout,
			config.VoiceOutputModeClipboard,
			config.VoiceOutputModeClipboardAutopaste,
		},
		nil,
	)
	ff.autopaste = entryWithText(w.cfg.Output.AutopasteCommand, "auto")
	ff.hotkeyEnabled = widget.NewCheck("", nil)
	ff.hotkeyKey = entryWithText(w.cfg.Hotkey.Key, "R")
	ff.hotkeyMods = entryWithText(strings.Join(w.cfg.Hotkey.Modifiers, ","), "super")
	ff.hotkeyMode = widget.NewSelect(
		[]string{
			string(config.VoiceHotkeyModeToggle),
			string(config.VoiceHotkeyModeHold),
		},
		nil,
	)
	ff.hotkeyBackend = widget.NewSelect(
		[]string{
			string(config.VoiceHotkeyBackendAuto),
			string(config.VoiceHotkeyBackendX11),
			string(config.VoiceHotkeyBackendNone),
		},
		nil,
	)
	ff.daemonSocketPath = entryWithText(w.cfg.Daemon.SocketPath, "")
	ff.daemonGracePeriod = entryWithText(formatDuration(w.cfg.Daemon.ShutdownGracePeriod), "15s")
	ff.tempDir = entryWithText(w.cfg.TempDir, "")
	ff.convertTimeout = entryWithText(formatDuration(w.cfg.ConvertTimeout), "30s")
	ff.transcribeTimeout = entryWithText(formatDuration(w.cfg.TranscribeTimeout), "60s")
	ff.logLevel = widget.NewSelect(
		[]string{
			config.VoiceLogLevelDebug,
			config.VoiceLogLevelInfo,
			config.VoiceLogLevelWarn,
			config.VoiceLogLevelError,
		},
		nil,
	)
	ff.logTranscript = widget.NewCheck("", nil)
	ff.keepAudio = widget.NewCheck("", nil)
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

	ff.provider.SetSelected(w.cfg.Provider)
	ff.captureBackend.SetSelected(captureBackend)
	ff.outputMode.SetSelected(w.cfg.Output.Mode)
	ff.hotkeyMode.SetSelected(hotkeyMode)
	ff.hotkeyBackend.SetSelected(hotkeyBackend)
	ff.logLevel.SetSelected(logLevel)
	ff.whisperAutoDownload.SetChecked(w.cfg.GoWhisper.AutoDownload)
	ff.sttRetryEnabled.SetChecked(w.cfg.STTRetry.Enabled)
	ff.hotkeyEnabled.SetChecked(w.cfg.Hotkey.Enabled)
	ff.logTranscript.SetChecked(w.cfg.Privacy.LogTranscript)
	ff.keepAudio.SetChecked(w.cfg.Privacy.KeepAudio)
}

func (w *Window) save(ff *formFields) {
	w.applySTTFields(ff)
	w.applyCaptureFields(ff)
	w.applyOutputFields(ff)
	w.applyHotkeyFields(ff)
	w.applyDaemonFields(ff)

	w.cfg.Privacy.LogTranscript = ff.logTranscript.Checked
	w.cfg.Privacy.KeepAudio = ff.keepAudio.Checked

	w.persistSave()
}

// applySTTFields writes STT-related form values back to the config.
func (w *Window) applySTTFields(ff *formFields) {
	w.cfg.Provider = ff.provider.Selected
	w.cfg.Language = ff.language.Text
	w.cfg.GoWhisper.URL = ff.whisperURL.Text
	w.cfg.GoWhisper.Prefix = ff.whisperPrefix.Text
	w.cfg.GoWhisper.Model = ff.whisperModel.Text
	w.cfg.GoWhisper.Timeout = parseDuration(ff.whisperTimeout.Text)
	w.cfg.GoWhisper.AutoDownload = ff.whisperAutoDownload.Checked
	w.cfg.ModelPath = ff.modelPath.Text
	w.cfg.CloudProvider = ff.cloudProvider.Text
	w.cfg.CloudBaseURL = ff.cloudBaseURL.Text

	if ff.cloudAPIKey.Text != "" {
		w.cfg.CloudAPIKey = ff.cloudAPIKey.Text
	}

	w.cfg.STTRetry.Enabled = ff.sttRetryEnabled.Checked
	w.cfg.STTRetry.InitialDelay = parseDuration(ff.sttRetryInitDelay.Text)
	w.cfg.STTRetry.MaxDelay = parseDuration(ff.sttRetryMaxDelay.Text)
	w.cfg.STTRetry.MaxAttempts = parseIntEntry(ff.sttRetryMaxAttempts.Text)
}

// applyCaptureFields writes capture form values back to the config.
func (w *Window) applyCaptureFields(ff *formFields) {
	w.cfg.Capture.Backend = ff.captureBackend.Selected
	w.cfg.Capture.SampleRate = parseIntEntry(ff.captureSampleRate.Text)
	w.cfg.Capture.Channels = parseIntEntry(ff.captureChannels.Text)
	w.cfg.Capture.MaxDuration = parseDuration(ff.captureMaxDuration.Text)
}

// applyOutputFields writes output form values back to the config.
func (w *Window) applyOutputFields(ff *formFields) {
	w.cfg.Output.Mode = ff.outputMode.Selected
	w.cfg.Output.AutopasteCommand = ff.autopaste.Text
}

// applyHotkeyFields writes hotkey form values back to the config.
func (w *Window) applyHotkeyFields(ff *formFields) {
	w.cfg.Hotkey.Enabled = ff.hotkeyEnabled.Checked
	w.cfg.Hotkey.Key = ff.hotkeyKey.Text
	w.cfg.Hotkey.Modifiers = splitTrimmed(ff.hotkeyMods.Text)
	w.cfg.Hotkey.Mode = config.VoiceHotkeyMode(ff.hotkeyMode.Selected)
	w.cfg.Hotkey.Backend = config.VoiceHotkeyBackend(ff.hotkeyBackend.Selected)
}

// applyDaemonFields writes daemon form values back to the config.
func (w *Window) applyDaemonFields(ff *formFields) {
	w.cfg.Daemon.SocketPath = ff.daemonSocketPath.Text
	w.cfg.Daemon.ShutdownGracePeriod = parseDuration(ff.daemonGracePeriod.Text)
	w.cfg.TempDir = ff.tempDir.Text
	w.cfg.ConvertTimeout = parseDuration(ff.convertTimeout.Text)
	w.cfg.TranscribeTimeout = parseDuration(ff.transcribeTimeout.Text)
	w.cfg.LogLevel = ff.logLevel.Selected
}

// persistSave calls SaveConfig and shows the result dialog.
func (w *Window) persistSave() {
	if err := SaveConfig(w.cfg); err != nil {
		w.log.Warn("settings: save failed", slog.Any("err", err))

		if w.win != nil {
			dialog.ShowError(err, w.win)
		}

		return
	}

	w.log.Info("settings: config saved")

	if w.win != nil {
		const msg = "Настройки сохранены.\nПерезапустите демон чтобы изменения вступили в силу."

		dialog.ShowInformation("Сохранено", msg, w.win)
	}
}

func entryWithText(text, placeholder string) *widget.Entry {
	ee := widget.NewEntry()
	ee.SetText(text)
	ee.SetPlaceHolder(placeholder)

	return ee
}

// formatDuration formats a duration as a string; returns empty string for zero.
func formatDuration(dd time.Duration) string {
	if dd == 0 {
		return ""
	}

	return dd.String()
}

// parseDuration parses a duration string; returns zero on empty or parse error.
func parseDuration(ss string) time.Duration {
	if ss == "" {
		return 0
	}

	dd, err := time.ParseDuration(ss)
	if err != nil {
		return 0
	}

	return dd
}

// parseIntEntry parses an integer string; returns zero on empty or parse error.
func parseIntEntry(ss string) int {
	if ss == "" {
		return 0
	}

	nn, err := strconv.Atoi(ss)
	if err != nil {
		return 0
	}

	return nn
}

// intOrEmpty converts an int to its string representation; returns empty string for zero.
func intOrEmpty(nn int) string {
	if nn == 0 {
		return ""
	}

	return strconv.Itoa(nn)
}
