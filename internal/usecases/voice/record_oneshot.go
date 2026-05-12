package voice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/partyzanex/a2text/internal/domain"
)

// RecordOneshotUseCase records a fixed-duration audio sample, transcribes it,
// and delivers the result via Output. It is a thin wrapper over VoiceUseCase
// for the one-shot CLI flow (no toggle / no state machine), which always
// records for the full requested duration with a single context governing the
// whole pipeline.
type RecordOneshotUseCase struct {
	uc *VoiceUseCase
}

// NewRecordOneshotUseCase wires the dependencies. All four are required —
// passing nil panics at construction (delegated to NewVoiceUseCase) rather
// than silently misbehaving.
func NewRecordOneshotUseCase(
	recorder Recorder, transcriber Transcriber, output Output, log *slog.Logger,
) *RecordOneshotUseCase {
	return &RecordOneshotUseCase{uc: NewVoiceUseCase(recorder, transcriber, output, log)}
}

// Run executes the record→transcribe→deliver pipeline once.
//
// duration must be positive; passing 0 returns an error rather than recording
// indefinitely — that semantics is reserved for the daemon flow (I.2), not
// the one-shot use case.
//
// lang is the ISO-639-1 hint passed to the transcriber. Must be non-empty;
// auto-detect is the daemon's responsibility, not the one-shot pipeline.
//
// Context model: the same ctx governs both recording and transcription —
// one-shot has no toggle to stop recording while letting STT finish, so
// independent cancellation buys nothing. Callers that need that split should
// use VoiceUseCase.Cycle directly with two contexts.
//
// The underlying VoiceUseCase wraps pipeline errors as *domain.CycleError; Run
// unwraps that one layer so callers see the original recorder/transcriber/
// output error directly (errors.Is keeps working either way).
func (uc *RecordOneshotUseCase) Run(ctx context.Context, duration time.Duration, lang string) error {
	if duration <= 0 {
		return fmt.Errorf("record duration must be positive (got %s)", duration)
	}

	if strings.TrimSpace(lang) == "" {
		return errors.New("voice: Run: lang must not be empty")
	}

	_, err := uc.uc.Cycle(ctx, ctx, domain.RecordOpts{MaxDuration: duration}, lang)
	if err == nil {
		return nil
	}

	// Unwrap *domain.CycleError so Run's caller sees the underlying recorder/
	// transcriber/output error directly. If domain.CycleError carries no wrapped
	// err (defensive: should not happen in practice), fall through to
	// returning it as-is so the caller can still inspect Phase.
	var ce *domain.CycleError
	if errors.As(err, &ce) && ce.Err != nil {
		return ce.Err
	}

	return err
}
