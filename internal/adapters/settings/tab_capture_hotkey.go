package settings

import (
	"log/slog"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"github.com/partyzanex/a2text/internal/i18n"
	"github.com/partyzanex/a2text/internal/infra/config"
)

// buildCaptureHotkeyTab assembles the "Захват + Хоткей + Вывод" tab —
// capture settings define what audio is recorded, hotkey controls how
// the cycle is initiated, and output determines where the result goes.
// Together they form the complete recording-to-delivery pipeline.
func (w *Window) buildCaptureHotkeyTab(ff *formFields) fyne.CanvasObject {
	capture := rowsCard(i18n.T(i18n.KeyCardCaptureAudio),
		formRowWithHelp(i18n.T(i18n.KeyLabelBackend), "help.capture_backend", ff.captureBackend),
		formRowValidatedWithHelp(i18n.T(i18n.KeyLabelSampleRate), "help.sample_rate",
			ff.captureSampleRate, validatePositiveInt),
		formRowValidatedWithHelp(i18n.T(i18n.KeyLabelChannels), "help.channels",
			ff.captureChannels, validatePositiveInt),
		formRowValidatedWithHelp(i18n.T(i18n.KeyLabelMaxDuration), "help.max_duration",
			ff.captureMaxDuration, validateDuration),
		formRowValidatedWithHelp(i18n.T(i18n.KeyLabelSilenceThreshold), "help.silence_threshold",
			ff.captureSilenceThreshold, validateNonPositiveFloat),
	)

	hotkey := rowsCard(i18n.T(i18n.KeyCardHotkey),
		formRowWithHelp(i18n.T(i18n.KeyLabelHotkeyBinding), "help.hotkey_binding", ff.hotkeyBinding),
		formRowWithHelp(i18n.T(i18n.KeyLabelMode), "help.hotkey_mode", ff.hotkeyMode),
	)

	output := rowsCard(i18n.T(i18n.KeyCardOutput),
		formRowWithHelp(i18n.T(i18n.KeyLabelOutputMode), "help.output_mode", ff.outputMode),
		formRowWithHelp(i18n.T(i18n.KeyLabelAutopasteCommand), "help.autopaste_command", ff.autopaste),
		formRowWithHelp(i18n.T(i18n.KeyLabelRestoreClipboard), "help.restore_clipboard",
			leftAlign(ff.restoreClipboard)),
	)

	return tabBody(capture, hotkey, output)
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
	ff.captureSilenceThreshold = entryWithText(
		floatOrEmpty(w.cfg.Capture.SilenceThresholdDBFS),
		"-45.0",
	)
}

// buildHotkeyFieldWidgets allocates hotkey-row widgets (capture button +
// mode select). The evdev backend is implicit and the listener is always
// active, so no backend select or enable check is exposed.
func (w *Window) buildHotkeyFieldWidgets(ff *formFields) {
	ff.hotkeyBinding = newHotkeyCaptureButton(
		w.cfg.Hotkey.Key, w.cfg.Hotkey.Modifiers, w.log,
		func(key string, mods []string) {
			w.log.Debug("hotkey-capture: binding committed",
				slog.String("key", key),
				slog.Any("modifiers", mods),
			)

			if w.saver != nil {
				w.saver.Schedule()
			}
		},
	)
	ff.hotkeyMode = widget.NewSelect(
		[]string{
			string(config.VoiceHotkeyModeToggle),
			string(config.VoiceHotkeyModeHold),
		},
		nil,
	)
}

// applyCaptureFields writes capture form values back to the config.
func (w *Window) applyCaptureFields(ff *formFields) {
	w.cfg.Capture.Backend = ff.captureBackend.Selected
	w.cfg.Capture.SampleRate = parseIntEntry(ff.captureSampleRate.Text)
	w.cfg.Capture.Channels = parseIntEntry(ff.captureChannels.Text)
	w.cfg.Capture.MaxDuration = parseDuration(ff.captureMaxDuration.Text)
	w.cfg.Capture.SilenceThresholdDBFS = parseFloatEntry(ff.captureSilenceThreshold.Text)
}

// applyHotkeyFields writes hotkey form values back to the config.
func (w *Window) applyHotkeyFields(ff *formFields) {
	w.cfg.Hotkey.Key = ff.hotkeyBinding.Key()
	w.cfg.Hotkey.Modifiers = ff.hotkeyBinding.Modifiers()
	w.cfg.Hotkey.Mode = config.VoiceHotkeyMode(ff.hotkeyMode.Selected)
}
