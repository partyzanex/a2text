//go:build linux

package hotkey

// portal_linux.go implements Listener on top of the
// xdg-desktop-portal `org.freedesktop.portal.GlobalShortcuts` D-Bus interface.
//
// Compared to the X11 (XGrabKey) backend this one:
//
//   - works on Wayland sessions (GNOME 45+, KDE 5.27+, wlroots with
//     xdg-desktop-portal-wlr) — no compositor-specific code;
//   - delivers BOTH press (Activated) and release (Deactivated) signals,
//     making hold-mode push-to-talk possible without DE shortcuts;
//   - does not require CGo or libX11.
//
// The implementation follows the Request/Response async pattern documented
// at https://flatpak.github.io/xdg-desktop-portal/docs/ : every Method call
// returns a Request object path, and the actual result arrives later on a
// `Response` signal emitted on that path. We subscribe to those signals
// before calling the method, await them with a context-bound timeout, then
// unsubscribe.
//
// Lifecycle: NewPortalHotkey constructs the backend with handler + key
// config. Listen() opens the D-Bus session bus, runs CreateSession +
// BindShortcuts, subscribes to Activated/Deactivated, dispatches events
// to the handler until ctx is cancelled or Stop is called.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// Portal D-Bus identifiers — pinned in const so a typo blows up at
// compile time rather than silently mismatching a signal subscription.
const (
	portalBusName    = "org.freedesktop.portal.Desktop"
	portalObjectPath = "/org/freedesktop/portal/desktop"
	portalShortcutsI = "org.freedesktop.portal.GlobalShortcuts"
	portalRequestI   = "org.freedesktop.portal.Request"

	signalChannelBufSize         = 16
	responseSignalChannelBufSize = 4

	// shortcutID is our internal identifier for the one shortcut we bind.
	// The portal uses it to tag Activated/Deactivated signals — multiple
	// bindings on the same session disambiguate via this string.
	shortcutID = "voice-toggle"

	// portalReplyTimeout caps how long we wait for a Response signal
	// from CreateSession / BindShortcuts. Real portals reply in <100ms
	// for already-granted shortcuts; 10s budgets headroom for the
	// permission-prompt path where the compositor blocks the request
	// until the operator clicks Allow/Deny. Not configurable on
	// purpose: there is no scenario where shorter is correct (would
	// false-positive into "portal hung" during a normal prompt), and
	// longer is useless because waitAndToggle / ctx already cap the
	// outer call site.
	portalReplyTimeout = 10 * time.Second
)

// portalShortcut represents one shortcut entry in the D-Bus BindShortcuts call.
// Field order is wire-format: D-Bus serializes positionally into the tuple type
// `a(sa{sv})` that the portal expects. Reordering for alignment would flip the
// signature to `a(a{sv}s)` and cause a type mismatch.
type portalShortcut struct {
	ID    string
	Props map[string]dbus.Variant
}

// ErrPortalUnavailable is returned by NewPortalHotkey when the portal
// interface is not registered on the session bus. Distinct from generic
// dial errors so callers can fall back to a different backend.
var ErrPortalUnavailable = errors.New("hotkey: org.freedesktop.portal.GlobalShortcuts not available on session bus")

// PortalHotkey is the xdg-desktop-portal-backed implementation of
// Listener.
type PortalHotkey struct {
	handler Handler
	log     *slog.Logger

	key       string
	modifiers []string

	mu      sync.Mutex
	stopCh  chan struct{}
	stopped bool
}

// NewPortalHotkey constructs the portal-backed listener. Validation is
// minimal: handler must be non-nil, key must be non-empty. The portal
// itself rejects malformed accelerator strings later, in BindShortcuts.
func NewPortalHotkey(handler Handler, key string, modifiers []string, log *slog.Logger) (*PortalHotkey, error) {
	if handler == nil {
		return nil, ErrHandlerNil
	}

	if key == "" {
		return nil, ErrKeyEmpty
	}

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &PortalHotkey{
		handler:   handler,
		log:       log,
		key:       key,
		modifiers: append([]string(nil), modifiers...),
		stopCh:    make(chan struct{}),
	}, nil
}

// Listen runs the portal session until ctx is cancelled or Stop is called.
// Blocking. Returns nil on clean shutdown; any portal-side error is
// wrapped and returned. Only one Listen call per instance is allowed.
func (h *PortalHotkey) Listen(ctx context.Context) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("hotkey: connect session bus: %w", err)
	}

	defer func() {
		if connErr := conn.Close(); connErr != nil {
			_ = connErr
		}
	}()

	if !portalAvailable(conn) {
		return ErrPortalUnavailable
	}

	sessionHandle, err := h.createSession(ctx, conn)
	if err != nil {
		return fmt.Errorf("hotkey: create portal session: %w", err)
	}

	if bindErr := h.bindShortcut(ctx, conn, sessionHandle); bindErr != nil {
		return fmt.Errorf("hotkey: bind portal shortcut: %w", bindErr)
	}

	h.log.InfoContext(ctx, "voice: portal hotkey bound",
		slog.String("session", string(sessionHandle)),
		slog.String("key", h.key),
		slog.Any("modifiers", h.modifiers),
	)

	return h.runSignalLoop(ctx, conn, sessionHandle)
}

// Stop releases the listener. Safe to call multiple times.
//
// Portal sessions are released by closing the D-Bus connection (which
// happens in Listen's defer when stopCh fires). We do not call the
// portal's CloseSession explicitly: the portal cleans up sessions
// whose owning peer disconnects, so close-on-defer is sufficient and
// avoids one round-trip.
func (h *PortalHotkey) Stop() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.stopped {
		return nil
	}

	h.stopped = true
	close(h.stopCh)

	return nil
}

// portalAvailable probes for org.freedesktop.portal.GlobalShortcuts via
// introspection. Cheap (~1ms): we read the interface list and check.
// Used by depcheck and as a self-guard inside Listen.
func portalAvailable(conn *dbus.Conn) bool {
	obj := conn.Object(portalBusName, portalObjectPath)

	var xml string
	if err := obj.Call("org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&xml); err != nil {
		return false
	}

	return strings.Contains(xml, portalShortcutsI)
}

// IsPortalAvailable opens a fresh session bus connection, probes the
// GlobalShortcuts interface, and reports whether the daemon could realistically
// register a hotkey there. Used by depcheck so daemon startup logs a clear
// "portal missing — your distro needs xdg-desktop-portal-gnome 45+" line
// before the listener even tries to start.
//
// Returns false on dial failure (no D-Bus session) AND on missing interface;
// the caller cannot distinguish, but for depcheck purposes "not usable" is
// the only fact that matters.
func IsPortalAvailable() bool {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false
	}

	defer func() {
		if connErr := conn.Close(); connErr != nil {
			_ = connErr
		}
	}()

	return portalAvailable(conn)
}

// createSession invokes GlobalShortcuts.CreateSession and waits for the
// Response signal on the returned request path. Returns the actual
// session handle on success.
func (h *PortalHotkey) createSession(ctx context.Context, conn *dbus.Conn) (dbus.ObjectPath, error) {
	obj := conn.Object(portalBusName, portalObjectPath)

	options := map[string]dbus.Variant{
		"handle_token":         dbus.MakeVariant(handleToken("a2text")),
		"session_handle_token": dbus.MakeVariant(handleToken("a2text_session")),
	}

	var requestPath dbus.ObjectPath
	if err := obj.CallWithContext(
		ctx,
		portalShortcutsI+".CreateSession",
		0,
		options,
	).Store(&requestPath); err != nil {
		return "", fmt.Errorf("CreateSession call: %w", err)
	}

	results, err := awaitResponse(ctx, conn, requestPath, h.log)
	if err != nil {
		return "", fmt.Errorf("CreateSession response: %w", err)
	}

	handleVar, ok := results["session_handle"]
	if !ok {
		return "", errors.New("CreateSession response missing session_handle")
	}

	var handle string

	if err := handleVar.Store(&handle); err != nil {
		return "", fmt.Errorf("CreateSession session_handle type: %w", err)
	}

	return dbus.ObjectPath(handle), nil
}

// bindShortcut registers our single (key, modifiers) combination under
// shortcutID. The first call on a fresh session triggers a one-shot
// permission prompt in the compositor; subsequent runs are silent.
func (h *PortalHotkey) bindShortcut(ctx context.Context, conn *dbus.Conn, sessionHandle dbus.ObjectPath) error {
	obj := conn.Object(portalBusName, portalObjectPath)

	trigger := formatAccelerator(h.key, h.modifiers)

	shortcutProps := map[string]dbus.Variant{
		"description":       dbus.MakeVariant("a2text voice toggle"),
		"preferred_trigger": dbus.MakeVariant(trigger),
	}

	shortcuts := []portalShortcut{
		{ID: shortcutID, Props: shortcutProps},
	}

	options := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(handleToken("a2text_bind")),
	}

	var requestPath dbus.ObjectPath
	if err := obj.CallWithContext(
		ctx,
		portalShortcutsI+".BindShortcuts",
		0,
		sessionHandle,
		shortcuts,
		"", // parent_window — empty is fine for headless apps
		options,
	).Store(&requestPath); err != nil {
		return fmt.Errorf("BindShortcuts call: %w", err)
	}

	if _, err := awaitResponse(ctx, conn, requestPath, h.log); err != nil {
		return fmt.Errorf("BindShortcuts response: %w", err)
	}

	return nil
}

// runSignalLoop subscribes to Activated/Deactivated and dispatches the
// matching HotkeyEvent to the handler until ctx cancels or Stop is called.
//
// AddMatchSignal narrows the filter so this connection only buffers events
// for our session; without the filter, all D-Bus traffic on the bus would
// route through the channel and Activated parsing would crash on unrelated
// signals.
func (h *PortalHotkey) runSignalLoop(ctx context.Context, conn *dbus.Conn, sessionHandle dbus.ObjectPath) error {
	matchOpts := []dbus.MatchOption{
		dbus.WithMatchInterface(portalShortcutsI),
		dbus.WithMatchObjectPath(portalObjectPath),
	}

	if err := conn.AddMatchSignal(matchOpts...); err != nil {
		return fmt.Errorf("AddMatchSignal: %w", err)
	}

	defer func() {
		if err := conn.RemoveMatchSignal(matchOpts...); err != nil {
			_ = err
		}
	}()

	signals := make(chan *dbus.Signal, signalChannelBufSize)
	conn.Signal(signals)

	defer conn.RemoveSignal(signals)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-h.stopCh:
			return nil
		case sig, ok := <-signals:
			if !ok {
				return errors.New("portal signal channel closed unexpectedly")
			}

			h.dispatchSignal(ctx, sig, sessionHandle)
		}
	}
}

// dispatchSignal routes one portal signal to the user's handler. Matches
// only Activated/Deactivated on our session — anything else (stray signals
// from concurrent portal users, ShortcutsChanged, malformed bodies) is
// silently ignored.
//
// Fire-and-forget: Handler returns no error, and the daemon explicitly
// expects sub-millisecond handler dispatch (state-machine Apply is cheap;
// long work is goroutine-dispatched by the daemon itself). Surfacing
// failures here would have no recipient — by the time we are inside the
// signal loop the per-IPC reply path is long gone.
func (h *PortalHotkey) dispatchSignal(ctx context.Context, sig *dbus.Signal, sessionHandle dbus.ObjectPath) {
	if len(sig.Body) < 2 { //nolint:mnd // signal carries (session, shortcut_id, timestamp, options)
		return
	}

	sessPath, ok := sig.Body[0].(dbus.ObjectPath)
	if !ok || sessPath != sessionHandle {
		return
	}

	shortID, ok := sig.Body[1].(string)
	if !ok || shortID != shortcutID {
		return
	}

	switch sig.Name {
	case portalShortcutsI + ".Activated":
		h.handler(ctx, Press)
	case portalShortcutsI + ".Deactivated":
		h.handler(ctx, Release)
	default:
		// ShortcutsChanged et al — not relevant to single-binding daemons.
	}
}

// awaitResponse subscribes to the per-Request Response signal, blocks
// until it fires (or ctx times out), and returns the results dict.
//
// The portal Response payload is (uint32 status, dict results). status:
//
//	0 = success, 1 = cancelled by user, 2 = other error.
//
// Anything non-zero is treated as a failure with the raw code in the
// returned error so logs are unambiguous.
func awaitResponse(
	ctx context.Context, conn *dbus.Conn, requestPath dbus.ObjectPath, log *slog.Logger,
) (map[string]dbus.Variant, error) {
	matchOpts := []dbus.MatchOption{
		dbus.WithMatchInterface(portalRequestI),
		dbus.WithMatchMember("Response"),
		dbus.WithMatchObjectPath(requestPath),
	}

	if err := conn.AddMatchSignal(matchOpts...); err != nil {
		return nil, fmt.Errorf("subscribe Response: %w", err)
	}

	defer func() {
		if err := conn.RemoveMatchSignal(matchOpts...); err != nil {
			_ = err
		}
	}()

	// Fresh signal channel per request to avoid cross-talk between
	// concurrent portal calls — Conn.Signal fanouts every received
	// signal to all registered channels, and a shared channel would
	// race two awaitResponse callers reading each other's Response.
	signals := make(chan *dbus.Signal, responseSignalChannelBufSize)
	conn.Signal(signals)

	defer conn.RemoveSignal(signals)

	timeoutCtx, cancel := context.WithTimeout(ctx, portalReplyTimeout)
	defer cancel()

	for {
		select {
		case <-timeoutCtx.Done():
			return nil, fmt.Errorf("portal Response timeout: %w", timeoutCtx.Err())
		case sig := <-signals:
			if sig == nil || sig.Path != requestPath {
				continue
			}

			if sig.Name != portalRequestI+".Response" {
				continue
			}

			return parseResponse(sig, log)
		}
	}
}

// parseResponse unpacks a Response signal body into (status, results).
// Status != 0 → error with the raw code surfaced so journal grep yields
// "code=1" for user-cancelled vs "code=2" for compositor-side failure.
func parseResponse(sig *dbus.Signal, log *slog.Logger) (map[string]dbus.Variant, error) {
	if len(sig.Body) < 2 { //nolint:mnd // (status, results)
		return nil, fmt.Errorf("portal Response: body too short (%d args)", len(sig.Body))
	}

	status, ok := sig.Body[0].(uint32)
	if !ok {
		return nil, fmt.Errorf("portal Response: status is %T, want uint32", sig.Body[0])
	}

	switch status {
	case 0:
		// success — fall through to results parsing
	case 1:
		return nil, fmt.Errorf("portal Response code=%d: %w", status, ErrPortalPermissionDenied)
	default:
		return nil, fmt.Errorf("portal Response code=%d: %w", status, ErrPortalBindRejected)
	}

	results, ok := sig.Body[1].(map[string]dbus.Variant)
	if !ok {
		log.Warn("voice: portal Response results not a dict; returning empty",
			slog.String("type", fmt.Sprintf("%T", sig.Body[1])),
		)

		return map[string]dbus.Variant{}, nil
	}

	return results, nil
}

// formatAccelerator turns ("D", ["super", "shift"]) into the GNOME-style
// accelerator string "SUPER+SHIFT+d" the portal expects in
// preferred_trigger. The portal is case-insensitive on modifiers but
// accepts only specific names; we normalise + filter unknown ones.
//
// Modifier name mapping (case-insensitive, whitespace-trimmed):
//
//	super, mod4, win, meta → SUPER
//	alt,   mod1            → ALT
//	ctrl,  control         → CTRL
//	shift                  → SHIFT
//	anything else          → silently dropped
//
// Aliases exist so config copied from xbindkeys / sxhkd / GNOME tutorials
// "just works" without forcing the operator to memorise our canonical
// vocabulary. Unknown modifiers are dropped (not errored) so a typo
// degrades to a plain-key binding the user will notice immediately
// rather than a daemon-startup failure.
const (
	modifierSuper = "super"
	modifierCtrl  = "ctrl"
	modifierShift = "shift"
)

func formatAccelerator(key string, modifiers []string) string {
	parts := make([]string, 0, len(modifiers)+1)

	for _, mod := range modifiers {
		switch strings.ToLower(strings.TrimSpace(mod)) {
		case modifierSuper, "mod4", "win", "meta":
			parts = append(parts, "SUPER")
		case "alt", "mod1":
			parts = append(parts, "ALT")
		case modifierCtrl, "control":
			parts = append(parts, "CTRL")
		case modifierShift:
			parts = append(parts, "SHIFT")
		}
	}

	parts = append(parts, strings.ToLower(strings.TrimSpace(key)))

	return strings.Join(parts, "+")
}

// handleToken builds a unique-per-call token that the portal uses to
// derive a Request object path. The portal expects only [A-Za-z0-9_]
// characters; we just append a nanosecond timestamp to a stable prefix.
//
// Uniqueness: practically unique per call — collision requires two
// invocations within the same nanosecond on a single CPU, which Go's
// runtime does not produce on modern hardware (UnixNano resolution is
// closer to 30-100ns wall clock per call). A collision would manifest
// as two Request objects sharing one path and the second Response
// silently shadowing the first; if this ever becomes an issue (e.g.
// some niche kernel returning coarser clocks), append a crypto/rand
// suffix.
func handleToken(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

// Ensure PortalHotkey satisfies Listener at compile time.
var _ Listener = (*PortalHotkey)(nil)
