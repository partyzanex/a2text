// Package tray provides an optional system-tray icon that reflects the
// voice daemon state. It registers an SNI (StatusNotifierItem) service on
// the D-Bus session bus and requires a compatible host — e.g. the
// ubuntu-appindicators GNOME extension or KDE Plasma. When no graphical
// session or no StatusNotifierWatcher is found, Run returns immediately so
// the daemon starts cleanly in headless environments.
package tray

import (
	"context"
	"image"
	"image/color"
	"log/slog"
	"math"
	"os"
	"sync"

	"fyne.io/systray"
	"github.com/godbus/dbus/v5"

	"github.com/partyzanex/a2text/internal/domain"
	"github.com/partyzanex/a2text/internal/i18n"
)

// Icon geometry and rendering constants.
const (
	iconSizeConst = 22
	iconCenter    = float64(iconSizeConst) / 2.0
	iconRadius    = 9.0
	iconHalfPixel = 0.5
	iconMaxAlpha  = 255.0
)

// Icon RGB colour constants for each state.
const (
	colorRecordingR    uint8 = 204
	colorRecordingG    uint8 = 0
	colorRecordingB    uint8 = 0
	colorTranscribingR uint8 = 204
	colorTranscribingG uint8 = 136
	colorTranscribingB uint8 = 0
	colorDeliveringR   uint8 = 0
	colorDeliveringG   uint8 = 153
	colorDeliveringB   uint8 = 51
	colorErrorR        uint8 = 204
	colorErrorG        uint8 = 51
	colorErrorB        uint8 = 0
	colorShutdownR     uint8 = 64
	colorShutdownG     uint8 = 64
	colorShutdownB     uint8 = 64
	colorIdleR         uint8 = 128
	colorIdleG         uint8 = 128
	colorIdleB         uint8 = 128
)

// stateChBufTray is the capacity of the tray's internal state-change channel.
const stateChBufTray = 16

// Tray is the system-tray component. Create with New, optionally wire an
// external state channel with SetInputCh or call SetState directly, then
// launch Run in a goroutine.
type Tray struct {
	log        *slog.Logger
	externalCh <-chan domain.State
	stateIn    chan domain.State
	toggleFn   func()
	settingsFn func()
	quitFn     func()
	icons      map[domain.State][]byte
	iconOnce   sync.Once

	// menuMu protects the menu-item handles which are created inside
	// systray.Run on first start and updated by RefreshLabels when the
	// UI language changes at runtime.
	menuMu    sync.Mutex
	mToggle   *systray.MenuItem
	mSettings *systray.MenuItem
	mQuit     *systray.MenuItem
}

// New returns a Tray wired with the given callbacks.
// toggleFn is called when the user clicks the toggle menu item.
// settingsFn is called when the user clicks "Настройки".
// quitFn is called when the user clicks "Выход".
// Any callback may be nil; nil callbacks are replaced with no-ops.
func New(log *slog.Logger, toggleFn, settingsFn, quitFn func()) *Tray {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	noop := func() {}

	if toggleFn == nil {
		toggleFn = noop
	}

	if settingsFn == nil {
		settingsFn = noop
	}

	if quitFn == nil {
		quitFn = noop
	}

	return &Tray{
		stateIn:    make(chan domain.State, stateChBufTray),
		log:        log,
		toggleFn:   toggleFn,
		settingsFn: settingsFn,
		quitFn:     quitFn,
	}
}

// SetInputCh wires an external channel from which state transitions are read
// (e.g. the daemon's notify channel). The relay goroutine starts in Run so
// that context cancellation stops it cleanly. Must be called before Run.
func (tr *Tray) SetInputCh(ch <-chan domain.State) {
	tr.externalCh = ch
}

// SetState updates the tray icon and tooltip to reflect state. Recognised
// values are "inactive", "recording", and "transcribing"; anything else is
// treated as "inactive". The call is non-blocking: a lagging tray never
// stalls callers.
func (tr *Tray) SetState(state string) {
	st := stateFromString(state)

	select {
	case tr.stateIn <- st:
	default:
	}
}

// Run starts the tray icon and blocks until ctx is cancelled or Quit is
// triggered from the menu. Returns immediately (no error) when:
//   - $DISPLAY and $WAYLAND_DISPLAY are both unset (headless environment);
//   - org.kde.StatusNotifierWatcher is absent on the session bus.
func (tr *Tray) Run(ctx context.Context) {
	if !hasDisplay() {
		tr.log.Info("voice: tray disabled — no graphical session detected")

		return
	}

	if !sniWatcherPresent() {
		tr.log.Info("voice: tray disabled — no StatusNotifierWatcher on session bus")

		return
	}

	if tr.externalCh != nil {
		go tr.relayStates(ctx)
	}

	systray.Run(func() {
		systray.SetIcon(tr.iconFor(domain.StateIdle))
		systray.SetTooltip("a2text: idle")

		tr.menuMu.Lock()
		tr.mToggle = systray.AddMenuItem(i18n.T(i18n.KeyTrayToggle), "")
		tr.mSettings = systray.AddMenuItem(i18n.T(i18n.KeyTraySettings), "")

		systray.AddSeparator()

		tr.mQuit = systray.AddMenuItem(i18n.T(i18n.KeyTrayQuit), "")
		tr.menuMu.Unlock()

		// Re-resolve menu labels every time the UI language changes
		// at runtime. Registered after the items exist so the very
		// first invocation has handles to update.
		i18n.OnLocaleChange(tr.refreshLabels)

		go tr.loop(ctx, tr.mToggle, tr.mSettings, tr.mQuit)
	}, func() {})
}

// refreshLabels re-resolves every menu item's title through i18n.T.
// Used as the i18n locale-change listener so a runtime language
// switch propagates to the long-lived tray menu without restart.
func (tr *Tray) refreshLabels() {
	tr.menuMu.Lock()
	defer tr.menuMu.Unlock()

	if tr.mToggle != nil {
		tr.mToggle.SetTitle(i18n.T(i18n.KeyTrayToggle))
	}

	if tr.mSettings != nil {
		tr.mSettings.SetTitle(i18n.T(i18n.KeyTraySettings))
	}

	if tr.mQuit != nil {
		tr.mQuit.SetTitle(i18n.T(i18n.KeyTrayQuit))
	}
}

// relayStates forwards state transitions from the external channel to the
// internal stateIn channel. Exits when ctx is done or the external channel
// is closed.
func (tr *Tray) relayStates(ctx context.Context) {
	ch := tr.externalCh

	for {
		select {
		case <-ctx.Done():
			return

		case st, ok := <-ch:
			if !ok {
				return
			}

			select {
			case tr.stateIn <- st:
			default:
			}
		}
	}
}

// loop is the tray event loop. It multiplexes context cancellation, incoming
// state changes, and menu-item clicks.
func (tr *Tray) loop(ctx context.Context, mToggle, mSettings, mQuit *systray.MenuItem) {
	for {
		select {
		case <-ctx.Done():
			systray.Quit()

			return

		case st, ok := <-tr.stateIn:
			if !tr.applyState(st, ok) {
				systray.Quit()

				return
			}

		case <-mToggle.ClickedCh:
			tr.toggleFn()

		case <-mSettings.ClickedCh:
			go tr.settingsFn()

		case <-mQuit.ClickedCh:
			tr.quitFn()
		}
	}
}

// applyState updates the tray icon and tooltip to reflect the new state.
// Returns false when the channel was closed (caller should quit).
func (tr *Tray) applyState(st domain.State, ok bool) bool {
	if !ok {
		return false
	}

	systray.SetIcon(tr.iconFor(st))
	systray.SetTooltip("a2text: " + string(st))

	return true
}

// stateFromString maps external string state names to domain.State values.
// "inactive" and unrecognised values map to StateIdle.
func stateFromString(state string) domain.State {
	switch state {
	case "recording":
		return domain.StateRecording

	case "transcribing":
		return domain.StateTranscribing

	default:
		return domain.StateIdle
	}
}

// hasDisplay reports whether a graphical session is available.
func hasDisplay() bool {
	return os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DISPLAY") != ""
}

// sniWatcherPresent probes the session bus for org.kde.StatusNotifierWatcher.
// Without an active watcher the SNI icon would never be shown.
func sniWatcherPresent() bool {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false
	}

	defer conn.Close() //nolint:errcheck // probe connection; error on close is not actionable

	var has bool

	callErr := conn.BusObject().Call(
		"org.freedesktop.DBus.NameHasOwner", 0,
		"org.kde.StatusNotifierWatcher",
	).Store(&has)

	return callErr == nil && has
}

// colorForState maps each domain state to an RGB colour tuple for the icon.
func colorForState(state domain.State) (red, green, blue uint8) {
	switch state {
	case domain.StateRecording:
		return colorRecordingR, colorRecordingG, colorRecordingB

	case domain.StateTranscribing:
		return colorTranscribingR, colorTranscribingG, colorTranscribingB

	case domain.StateDelivering:
		return colorDeliveringR, colorDeliveringG, colorDeliveringB

	case domain.StateError:
		return colorErrorR, colorErrorG, colorErrorB

	case domain.StateShuttingDown:
		return colorShutdownR, colorShutdownG, colorShutdownB

	default: // idle and any future states
		return colorIdleR, colorIdleG, colorIdleB
	}
}

// circleIcon renders a 22×22 NRGBA PNG with a filled, anti-aliased circle.
func circleIcon(red, green, blue uint8) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, iconSizeConst, iconSizeConst))

	for iy := range iconSizeConst {
		for ix := range iconSizeConst {
			dx := float64(ix) + iconHalfPixel - iconCenter
			dy := float64(iy) + iconHalfPixel - iconCenter
			dist := math.Sqrt(dx*dx + dy*dy)

			if dist > iconRadius {
				continue
			}

			frac := iconRadius - dist
			if frac > 1 {
				frac = 1
			}

			alpha := uint8(iconMaxAlpha * frac)
			img.SetNRGBA(ix, iy, color.NRGBA{R: red, G: green, B: blue, A: alpha})
		}
	}

	return encodePNG(img)
}
