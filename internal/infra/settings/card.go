package settings

import (
	"image/color"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/partyzanex/a2text/internal/i18n"
	"github.com/partyzanex/a2text/internal/ui"
)

// rowsCard wraps a sequence of formRow containers in a visual card: a
// rounded Palette.Surface rectangle behind an icon + uppercase header
// row and padded content area. Used once per logical group ("Общее",
// "go-whisper", …) so visual grouping matches the underlying config
// sections.
//
// Replaces widget.NewCard — the stock card widget renders its title in
// the heading font (large, foreground colour) which is wrong here; the
// spec calls for small, accent-tinted, uppercased, tracked subheaders
// with a leading icon glyph.
//
// Icon resolves through cardIconFor(title) so call sites stay short
// and the icon-to-section mapping lives in one place.
func rowsCard(title string, rows ...*fyne.Container) fyne.CanvasObject {
	headerText := canvas.NewText(formatSectionHeader(title), theme.Color(theme.ColorNamePrimary))
	headerText.TextStyle = fyne.TextStyle{Bold: true}
	headerText.TextSize = ui.SectionHeaderTextSize

	iconResource := cardIconFor(title)
	icon := widget.NewIcon(iconResource)
	// Pin every section icon to a fixed 14×14 square. The previous
	// "scale relative to header text" heuristic produced 15.4dp from
	// the 11dp header — fine in isolation but visually inconsistent
	// next to the stock 16dp Fyne icons used elsewhere on the form.
	// 14dp matches the spec and sits cleanly with the small-caps
	// header text without dominating it.
	const (
		cardIconSide     float32 = 14
		cardIconRightGap float32 = 4
	)

	iconBox := container.NewGridWrap(fyne.NewSize(cardIconSide, cardIconSide), icon)
	gap := canvas.NewRectangle(color.Transparent)
	gap.SetMinSize(fyne.NewSize(cardIconRightGap, 1))

	header := container.NewHBox(iconBox, gap, headerText)

	rowObjs := make([]fyne.CanvasObject, 0, len(rows)+1)
	rowObjs = append(rowObjs, header)

	for _, row := range rows {
		rowObjs = append(rowObjs, row)
	}

	bg := canvas.NewRectangle(ui.Palette.Surface)
	bg.CornerRadius = ui.CardCornerRadius

	body := container.NewPadded(container.NewVBox(rowObjs...))

	return container.NewStack(bg, body)
}

// cardStack stacks card objects vertically with a tight fixed gap.
// The stock VBox layout uses theme padding (~10dp) and earlier we also
// inserted a Separator between every pair — together that produced a
// ~20–25dp visual gulf between cards. Switching to a custom-padded
// VBox layout with 8dp pins the inter-card distance regardless of
// theme padding, and the cards themselves provide enough visual
// grouping that no divider line is needed.
func cardStack(cards ...fyne.CanvasObject) *fyne.Container {
	const interCardGap float32 = 8

	if len(cards) == 0 {
		return container.New(layout.NewCustomPaddedVBoxLayout(interCardGap))
	}

	return container.New(layout.NewCustomPaddedVBoxLayout(interCardGap), cards...)
}

// tabBody wraps a cardStack in a horizontally-padded scroll container.
// Tabs use this instead of constructing the Scroll themselves so the
// 16dp horizontal padding (needed because Scroll reserves room for its
// vertical scrollbar on the right) is applied uniformly. The Scroll
// type is preserved so fitWindowToTab's downcast still succeeds.
func tabBody(cards ...fyne.CanvasObject) *container.Scroll {
	const horizontalPadding float32 = 16

	padded := container.New(
		layout.NewCustomPaddedLayout(0, 0, horizontalPadding, horizontalPadding),
		cardStack(cards...),
	)

	return container.NewScroll(padded)
}

// cardIconFor maps a localised section title to a meaningful Fyne
// built-in icon. Backed by a map so cyclop stays happy as the lookup
// grows. Unknown titles fall back to a generic settings icon so a
// missed key never breaks the layout.
//
//nolint:ireturn // fyne.Resource is the lookup table's value type
func cardIconFor(title string) fyne.Resource {
	if icon, ok := cardIconTable()[title]; ok {
		return icon
	}

	return theme.SettingsIcon()
}

// cardIconTable returns a fresh icon-lookup map. Rebuilt on each call
// because i18n.T() resolves against the current locale, which the user
// can switch at runtime — caching the map would freeze it to the
// language active at first call.
func cardIconTable() map[string]fyne.Resource {
	return map[string]fyne.Resource{
		i18n.T("card.general"):       theme.SettingsIcon(),
		i18n.T("card.go_whisper"):    theme.MediaPlayIcon(),
		i18n.T("card.whisper_cpp"):   theme.FolderOpenIcon(),
		i18n.T("card.cloud"):         theme.StorageIcon(),
		i18n.T("card.stt_retry"):     theme.ViewRefreshIcon(),
		i18n.T("card.capture_audio"): theme.VolumeUpIcon(),
		// Fyne does not ship a KeyboardIcon; ComputerIcon is the
		// closest semantic match for a system-wide global hotkey.
		i18n.T("card.hotkey"):  theme.ComputerIcon(),
		i18n.T("card.output"):  theme.ContentPasteIcon(),
		i18n.T("card.ipc"):     theme.ComputerIcon(),
		i18n.T("card.files"):   theme.FolderIcon(),
		i18n.T("card.logging"): theme.DocumentIcon(),
		i18n.T("card.privacy"): theme.VisibilityOffIcon(),
	}
}

// formatSectionHeader uppercases and letter-spaces a section title to
// match the spec's "subheading" look. Inserts a hair space between every
// rune — close enough to CSS letter-spacing without a custom renderer.
func formatSectionHeader(title string) string {
	upper := strings.ToUpper(title)
	runes := []rune(upper)

	if len(runes) <= 1 {
		return upper
	}

	var b strings.Builder

	b.Grow(len(upper) + (len(runes)-1)*len(ui.SectionHeaderTracking))

	for i, r := range runes {
		if i > 0 {
			b.WriteString(ui.SectionHeaderTracking)
		}

		b.WriteRune(r)
	}

	return b.String()
}
