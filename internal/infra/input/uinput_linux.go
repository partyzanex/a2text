//go:build linux

// Package input is the platform-side implementation of the
// adapter's inject.InputDriver contract. On Linux it owns a
// persistent uinput virtual keyboard and translates daemon-level
// inject requests (Ctrl+V chord today; per-character typing later)
// into low-level evdev key events the kernel routes to whatever
// window currently has focus in the user's session.
//
// The Linux implementation requires write access to /dev/uinput,
// which mode-0660 group "input" usually gates. The deployment is
// expected to run the daemon as a system user that is a member of
// the input group; on permissions failure the constructor returns
// domain.ErrCaptureUnavailable so the daemon can surface the
// install hint to the operator.
package input

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/bendahl/uinput"

	"github.com/partyzanex/a2text/internal/domain"
)

// devicePath is the kernel uinput interface the wrapper opens.
const devicePath = "/dev/uinput"

// deviceName is the visible name of the virtual keyboard the
// daemon creates. The hotkey reader's evdev filter skips any
// keyboard whose name matches this string so the daemon never
// hears its own synthetic events back.
const deviceName = "a2text-autopaste"

// registerDelay is the pause after device creation that lets the
// compositor (Wayland) or X server pick the new keyboard up via
// udev before the first chord is sent. Without it the very first
// PasteChord can land on a focused window that has not yet been
// told about the new input device.
const registerDelay = 500 * time.Millisecond

// interKeyDelay is the pause between each KeyDown / KeyUp. The
// compositor / target application needs enough wall-clock between
// modifier state changes to register Ctrl+V as a chord rather than
// coalescing it into a single "literal v" keystroke.
const interKeyDelay = 10 * time.Millisecond

// PasteChord writes four low-level events on a successful
// invocation: Ctrl down, V down, V up, Ctrl up. The constants
// label the running count at each step so a partial-failure error
// path can return the exact prefix that did land on /dev/uinput
// without the magic-number detector flagging the literals.
const (
	stepCtrlDown int32 = 1
	stepVDown    int32 = 2
	stepVUp      int32 = 3
	stepCtrlUp   int32 = 4
)

// keyboard is the subset of bendahl/uinput.Keyboard the driver
// actually uses. Declared here so unit tests can substitute a fake
// implementation without opening /dev/uinput.
type keyboard interface {
	KeyDown(key int) error
	KeyUp(key int) error
	Close() error
}

// UinputDriver is the concrete InputDriver for Linux. It holds a
// persistent virtual keyboard for the daemon lifetime so the
// compositor / window-manager state machine sees a single stable
// "a2text-autopaste" device rather than a fresh device per cycle
// (the latter would force a re-discovery round-trip on every
// paste).
type UinputDriver struct {
	log *slog.Logger
	kb  keyboard
}

// NewUinputDriver opens /dev/uinput and registers the virtual
// keyboard. log may be nil; it is replaced with a discard handler.
//
// Returns domain.ErrCaptureUnavailable when the device cannot be
// opened (missing kernel module, no permissions, etc.) so the
// adapter layer can map the failure to the right gRPC status code.
func NewUinputDriver(log *slog.Logger) (*UinputDriver, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	kb, err := uinput.CreateKeyboard(devicePath, []byte(deviceName))
	if err != nil {
		if errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %w", domain.ErrCaptureUnavailable, err)
		}

		return nil, fmt.Errorf("input: create uinput keyboard: %w", err)
	}

	time.Sleep(registerDelay)

	log.Info("input: uinput driver ready",
		slog.String("device", deviceName),
		slog.String("path", devicePath),
	)

	return &UinputDriver{
		log: log,
		kb:  kb,
	}, nil
}

// PasteChord synthesises a Ctrl+V chord on the virtual keyboard.
// Returns the number of low-level key events written; on partial
// failure the count reflects the prefix that did get through so
// the caller's audit log can show what the focused window may
// have observed.
func (u *UinputDriver) PasteChord(ctx context.Context) (int32, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("input: paste chord: %w", err)
	}

	if err := u.kb.KeyDown(uinput.KeyLeftctrl); err != nil {
		return 0, fmt.Errorf("input: ctrl down: %w", err)
	}

	time.Sleep(interKeyDelay)

	if err := u.kb.KeyDown(uinput.KeyV); err != nil {
		return stepCtrlDown, fmt.Errorf("input: v down: %w", err)
	}

	time.Sleep(interKeyDelay)

	if err := u.kb.KeyUp(uinput.KeyV); err != nil {
		return stepVDown, fmt.Errorf("input: v up: %w", err)
	}

	time.Sleep(interKeyDelay)

	if err := u.kb.KeyUp(uinput.KeyLeftctrl); err != nil {
		return stepVUp, fmt.Errorf("input: ctrl up: %w", err)
	}

	return stepCtrlUp, nil
}

// Close destroys the underlying virtual keyboard. Intended for use
// from the shutdown manager so the device is removed cleanly on
// daemon stop rather than left dangling for the kernel to garbage
// collect on process exit.
func (u *UinputDriver) Close() error {
	if u.kb == nil {
		return nil
	}

	if err := u.kb.Close(); err != nil {
		return fmt.Errorf("input: close uinput keyboard: %w", err)
	}

	return nil
}
