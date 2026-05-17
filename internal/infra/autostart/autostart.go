// Package autostart manages the XDG autostart entry that launches the
// a2text daemon on graphical login. Targets `$XDG_CONFIG_HOME/autostart/`
// (or `~/.config/autostart/` when XDG_CONFIG_HOME is unset) so the file
// is per-user and never needs root.
//
// XDG autostart was chosen over systemd --user because it runs inside
// the graphical session with DISPLAY/WAYLAND_DISPLAY/DBUS already
// populated — the tray and autopaste backends need that, and a system
// systemd unit cannot provide it without contortions.
//
// State source of truth = presence of the .desktop file. There is no
// separate flag in the YAML config: enabling/disabling autostart writes
// or removes the file, and IsEnabled simply stats it. This keeps the
// settings checkbox in sync with whatever the user does outside the app
// (e.g., deleting the file by hand or via GNOME Tweaks).
package autostart

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DesktopID matches the StartupWMClass and the installed system
	// .desktop file basename — keeping the per-user autostart entry on
	// the same ID lets GNOME group the running window with both.
	DesktopID = "io.github.partyzanex.a2text"

	desktopFileName = DesktopID + ".desktop"

	// desktopFilePerm and desktopDirPerm are intentionally world-readable
	// (no secrets in the file) but writable only by the owner.
	desktopFilePerm os.FileMode = 0o644
	desktopDirPerm  os.FileMode = 0o755
)

// desktopTemplate is the XDG .desktop body written when autostart is
// enabled. Exec is parameterised; everything else mirrors the system
// .desktop entry shipped by `make install` so GNOME shows the same
// icon/name in the dock whether the daemon was launched manually or by
// autostart.
//
// X-GNOME-Autostart-Delay=5 keeps a2text from racing the tray host
// (ubuntu-appindicators) and the compositor — without it the
// StatusNotifierItem registration sometimes lands before the watcher
// is ready and the tray icon never appears.
const desktopTemplate = `[Desktop Entry]
Type=Application
Name=a2text
Comment=Voice-to-text dictation daemon (autostart entry)
Exec=%s --daemon
Icon=%s
StartupWMClass=a2text
X-GNOME-Autostart-enabled=true
X-GNOME-Autostart-Delay=5
NoDisplay=false
Categories=Utility;Accessibility;
`

// Path returns the absolute path of the autostart .desktop file for the
// current user, honouring XDG_CONFIG_HOME. Does not create anything.
func Path() (string, error) {
	dir, err := autostartDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, desktopFileName), nil
}

// IsEnabled reports whether the autostart entry exists. Returns false
// without an error when the file is simply absent — the caller is the
// settings UI, and a missing entry is the steady-state "off" position,
// not a failure.
func IsEnabled() (bool, error) {
	path, err := Path()
	if err != nil {
		return false, err
	}

	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}

		return false, fmt.Errorf("autostart: stat %s: %w", path, err)
	}

	return !info.IsDir() && info.Size() > 0, nil
}

// Enable writes the .desktop file pointing at execPath. Overwrites any
// existing entry so the Exec= line picks up moves of the binary (e.g.,
// the user switched from `~/.local/bin` to `/usr/local/bin` between
// runs).
func Enable(execPath string) error {
	execPath = strings.TrimSpace(execPath)
	if execPath == "" {
		return errors.New("autostart: execPath must not be empty")
	}

	dir, err := autostartDir()
	if err != nil {
		return err
	}

	if mkErr := os.MkdirAll(dir, desktopDirPerm); mkErr != nil {
		return fmt.Errorf("autostart: mkdir %s: %w", dir, mkErr)
	}

	path := filepath.Join(dir, desktopFileName)
	body := fmt.Sprintf(desktopTemplate, execPath, DesktopID)

	if writeErr := os.WriteFile(path, []byte(body), desktopFilePerm); writeErr != nil {
		return fmt.Errorf("autostart: write %s: %w", path, writeErr)
	}

	return nil
}

// Disable removes the autostart entry. A missing file is a no-op — the
// goal is "autostart is off", and "the file was already gone" satisfies
// that goal.
func Disable() error {
	path, err := Path()
	if err != nil {
		return err
	}

	if rmErr := os.Remove(path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		return fmt.Errorf("autostart: remove %s: %w", path, rmErr)
	}

	return nil
}

// autostartDir resolves `$XDG_CONFIG_HOME/autostart` with a fallback to
// `~/.config/autostart` per the freedesktop Base Directory Specification.
func autostartDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "autostart"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("autostart: resolve $HOME: %w", err)
	}

	return filepath.Join(home, ".config", "autostart"), nil
}
