package settings

import (
	"context"
	"errors"
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
)

// buildDaemonTab assembles the "Процесс" tab: autostart, IPC,
// working-files, logging and privacy. Technical infrastructure
// settings for the daemon. The file is still named tab_daemon.go for
// git-history continuity — the in-app label has changed but the
// surface this builds has not.
func (w *Window) buildDaemonTab(ff *formFields) fyne.CanvasObject {
	autostartCard := rowsCard(i18n.T(i18n.KeyCardAutostart),
		checkboxRow(ff.autostart, i18n.T(i18n.KeyLabelAutostartEnabled), "help.autostart_enabled"),
	)

	daemonLifecycle := rowsCard(i18n.T(i18n.KeyCardShutdown),
		formRowValidatedWithHelp(i18n.T(i18n.KeyLabelGracePeriod), "help.grace_period",
			ff.daemonGracePeriod, validateDuration),
	)

	files := rowsCard(i18n.T(i18n.KeyCardFiles),
		formRowWithHelp(i18n.T(i18n.KeyLabelTempDir), "help.temp_dir",
			w.buildTempDirField(ff)),
		formRowValidatedWithHelp(i18n.T(i18n.KeyLabelConvertTimeout), "help.convert_timeout",
			ff.convertTimeout, validateDuration),
		formRowValidatedWithHelp(i18n.T(i18n.KeyLabelTranscribeTimeout), "help.transcribe_timeout",
			ff.transcribeTimeout, validateDuration),
	)

	logging := rowsCard(i18n.T(i18n.KeyCardLogging),
		formRowWithHelp(i18n.T(i18n.KeyLabelLogLevel), "help.log_level", ff.logLevel),
	)

	privacy := rowsCard(i18n.T(i18n.KeyCardPrivacy),
		formRowWithHelp(i18n.T(i18n.KeyLabelLogTranscript), "help.log_transcript",
			leftAlign(ff.logTranscript)),
		formRowWithHelp(i18n.T(i18n.KeyLabelKeepAudio), "help.keep_audio",
			leftAlign(ff.keepAudio)),
		formRowWithHelp(i18n.T(i18n.KeyLabelKeepAudioDir), "help.keep_audio_dir",
			w.buildKeepAudioDirField(ff)),
		formRowWithHelp(i18n.T(i18n.KeyLabelKeepAudioFormat), "help.keep_audio_format", ff.keepAudioFormat),
	)

	return tabBody(autostartCard, daemonLifecycle, files, logging, privacy)
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
	ff.autostart = widget.NewCheck("", nil)
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

	selectedPath, err := pickFolder(currentPath)
	if errors.Is(err, errDialogCancelled) {
		return
	}

	if err != nil {
		w.openFyneFolderDialogForTempDir(ff)

		return
	}

	ff.tempDir.SetText(selectedPath)
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

	selectedPath, err := pickFolder(currentPath)
	if errors.Is(err, errDialogCancelled) {
		return
	}

	if err != nil {
		w.openFyneFolderDialog(ff)

		return
	}

	ff.keepAudioDir.SetText(selectedPath)
}

// pickFolder runs the first available native folder picker (zenity →
// kdialog) and returns its selection. Cancellation (Escape/Cancel)
// surfaces as errDialogCancelled so the caller stops — it must NOT
// open a second dialog on top. Absence of every native backend
// surfaces as errDialogUnavailable so the caller can fall back to the
// Fyne picker.
func pickFolder(currentPath string) (string, error) {
	selected, err := tryZenity(currentPath)
	if err == nil {
		return selected, nil
	}

	if errors.Is(err, errDialogCancelled) {
		return "", errDialogCancelled
	}

	selected, err = tryKdialog(currentPath)
	if err == nil {
		return selected, nil
	}

	if errors.Is(err, errDialogCancelled) {
		return "", errDialogCancelled
	}

	return "", errDialogUnavailable
}

// applyOutputFields writes output form values back to the config.
func (w *Window) applyOutputFields(ff *formFields) {
	w.cfg.Output.Mode = ff.outputMode.Selected
	w.cfg.Output.AutopasteCommand = ff.autopaste.Selected
	w.cfg.Output.RestoreClipboard = ff.restoreClipboard.Checked
}

// applyDaemonFields writes daemon form values back to the config.
func (w *Window) applyDaemonFields(ff *formFields) {
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

// errDialogUnavailable means the binary is not installed / not in PATH —
// callers should fall through to the next backend (kdialog → Fyne).
// errDialogCancelled means the binary ran but the user dismissed the
// picker (Escape, Cancel, window close). Callers must treat this as a
// terminal "no choice made" and NOT open a second dialog on top.
var (
	errDialogUnavailable = errors.New("settings: folder dialog backend unavailable")
	errDialogCancelled   = errors.New("settings: folder dialog cancelled by user")
)

func tryZenity(currentPath string) (string, error) {
	if _, err := exec.LookPath("zenity"); err != nil {
		return "", errDialogUnavailable
	}

	path := sanitizeDialogPath(currentPath)

	//nolint:gosec // path is sanitized via sanitizeDialogPath (rejects leading "-")
	cmd := exec.CommandContext(context.Background(), "zenity",
		"--file-selection", "--directory", "--filename="+path)

	output, err := cmd.Output()
	if err != nil {
		// Non-zero exit (Cancel/Escape) → user cancelled. Distinguish
		// from "binary disappeared mid-call" via *exec.ExitError: the
		// process ran to completion, just chose a non-zero status.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", errDialogCancelled
		}

		return "", errDialogUnavailable
	}

	selected := strings.TrimSpace(string(output))
	if selected == "" {
		return "", errDialogCancelled
	}

	return selected, nil
}

func tryKdialog(currentPath string) (string, error) {
	if _, err := exec.LookPath("kdialog"); err != nil {
		return "", errDialogUnavailable
	}

	path := sanitizeDialogPath(currentPath)

	//nolint:gosec // path is sanitized via sanitizeDialogPath (rejects leading "-")
	cmd := exec.CommandContext(context.Background(), "kdialog", "--getexistingdirectory", path)

	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", errDialogCancelled
		}

		return "", errDialogUnavailable
	}

	selected := strings.TrimSpace(string(output))
	if selected == "" {
		return "", errDialogCancelled
	}

	return selected, nil
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
