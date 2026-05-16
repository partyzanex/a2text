package factory

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/partyzanex/a2text/internal/usecases/voice"
	"github.com/partyzanex/a2text/pkg/audio"
	"github.com/partyzanex/a2text/pkg/stt"
)

// silenceGate decorates a voice.Transcriber to short-circuit STT calls
// when the input WAV's RMS is below thresholdDBFS. Returns "" with nil
// error so the voice cycle treats the result as an empty transcription
// — matching the existing "user said nothing meaningful" path.
//
// Motivation: Whisper-family models hallucinate subtitle-style filler
// ("Спасибо за просмотр", "Редактор субтитров А.Семкин", "Продолжение
// следует…") on near-silent audio because their training corpora are
// dominated by YouTube transcripts. A simple RMS gate before STT
// eliminates the bulk of these false transcripts at near-zero cost.
type silenceGate struct {
	inner         voice.Transcriber
	thresholdDBFS float64
	log           *slog.Logger
	// rmsFn is a seam for tests; production wiring binds it to audio.RMSdBFS.
	rmsFn func(string) (float64, error)
}

// WrapWithSilenceGate decorates a voice.Transcriber with an RMS-based
// silence filter. thresholdDBFS == 0 disables the wrap and returns inner
// unchanged — call sites can pass cfg.Capture.SilenceThresholdDBFS
// verbatim without checking it themselves.
//
//nolint:ireturn // wraps + returns the voice.Transcriber contract (DIP, owned by usecase)
func WrapWithSilenceGate(inner voice.Transcriber, thresholdDBFS float64, log *slog.Logger) voice.Transcriber {
	if thresholdDBFS == 0 {
		return inner
	}

	gate := newSilenceGate(inner, thresholdDBFS, log)

	if log != nil {
		log.Info("voice: silence gate enabled",
			slog.Float64("threshold_dbfs", thresholdDBFS),
		)
	}

	// Preserve streaming capability through the gate. Otherwise the
	// daemon's reload path (which always wraps with the gate) would
	// strip the Stream method from a streaming-capable inner, and the
	// voice use-case would silently fall back to the file-based cycle.
	// The RMS check requires a WAV file and is therefore inapplicable
	// to live PCM — Stream simply passes through to inner.
	if streamer, ok := inner.(stt.StreamCapable); ok {
		return &streamingSilenceGate{silenceGate: gate, streamer: streamer}
	}

	return gate
}

// streamingSilenceGate adds a Stream pass-through to silenceGate so the
// method set survives the type assertion in voice.streamingCapableTranscriber.
// The silence-skip behaviour applies only to the file-based Transcribe
// path; live streaming bypasses the RMS check (no WAV to measure).
type streamingSilenceGate struct {
	*silenceGate

	streamer stt.StreamCapable
}

// Stream forwards to the streaming inner without an RMS check.
func (g *streamingSilenceGate) Stream(
	ctx context.Context, pcm io.Reader, lang string,
) (string, error) {
	text, err := g.streamer.Stream(ctx, pcm, lang)
	if err != nil {
		return text, fmt.Errorf("silence gate: stream: %w", err)
	}

	return text, nil
}

// newSilenceGate constructs the decorator. A nil inner Transcriber is a
// programming error (panic at construction, like NewVoiceUseCase) — gate
// only makes sense in front of a real STT.
func newSilenceGate(inner voice.Transcriber, thresholdDBFS float64, log *slog.Logger) *silenceGate {
	if inner == nil {
		panic("factory: silence_gate: inner Transcriber is required")
	}

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &silenceGate{
		inner:         inner,
		thresholdDBFS: thresholdDBFS,
		log:           log,
		rmsFn:         audio.RMSdBFS,
	}
}

// Transcribe computes the RMS of the WAV at audioPath and, if it sits
// below the configured threshold, returns "" without invoking the inner
// transcriber. RMS computation errors are logged but never block STT —
// the gate fails open so a corrupt WAV header does not break the cycle.
func (g *silenceGate) Transcribe(ctx context.Context, audioPath, lang string) (string, error) {
	dbfs, err := g.rmsFn(audioPath)
	if err != nil {
		g.log.Warn("voice: silence gate: RMS failed, proceeding to STT",
			slog.String("audio", audioPath),
			slog.Any("err", err),
		)

		text, innerErr := g.inner.Transcribe(ctx, audioPath, lang)
		if innerErr != nil {
			return text, fmt.Errorf("silence gate: inner transcribe: %w", innerErr)
		}

		return text, nil
	}

	if dbfs < g.thresholdDBFS {
		g.log.Info("voice: silence gate: audio below threshold, STT skipped",
			slog.Float64("rms_dbfs", dbfs),
			slog.Float64("threshold_dbfs", g.thresholdDBFS),
			slog.String("audio", audioPath),
		)

		return "", nil
	}

	text, err := g.inner.Transcribe(ctx, audioPath, lang)
	if err != nil {
		return text, fmt.Errorf("silence gate: inner transcribe: %w", err)
	}

	return text, nil
}
