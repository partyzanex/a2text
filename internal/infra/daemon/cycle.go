package daemon

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/partyzanex/a2text/internal/domain"
	"github.com/partyzanex/a2text/internal/usecases/voice"
)

// startCycle kicks off a new dictation cycle in the background. It runs
// record→transcribe→deliver as a single op and feeds completion events
// into the state machine.
//
// A daemon-side timer enforces the recording cap independently of the
// recorder: even if the recorder honours MaxDuration and returns naturally,
// the SM stays in domain.StateRecording until something fires domain.EventTimeout. Without
// this timer a successfully-completed natural-finish cycle would try to
// Apply(domain.EventTranscribeDone) from domain.StateRecording and be rejected, leaving
// the daemon stuck in "recording" forever.
//
// Refuses to start if a cycle is already in flight (cycleCancel != nil) —
// the state machine should already prevent this via domain.ErrBusy, but a cheap
// guard here avoids leaking a goroutine if the SM ever has a regression.
func (d *Daemon) startCycle(parent context.Context) {
	cycleCtx, cycleCancel := context.WithCancel(parent)
	recordCtx, recordCancel := context.WithCancel(cycleCtx)

	d.cycleMu.Lock()

	if d.cycleCancel != nil {
		d.cycleMu.Unlock()
		cycleCancel()
		recordCancel()
		d.log.Warn("voice: startCycle invoked while cycle already running — state machine bug?")

		return
	}

	d.cycleCancel = cycleCancel
	d.recordingCancel = recordCancel

	d.cycleMu.Unlock()

	// Defensive: a misconfigured pickMaxRecord (or future config flag set
	// to 0/negative) would fire AfterFunc immediately and stop the user's
	// recording before it began. Normalise here so the timer always has a
	// sane positive duration regardless of how maxRecord was computed.
	maxRecord := d.maxRecord
	if maxRecord <= 0 {
		maxRecord = defaultMaxRecord
	}

	// time.AfterFunc fires in its own goroutine. The state machine
	// serialises Apply, so timer-vs-manual-toggle is decided at the SM
	// level: whoever calls Apply(domain.EventTimeout)/Apply(domain.EventToggle) first
	// transitions Recording→Transcribing; the other gets domain.ErrBusy.
	timeoutTimer := time.AfterFunc(maxRecord, func() {
		if _, _, err := d.machine.Apply(domain.EventTimeout); err != nil {
			// domain.State already moved out of Recording — a manual toggle
			// reached the SM before us. Nothing to do.
			return
		}

		// We won the race. Cancel the recording phase so the recorder
		// finalises its WAV; transcribe + deliver continue on cycleCtx.
		d.cancelRecordingPhase()
	})

	go func() {
		defer func() {
			// Stop is safe to call after the timer fired — returns false.
			// Doing it inside the goroutine that owns the cycle keeps the
			// timer's lifetime bounded by the cycle's lifetime.
			timeoutTimer.Stop()

			d.cycleMu.Lock()
			d.cycleCancel = nil
			d.recordingCancel = nil
			d.cycleMu.Unlock()

			recordCancel()
			cycleCancel()
		}()

		result, cycleErr := d.useCase.Cycle(
			cycleCtx, recordCtx,
			domain.RecordOpts{MaxDuration: maxRecord},
			d.cfg.Language,
		)
		if cycleErr != nil {
			d.handleCycleError(cycleErr)

			return
		}

		d.advanceCycleSuccess(result)
	}()
}

// advanceCycleSuccess is the success-path continuation after Cycle returns.
// It logs the transcript when configured, bridges out of domain.StateRecording if
// necessary, then advances the state machine through TranscribeDone →
// DeliverDone. Errors on state machine transitions are logged and the method
// returns early — the goroutine that called this has nothing useful to do if
// the SM rejects a post-cycle event.
func (d *Daemon) advanceCycleSuccess(result domain.CycleResult) {
	var textLen []slog.Attr
	if d.cfg.Privacy.LogTranscript && result.Text != "" {
		// text_len is gated on LogTranscript: even the length of a transcription
		// can be sensitive in strict-privacy deployments.
		textLen = []slog.Attr{slog.Int("text_len", len(result.Text))}
	}

	d.log.Info("voice: cycle completed",
		voice.CycleAttrs(result, textLen...),
		slog.String("provider", d.cfg.Provider),
	)

	if d.cfg.Privacy.LogTranscript && result.Text != "" {
		// Emit the full transcript at DEBUG so it appears in dev logs without
		// polluting INFO-level journal entries.
		d.log.Debug("voice: transcript",
			slog.String("model", d.cfg.GoWhisper.Model),
			slog.String("text", result.Text),
		)
	}

	// Bridge: if the recorder finished naturally at MaxDuration before
	// either the daemon timer or a manual toggle moved the SM, we are
	// still in domain.StateRecording. Drive the SM through domain.EventTimeout so
	// the upcoming domain.EventTranscribeDone is valid.
	//
	// Do NOT pre-check State() — it and Apply are not atomic. Apply
	// itself validates the transition; domain.ErrBusy means the SM already
	// moved (timer or manual toggle won the race), which is fine.
	if _, _, applyErr := d.machine.Apply(domain.EventTimeout); applyErr != nil && !errors.Is(applyErr, domain.ErrBusy) {
		d.log.Warn("voice: post-cycle bridge to transcribing rejected",
			slog.Any("err", applyErr),
		)

		return
	}

	if _, _, applyErr := d.machine.Apply(domain.EventTranscribeDone); applyErr != nil {
		d.log.Warn("voice: cycle done but state rejected",
			slog.Any("err", applyErr),
		)

		return
	}

	// Output already happened inside Cycle; jump straight to delivered.
	if _, _, applyErr := d.machine.Apply(domain.EventDeliverDone); applyErr != nil {
		d.log.Warn("voice: deliver-done bookkeeping rejected",
			slog.Any("err", applyErr),
		)
	}
}

// cancelRecordingPhase cancels the recording sub-context only. Transcribe
// and deliver continue with the surviving cycle ctx.
func (d *Daemon) cancelRecordingPhase() {
	d.cycleMu.Lock()
	defer d.cycleMu.Unlock()

	if d.recordingCancel != nil {
		d.recordingCancel()
	}
}

func (d *Daemon) cancelCycle() {
	d.cycleMu.Lock()
	defer d.cycleMu.Unlock()

	if d.cycleCancel != nil {
		d.cycleCancel()
	}
}

func (d *Daemon) handleCycleError(err error) {
	if errors.Is(err, domain.ErrEmptyResult) {
		d.log.Info("voice: cycle produced empty transcription")

		// Bridge past domain.StateRecording first: Cycle errors short-circuit
		// before the success-path bridge runs, so the SM is typically
		// still in domain.StateRecording even though the recording phase is over.
		// domain.EventEmptyResult is only valid from domain.StateTranscribing — without
		// this bridge the daemon would stay in "recording" until the next
		// manual toggle.
		d.bridgeOutOfRecording("empty-result")

		// Empty result is not a failure: STT succeeded, the audio simply
		// had no speech. Skip domain.StateDelivering entirely via domain.EventEmptyResult
		// — going through delivering with no real text would mislead
		// sd_notify/IPC about what the daemon is doing.
		if _, _, applyErr := d.machine.Apply(domain.EventEmptyResult); applyErr != nil {
			d.log.Warn("voice: empty-result event rejected",
				slog.Any("err", applyErr),
			)
		}

		return
	}

	// Phase-aware logging: which step actually failed matters for triage.
	var cycleErr *domain.CycleError
	if errors.As(err, &cycleErr) {
		d.log.Warn("voice: cycle failed",
			slog.String("phase", string(cycleErr.Phase)),
			slog.Any("err", cycleErr.Err),
		)
	} else {
		d.log.Warn("voice: cycle failed", slog.Any("err", err))
	}

	// Phase-aware SM routing. Cycle errors short-circuit before the success
	// path's bridge runs, so the SM is still in domain.StateRecording regardless of
	// which phase actually failed:
	//
	//   - domain.PhaseRecord  → domain.EventRecordFailed: Recording → Error directly.
	//   - other phases → domain.EventRecordFailed semantically wrong (we DID get
	//     audio); bridge through domain.EventTimeout to Transcribing, then
	//     domain.EventTranscribeFailed → Error.
	//
	// Without this routing, ApplyWithError(domain.EventTranscribeFailed) from
	// domain.StateRecording is treated as a late/stale event and rejected,
	// leaving the daemon wedged in "recording" forever.
	if cycleErr != nil && cycleErr.Phase == domain.PhaseRecord {
		if _, _, applyErr := d.machine.ApplyWithError(domain.EventRecordFailed, err.Error()); applyErr != nil {
			d.log.Warn("voice: failed to record capture failure in state machine",
				slog.Any("err", applyErr),
			)
		}

		return
	}

	// Non-record-phase failure: bridge past domain.StateRecording first.
	d.bridgeOutOfRecording("transcribe/deliver-failure")

	if _, _, applyErr := d.machine.ApplyWithError(domain.EventTranscribeFailed, err.Error()); applyErr != nil {
		d.log.Warn("voice: failed to record cycle error in state machine",
			slog.Any("err", applyErr),
		)
	}
}

// bridgeOutOfRecording advances the SM out of domain.StateRecording via
// domain.EventTimeout when an error-path event arrives before the success-path
// bridge ran. Cycle errors short-circuit `startCycle`'s goroutine before
// the post-cycle Apply(domain.EventTimeout), so the SM may still be in Recording
// when handleCycleError fires; without this bridge the follow-up
// domain.EventTranscribeFailed / domain.EventEmptyResult would be rejected as
// "known event invalid for current state" and the daemon would wedge.
//
// `domain.ErrBusy` is expected here: a parallel manual toggle (or the
// daemon-side timer) may have already moved the SM. Log only non-busy
// rejections so the journal stays clean during normal races.
func (d *Daemon) bridgeOutOfRecording(reason string) {
	// Do NOT pre-check State() — it and Apply are not atomic.
	// domain.ErrBusy from Apply means the SM already moved; that is the expected
	// race outcome and must not be logged as an error.
	_, _, applyErr := d.machine.Apply(domain.EventTimeout)
	if applyErr == nil || errors.Is(applyErr, domain.ErrBusy) {
		return
	}

	d.log.Warn("voice: bridge to transcribing rejected",
		slog.String("reason", reason),
		slog.Any("err", applyErr),
	)
}
