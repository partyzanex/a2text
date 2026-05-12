package output

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Autopaster is the minimal interface ClipboardAutopasteOutput needs from
// clipboard autopaste adapters. Defined here (consumer side) so output
// does not import internal/adapters/clipboard — wiring connects them.
//
//go:generate go run go.uber.org/mock/mockgen@latest -package=output -destination=clipboard_autopaste_mocks_test.go -source=clipboard_autopaste.go Autopaster,ClipboardDelivery
type Autopaster interface {
	Paste(ctx context.Context) error
}

// ClipboardDelivery is the subset of *ClipboardOutput we need. Lets tests
// drive ClipboardAutopasteOutput with a fake clipboard step instead of
// wiring an entire ClipboardOutput stack.
type ClipboardDelivery interface {
	Deliver(ctx context.Context, text string) error
}

// defaultPasteDelay is the brief pause between writing to the clipboard
// and dispatching Ctrl+V. Both wl-copy and Wayland compositors hand off
// the selection asynchronously; with no delay the keystroke may fire
// against a stale clipboard and paste the *previous* contents. 50ms is
// well under the human-perceivable threshold and is the value GNOME's
// own clipboard tooling uses internally.
const defaultPasteDelay = 50 * time.Millisecond

// ErrClipboardAutopasteNotInitialized signals that ClipboardAutopasteOutput
// was used as a zero value (or with nil-typed dependencies). Distinct from
// a runtime delivery failure so the daemon's state machine can treat it
// as a wiring bug rather than a transient I/O error.
var ErrClipboardAutopasteNotInitialized = errors.New("output: clipboard autopaste output is not initialized")

// ClipboardAutopasteOutput delivers text via clipboard (with stdout
// fallback inherited from ClipboardOutput) AND simulates Ctrl+V so the
// transcription appears at the cursor without the user touching the
// keyboard.
//
// Two-stage semantics, both stages best-effort but with different
// criticality:
//
//  1. Clipboard delivery — required for data preservation. The text MUST
//     end up somewhere recoverable (clipboard or stdout/journal). A failure
//     here is propagated; the caller treats it as a real delivery error.
//
//  2. Autopaste — convenience only. If the clipboard step succeeded but
//     the keystroke fails (wedged ydotoold, focus stolen by a screen
//     lock, etc.), we LOG WARN and return nil. The user can still recover
//     the text with a manual Ctrl+V; surfacing a non-nil error here would
//     mislead the daemon's state machine into thinking nothing was
//     delivered. EXCEPT for context.Canceled / context.DeadlineExceeded —
//     those propagate, otherwise a shutdown would look like a successful
//     delivery and the daemon would mark the cycle done.
//
// field order is grouped by lifecycle (deps → tuning → infra), not alignment;
// struct is constructed once per daemon.
type ClipboardAutopasteOutput struct {
	delivery   ClipboardDelivery
	autopaster Autopaster
	delay      time.Duration
	log        *slog.Logger
}

// NewClipboardAutopasteOutput wires a clipboard-delivery step with an
// autopaste step. Both arguments are required; logger may be nil.
//
// delay defaults to 50ms when zero is passed — see defaultPasteDelay.
func NewClipboardAutopasteOutput(
	delivery ClipboardDelivery, paster Autopaster, delay time.Duration, log *slog.Logger,
) *ClipboardAutopasteOutput {
	if delivery == nil || paster == nil {
		panic("output: NewClipboardAutopasteOutput: delivery and paster are required")
	}

	if delay <= 0 {
		delay = defaultPasteDelay
	}

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &ClipboardAutopasteOutput{delivery: delivery, autopaster: paster, delay: delay, log: log}
}

// Deliver runs the two-stage clipboard-then-paste pipeline. Returns nil
// as long as the clipboard stage produced a recoverable copy of the text.
//
// Defensive against hand-built receivers: ClipboardAutopasteOutput is an
// exported struct, so a caller might assemble it with nil fields. We
// surface that as ErrClipboardAutopasteNotInitialized rather than panic.
func (o *ClipboardAutopasteOutput) Deliver(ctx context.Context, text string) error {
	if o == nil || o.delivery == nil || o.autopaster == nil {
		return ErrClipboardAutopasteNotInitialized
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("clipboard autopaste: %w", err)
	}

	if text == "" {
		return nil
	}

	if err := o.delivery.Deliver(ctx, text); err != nil {
		return fmt.Errorf("clipboard autopaste: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("clipboard autopaste: %w", err)
	}

	return o.pasteAfterDelay(ctx, text)
}

func (o *ClipboardAutopasteOutput) pasteAfterDelay(ctx context.Context, text string) error {
	delay := o.delay
	if delay <= 0 {
		delay = defaultPasteDelay
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-ctx.Done():
		return fmt.Errorf("clipboard autopaste: %w", ctx.Err())
	}

	if err := o.autopaster.Paste(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("clipboard autopaste: %w", err)
		}

		o.logger().Warn("voice: autopaste failed, text remains in clipboard",
			slog.String("err", err.Error()),
			slog.Int("text_len", len(text)),
		)
	}

	return nil
}

// logger returns a non-nil *slog.Logger even if the field was never set
// or if the receiver itself is nil. Both situations indicate a hand-built
// object; we must not panic on the WARN line.
func (o *ClipboardAutopasteOutput) logger() *slog.Logger {
	if o != nil && o.log != nil {
		return o.log
	}

	return slog.New(slog.DiscardHandler)
}
