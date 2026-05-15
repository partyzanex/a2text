package settings

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/partyzanex/a2text/internal/i18n"
	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/infra/sysd"
)

// buildDaemonTab assembles the "Демон" tab: IPC, working-files,
// logging and privacy. Technical infrastructure settings for the daemon.
func (w *Window) buildDaemonTab(ff *formFields) fyne.CanvasObject {
	ipc := rowsCard(i18n.T("card.ipc"),
		formRowWithHelp(i18n.T("label.socket_path"), "help.socket_path", ff.daemonSocketPath),
		formRowValidatedWithHelp(i18n.T("label.grace_period"), "help.grace_period",
			ff.daemonGracePeriod, validateDuration),
	)

	files := rowsCard(i18n.T("card.files"),
		formRowWithHelp(i18n.T("label.temp_dir"), "help.temp_dir",
			w.buildTempDirField(ff)),
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

	return tabBody(ipc, files, logging, privacy)
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
	ff.daemonSocketPath = entryWithText(w.cfg.Daemon.SocketPath, sysd.DefaultSocketPath())
	ff.daemonGracePeriod = entryWithText(formatDuration(w.cfg.Daemon.ShutdownGracePeriod), "15s")
	ff.tempDir = entryWithText(w.cfg.TempDir, "")
	ff.tempDirButton = widget.NewButton("", nil)
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

// buildTempDirField composes the "Временная папка" field: the Entry on the
// left for direct typing, plus a folder-icon button on the right that opens
// the native folder picker.
func (w *Window) buildTempDirField(ff *formFields) *fyne.Container {
	browse := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() {
		w.openTempDirPicker(ff)
	})

	return container.NewBorder(nil, nil, nil, browse, ff.tempDir)
}

// openTempDirPicker shows the native folder-open dialog for temp directory.
func (w *Window) openTempDirPicker(ff *formFields) {
	currentPath := strings.TrimSpace(ff.tempDir.Text)
	if currentPath == "" {
		currentPath = os.ExpandEnv("$HOME")
	}

	selectedPath, err := tryZenity(currentPath)
	if err == nil {
		ff.tempDir.SetText(selectedPath)

		return
	}

	selectedPath, err = tryKdialog(currentPath)
	if err == nil {
		ff.tempDir.SetText(selectedPath)

		return
	}

	w.openFyneFolderDialogForTempDir(ff)
}

// openFyneFolderDialogForTempDir shows the Fyne folder picker for temp directory.
func (w *Window) openFyneFolderDialogForTempDir(ff *formFields) {
	if w.win == nil {
		return
	}

	dirDialog := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil {
			w.log.Warn("settings: folder picker failed", slog.Any("err", err))

			return
		}

		if uri == nil {
			return
		}

		ff.tempDir.SetText(uri.Path())
	}, w.win)

	dirDialog.SetFilter(nil)
	dirDialog.Show()
}

// openKeepAudioDirPicker shows the native folder-open dialog.
func (w *Window) openKeepAudioDirPicker(ff *formFields) {
	currentPath := strings.TrimSpace(ff.keepAudioDir.Text)
	if currentPath == "" {
		currentPath = os.ExpandEnv("$HOME")
	}

	// Try system dialogs first (zenity for GNOME/GTK, kdialog for KDE).
	// Fall back to Fyne dialog if neither is available.
	selectedPath, err := tryZenity(currentPath)
	if err == nil {
		ff.keepAudioDir.SetText(selectedPath)

		return
	}

	selectedPath, err = tryKdialog(currentPath)
	if err == nil {
		ff.keepAudioDir.SetText(selectedPath)

		return
	}

	// Fall back to Fyne dialog if system dialogs unavailable.
	w.openFyneFolderDialog(ff)
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

// sanitizeDialogPath returns currentPath when safe to pass to a folder dialog,
// or the user's home directory as a safe fallback. Rejects values starting
// with "-" so they cannot be interpreted as flags by zenity/kdialog.
func sanitizeDialogPath(currentPath string) string {
	trimmed := strings.TrimSpace(currentPath)
	if trimmed == "" || strings.HasPrefix(trimmed, "-") {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}

		return "/"
	}

	return trimmed
}

func tryZenity(currentPath string) (string, error) {
	path := sanitizeDialogPath(currentPath)

	//nolint:gosec // path is sanitized via sanitizeDialogPath (rejects leading "-")
	cmd := exec.CommandContext(context.Background(), "zenity",
		"--file-selection", "--directory", "--filename="+path)

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("zenity failed: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

func tryKdialog(currentPath string) (string, error) {
	path := sanitizeDialogPath(currentPath)

	//nolint:gosec // path is sanitized via sanitizeDialogPath (rejects leading "-")
	cmd := exec.CommandContext(context.Background(), "kdialog", "--getexistingdirectory", path)

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("kdialog failed: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

func (w *Window) openFyneFolderDialog(ff *formFields) {
	if w.win == nil {
		return
	}

	dirDialog := dialog.NewFolderOpen(func(uri fyne.ListableURI, err error) {
		if err != nil {
			w.log.Warn("settings: folder picker failed", slog.Any("err", err))

			return
		}

		if uri == nil {
			return
		}

		ff.keepAudioDir.SetText(uri.Path())
	}, w.win)
	dirDialog.SetFilter(nil)
	dirDialog.Show()
}
