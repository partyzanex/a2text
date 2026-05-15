package settings

import (
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/partyzanex/a2text/internal/adapters/ui"
)

// helpIconSize is the visual diameter of the rendered help glyph. Kept
// small (~16dp on default Fyne scale) so the icon never visually
// outweighs the label it sits next to.
const helpIconSize float32 = 16

// helpIconGlyphScale shrinks the centred "?" glyph relative to the
// surrounding circle so it sits comfortably inside, with breathing
// room on every side.
const helpIconGlyphScale float32 = 0.7

// helpPopupWidth pins the tooltip popup to a comfortable reading
// width. Without an explicit width Fyne lays the wrapped label out at
// its MinSize — which for a Label with word-wrap collapses to the
// width of the longest single word, producing a one-character-wide
// scroll column. 360dp matches the spec's content density.
const helpPopupWidth float32 = 360

// helpPopupDelay throttles tooltip appearance — common UX convention
// across desktop toolkits prevents tooltips from flashing when the
// user only moves the cursor through the icon.
const helpPopupDelay = 250 * time.Millisecond

// helpIcon is a small "?" glyph that pops up tooltipText on hover. It
// implements fyne.Widget plus desktop.Hoverable, so the Fyne event loop
// drives the MouseIn / MouseOut callbacks. Production-quality tooltips
// have not landed in fyne yet (v2.7) — this is the smallest sufficient
// substitute for our settings form.
type helpIcon struct {
	widget.BaseWidget

	text     string
	popup    *widget.PopUp
	showWhen *time.Timer
}

func newHelpIcon(text string) *helpIcon {
	icon := &helpIcon{text: text}
	icon.ExtendBaseWidget(icon)

	return icon
}

// CreateRenderer paints a single circle with a centred "?" rune.
// Colour weight is deliberately low: the help icon is a hint, not a
// call-to-action, so we tint it with theme.ColorNameDisabled instead
// of the accent. The glyph stays in the theme background colour so it
// reads cleanly against the disabled-tinted circle.
func (h *helpIcon) CreateRenderer() fyne.WidgetRenderer {
	fill := theme.Color(theme.ColorNameDisabled)
	circle := canvas.NewCircle(fill)
	circle.StrokeColor = fill
	circle.StrokeWidth = 1

	glyph := canvas.NewText("?", theme.Color(theme.ColorNameBackground))
	glyph.Alignment = fyne.TextAlignCenter
	glyph.TextStyle = fyne.TextStyle{Bold: true}
	glyph.TextSize = helpIconSize * helpIconGlyphScale

	return &helpIconRenderer{icon: h, circle: circle, glyph: glyph}
}

// MinSize pins the icon to a fixed square; Fyne uses this both for
// layout and to bound the hit region for hover events.
func (h *helpIcon) MinSize() fyne.Size {
	return fyne.NewSize(helpIconSize, helpIconSize)
}

// MouseIn schedules a tooltip popup. Delayed so a fast cursor sweep
// through the icon does not flash a popup the user did not want.
func (h *helpIcon) MouseIn(*desktop.MouseEvent) {
	if h.text == "" {
		return
	}

	if h.showWhen != nil {
		h.showWhen.Stop()
	}

	h.showWhen = time.AfterFunc(helpPopupDelay, func() {
		fyne.Do(h.showPopup)
	})
}

// MouseMoved is required by desktop.Hoverable but has no behaviour for
// us — the popup position is anchored to the icon, not the cursor.
func (h *helpIcon) MouseMoved(*desktop.MouseEvent) {}

// MouseOut cancels a pending popup and hides any visible one.
func (h *helpIcon) MouseOut() {
	if h.showWhen != nil {
		h.showWhen.Stop()
		h.showWhen = nil
	}

	if h.popup != nil {
		h.popup.Hide()
	}
}

// showPopup constructs the popup if needed and anchors it just below
// the icon's current screen position. Called on the Fyne goroutine via
// fyne.Do from MouseIn.
//
// Layout rationale: a TextWrapWord Label only wraps when its container
// constrains the width. We pre-resize the label to helpPopupWidth so
// its internal layout breaks lines, then read back the resulting
// height to size the popup. Without this Fyne uses MinSize (the
// longest single word) and produces a tall one-character column.
func (h *helpIcon) showPopup() {
	canv := fyne.CurrentApp().Driver().CanvasForObject(h)
	if canv == nil {
		return
	}

	const (
		// padScale converts theme.Padding() into the inner-padding
		// budget the popup loses to NewPadded on each side.
		padScale float32 = 2
		// minPopupHeight stops the popup degenerating into a hair-line
		// strip when help text is shorter than one line (edge case).
		minPopupHeight float32 = 28
	)

	pad := theme.Padding()
	innerWidth := helpPopupWidth - pad*padScale

	label := widget.NewLabel(h.text)
	label.Wrapping = fyne.TextWrapWord
	// Force the label to wrap to innerWidth before we read its
	// height. The 1-px sentinel height is overridden by the wrapped
	// content's natural MinSize on the next layout pass.
	label.Resize(fyne.NewSize(innerWidth, 1))

	wrappedHeight := label.MinSize().Height
	if wrappedHeight < minPopupHeight {
		wrappedHeight = minPopupHeight
	}

	bg := canvas.NewRectangle(ui.Palette.Surface)
	bg.CornerRadius = ui.CardCornerRadius
	bg.StrokeColor = ui.Palette.Accent
	bg.StrokeWidth = 1

	content := container.NewStack(bg, container.NewPadded(label))

	// Hide any previous popup before replacing the reference — Fyne
	// keeps a hold on visible popups in the canvas overlay stack and
	// will not garbage-collect them just because the field that used
	// to point at them was overwritten. Without this, repeated hover
	// passes leak one popup each.
	if h.popup != nil {
		h.popup.Hide()
	}

	h.popup = widget.NewPopUp(content, canv)

	pos := fyne.CurrentApp().Driver().AbsolutePositionForObject(h)
	pos.Y += h.Size().Height

	h.popup.ShowAtPosition(pos)
	// Resize after Show so Fyne lays out the stack at the explicit
	// dimensions — the rectangle fills the full popup area and the
	// label sits centred with its wrapped content visible.
	h.popup.Resize(fyne.NewSize(helpPopupWidth, wrappedHeight+pad*padScale))
}

type helpIconRenderer struct {
	icon   *helpIcon
	circle *canvas.Circle
	glyph  *canvas.Text
}

// Layout keeps the rendered icon a perfect square even when Fyne hands
// the widget a non-square allocation (which the HBox parent inside a
// fixed-height row will do — row height is the label height, but our
// MinSize is helpIconSize×helpIconSize). Without this the circle
// stretches into a vertical oval.
func (r *helpIconRenderer) Layout(size fyne.Size) {
	const centerDivisor float32 = 2

	diameter := min(size.Width, size.Height)
	offsetX := (size.Width - diameter) / centerDivisor
	offsetY := (size.Height - diameter) / centerDivisor

	r.circle.Move(fyne.NewPos(offsetX, offsetY))
	r.circle.Resize(fyne.NewSize(diameter, diameter))
	r.glyph.Move(fyne.NewPos(offsetX, offsetY))
	r.glyph.Resize(fyne.NewSize(diameter, diameter))
}

func (r *helpIconRenderer) MinSize() fyne.Size {
	return fyne.NewSize(helpIconSize, helpIconSize)
}

func (r *helpIconRenderer) Objects() []fyne.CanvasObject {
	return []fyne.CanvasObject{r.circle, r.glyph}
}

func (r *helpIconRenderer) Destroy() {}

func (r *helpIconRenderer) Refresh() {
	fill := theme.Color(theme.ColorNameDisabled)
	r.circle.FillColor = fill
	r.circle.StrokeColor = fill
	r.glyph.Color = theme.Color(theme.ColorNameBackground)
	r.circle.Refresh()
	r.glyph.Refresh()
}
