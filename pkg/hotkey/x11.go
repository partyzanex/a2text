//go:build linux && x11

package hotkey

/*
#cgo LDFLAGS: -lX11
#include <X11/Xlib.h>
#include <X11/keysym.h>
#include <stdlib.h>

typedef struct {
	Display *dpy;
	Window   win;
	int      kc;
} hkState;

hkState* hkAlloc(void) {
	return (hkState*)calloc(1, sizeof(hkState));
}

void hkFree(hkState *s) {
	free(s);
}

int hkSetup(hkState *s, const char *displayName, const char *keystr, unsigned int mods) {
	s->dpy = XOpenDisplay(displayName);
	if (!s->dpy) return -1;

	KeySym ks = XStringToKeysym(keystr);
	if (ks == NoSymbol) { XCloseDisplay(s->dpy); s->dpy = NULL; return -2; }

	s->kc = XKeysymToKeycode(s->dpy, ks);
	if (s->kc == 0) { XCloseDisplay(s->dpy); s->dpy = NULL; return -3; }

	s->win = DefaultRootWindow(s->dpy);

	unsigned int masks[] = {0, Mod2Mask, LockMask, Mod2Mask|LockMask};
	for (int i = 0; i < 4; i++) {
		XGrabKey(s->dpy, s->kc, mods|masks[i], s->win, True, GrabModeAsync, GrabModeAsync);
	}
	XFlush(s->dpy);
	return 0;
}

// hkConnectionFd returns the file descriptor of the X11 connection so Go
// can poll it. Returns -1 if the display is not open.
int hkConnectionFd(hkState *s) {
	if (!s->dpy) return -1;
	return ConnectionNumber(s->dpy);
}

// hkProcessOneEvent processes at most one pending X11 event without blocking.
// Returns 2 if a KeyPress was processed, 1 if another event type was processed,
// 0 if no events were pending, -1 on error.
int hkProcessOneEvent(hkState *s) {
	if (!s->dpy) return -1;

	if (!XPending(s->dpy)) return 0;

	XEvent ev;
	XNextEvent(s->dpy, &ev);

	if (ev.type == KeyPress) return 2;

	return 1;
}

void hkCleanup(hkState *s, unsigned int mods) {
	if (!s->dpy) return;
	if (s->win && s->kc) {
		unsigned int masks[] = {0, Mod2Mask, LockMask, Mod2Mask|LockMask};
		for (int i = 0; i < 4; i++) {
			XUngrabKey(s->dpy, s->kc, mods|masks[i], s->win);
		}
	}
	XCloseDisplay(s->dpy);
	s->dpy = NULL;
	s->win = 0;
	s->kc  = 0;
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"
)

// pollTimeoutMs is the poll(2) timeout for the X11 event loop in milliseconds.
// 100 ms balances responsiveness to stop signals against CPU load.
const pollTimeoutMs = 100

// X11Hotkey registers a global hotkey using XGrabKey via CGo (libX11).
type X11Hotkey struct {
	handler Handler
	log     *slog.Logger

	key  string
	mods uint

	mu      sync.Mutex
	stopCh  chan struct{}
	stopped bool
}

// NewX11Hotkey creates a hotkey for the given key combination.
//
// key is an X11 keysym name (e.g. "D", "F12", "space").
// modifiers is a bitmask: Mod4Mask=Super, Mod1Mask=Alt, ControlMask=Ctrl, ShiftMask=Shift.
// A nil log is accepted and replaced with a discard handler.
func NewX11Hotkey(handler Handler, key string, modifiers uint, log *slog.Logger) (*X11Hotkey, error) {
	if handler == nil {
		return nil, ErrHandlerNil
	}

	if key == "" {
		return nil, ErrKeyEmpty
	}

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &X11Hotkey{
		handler: handler,
		log:     log,
		key:     key,
		mods:    modifiers,
		stopCh:  make(chan struct{}),
	}, nil
}

// Listen starts listening for the hotkey. Blocks until ctx is cancelled
// or Stop is called. Only one Listen call is allowed per X11Hotkey.
//
// Listen: fail-hard on X11 setup (returns error immediately); fail-soft on
// the event loop (logs transient event processing errors and continues).
//
// The event loop is driven from Go using poll(2) on the X11 connection fd.
// No background goroutine is spawned: handler is called inline, so
// XCloseDisplay cannot race with pending event processing.
func (h *X11Hotkey) Listen(ctx context.Context) error {
	h.mu.Lock()
	if h.stopped {
		h.mu.Unlock()
		return errors.New("hotkey: already stopped")
	}
	h.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	st := C.hkAlloc()
	if st == nil {
		return errors.New("hotkey: hkAlloc returned nil (out of memory)")
	}

	defer func() {
		C.hkCleanup(st, C.uint(h.mods))
		C.hkFree(st)
	}()

	displayName := os.Getenv("DISPLAY")
	if displayName == "" {
		displayName = ":0"
	}

	cDisplay := C.CString(displayName)
	cKey := C.CString(h.key)
	defer C.free(unsafe.Pointer(cDisplay))
	defer C.free(unsafe.Pointer(cKey))

	switch rc := C.hkSetup(st, cDisplay, cKey, C.uint(h.mods)); rc {
	case 0:
		// success
	case -1:
		return fmt.Errorf("%w: check DISPLAY env or running Xorg", ErrX11DisplayUnavailable)
	case -2:
		return fmt.Errorf("%w: %q", ErrX11InvalidKeySym, h.key)
	case -3:
		return fmt.Errorf("%w: %q", ErrX11InvalidKeyCode, h.key)
	default:
		return fmt.Errorf("hotkey: X11 setup failed (code %d)", int(rc))
	}

	x11Fd := int(C.hkConnectionFd(st))
	if x11Fd < 0 {
		return errors.New("hotkey: X11 connection fd not available")
	}

	pollFds := []unix.PollFd{
		{Fd: int32(x11Fd), Events: unix.POLLIN},
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-h.stopCh:
			return nil
		default:
		}

		n, err := unix.Poll(pollFds, pollTimeoutMs)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}

			return fmt.Errorf("hotkey: poll failed: %w", err)
		}

		if n > 0 && (pollFds[0].Revents&unix.POLLIN) != 0 {
			h.drainEvents(ctx, st)
		}
	}
}

// drainEvents processes all pending X11 events in the queue. KeyPress events
// invoke the handler; other events are silently consumed. Returns early when
// ctx is cancelled, the queue is empty, or a processing error occurs (rc < 0).
func (h *X11Hotkey) drainEvents(ctx context.Context, st *C.hkState) {
	for {
		rc := C.hkProcessOneEvent(st)

		switch {
		case rc == 2:
			// KeyPress. KeyRelease delivery requires the CGo C wrapper to
			// also XGrabKey on release events; for now this backend stays
			// press-only and hold-mode falls back to one-shot Start (see
			// Daemon.HotkeyHandler degradation note).
			h.handler(ctx, Press)
		case rc == 1:
			// non-KeyPress event consumed; drain next
		case rc < 0:
			h.log.WarnContext(ctx, "hotkey: X11 event processing error",
				slog.String("key", h.key))
			return
		default:
			// rc == 0: no more pending events
			return
		}
	}
}

// Stop releases the hotkey grab. Idempotent.
func (h *X11Hotkey) Stop() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.stopped {
		return nil
	}

	h.stopped = true
	close(h.stopCh)

	return nil
}

// Ensure X11Hotkey satisfies HotkeyListener at compile time.
var _ Listener = (*X11Hotkey)(nil)

// Modifier constants matching X11 modifier masks.
const (
	ModShift   = uint(C.ShiftMask)
	ModLock    = uint(C.LockMask)
	ModControl = uint(C.ControlMask)
	Mod1       = uint(C.Mod1Mask) // Alt
	Mod2       = uint(C.Mod2Mask) // NumLock
	Mod3       = uint(C.Mod3Mask)
	Mod4       = uint(C.Mod4Mask) // Super/Windows
	Mod5       = uint(C.Mod5Mask)
)
