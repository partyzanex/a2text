// Package session provides helpers for detecting the current graphical session type
// from environment variables. It has no build constraints and works on any platform.
package session

import (
	"os"
	"strings"
)

// DetectX11 reports whether the current session is X11.
// XDG_SESSION_TYPE is matched case-insensitively for robustness in
// containers and test environments.
func DetectX11() bool {
	if sessionType() == "x11" {
		return true
	}

	return os.Getenv("DISPLAY") != "" && os.Getenv("WAYLAND_DISPLAY") == ""
}

// DetectWayland reports whether the current session is Wayland.
// XDG_SESSION_TYPE is matched case-insensitively for robustness in
// containers and test environments.
func DetectWayland() bool {
	if sessionType() == "wayland" {
		return true
	}

	return os.Getenv("WAYLAND_DISPLAY") != ""
}

// DetectSessionType returns "wayland", "x11", or "" if the session type is unknown.
func DetectSessionType() string {
	switch {
	case DetectWayland():
		return "wayland"
	case DetectX11():
		return "x11"
	default:
		return ""
	}
}

// sessionType returns XDG_SESSION_TYPE normalised to lowercase with surrounding
// whitespace removed.
func sessionType() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv("XDG_SESSION_TYPE")))
}
