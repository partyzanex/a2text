package settings

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/partyzanex/a2text/internal/i18n"
	"github.com/partyzanex/a2text/internal/adapters/ui"
)

// formRow builds a single settings row: a fixed-width, right-aligned label
// on the left and the editable widget filling the remaining horizontal
// space. The label column width comes from ui.LabelColumnWidth so every
// tab and card aligns identically.
//
// Replaces widget.NewForm — which does not expose label width or alignment
// — across all tabs. Keep it here (not in pkg/ui) because it is specific
// to the settings window's two-column layout.
func formRow(label string, field fyne.CanvasObject) *fyne.Container {
	return formRowWithHelp(label, "", field)
}

// formRowWithHelp is formRow with a non-empty help i18n key attached. The
// key is resolved at call time via i18n.T; passing "" suppresses the
// icon entirely. Use this whenever the field deserves a one-sentence
// description; use plain formRow for self-explanatory labels.
func formRowWithHelp(label, helpKey string, field fyne.CanvasObject) *fyne.Container {
	lblBox := buildLabelBox(label, helpKey)

	return container.NewBorder(nil, nil, lblBox, nil, field)
}

// buildLabelBox composes the fixed-width label column with an optional
// help-icon anchored to the right edge. The column width stays at
// ui.LabelColumnWidth regardless of whether a help icon is present —
// otherwise columns would jitter between rows with and without help.
//
// Layout details: the help icon (when present) is right-anchored via
// container.NewBorder so the label text — itself right-aligned via
// TextAlignTrailing — terminates flush against the icon's left edge.
// Using HBox here would left-pack the label and lose the right-edge
// alignment that the rest of the form relies on.
func buildLabelBox(label, helpKey string) fyne.CanvasObject {
	lbl := widget.NewLabelWithStyle(label, fyne.TextAlignTrailing, fyne.TextStyle{})

	var inner fyne.CanvasObject = lbl

	if helpKey != "" {
		helpText := i18n.T(helpKey)
		if helpText != "" && helpText != helpKey {
			inner = container.NewBorder(nil, nil, nil, newHelpIcon(helpText), lbl)
		}
	}

	return container.NewGridWrap(
		fyne.NewSize(ui.LabelColumnWidth, lbl.MinSize().Height),
		inner,
	)
}

// formRowSelectEntryValidatedWithHelp is the SelectEntry-aware twin of
// formRowValidatedWithHelp. SelectEntry is a composite (Entry +
// dropdown button); passing its embedded Entry to the regular helper
// orphans the dropdown and any later tap on it nil-panics inside
// NewPopUpMenu because the dropdown was never attached to a canvas.
// This helper places the full SelectEntry in the row and wires the
// validator + error caption around the embedded Entry directly.
func formRowSelectEntryValidatedWithHelp(
	label, helpKey string,
	selectEntry *widget.SelectEntry,
	validator fyne.StringValidator,
) *fyne.Container {
	selectEntry.Validator = validator

	errText := canvas.NewText("", errorTextColor())
	errText.TextSize = ui.SectionHeaderTextSize
	errText.Hide()

	selectEntry.SetOnValidationChanged(func(err error) {
		if err == nil {
			errText.Text = ""
			errText.Hide()
			errText.Refresh()

			return
		}

		errText.Text = err.Error()
		errText.Show()
		errText.Refresh()
	})

	if validator != nil {
		// Initial validation pass so a stale invalid value lights up
		// red immediately rather than waiting for the first keystroke.
		if validateErr := selectEntry.Validate(); validateErr != nil {
			errText.Text = validateErr.Error()
			errText.Show()
			errText.Refresh()
		}
	}

	field := container.NewVBox(selectEntry, errText)
	lblBox := buildLabelBox(label, helpKey)

	return container.NewBorder(nil, nil, lblBox, nil, field)
}

// formRowValidatedWithHelp is formRow plus inline validation feedback:
// the entry gets the supplied Validator (Fyne paints the entry red when
// validation fails), and a small error-coloured caption sits below the
// entry showing the validator's error message. When the validator
// passes, the caption is hidden and takes no vertical space.
func formRowValidatedWithHelp(
	label, helpKey string,
	entry *widget.Entry,
	validator fyne.StringValidator,
) *fyne.Container {
	entry.Validator = validator

	errText := canvas.NewText("", errorTextColor())
	errText.TextSize = ui.SectionHeaderTextSize
	errText.Hide()

	entry.SetOnValidationChanged(func(err error) {
		if err == nil {
			errText.Text = ""
			errText.Hide()
			errText.Refresh()

			return
		}

		errText.Text = err.Error()
		errText.Show()
		errText.Refresh()
	})

	if validator != nil {
		if err := entry.Validate(); err != nil {
			// Already surfaced inline via OnValidationChanged; nothing
			// more to do here. The branch exists so the error is not
			// dropped silently.
			_ = err
		}
	}

	field := container.NewVBox(entry, errText)
	lblBox := buildLabelBox(label, helpKey)

	return container.NewBorder(nil, nil, lblBox, nil, field)
}

// formRowValidatedWithTrailingButton is formRowValidatedWithHelp with
// an inline button glued to the right edge of the entry. Used by rows
// where the entry's value is the input to an action button (e.g. URL +
// "Проверить подключение"): putting the action on a separate row
// disconnects the visual cause-effect link and wastes vertical space.
//
// The optional caption (e.g. service-check status) is rendered below
// the entry alongside the validator error, sharing the same column
// width so it lines up under the field — not under the label.
func formRowValidatedWithTrailingButton(
	label, helpKey string,
	entry *widget.Entry,
	btn *widget.Button,
	caption *canvas.Text,
	validator fyne.StringValidator,
) *fyne.Container {
	entry.Validator = validator

	errText := canvas.NewText("", errorTextColor())
	errText.TextSize = ui.SectionHeaderTextSize
	errText.Hide()

	entry.SetOnValidationChanged(func(err error) {
		if err == nil {
			errText.Text = ""
			errText.Hide()
			errText.Refresh()

			return
		}

		errText.Text = err.Error()
		errText.Show()
		errText.Refresh()
	})

	if validator != nil {
		if err := entry.Validate(); err != nil {
			_ = err
		}
	}

	fieldRow := container.NewBorder(nil, nil, nil, btn, entry)

	fieldChildren := []fyne.CanvasObject{fieldRow, errText}
	if caption != nil {
		fieldChildren = append(fieldChildren, caption)
	}

	field := container.NewVBox(fieldChildren...)
	lblBox := buildLabelBox(label, helpKey)

	return container.NewBorder(nil, nil, lblBox, nil, field)
}

// leftAlign wraps obj in an HBox so its parent (typically a NewBorder
// "centre" cell) cannot stretch it horizontally. Used for compact
// widgets like widget.Check that look adrift when stretched to the
// full field-column width — the HBox keeps them pinned to the left
// edge of the value column at their natural MinSize.
func leftAlign(obj fyne.CanvasObject) fyne.CanvasObject {
	return container.NewHBox(obj)
}

// statusKind selects the colour family for a status caption built via
// newStatusText / setStatusText. Kept independent from the theme's
// validator colour so the success branch can stay green even if a
// future theme variant re-tints error red.
type statusKind int

const (
	statusKindNeutral statusKind = iota
	statusKindSuccess
	statusKindError
)

// newStatusText constructs an empty single-line text widget used for
// connection-check feedback. Caller sets text + colour via setStatusText.
func newStatusText() *canvas.Text {
	txt := canvas.NewText("", statusColorFor(statusKindNeutral))
	txt.TextSize = ui.SectionHeaderTextSize

	return txt
}

// setStatusText updates a canvas.Text in place: text content, colour
// chosen by kind, and a Refresh so Fyne repaints.
func setStatusText(target *canvas.Text, text string, kind statusKind) {
	target.Text = text
	target.Color = statusColorFor(kind)
	target.Refresh()
}

// statusColorFor maps a statusKind to a concrete colour.
func statusColorFor(kind statusKind) color.Color {
	const (
		fallbackChannel uint8 = 0x88
		alphaOpaque     uint8 = 0xff
	)

	fyneApp := fyne.CurrentApp()
	if fyneApp == nil {
		return color.NRGBA{
			R: fallbackChannel,
			G: fallbackChannel,
			B: fallbackChannel,
			A: alphaOpaque,
		}
	}

	themeRef := fyneApp.Settings().Theme()
	variant := fyneApp.Settings().ThemeVariant()

	switch kind {
	case statusKindSuccess:
		return themeRef.Color(theme.ColorNameSuccess, variant)
	case statusKindError:
		return themeRef.Color(theme.ColorNameError, variant)
	case statusKindNeutral:
		fallthrough
	default:
		return themeRef.Color(theme.ColorNameForeground, variant)
	}
}

// errorTextColor returns the foreground colour for inline validation
// captions.
func errorTextColor() color.Color {
	const (
		channelMax uint8 = 0xff
		channelMid uint8 = 0x55
	)

	currentApp := fyne.CurrentApp()
	if currentApp == nil {
		return color.NRGBA{R: channelMax, G: channelMid, B: channelMid, A: channelMax}
	}

	return currentApp.Settings().Theme().Color(
		theme.ColorNameError, currentApp.Settings().ThemeVariant(),
	)
}
