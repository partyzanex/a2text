// Package clipboard adapts external clipboard utilities (wl-copy on
// Wayland, xclip on X11 — added in stage I.4) to the voice.Output
// interface used by the daemon.
//
// Selection happens at construction time based on $XDG_SESSION_TYPE.
package clipboard
