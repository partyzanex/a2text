package output

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Autopaster is the minimal interface ClipboardAutopasteOutput needs from
// clipboard autopaste adapters. Defined here (consumer side) so output
// does not import internal/adapters/clipboard — wiring connects them.
//
//go:generate go run go.uber.org/mock/mockgen@latest -write_package_comment=false -package=output -destination=clipboard_autopaste_mocks_test.go -source=clipboard_autopaste.go Autopaster,ClipboardDelivery,ClipboardSnapshotter,ClipboardTypedCopier
type Autopaster interface {
	Paste(ctx context.Context) error
}

// ClipboardDelivery is the subset of *ClipboardOutput we need. Lets tests
// drive ClipboardAutopasteOutput with a fake clipboard step instead of
// wiring an entire ClipboardOutput stack.
type ClipboardDelivery interface {
	Deliver(ctx context.Context, text string) error
}

// ClipboardSnapshot is the result of a pre-paste clipboard read. Mirrors
// pkg/clipboard.Snapshot but is declared here so the output package does
// not import the clipboard pkg directly — consumer-side interface.
type ClipboardSnapshot struct {
	// MIME is the primary content type. Empty when Empty == true.
	MIME string
	// Data is the raw clipboard bytes for MIME. May be nil for empty.
	Data []byte
	// Empty signals there was nothing to snapshot.
	Empty bool
}

// ClipboardSnapshotter reads the current clipboard before transcript
// delivery so it can be restored after the autopaste keystroke. Opt-in
// dependency — nil disables snapshot/restore and Deliver behaves as
// before (transcript replaces clipboard contents permanently).
type ClipboardSnapshotter interface {
	Snapshot(ctx context.Context) (ClipboardSnapshot, error)
}

// ClipboardTypedCopier writes raw bytes back to the clipboard with an
// explicit MIME type. Used only by the restore path; the transcript
// itself goes through ClipboardDelivery.
type ClipboardTypedCopier interface {
	CopyTyped(ctx context.Context, mime string, data []byte) error
}

// defaultPasteDelay is the brief pause between writing to the clipboard
// and dispatching Ctrl+V. Both wl-copy and Wayland compositors hand off
// the selection asynchronously; with no delay the keystroke may fire
// against a stale clipboard and paste the *previous* contents. 50ms is
// well under the human-perceivable threshold and is the value GNOME's
// own clipboard tooling uses internally.
const defaultPasteDelay = 50 * time.Millisecond

// restoreDelay is the pause between the autopaste keystroke and writing
// the previous clipboard payload back. wtype/ydotool return after the
// kernel/compositor accepts the events, but the target application
// reads the selection asynchronously — restoring too early hands the
// previous payload to the focused window instead of the transcript.
// 250ms is well above worst-case observed read latency on GNOME/KDE
// and small enough that the user does not see clipboard managers flash
// twice.
const restoreDelay = 250 * time.Millisecond

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
	// snapshotter and restorer enable the optional save/restore-previous
	// clipboard flow. Both must be non-nil for the flow to run; either nil
	// disables snapshot/restore entirely (Deliver behaves as before).
	snapshotter ClipboardSnapshotter
	restorer    ClipboardTypedCopier
	delay       time.Duration
	log         *slog.Logger

	// preMu guards preSnap. preSnap is populated by PreSnapshot and
	// consumed by takeSnapshot; protects the read-then-clear pattern
	// without coupling cycle ordering to a specific goroutine.
	preMu   sync.Mutex
	preSnap <-chan ClipboardSnapshot
}

// preSnapshotWaitGrace caps how long takeSnapshot blocks waiting on the
// background pre-snapshot if it has not finished yet. The pre-snapshot
// starts at recording-start, so by the time Deliver is called it should
// be ready instantly. The cap is a defensive fallback for unusual cases
// (very short recordings, slow wl-paste) so we never block longer than
// the synchronous path would have.
const preSnapshotWaitGrace = 100 * time.Millisecond

// NewClipboardAutopasteOutput wires a clipboard-delivery step with an
// autopaste step. delivery and paster are required; logger may be nil.
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

// WithClipboardRestore returns a new ClipboardAutopasteOutput that also
// snapshots the clipboard before delivery and restores it after the
// autopaste keystroke. Either argument nil → returns the receiver
// unchanged; this lets the factory call it unconditionally and the
// runtime config decide.
//
// Restore is best-effort: snapshot failure → WARN + skip restore;
// restore failure → WARN, transcript stays in clipboard. Race-guard:
// before restoring, the current clipboard is re-read; if it no longer
// matches the transcript we just wrote, the user has copied something
// new in the meantime and we leave it alone.
func (o *ClipboardAutopasteOutput) WithClipboardRestore(
	snap ClipboardSnapshotter, restorer ClipboardTypedCopier,
) *ClipboardAutopasteOutput {
	if o == nil || snap == nil || restorer == nil {
		return o
	}

	// Field-by-field copy — direct struct dereference would copy the
	// preMu sync.Mutex, which the vet copylocks check (rightly) rejects.
	return &ClipboardAutopasteOutput{
		delivery:    o.delivery,
		autopaster:  o.autopaster,
		snapshotter: snap,
		restorer:    restorer,
		delay:       o.delay,
		log:         o.log,
	}
}

// Deliver runs the clipboard → paste (→ restore-previous) pipeline.
// Returns nil as long as the clipboard stage produced a recoverable
// copy of the text. The restore stage is best-effort and never raises.
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

	prev := o.takeSnapshot(ctx)

	if err := o.delivery.Deliver(ctx, text); err != nil {
		return fmt.Errorf("clipboard autopaste: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("clipboard autopaste: %w", err)
	}

	if err := o.pasteAfterDelay(ctx, text); err != nil {
		return err
	}

	o.maybeRestore(ctx, text, prev)

	return nil
}

// PreSnapshot kicks off the clipboard snapshot in the background so the
// result is ready by the time Deliver is called. Voice.UseCase invokes
// this at the start of recording — the snapshot then runs in parallel
// with the ~few-second-long capture phase, hiding wl-paste's 300ms
// round-trip behind work we were going to do anyway.
//
// Calling twice before Deliver replaces the in-flight snapshot. Safe
// to call when no snapshotter is wired (no-op) or when restore is
// disabled (the channel is harvested but discarded).
func (o *ClipboardAutopasteOutput) PreSnapshot(ctx context.Context) {
	if o == nil || o.snapshotter == nil || o.restorer == nil {
		return
	}

	ch := make(chan ClipboardSnapshot, 1)

	go func() {
		snap, err := o.snapshotter.Snapshot(ctx)
		if err != nil {
			o.logger().Debug("voice: pre-snapshot failed, will retry sync at deliver",
				slog.String("err", err.Error()))

			ch <- ClipboardSnapshot{}

			return
		}

		ch <- snap
	}()

	o.preMu.Lock()
	o.preSnap = ch
	o.preMu.Unlock()
}

// takeSnapshot returns the clipboard snapshot to restore after autopaste.
// Prefers the pre-snapshot started by PreSnapshot; falls back to a
// synchronous read if the pre-snapshot is unavailable or did not finish
// within preSnapshotWaitGrace.
//
// Returns a zero ClipboardSnapshot on any failure or when no snapshotter
// is configured — the restore step short-circuits on Empty/MIME=="".
func (o *ClipboardAutopasteOutput) takeSnapshot(ctx context.Context) ClipboardSnapshot {
	if o.snapshotter == nil || o.restorer == nil {
		return ClipboardSnapshot{}
	}

	if snap, ok := o.consumePreSnapshot(); ok {
		return snap
	}

	snap, err := o.snapshotter.Snapshot(ctx)
	if err != nil {
		o.logger().Warn("voice: clipboard snapshot failed, restore disabled for this cycle",
			slog.String("err", err.Error()))

		return ClipboardSnapshot{}
	}

	return snap
}

// consumePreSnapshot drains the pre-snapshot channel if one is pending.
// Returns (snap, true) when the background snapshot finished in time;
// (zero, false) when no pre-snapshot is available OR the wait grace
// expired (in which case the caller falls back to a synchronous read).
//
// The channel is always cleared so a stale pre-snapshot cannot leak into
// the next cycle.
func (o *ClipboardAutopasteOutput) consumePreSnapshot() (ClipboardSnapshot, bool) {
	o.preMu.Lock()
	ch := o.preSnap
	o.preSnap = nil
	o.preMu.Unlock()

	if ch == nil {
		return ClipboardSnapshot{}, false
	}

	select {
	case snap := <-ch:
		return snap, true
	case <-time.After(preSnapshotWaitGrace):
		o.logger().Debug("voice: pre-snapshot not ready in time, falling back to sync read")

		return ClipboardSnapshot{}, false
	}
}

// maybeRestore writes the previous clipboard payload back, guarded by a
// race-check: if the clipboard no longer holds our transcript, the user
// has copied something else in the meantime and we leave them alone.
//
// The guard is best-effort. snapshotter.Snapshot may legitimately fail
// here (transient compositor hiccup) — in that case we err on the side
// of "do not clobber" and skip restore.
func (o *ClipboardAutopasteOutput) maybeRestore(ctx context.Context, transcript string, prev ClipboardSnapshot) {
	if !o.canRestore(prev) {
		return
	}

	if !o.waitRestoreDelay(ctx) {
		return
	}

	if !o.guardClipboard(ctx, transcript) {
		return
	}

	o.runRestore(ctx, prev)
}

// canRestore short-circuits when the restore stage was not wired or
// when there is nothing meaningful to write back.
func (o *ClipboardAutopasteOutput) canRestore(prev ClipboardSnapshot) bool {
	if o.snapshotter == nil || o.restorer == nil {
		return false
	}

	if prev.Empty || prev.MIME == "" || len(prev.Data) == 0 {
		return false
	}

	return true
}

// waitRestoreDelay waits restoreDelay or until ctx is cancelled. Returns
// false on cancellation so the caller skips restore.
func (o *ClipboardAutopasteOutput) waitRestoreDelay(ctx context.Context) bool {
	timer := time.NewTimer(restoreDelay)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// guardClipboard re-reads the clipboard and returns true only when it
// still holds the transcript we just wrote. Any other state — a foreign
// payload, an empty clipboard, a read failure — means it is unsafe to
// clobber whatever is there now.
func (o *ClipboardAutopasteOutput) guardClipboard(ctx context.Context, transcript string) bool {
	current, err := o.snapshotter.Snapshot(ctx)
	if err != nil {
		o.logger().Warn("voice: clipboard race-check failed, skipping restore",
			slog.String("err", err.Error()))

		return false
	}

	if !sameAsTranscript(current, transcript) {
		o.logger().Debug("voice: clipboard changed since paste, skipping restore",
			slog.String("current_mime", current.MIME))

		return false
	}

	return true
}

func (o *ClipboardAutopasteOutput) runRestore(ctx context.Context, prev ClipboardSnapshot) {
	if err := o.restorer.CopyTyped(ctx, prev.MIME, prev.Data); err != nil {
		o.logger().Warn("voice: clipboard restore failed",
			slog.String("mime", prev.MIME),
			slog.Int("bytes", len(prev.Data)),
			slog.String("err", err.Error()),
		)

		return
	}

	o.logger().Debug("voice: clipboard restored",
		slog.String("mime", prev.MIME),
		slog.Int("bytes", len(prev.Data)),
	)
}

// sameAsTranscript reports whether snap holds (only) the transcript we
// just wrote. Comparison is byte-exact against the UTF-8 encoded
// transcript so trailing whitespace differences are not silently
// normalised away.
func sameAsTranscript(snap ClipboardSnapshot, transcript string) bool {
	if snap.Empty {
		return false
	}

	return bytes.Equal(snap.Data, []byte(transcript))
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
