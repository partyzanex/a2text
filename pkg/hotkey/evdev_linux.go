//go:build linux

package hotkey

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/bendahl/uinput"
	"golang.org/x/sys/unix"
)

// Linux input subsystem constants. The Linux input_event packet wire format
// is documented in include/uapi/linux/input.h. We need it byte-exact because
// we read raw event packets from /dev/input/event*.
//
// On 64-bit Linux the layout is:
//
//	struct input_event {
//	    struct timeval time;  // 16 bytes (sec int64, usec int64)
//	    __u16          type;
//	    __u16          code;
//	    __s32          value;
//	};
//
// Total 24 bytes. timeval is unused here — we don't care about wall-clock
// timestamps, only the (type, code, value) tuple.
const (
	inputEventSize        = 24
	inputEventTypeOffset  = 16
	inputEventCodeOffset  = 18
	inputEventValueOffset = 20

	// Linux input event values for EV_KEY.
	keyValueRelease = 0
	keyValuePress   = 1
	keyValueRepeat  = 2

	// devInputGlob matches every input device node the kernel exposes.
	devInputGlob = "/dev/input/event*"

	// sysInputClass is the sysfs path where each event device's metadata
	// lives. /sys/class/input/eventN/device/name holds the human-readable
	// device name we use to skip our own virtual uinput keyboard.
	sysInputClass = "/sys/class/input"

	// virtualUinputDeviceName is the name of the a2text autopaste uinput
	// device. We must NOT read our own injected events back as hotkey input
	// — that would turn every Ctrl+V into a phantom hotkey edge.
	//
	// Kept as a private constant rather than imported from pkg/clipboard to
	// avoid cross-package coupling for a single string. If the clipboard
	// package renames the device, this must follow.
	virtualUinputDeviceName = "a2text-autopaste"
)

// keycodeMap translates user-facing key names from config.yaml into Linux
// KEY_* codes. Only the keys we actually expect users to bind are listed —
// alphanumerics, function keys, common navigation keys. Extend on demand.
var keycodeMap = map[string]uint16{ //nolint:gochecknoglobals // lookup table, immutable
	"F1": uint16(uinput.KeyF1), "F2": uint16(uinput.KeyF2), "F3": uint16(uinput.KeyF3), "F4": uint16(uinput.KeyF4),
	"F5": uint16(uinput.KeyF5), "F6": uint16(uinput.KeyF6), "F7": uint16(uinput.KeyF7), "F8": uint16(uinput.KeyF8),
	"F9": uint16(uinput.KeyF9), "F10": uint16(uinput.KeyF10), "F11": uint16(uinput.KeyF11), "F12": uint16(uinput.KeyF12),

	"A": uint16(uinput.KeyA), "B": uint16(uinput.KeyB), "C": uint16(uinput.KeyC), "D": uint16(uinput.KeyD),
	"E": uint16(uinput.KeyE), "F": uint16(uinput.KeyF), "G": uint16(uinput.KeyG), "H": uint16(uinput.KeyH),
	"I": uint16(uinput.KeyI), "J": uint16(uinput.KeyJ), "K": uint16(uinput.KeyK), "L": uint16(uinput.KeyL),
	"M": uint16(uinput.KeyM), "N": uint16(uinput.KeyN), "O": uint16(uinput.KeyO), "P": uint16(uinput.KeyP),
	"Q": uint16(uinput.KeyQ), "R": uint16(uinput.KeyR), "S": uint16(uinput.KeyS), "T": uint16(uinput.KeyT),
	"U": uint16(uinput.KeyU), "V": uint16(uinput.KeyV), "W": uint16(uinput.KeyW), "X": uint16(uinput.KeyX),
	"Y": uint16(uinput.KeyY), "Z": uint16(uinput.KeyZ),

	"SPACE": uint16(uinput.KeySpace), "ENTER": uint16(uinput.KeyEnter), "TAB": uint16(uinput.KeyTab),
	"ESC": uint16(uinput.KeyEsc), "INSERT": uint16(uinput.KeyInsert), "DELETE": uint16(uinput.KeyDelete),
	"HOME": uint16(uinput.KeyHome), "END": uint16(uinput.KeyEnd),
	"PAGEUP": uint16(uinput.KeyPageup), "PAGEDOWN": uint16(uinput.KeyPagedown),
	"PAUSE": uint16(uinput.KeyPause), "PRINT": uint16(uinput.KeySysrq),
}

// Canonical modifier names. Centralised so goconst stays happy and the
// alias-normaliser cannot drift from the map keys below.
const (
	modCtrl  = "ctrl"
	modShift = "shift"
	modAlt   = "alt"
	modSuper = "super"
)

// modifierKeycodes maps each modifier name to the pair of Linux keycodes
// (left and right) that satisfy it. Pressing either physical key counts.
var modifierKeycodes = map[string][]uint16{ //nolint:gochecknoglobals // lookup table, immutable
	modCtrl:  {uint16(uinput.KeyLeftctrl), uint16(uinput.KeyRightctrl)},
	modShift: {uint16(uinput.KeyLeftshift), uint16(uinput.KeyRightshift)},
	modAlt:   {uint16(uinput.KeyLeftalt), uint16(uinput.KeyRightalt)},
	modSuper: {uint16(uinput.KeyLeftmeta), uint16(uinput.KeyRightmeta)},
}

// ErrEvdevNoDevices is returned by Listen when no input devices are
// accessible. Typically means the user is not in the "input" group and
// /dev/input/event* nodes are not world-readable.
var ErrEvdevNoDevices = errors.New(
	"hotkey: no /dev/input/event* devices accessible " +
		"(add user to the 'input' group or set ACLs on the device nodes)",
)

// EvdevHotkey listens for global hotkey events by reading raw key events
// from /dev/input/event* devices. Works under any session type (Wayland,
// X11, console) because it talks to the kernel directly — no compositor
// involvement, no X11 server, no D-Bus.
//
// The trade-off: requires read access to /dev/input/event* (typically via
// membership in the "input" group or per-device ACLs).
//
// Modifier handling: state is aggregated across ALL keyboards on the system,
// so pressing Ctrl on one keyboard and the main key on another counts as a
// satisfied chord. This matches how X11/Wayland hotkeys behave.
//
// The device named "a2text-autopaste" (our own uinput injection device) is
// always skipped to prevent feedback loops where an injected Ctrl+V is read
// back as a hotkey.
type EvdevHotkey struct {
	handler Handler
	log     *slog.Logger

	keyCode   uint16
	modGroups [][]uint16 // each inner slice = one modifier name's keycode pair
	modCodes  map[uint16]struct{}

	mu       sync.Mutex
	modState map[uint16]bool // keycode → currently pressed
	keyDown  bool            // main key currently pressed

	stopOnce sync.Once
	stopCh   chan struct{}
}

// NewEvdevHotkey creates an evdev-based hotkey listener.
//
// key is a logical key name (e.g. "F4", "Space", "A") — case-insensitive,
// resolved through keycodeMap. modifiers is a list of modifier names
// ("ctrl", "shift", "alt", "super" — also "control"/"mod1"/"mod4"/"win").
// A nil log is accepted and replaced with a discard handler.
func NewEvdevHotkey(handler Handler, key string, modifiers []string, log *slog.Logger) (*EvdevHotkey, error) {
	if handler == nil {
		return nil, ErrHandlerNil
	}

	if strings.TrimSpace(key) == "" {
		return nil, ErrKeyEmpty
	}

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	keyCode, ok := keycodeMap[strings.ToUpper(strings.TrimSpace(key))]
	if !ok {
		return nil, fmt.Errorf("hotkey: evdev: unsupported key %q", key)
	}

	modGroups, modCodes, err := resolveModifiers(modifiers)
	if err != nil {
		return nil, err
	}

	return &EvdevHotkey{
		handler:   handler,
		log:       log,
		keyCode:   keyCode,
		modGroups: modGroups,
		modCodes:  modCodes,
		modState:  make(map[uint16]bool),
		stopCh:    make(chan struct{}),
	}, nil
}

// resolveModifiers maps each user-facing modifier name to its pair of
// keycodes and returns both the per-group view (for satisfaction checks)
// and a flat set (for fast "is this code a tracked modifier?" lookups).
func resolveModifiers(
	modifiers []string,
) (groups [][]uint16, codes map[uint16]struct{}, err error) {
	codes = make(map[uint16]struct{})

	for _, raw := range modifiers {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		// Normalise aliases — keep behaviour consistent with the X11 backend.
		switch name {
		case "control":
			name = modCtrl
		case "mod1":
			name = modAlt
		case "mod4", "win":
			name = modSuper
		}

		pair, ok := modifierKeycodes[name]
		if !ok {
			return nil, nil, fmt.Errorf("hotkey: evdev: unknown modifier %q", raw)
		}

		groups = append(groups, pair)

		for _, code := range pair {
			codes[code] = struct{}{}
		}
	}

	return groups, codes, nil
}

// Listen opens every accessible /dev/input/event* device, spawns one reader
// goroutine per device, and dispatches Press/Release edges to the handler.
// Blocks until ctx is cancelled or Stop is called. Returns ErrEvdevNoDevices
// when no devices could be opened — the daemon should treat this as a
// configuration error (group membership / ACL).
func (e *EvdevHotkey) Listen(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("hotkey: evdev: %w", err)
	}

	files, err := openInputDevices(e.log)
	if err != nil {
		return err
	}

	if len(files) == 0 {
		return ErrEvdevNoDevices
	}

	defer closeAll(files)

	e.log.Info("hotkey: evdev: started",
		slog.Int("devices", len(files)),
		slog.String("key_code", strconv.FormatUint(uint64(e.keyCode), 10)),
		slog.Int("modifier_groups", len(e.modGroups)),
	)

	var wg sync.WaitGroup

	readerCtx, cancelReaders := context.WithCancel(ctx)
	defer cancelReaders()

	for _, file := range files {
		wg.Add(1)

		go e.readDeviceLoop(readerCtx, file, &wg)
	}

	select {
	case <-ctx.Done():
	case <-e.stopCh:
	}

	cancelReaders()
	// Closing files unblocks any goroutine blocked in read(2) — see closeAll.
	closeAll(files)
	wg.Wait()

	return nil
}

// Stop releases resources and unblocks Listen. Idempotent.
func (e *EvdevHotkey) Stop() error {
	e.stopOnce.Do(func() {
		close(e.stopCh)
	})

	return nil
}

// readDeviceLoop reads input_event packets from one device until the device
// is closed or ctx is cancelled. Each EV_KEY event is forwarded to handleKey.
// All other event types (EV_SYN, EV_MSC, EV_REL, EV_ABS) are ignored.
func (e *EvdevHotkey) readDeviceLoop(ctx context.Context, file *os.File, wg *sync.WaitGroup) {
	defer wg.Done()

	buf := make([]byte, inputEventSize)

	for {
		n, err := file.Read(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
				return
			}

			e.log.Debug("hotkey: evdev: read error",
				slog.String("device", file.Name()),
				slog.Any("err", err),
			)

			return
		}

		if n != inputEventSize {
			continue
		}

		evType := binary.LittleEndian.Uint16(buf[inputEventTypeOffset:inputEventCodeOffset])
		if evType != unix.EV_KEY {
			continue
		}

		code := binary.LittleEndian.Uint16(buf[inputEventCodeOffset:inputEventValueOffset])
		raw := binary.LittleEndian.Uint32(buf[inputEventValueOffset:inputEventSize])
		value := int32(raw) //nolint:gosec // wire-format reinterpretation

		e.handleKey(ctx, code, value)
	}
}

// handleKey applies a single key transition to the listener state and
// dispatches Press/Release to the handler when the configured chord matches.
//
// Modifier keys update the aggregated modifier state but never fire the
// handler. The main key fires Press only when ALL modifier groups are
// satisfied at the moment of press; Release fires unconditionally whenever
// the main key transitions from down→up (matching X11 hotkey semantics —
// once recording starts, releasing the modifier first should not strand the
// daemon in recording state).
func (e *EvdevHotkey) handleKey(ctx context.Context, code uint16, value int32) {
	if _, isModifier := e.modCodes[code]; isModifier {
		e.recordModifier(code, value)

		return
	}

	if code != e.keyCode {
		return
	}

	switch value {
	case keyValuePress:
		if e.tryPressMainKey() {
			e.handler(ctx, Press)
		}
	case keyValueRelease:
		if e.tryReleaseMainKey() {
			e.handler(ctx, Release)
		}
	case keyValueRepeat:
		// Auto-repeat from the kernel — ignore. We only care about edges.
	}
}

func (e *EvdevHotkey) recordModifier(code uint16, value int32) {
	e.mu.Lock()
	defer e.mu.Unlock()

	switch value {
	case keyValuePress, keyValueRepeat:
		e.modState[code] = true
	case keyValueRelease:
		delete(e.modState, code)
	}
}

// tryPressMainKey atomically checks the modifier set and flips keyDown
// to true. Returns true when the caller should fire Press: the chord matches
// AND we were not already in the pressed state (deduplicates repeats from
// multiple devices reporting the same key).
func (e *EvdevHotkey) tryPressMainKey() bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.keyDown {
		return false
	}

	if !e.modifiersSatisfiedLocked() {
		return false
	}

	e.keyDown = true

	return true
}

// tryReleaseMainKey atomically flips keyDown to false. Returns true when
// the caller should fire Release: we were previously in the pressed state.
func (e *EvdevHotkey) tryReleaseMainKey() bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.keyDown {
		return false
	}

	e.keyDown = false

	return true
}

// modifiersSatisfiedLocked reports whether every configured modifier group
// has at least one of its keycodes pressed. Caller must hold e.mu.
func (e *EvdevHotkey) modifiersSatisfiedLocked() bool {
	for _, group := range e.modGroups {
		matched := false

		for _, code := range group {
			if e.modState[code] {
				matched = true

				break
			}
		}

		if !matched {
			return false
		}
	}

	return true
}

// openInputDevices globs /dev/input/event*, skips our own virtual uinput
// keyboard, opens each remaining device for reading, and returns the open
// files. Devices we cannot open (permission denied, transient unplug) are
// logged at DEBUG and skipped — a partial enumeration is still useful.
func openInputDevices(log *slog.Logger) ([]*os.File, error) {
	paths, err := filepath.Glob(devInputGlob)
	if err != nil {
		return nil, fmt.Errorf("hotkey: evdev: glob %q: %w", devInputGlob, err)
	}

	var files []*os.File

	for _, path := range paths {
		if isVirtualDevice(path) {
			continue
		}

		file, err := os.Open(path) //nolint:gosec // path is a kernel-managed device node
		if err != nil {
			log.Debug("hotkey: evdev: cannot open device",
				slog.String("path", path),
				slog.Any("err", err),
			)

			continue
		}

		files = append(files, file)
	}

	return files, nil
}

// isVirtualDevice returns true when the event device belongs to our own
// uinput virtual keyboard. The device name is read from sysfs
// (/sys/class/input/eventN/device/name); on read failure we err on the side
// of NOT skipping — better a duplicate event than a missed real keyboard.
func isVirtualDevice(devPath string) bool {
	base := filepath.Base(devPath)
	namePath := filepath.Join(sysInputClass, base, "device", "name")

	data, err := os.ReadFile(namePath) //nolint:gosec // sysfs path
	if err != nil {
		return false
	}

	return strings.TrimSpace(string(data)) == virtualUinputDeviceName
}

// closeAll closes every file. Errors are intentionally ignored: closing an
// already-closed file or one whose underlying device has been unplugged is
// expected during shutdown and not actionable.
func closeAll(files []*os.File) {
	for _, file := range files {
		_ = file.Close() //nolint:errcheck // see godoc
	}
}

// Ensure EvdevHotkey satisfies the Listener contract at compile time.
var _ Listener = (*EvdevHotkey)(nil)
