// Package hotkeyreader wires the kernel evdev hotkey pipeline
// (pkg/hotkey) to the daemon's cycle state machine (hotkey.Hub).
//
// On every physical key edge the evdev backend reports, Reader
// decides — based on the configured HOLD / TOGGLE mode — whether
// to start a new cycle (mint an inject_token via cycletoken.Store,
// call Hub.Start) or end the in-flight one (call Hub.End). The
// gRPC-side StartCycle adapter call reuses the same Hub + token
// store so the two trigger paths share a single source of truth.
package hotkeyreader

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/partyzanex/a2text/internal/infra/cycletoken"
	"github.com/partyzanex/a2text/internal/usecases/hotkey"
	pkghotkey "github.com/partyzanex/a2text/pkg/hotkey"
	a2textv1 "github.com/partyzanex/a2text/pkg/proto/a2text/v1"
)

// Reader owns the evdev listener goroutine and translates raw key
// edges into Hub.Start / Hub.End calls, minting fresh tokens via
// cycletoken.Store on each cycle start.
type Reader struct {
	log    *slog.Logger
	mode   a2textv1.HotkeyMode
	tokens *cycletoken.Store
	hub    *hotkey.Hub
	evdev  *pkghotkey.EvdevHotkey
}

// New constructs a Reader bound to the given key / modifiers and
// the daemon's Hub + token store. log may be nil; it is replaced
// with a discard handler.
func New(
	log *slog.Logger,
	mode a2textv1.HotkeyMode,
	tokens *cycletoken.Store,
	hub *hotkey.Hub,
	key string,
	modifiers []string,
) (*Reader, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	reader := &Reader{
		log:    log,
		mode:   mode,
		tokens: tokens,
		hub:    hub,
	}

	evdev, err := pkghotkey.NewEvdevHotkey(reader.onEdge, key, modifiers, log)
	if err != nil {
		return nil, fmt.Errorf("hotkeyreader: build evdev: %w", err)
	}

	reader.evdev = evdev

	return reader, nil
}

// Listen blocks until ctx is cancelled or the underlying evdev
// listener stops. Intended to run in its own goroutine.
func (r *Reader) Listen(ctx context.Context) error {
	if err := r.evdev.Listen(ctx); err != nil {
		return fmt.Errorf("hotkeyreader: listen: %w", err)
	}

	return nil
}

// Close stops the evdev listener. Intended for use from the
// shutdown manager.
func (r *Reader) Close() error {
	if r.evdev == nil {
		return nil
	}

	if err := r.evdev.Stop(); err != nil {
		return fmt.Errorf("hotkeyreader: stop: %w", err)
	}

	return nil
}

// onEdge is the handler the evdev backend invokes on every key
// transition. Translation depends on mode:
//
//   - HOLD   : PRESS → cycle start, RELEASE → cycle end.
//   - TOGGLE : every PRESS flips the cycle on/off; RELEASE is
//     suppressed by the evdev backend mode-decoder.
func (r *Reader) onEdge(ctx context.Context, evt pkghotkey.Event) {
	if r.mode == a2textv1.HotkeyMode_HOTKEY_MODE_HOLD {
		r.handleHold(ctx, evt)

		return
	}

	r.handleToggle(ctx, evt)
}

// handleHold maps HOLD-mode edges 1:1 onto Hub.Start / Hub.End.
func (r *Reader) handleHold(ctx context.Context, evt pkghotkey.Event) {
	switch evt {
	case pkghotkey.Press:
		r.startCycle(ctx)
	case pkghotkey.Release:
		r.hub.End()
	}
}

// handleToggle inverts the cycle state on every press; release is
// ignored. Issue is the authoritative state probe: it returns
// ErrAlreadyActive when a cycle is in flight, so a failed Issue
// here means "user wants to stop".
func (r *Reader) handleToggle(ctx context.Context, evt pkghotkey.Event) {
	if evt != pkghotkey.Press {
		return
	}

	if !r.startCycle(ctx) {
		r.hub.End()
	}
}

// startCycle mints a fresh inject_token and asks Hub to begin a
// cycle. Returns true when a new cycle was started, false when the
// token store reports a cycle is already active (the caller decides
// what to do with that signal, e.g. flip to End in TOGGLE mode).
func (r *Reader) startCycle(ctx context.Context) bool {
	tok, _, err := r.tokens.Issue()
	if err != nil {
		if errors.Is(err, cycletoken.ErrAlreadyActive) {
			return false
		}

		r.log.Error("hotkeyreader: token issue failed",
			slog.Any("error", err),
		)

		return false
	}

	if err := r.hub.Start(ctx, string(tok)); err != nil {
		r.log.Error("hotkeyreader: hub start failed",
			slog.String("token", string(tok)),
			slog.Any("error", err),
		)

		return false
	}

	return true
}
