package voice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/partyzanex/a2text/internal/domain"
)

// Recording defaults are defined in domain/audio.go as exported constants
// (domain.DefaultRecordSampleRate, domain.DefaultRecordChannels).

// VoiceUseCase chains record → transcribe → deliver for a single
// dictation cycle. It owns the temp WAV file the recorder produces and
// cleans it up after delivery.
//
// VoiceUseCase intentionally has NO state: it does not know what state
// the daemon is in, it does not run a state machine, and it does not
// listen for IPC. The daemon owns all that. This use case is just the
// "do one cycle" arrow.
type VoiceUseCase struct {
	recorder    Recorder
	transcriber Transcriber
	output      Output
	archiver    KeptAudioArchiver
	log         *slog.Logger
}

// KeptAudioArchiver, when wired, takes a copy (or transcoded version)
// of the recorded WAV after a successful transcription. Returning
// quickly is important — Cycle blocks on it before the temp file is
// removed. Implementations that need to do heavy CPU work should
// either accept that latency or hand off to a worker themselves.
type KeptAudioArchiver interface {
	Archive(ctx context.Context, audioPath string) (savedPath string, err error)
}

// NewVoiceUseCase wires the dependencies. Recorder, Transcriber, and Output
// are required — passing nil panics at construction so misconfiguration surfaces
// immediately. A nil log is accepted and replaced with a discard handler,
// consistent with the nil-log policy used by other voice components.
func NewVoiceUseCase(
	recorder Recorder, transcriber Transcriber, output Output, log *slog.Logger,
) *VoiceUseCase {
	if recorder == nil || transcriber == nil || output == nil {
		panic("voice: NewVoiceUseCase: recorder, transcriber and output are required")
	}

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &VoiceUseCase{
		recorder:    recorder,
		transcriber: transcriber,
		output:      output,
		log:         log,
	}
}

// SwapTranscriber atomically replaces the current Transcriber with a
// new one. Safe to call concurrently with Cycle: a cycle in flight
// holds the previous transcriber on its goroutine stack and finishes
// against it; the swap only affects subsequent cycles. The voice use
// case does not own the previous transcriber's lifecycle — callers
// are responsible for Close()ing it after the swap.
//
// Used by the daemon when the user changes STT-affecting settings in
// the live UI; pre-existing wiring built the transcriber once at
// startup, which made provider switches require a restart.
func (uc *VoiceUseCase) SwapTranscriber(next Transcriber) {
	if uc == nil || next == nil {
		return
	}

	uc.transcriber = next
}

// AttachArchiver wires (or rewires) the optional kept-audio archiver.
// Nil disables archiving. Safe to call before Cycle is ever invoked;
// not safe to call concurrently with Cycle — the daemon constructs the
// use-case during startup, long before the first event.
func (uc *VoiceUseCase) AttachArchiver(a KeptAudioArchiver) {
	if uc == nil {
		return
	}

	uc.archiver = a
}

// Cycle runs one record → transcribe → deliver pass with two contexts:
//
//   - recordCtx aborts the recording phase. Cancelling it (e.g. user
//     toggle-off) makes the recorder stop gracefully and Cycle proceeds
//     to transcription with whatever audio was captured.
//   - ctx aborts the whole cycle. Cancelling it (shutdown / discard)
//     stops everything; recordCtx is expected to be derived from ctx so
//     the same cancellation reaches the recorder.
//
// Pipeline errors are wrapped as *domain.CycleError with a phase tag so the
// caller can distinguish recording vs transcription vs delivery faults.
//
// Ownership: the recorder MUST return a freshly-created temp file (see
// Recorder interface contract). Cycle always deletes that file after the
// pipeline completes. Callers that want to keep the audio (privacy.keep_audio)
// must handle that at the session level, not inside Cycle.
//
// Cycle is nil-safe for the receiver: a nil VoiceUseCase returns an error
// rather than panicking. NewVoiceUseCase already rejects nil dependencies, so
// in practice this guard fires only when callers forgot to construct the
// use-case at all (typically a wiring bug surfaced in tests).
func (uc *VoiceUseCase) Cycle(
	ctx, recordCtx context.Context, opts domain.RecordOpts, lang string,
) (domain.CycleResult, error) {
	if uc == nil {
		return domain.CycleResult{}, errors.New("voice: Cycle called on nil VoiceUseCase")
	}

	lang = strings.TrimSpace(lang)

	if err := domain.ValidateCycleArgs(ctx, recordCtx, opts, lang); err != nil {
		return domain.CycleResult{}, fmt.Errorf("voice: cycle args: %w", err)
	}

	audioPath, audioSize, err := uc.recordAndValidate(recordCtx, opts)
	if err != nil {
		return domain.CycleResult{}, err
	}

	defer func() {
		// Archive BEFORE removing the temp file. The archiver runs on
		// the cycle ctx (not recordCtx) so toggling off during STT
		// does not kill the archive copy; the daemon ctx still does
		// at shutdown.
		uc.runArchiver(ctx, audioPath)

		if rmErr := os.Remove(audioPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			uc.log.Warn("voice: temp recording cleanup failed",
				slog.String("file", filepath.Base(audioPath)),
				slog.Any("err", rmErr),
			)
		}
	}()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return domain.CycleResult{}, &domain.CycleError{Phase: domain.PhaseTranscribe, Err: ctxErr}
	}

	return uc.transcribeAndDeliver(ctx, audioPath, audioSize, lang)
}

// runArchiver invokes the kept-audio archiver if one is wired,
// logging — but not propagating — any error. Archival is a privacy
// nicety; failing it must not affect the user-visible STT result.
func (uc *VoiceUseCase) runArchiver(ctx context.Context, audioPath string) {
	if uc.archiver == nil {
		return
	}

	savedPath, err := uc.archiver.Archive(ctx, audioPath)
	if err != nil {
		uc.log.Warn("voice: kept-audio archive failed",
			slog.String("source", filepath.Base(audioPath)),
			slog.Any("err", err),
		)

		return
	}

	// Empty savedPath means the archiver short-circuited (KeepAudio is
	// off). Don't pollute INFO logs with a misleading "archived" entry
	// in that case — only log when something was actually written.
	if savedPath == "" {
		return
	}

	uc.log.Info("voice: kept-audio archived",
		slog.String("path", savedPath),
	)
}

// transcribeAndDeliver runs the STT call and output delivery for Cycle.
func (uc *VoiceUseCase) transcribeAndDeliver(
	ctx context.Context,
	audioPath string,
	audioSize int64,
	lang string,
) (domain.CycleResult, error) {
	transcribeStart := time.Now()
	text, err := uc.transcriber.Transcribe(ctx, audioPath, lang)
	sttDuration := time.Since(transcribeStart)

	if err != nil {
		return domain.CycleResult{}, &domain.CycleError{Phase: domain.PhaseTranscribe, Err: err}
	}

	text = strings.TrimSpace(text)
	if text == "" {
		return domain.CycleResult{}, &domain.CycleError{Phase: domain.PhaseTranscribe, Err: domain.ErrEmptyResult}
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return domain.CycleResult{}, &domain.CycleError{Phase: domain.PhaseDeliver, Err: ctxErr}
	}

	if deliverErr := uc.output.Deliver(ctx, text); deliverErr != nil {
		return domain.CycleResult{}, &domain.CycleError{Phase: domain.PhaseDeliver, Err: deliverErr}
	}

	return domain.CycleResult{
		Text:          text,
		AudioDuration: domain.EstimateAudioDuration(audioSize),
		STTDuration:   sttDuration,
	}, nil
}

// recordAndValidate handles the record and validation phase, returning the audio path,
// its size, and any error. Extracted from Cycle to reduce cyclomatic complexity.
func (uc *VoiceUseCase) recordAndValidate(
	recordCtx context.Context,
	opts domain.RecordOpts,
) (path string, size int64, _ error) {
	audioPath, err := uc.recorder.RecordToFile(recordCtx, RecordOptions{
		Duration:   opts.MaxDuration,
		SampleRate: domain.DefaultRecordSampleRate,
		Channels:   domain.DefaultRecordChannels,
	})
	if err != nil {
		return "", 0, &domain.CycleError{Phase: domain.PhaseRecord, Err: err}
	}

	if audioPath == "" {
		return "", 0, &domain.CycleError{
			Phase: domain.PhaseRecord,
			Err:   errors.New("recorder returned empty audio path"),
		}
	}

	audioSize, fileErr := domain.ValidateRecordedFile(audioPath)
	if fileErr != nil {
		return "", 0, &domain.CycleError{Phase: domain.PhaseRecord, Err: fileErr}
	}

	return audioPath, audioSize, nil
}
