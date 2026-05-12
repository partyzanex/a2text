//go:build linux

package clipboard

import "github.com/partyzanex/a2text/pkg/session"

// DetectX11 reports whether the current session is X11.
func DetectX11() bool { return session.DetectX11() }

// DetectWayland reports whether the current session is Wayland.
func DetectWayland() bool { return session.DetectWayland() }
