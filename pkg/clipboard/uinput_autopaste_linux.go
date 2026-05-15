//go:build linux

package clipboard

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/bendahl/uinput"
)

const (
	autopasteBackendUinput = "uinput"

	// AutopasteBackendUinput is the exported identifier for the persistent
	// uinput virtual-keyboard backend. Unlike ydotool (which creates and
	// destroys a uinput device per invocation), this backend holds the device
	// open for the daemon's lifetime. The compositor opens the device once at
	// startup and routes keyboard events from it on every Paste call.
	AutopasteBackendUinput = autopasteBackendUinput

	// uinputDeviceName is the name the virtual keyboard device is registered
	// under. Visible in /proc/bus/input/devices and compositor device logs.
	uinputDeviceName = "a2text-autopaste"

	// uinputDevicePath is the standard Linux path for the uinput interface.
	uinputDevicePath = "/dev/uinput"

	// uinputRegisterDelay is the pause after device creation to allow the
	// compositor to open the device via udev/logind before the first Paste.
	uinputRegisterDelay = 500 * time.Millisecond

	// uinputKeyDelay is the inter-key pause for Ctrl+V injection; gives the
	// compositor time to process each event before the next one arrives.
	uinputKeyDelay = 10 * time.Millisecond
)

// UinputAutopaster injects Ctrl+V via a persistent uinput virtual keyboard.
//
// The device is created in NewUinputAutopaster and kept open until Close is
// called. The compositor (GNOME Mutter, KWin, …) opens the device once via
// udev/logind and routes all subsequent key events to the currently focused
// surface — no permission dialog, no keyboard-routing side effects.
//
// Caveat: the chord is plain Ctrl+V. If the hotkey that triggers a2text uses
// a modifier (e.g. Super+R) and the user holds it while the paste fires, the
// focused window receives Super+Ctrl+V instead of Ctrl+V — Wayland-native
// apps silently drop that combo. Tap the hotkey, do not hold it.
//
// Requires write access to /dev/uinput (typically ACL or the "input" group).
type UinputAutopaster struct {
	mu  sync.Mutex
	kb  uinput.Keyboard
	log *slog.Logger
}

// NewUinputAutopaster creates a persistent uinput keyboard device and waits
// for the compositor to register it before returning.
func NewUinputAutopaster(log *slog.Logger) (*UinputAutopaster, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	kb, err := uinput.CreateKeyboard(uinputDevicePath, []byte(uinputDeviceName))
	if err != nil {
		return nil, fmt.Errorf("uinput autopaster: create keyboard: %w", err)
	}

	// Allow the compositor time to open the device via udev before any paste.
	time.Sleep(uinputRegisterDelay)

	log.Info("voice: uinput autopaste device ready",
		slog.String("device", uinputDeviceName),
		slog.String("path", uinputDevicePath),
	)

	return &UinputAutopaster{kb: kb, log: log}, nil
}

// Backend returns "uinput".
func (*UinputAutopaster) Backend() string { return autopasteBackendUinput }

// Close releases the uinput device.
func (ua *UinputAutopaster) Close() error {
	ua.mu.Lock()
	defer ua.mu.Unlock()

	if err := ua.kb.Close(); err != nil {
		return fmt.Errorf("uinput autopaster: close: %w", err)
	}

	return nil
}

// Paste injects Ctrl+V through the persistent uinput keyboard device.
func (ua *UinputAutopaster) Paste(_ context.Context) error {
	ua.mu.Lock()
	defer ua.mu.Unlock()

	if err := chord(ua.kb, uinput.KeyLeftctrl, uinput.KeyV, "ctrl", "v"); err != nil {
		return fmt.Errorf("uinput autopaste: %w", err)
	}

	ua.log.Debug("voice: autopaste fired", slog.String("backend", autopasteBackendUinput))

	return nil
}

// chord presses modifier, presses key, releases key, releases modifier with
// uinputKeyDelay between every transition. Names label the keys for error
// messages so callers can identify which chord failed.
func chord(kb uinput.Keyboard, modKey, mainKey int, modName, mainName string) error {
	if err := kb.KeyDown(modKey); err != nil {
		return fmt.Errorf("%s down: %w", modName, err)
	}

	time.Sleep(uinputKeyDelay)

	if err := kb.KeyDown(mainKey); err != nil {
		return fmt.Errorf("%s down: %w", mainName, err)
	}

	time.Sleep(uinputKeyDelay)

	if err := kb.KeyUp(mainKey); err != nil {
		return fmt.Errorf("%s up: %w", mainName, err)
	}

	time.Sleep(uinputKeyDelay)

	if err := kb.KeyUp(modKey); err != nil {
		return fmt.Errorf("%s up: %w", modName, err)
	}

	return nil
}
