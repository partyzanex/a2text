// Package hotkey provides global hotkey registration for the dictation daemon.
//
// On X11 this uses XGrabKey via CGo (libX11). On Wayland the user binds
// a2text to a DE custom shortcut — no in-process hotkey needed.
package hotkey

import "errors"

// ErrHandlerNil is returned by NewX11Hotkey when the handler argument is nil.
var ErrHandlerNil = errors.New("hotkey: handler is nil")

// ErrKeyEmpty is returned by NewX11Hotkey when the key argument is empty.
var ErrKeyEmpty = errors.New("hotkey: key is empty")

// ErrX11DisplayUnavailable is returned by Listen when XOpenDisplay fails.
// Typically means DISPLAY is not set or Xorg/XWayland is not running.
var ErrX11DisplayUnavailable = errors.New("hotkey: X11 display unavailable")

// ErrX11InvalidKeySym is returned by Listen when the key string is not a
// valid X11 keysym name (XStringToKeysym returned NoSymbol).
var ErrX11InvalidKeySym = errors.New("hotkey: invalid X11 keysym")

// ErrX11InvalidKeyCode is returned by Listen when the keysym has no keycode
// in the current X11 keymap (XKeysymToKeycode returned 0).
var ErrX11InvalidKeyCode = errors.New("hotkey: X11 keysym has no keycode in current keymap")

// ErrPortalPermissionDenied is returned by Listen when the xdg-desktop-portal
// returns Response code=1, meaning the user dismissed or denied the permission
// prompt. Callers should treat this as graceful degradation (no hotkey) rather
// than a daemon error.
var ErrPortalPermissionDenied = errors.New("hotkey: portal permission denied by user")

// ErrPortalBindRejected is returned by Listen when the portal returns
// Response code=2: compositor does not support GlobalShortcuts, the
// shortcut is already bound by another client, or the portal backend
// has no policy to grant it. Callers should treat this as graceful
// degradation and suggest using a DE-level shortcut instead.
var ErrPortalBindRejected = errors.New("hotkey: portal rejected shortcut binding")
