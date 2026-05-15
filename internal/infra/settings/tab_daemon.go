package settings

import (
	"log/slog"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/partyzanex/a2text/internal/i18n"
	"github.com/partyzanex/a2text/internal/infra/config"
)

// buildDaemonTab assembles the merged "Демон" tab: output, IPC,
// working-files, logging and privacy. The output card lives here
// because routing the recognised text (stdout / clipboard / autopaste)
// is an operator concern, conceptually adjacent to log level and
// privacy toggles — keeping it next to the other daemon-wide knobs
// shortens the tab list to three meaningful groups.
func (w *Window) buildDaemonTab(ff *formFields) fyne.CanvasObject {
	output := rowsCard(i18n.T("card.output"),
		formRowWithHelp(i18n.T("label.output_mode"), "help.output_mode", ff.outputMode),
		formRowWithHelp(i18n.T("label.autopaste_command"), "help.autopaste_command", ff.autopaste),
		formRowWithHelp(i18n.T("label.restore_clipboard"), "help.restore_clipboard",
			leftAlign(ff.restoreClipboard)),
	)

	ipc := rowsCard(i18n.T("card.ipc"),
		formRowWithHelp(i18n.T("label.socket_path"), "help.socket_path", ff.daemonSocketPath),
		formRowValidatedWithHelp(i18n.T("label.grace_period"), "help.grace_period",
			ff.daemonGracePeriod, validateDuration),
	)

	files := rowsCard(i18n.T("card.files"),
		formRowWithHelp(i18n.T("label.temp_dir"), "help.temp_dir", ff.tempDir),
		formRowValidatedWithHelp(i18n.T("label.convert_timeout"), "help.convert_timeout",
			ff.convertTimeout, validateDuration),
		formRowValidatedWithHelp(i18n.T("label.transcribe_timeout"), "help.transcribe_timeout",
			ff.transcribeTimeout, validateDuration),
	)

	logging := rowsCard(i18n.T("card.logging"),
		formRowWithHelp(i18n.T("label.log_level"), "help.log_level", ff.logLevel),
	)

	privacy := rowsCard(i18n.T("card.privacy"),
		formRowWithHelp(i18n.T("label.log_transcript"), "help.log_transcript",
			leftAlign(ff.logTranscript)),
		formRowWithHelp(i18n.T("label.keep_audio"), "help.keep_audio",
			leftAlign(ff.keepAudio)),
		formRowWithHelp(i18n.T("label.keep_audio_dir"), "help.keep_audio_dir",
			w.buildKeepAudioDirField(ff)),
		formRowWithHelp(i18n.T("label.keep_audio_format"), "help.keep_audio_format", ff.keepAudioFormat),
	)

	return tabBody(output, ipc, files, logging, privacy)
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
	ff.autopaste = widget.NewSelect(
		[]string{
			config.VoiceAutopasteCommandAuto,
			config.VoiceAutopasteCommandUinput,
			config.VoiceAutopasteCommandWtype,
			config.VoiceAutopasteCommandYdotool,
			config.VoiceAutopasteCommandXdotool,
		},
		nil,
	)
	w.buildHotkeyFieldWidgets(ff)
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
	ff.restoreClipboard = widget.NewCheck("", nil)
	w.buildPrivacyFieldWidgets(ff)
}

// buildPrivacyFieldWidgets allocates the kept-audio widgets (toggle,
// directory entry, format select).
func (w *Window) buildPrivacyFieldWidgets(ff *formFields) {
	ff.keepAudio = widget.NewCheck("", nil)
	ff.keepAudioDir = entryWithText(w.cfg.Privacy.KeepAudioDir, "")
	ff.keepAudioFormat = widget.NewSelect(
		[]string{
			config.VoiceKeepAudioFormatWAV,
			config.VoiceKeepAudioFormatOGG,
		},
		nil,
	)
}

// buildKeepAudioDirField composes the "Папка для аудио" field: the
// Entry on the left for direct typing, plus a folder-icon button on
// the right that opens the native folder picker.
func (w *Window) buildKeepAudioDirField(ff *formFields) *fyne.Container {
	browse := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		w.openKeepAudioDirPicker(ff)
	})

	return container.NewBorder(nil, nil, nil, browse, ff.keepAudioDir)
}

// openKeepAudioDirPicker shows the native folder-open dialog.
func (w *Window) openKeepAudioDirPicker(ff *formFields) {
	if w.win == nil {
		return
	}

	dialog.ShowFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil {
			w.log.Warn("settings: folder picker failed", slog.Any("err", err))

			return
		}

		if uri == nil {
			// User cancelled — leave the existing value untouched.
			return
		}

		// fyne returns a URI like "file:///home/dmitry/recordings".
		// The daemon expects a plain filesystem path, so strip the
		// scheme via .Path().
		ff.keepAudioDir.SetText(uri.Path())
	}, w.win)
}

// applyOutputFields writes output form values back to the config.
func (w *Window) applyOutputFields(ff *formFields) {
	w.cfg.Output.Mode = ff.outputMode.Selected
	w.cfg.Output.AutopasteCommand = ff.autopaste.Selected
	w.cfg.Output.RestoreClipboard = ff.restoreClipboard.Checked
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
