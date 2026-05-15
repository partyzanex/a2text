package settings

import (
	"log/slog"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/widget"

	"github.com/partyzanex/a2text/internal/i18n"
	"github.com/partyzanex/a2text/internal/infra/config"
)

// buildCaptureHotkeyTab assembles the merged "Захват + Хоткей" tab —
// both settings groups relate to how a recording cycle is initiated and
// what audio is captured, so they sit naturally side-by-side.
func (w *Window) buildCaptureHotkeyTab(ff *formFields) fyne.CanvasObject {
	capture := rowsCard(i18n.T("card.capture_audio"),
		formRowWithHelp(i18n.T("label.backend"), "help.capture_backend", ff.captureBackend),
		formRowValidatedWithHelp(i18n.T("label.sample_rate"), "help.sample_rate",
			ff.captureSampleRate, validatePositiveInt),
		formRowValidatedWithHelp(i18n.T("label.channels"), "help.channels",
			ff.captureChannels, validatePositiveInt),
		formRowValidatedWithHelp(i18n.T("label.max_duration"), "help.max_duration",
			ff.captureMaxDuration, validateDuration),
		formRowValidatedWithHelp(i18n.T("label.silence_threshold"), "help.silence_threshold",
			ff.captureSilenceThreshold, validateNonPositiveFloat),
	)

	hotkey := rowsCard(i18n.T("card.hotkey"),
		formRowWithHelp(i18n.T("label.hotkey_enabled"), "help.hotkey_enabled",
			leftAlign(ff.hotkeyEnabled)),
		formRowWithHelp(i18n.T("label.hotkey_binding"), "help.hotkey_binding", ff.hotkeyBinding),
		formRowWithHelp(i18n.T("label.mode"), "help.hotkey_mode", ff.hotkeyMode),
		formRowWithHelp(i18n.T("label.backend"), "help.hotkey_backend", ff.hotkeyBackend),
	)

	return tabBody(capture, hotkey)
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

// buildHotkeyFieldWidgets allocates hotkey-row widgets (enable check,
// capture button, mode + backend selects).
func (w *Window) buildHotkeyFieldWidgets(ff *formFields) {
	ff.hotkeyEnabled = widget.NewCheck("", nil)
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
	ff.hotkeyBackend = widget.NewSelect(
		[]string{
			string(config.VoiceHotkeyBackendAuto),
			string(config.VoiceHotkeyBackendNone),
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
	w.cfg.Hotkey.Enabled = ff.hotkeyEnabled.Checked
	w.cfg.Hotkey.Key = ff.hotkeyBinding.Key()
	w.cfg.Hotkey.Modifiers = ff.hotkeyBinding.Modifiers()
	w.cfg.Hotkey.Mode = config.VoiceHotkeyMode(ff.hotkeyMode.Selected)
	w.cfg.Hotkey.Backend = config.VoiceHotkeyBackend(ff.hotkeyBackend.Selected)
}
