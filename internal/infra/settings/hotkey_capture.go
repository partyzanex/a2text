package settings

import (
	"log/slog"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/widget"
)

// hotkeyCaptureButton is a single-row replacement for the typed
// "Клавиша" + "Модификаторы" entries. Click → press a key combo →
// widget stores the matching key name plus the modifier list. Escape
// cancels without changing anything.
//
// Implementation:
//   - Focusable: when the user clicks the inner button we grab keyboard
//     focus so subsequent key events route through TypedKey / KeyDown.
//   - desktop.Keyable: tracks pure-modifier presses via KeyDown/KeyUp
//     because fyne.KeyEvent does not carry a modifier mask. The set of
//     currently-held modifiers is recorded; when a non-modifier key
//     arrives it is combined with that set and committed.
//   - fyne.Tappable: clicking enters capture mode.
//
// The inner button serves only as a visual surface — we forward Tapped
// to startCapture and do not rely on its own OnTapped wiring.
type hotkeyCaptureButton struct {
	widget.BaseWidget

	key       string
	modifiers []string
	capturing bool
	heldMods  map[fyne.KeyName]struct{}
	btn       *widget.Button
	log       *slog.Logger

	onChanged func(key string, modifiers []string)
}

// Compile-time assertions: hotkeyCaptureButton must satisfy the
// interfaces the desktop driver uses to dispatch keyboard events.
// Failing the cast at runtime would only show up as silently-ignored
// presses, which is a much harder bug to spot than a build error.
var (
	_ fyne.Focusable    = (*hotkeyCaptureButton)(nil)
	_ fyne.Tappable     = (*hotkeyCaptureButton)(nil)
	_ desktop.Keyable   = (*hotkeyCaptureButton)(nil)
	_ fyne.CanvasObject = (*hotkeyCaptureButton)(nil)
)

// newHotkeyCaptureButton constructs a capture widget pre-populated
// with key + mods. onChanged is called after every successful capture.
func newHotkeyCaptureButton(
	key string,
	modifiers []string,
	log *slog.Logger,
	onChanged func(key string, modifiers []string),
) *hotkeyCaptureButton {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	widgetInst := &hotkeyCaptureButton{
		key:       key,
		modifiers: normaliseModifiers(modifiers),
		heldMods:  make(map[fyne.KeyName]struct{}),
		log:       log,
		onChanged: onChanged,
	}
	// Wire the inner button's OnTapped to our entry point. Setting it
	// to nil would let clicks land on the button, which would consume
	// the event without firing the outer widget's Tappable.Tapped —
	// the visible "nothing happens on click" bug.
	widgetInst.btn = widget.NewButton(widgetInst.idleLabel(), widgetInst.onButtonTapped)
	// Left-align the button's label so the captured binding text
	// terminates flush with the form's field column, matching the
	// flush-left look of every Entry/Select on the same tab. The
	// default centre alignment made the "F4" caption float in the
	// middle of the row and read as detached from its label.
	widgetInst.btn.Alignment = widget.ButtonAlignLeading
	widgetInst.ExtendBaseWidget(widgetInst)

	return widgetInst
}

// Key returns the captured Fyne KeyName ("R", "F4", "Slash", …).
func (h *hotkeyCaptureButton) Key() string {
	return h.key
}

// Modifiers returns a copy of the modifier list using the same
// lowercase strings the config uses ("super", "ctrl", "alt", "shift").
func (h *hotkeyCaptureButton) Modifiers() []string {
	out := make([]string, len(h.modifiers))
	copy(out, h.modifiers)

	return out
}

// SetBinding replaces the displayed binding without going through
// capture mode. Used by setFieldValues at window construction time.
func (h *hotkeyCaptureButton) SetBinding(key string, modifiers []string) {
	h.key = key
	h.modifiers = normaliseModifiers(modifiers)
	h.btn.SetText(h.idleLabel())
}

// CreateRenderer paints the inner button. SimpleRenderer keeps layout
// trivial: the hotkeyCaptureButton occupies exactly the button's box.
func (h *hotkeyCaptureButton) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(h.btn)
}

// --- fyne.Tappable -----------------------------------------------------

// Tapped is here so the widget satisfies fyne.Tappable, but in
// practice clicks land on the inner *widget.Button and reach us via
// onButtonTapped. This method covers the edge case where Fyne would
// route a tap straight at the outer widget.
func (h *hotkeyCaptureButton) Tapped(_ *fyne.PointEvent) {
	h.onButtonTapped()
}

// --- fyne.Focusable ----------------------------------------------------

// FocusGained is called when the canvas routes focus to this widget.
// We use Tapped() as the explicit entry to capture mode, so this
// method only resets the held-modifiers tracker — losing and regaining
// focus shouldn't carry stale state.
func (h *hotkeyCaptureButton) FocusGained() {
	h.log.Debug("hotkey-capture: focus gained")
	h.heldMods = make(map[fyne.KeyName]struct{})
}

// FocusLost cancels an in-flight capture. The user clicked away, or
// the dialog moved focus elsewhere — finalise without mutating the
// binding so the previous value survives.
func (h *hotkeyCaptureButton) FocusLost() {
	h.log.Debug("hotkey-capture: focus lost",
		slog.Bool("was_capturing", h.capturing),
	)

	if h.capturing {
		h.finishCapture()
	}
}

// TypedRune ignores text input — we only care about raw key events.
func (h *hotkeyCaptureButton) TypedRune(_ rune) {}

// TypedKey handles Escape (cancel) and forwards non-modifier keys to
// the commit path when no modifier-down has been seen yet (some
// desktops route lone-key presses through TypedKey only).
func (h *hotkeyCaptureButton) TypedKey(ev *fyne.KeyEvent) {
	h.log.Debug("hotkey-capture: TypedKey",
		slog.String("name", string(ev.Name)),
		slog.Bool("capturing", h.capturing),
		slog.Int("held_count", len(h.heldMods)),
	)

	if !h.capturing {
		return
	}

	if ev.Name == fyne.KeyEscape {
		h.finishCapture()

		return
	}

	if isPureModifierKey(ev.Name) {
		return
	}

	if len(h.heldMods) == 0 {
		h.commit(string(ev.Name), nil)
	}
}

// --- desktop.Keyable ---------------------------------------------------

// KeyDown tracks modifier presses and finalises the capture when a
// non-modifier key arrives. fyne.KeyEvent does not include a modifier
// mask, so we maintain heldMods ourselves.
func (h *hotkeyCaptureButton) KeyDown(ev *fyne.KeyEvent) {
	h.log.Debug("hotkey-capture: KeyDown",
		slog.String("name", string(ev.Name)),
		slog.Bool("capturing", h.capturing),
		slog.Bool("is_modifier", isPureModifierKey(ev.Name)),
	)

	if !h.capturing {
		return
	}

	if ev.Name == fyne.KeyEscape {
		h.finishCapture()

		return
	}

	if isPureModifierKey(ev.Name) {
		h.heldMods[ev.Name] = struct{}{}

		return
	}

	mods := modifiersFromHeld(h.heldMods)
	h.commit(string(ev.Name), mods)
}

// KeyUp clears the matching held-modifier flag so the user can swap
// modifiers mid-capture without committing a stale combo.
func (h *hotkeyCaptureButton) KeyUp(ev *fyne.KeyEvent) {
	if !h.capturing {
		return
	}

	if isPureModifierKey(ev.Name) {
		delete(h.heldMods, ev.Name)
	}
}

// --- internal ---------------------------------------------------------

// onButtonTapped is the inner-Button OnTapped handler. Logs and
// delegates to startCapture (which focuses the widget and updates the
// label). Kept separate so the button.OnTapped wiring stays trivial.
func (h *hotkeyCaptureButton) onButtonTapped() {
	h.log.Debug("hotkey-capture: button tapped",
		slog.String("current_key", h.key),
		slog.Any("current_mods", h.modifiers),
	)

	canv := fyne.CurrentApp().Driver().CanvasForObject(h)
	if canv == nil {
		h.log.Warn("hotkey-capture: no canvas for widget; cannot capture")

		return
	}

	canv.Focus(h)
	h.startCapture()
}

func (h *hotkeyCaptureButton) startCapture() {
	if h.capturing {
		h.log.Debug("hotkey-capture: startCapture called while already capturing")

		return
	}

	h.capturing = true
	h.heldMods = make(map[fyne.KeyName]struct{})
	h.btn.SetText(captureLabel)
	h.log.Debug("hotkey-capture: capture started")
}

func (h *hotkeyCaptureButton) finishCapture() {
	if !h.capturing {
		return
	}

	h.capturing = false
	h.heldMods = make(map[fyne.KeyName]struct{})
	h.btn.SetText(h.idleLabel())

	if canv := fyne.CurrentApp().Driver().CanvasForObject(h); canv != nil {
		canv.Unfocus()
	}
}

func (h *hotkeyCaptureButton) commit(key string, modifiers []string) {
	h.key = key
	h.modifiers = normaliseModifiers(modifiers)

	if h.onChanged != nil {
		h.onChanged(h.key, h.Modifiers())
	}

	h.finishCapture()
}

// idleLabel formats the binding for display. "Не назначено" when
// neither key nor modifiers are set so the button is never blank.
func (h *hotkeyCaptureButton) idleLabel() string {
	parts := make([]string, 0, len(h.modifiers)+1)
	for _, mod := range h.modifiers {
		parts = append(parts, capitalise(mod))
	}

	if h.key == "" {
		if len(parts) == 0 {
			return notAssignedLabel
		}

		return strings.Join(parts, "+")
	}

	parts = append(parts, h.key)

	return strings.Join(parts, "+")
}

// captureLabel and notAssignedLabel are package-level so a future
// i18n integration can swap them in one place.
//
//nolint:gochecknoglobals // user-visible labels, stable through window lifetime
var (
	captureLabel     = "Нажмите клавишу… (Esc — отмена)"
	notAssignedLabel = "Не назначено"
)

// modifiersFromHeld converts the held-down set into the lowercase
// modifier list the config uses. Sorted for stable YAML output.
func modifiersFromHeld(held map[fyne.KeyName]struct{}) []string {
	var mods []string

	if _, ok := held[desktop.KeyShiftLeft]; ok {
		mods = append(mods, "shift")
	} else if _, ok := held[desktop.KeyShiftRight]; ok {
		mods = append(mods, "shift")
	}

	if _, ok := held[desktop.KeyControlLeft]; ok {
		mods = append(mods, "ctrl")
	} else if _, ok := held[desktop.KeyControlRight]; ok {
		mods = append(mods, "ctrl")
	}

	if _, ok := held[desktop.KeyAltLeft]; ok {
		mods = append(mods, "alt")
	} else if _, ok := held[desktop.KeyAltRight]; ok {
		mods = append(mods, "alt")
	}

	if _, ok := held[desktop.KeySuperLeft]; ok {
		mods = append(mods, "super")
	} else if _, ok := held[desktop.KeySuperRight]; ok {
		mods = append(mods, "super")
	}

	sort.Strings(mods)

	return mods
}

// normaliseModifiers lowercases, dedups, and sorts the incoming list
// so two equivalent bindings round-trip identically.
func normaliseModifiers(in []string) []string {
	seen := make(map[string]struct{}, len(in))

	out := make([]string, 0, len(in))
	for _, raw := range in {
		mod := strings.ToLower(strings.TrimSpace(raw))
		if mod == "" {
			continue
		}

		if _, dup := seen[mod]; dup {
			continue
		}

		seen[mod] = struct{}{}

		out = append(out, mod)
	}

	sort.Strings(out)

	return out
}

// isPureModifierKey reports whether ev.Name is one of the dedicated
// modifier keys.
func isPureModifierKey(name fyne.KeyName) bool {
	//nolint:exhaustive // only modifier keys matter; others fall through
	switch name {
	case desktop.KeyShiftLeft, desktop.KeyShiftRight,
		desktop.KeyControlLeft, desktop.KeyControlRight,
		desktop.KeyAltLeft, desktop.KeyAltRight,
		desktop.KeySuperLeft, desktop.KeySuperRight,
		desktop.KeyMenu:
		return true
	}

	return false
}

// capitalise returns s with its first rune upper-cased. Stdlib does
// not ship a "title-case first letter only" helper without pulling in
// golang.org/x/text — this short routine suffices for the fixed set of
// modifier names ("ctrl" → "Ctrl") used by idleLabel.
func capitalise(text string) string {
	if text == "" {
		return text
	}

	first, size := utf8.DecodeRuneInString(text)
	if first == utf8.RuneError {
		return text
	}

	return string(unicode.ToUpper(first)) + text[size:]
}
