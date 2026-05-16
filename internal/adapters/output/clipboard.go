package output

import (
	"context"
	"fmt"
	"log/slog"
)

// ClipboardCopier is the minimal interface ClipboardOutput needs from
// clipboard adapters. Defined here (consumer side) so output doesn't
// import internal/adapters/clipboard — wiring connects them.
//
//go:generate go run go.uber.org/mock/mockgen@latest -write_package_comment=false -package=output -destination=clipboard_mocks_test.go -source=clipboard.go ClipboardCopier
type ClipboardCopier interface {
	Copy(ctx context.Context, text string) error
}

// ClipboardOutput delivers text to the system clipboard. On clipboard
// failure (utility crashed, Wayland compositor unresponsive) it degrades
// to the configured fallback (stdout in `mode: stdout` setups, structured
// slog elsewhere) with a WARN log so the transcription is still emitted
// somewhere recoverable.
//
// "Degrade rather than block" is deliberate: losing a transcription the
// user just spoke is worse than emitting it in a less-convenient channel.
type ClipboardOutput struct {
	primary  ClipboardCopier
	fallback ClipboardDelivery
	log      *slog.Logger
}

// NewClipboardOutput wires a clipboard copier with a fallback deliverer
// (any voice.Output-shaped type — typically LogOutput in production or
// StdoutOutput when `mode: stdout` is explicit).
// Both arguments are required; logger may be nil.
func NewClipboardOutput(primary ClipboardCopier, fallback ClipboardDelivery, log *slog.Logger) *ClipboardOutput {
	if primary == nil || fallback == nil {
		panic("output: NewClipboardOutput: primary and fallback are required")
	}

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &ClipboardOutput{primary: primary, fallback: fallback, log: log}
}

// Deliver attempts clipboard first; on error falls back to stdout. The
// returned error is nil if EITHER path succeeded — the caller wants to
// know "did the text get emitted?", not "did the preferred channel work?".
// The fallback path emits a WARN log so journal grep can still find
// degradations.
//
// Context is checked twice: once before clipboard so a cancelled call
// short-circuits, and once before fallback so a slow-failing clipboard
// (e.g. Wayland compositor that times out instead of refusing) does not
// trick us into writing past a deadline the caller has already given up on.
func (o *ClipboardOutput) Deliver(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("deliver: %w", err)
	}

	primaryErr := o.primary.Copy(ctx, text)
	if primaryErr == nil {
		return nil
	}

	o.log.Warn("voice: clipboard delivery degraded to fallback",
		slog.String("err", primaryErr.Error()),
		slog.Int("text_len", len(text)),
	)

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("deliver: %w", err)
	}

	if err := o.fallback.Deliver(ctx, text); err != nil {
		return fmt.Errorf("both clipboard and fallback failed: clipboard=%w fallback=%w", primaryErr, err)
	}

	return nil
}
