// Package hotkeyreader bridges the kernel evdev pipeline
// (pkg/hotkey) into the daemon-side cycle state machine
// (hotkey.Hub). The evdev backend and the gRPC StartCycle path
// converge on the same Hub + cycletoken.Store, so a UI subscribing
// via StreamHotkeyEvents sees identical state regardless of which
// trigger started a cycle.
package hotkeyreader

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/partyzanex/a2text/internal/infra/cycletoken"
	"github.com/partyzanex/a2text/internal/usecases/hotkey"
	pkghotkey "github.com/partyzanex/a2text/pkg/hotkey"
	a2textv1 "github.com/partyzanex/a2text/pkg/proto/a2text/v1"
)

type Reader struct {
	log    *slog.Logger
	mode   a2textv1.HotkeyMode
	tokens *cycletoken.Store
	hub    *hotkey.Hub
	evdev  *pkghotkey.EvdevHotkey
}

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

func (r *Reader) Listen(ctx context.Context) error {
	if err := r.evdev.Listen(ctx); err != nil {
		return fmt.Errorf("hotkeyreader: listen: %w", err)
	}

	return nil
}

func (r *Reader) Close() error {
	if r.evdev == nil {
		return nil
	}

	if err := r.evdev.Stop(); err != nil {
		return fmt.Errorf("hotkeyreader: stop: %w", err)
	}

	return nil
}

func (r *Reader) onEdge(ctx context.Context, evt pkghotkey.Event) {
	if r.mode == a2textv1.HotkeyMode_HOTKEY_MODE_HOLD {
		r.handleHold(ctx, evt)

		return
	}

	r.handleToggle(ctx, evt)
}

func (r *Reader) handleHold(ctx context.Context, evt pkghotkey.Event) {
	switch evt {
	case pkghotkey.Press:
		r.startCycle(ctx)
	case pkghotkey.Release:
		r.hub.End()
	}
}

// handleToggle uses Hub.IsRecording as the authoritative probe.
// Issue's ErrAlreadyActive used to double as a state signal, but
// that conflated "cycle in flight" with "transient Issue failure",
// so any crypto/rand or hub fault would silently abort a real
// cycle.
func (r *Reader) handleToggle(ctx context.Context, evt pkghotkey.Event) {
	if evt != pkghotkey.Press {
		return
	}

	if r.hub.IsRecording() {
		r.hub.End()

		return
	}

	r.startCycle(ctx)
}

func (r *Reader) startCycle(ctx context.Context) {
	tok, _, err := r.tokens.Issue()
	if err != nil {
		r.log.Error("hotkeyreader: token issue failed",
			slog.Any("error", err),
		)

		return
	}

	if err := r.hub.Start(ctx, string(tok)); err != nil {
		r.log.Error("hotkeyreader: hub start failed",
			slog.String("token", string(tok)),
			slog.Any("error", err),
		)

		// Single-slot store would stay locked until TTL; roll back
		// so the next edge can issue again.
		if consumeErr := r.tokens.Consume(tok); consumeErr != nil {
			r.log.Warn("hotkeyreader: rollback of unused token failed",
				slog.String("token", string(tok)),
				slog.Any("error", consumeErr),
			)
		}
	}
}
