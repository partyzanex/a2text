package voice

import (
	"log/slog"

	"github.com/partyzanex/a2text/internal/domain"
)

const cycleBaseAttrs = 2

// CycleAttrs returns a slog.Attr that nests completed-cycle fields under the
// "voice" key. The transcript text and text_len are intentionally absent from
// the default output — pass slog.Int("text_len", len(result.Text)) via extra
// only when cfg.Privacy.LogTranscript is true.
//
// Usage:
//
//	log.Info("cycle completed", voice.CycleAttrs(result), slog.String("provider", p))
//
//	// with optional text_len:
//	log.Info("cycle completed",
//	    voice.CycleAttrs(result, slog.Int("text_len", len(result.Text))),
//	    slog.String("provider", p),
//	)
func CycleAttrs(result domain.CycleResult, extra ...slog.Attr) slog.Attr {
	args := make([]any, 0, cycleBaseAttrs+len(extra))
	args = append(args,
		slog.Duration("audio_duration_est", result.AudioDuration),
		slog.Duration("stt_duration", result.STTDuration),
	)

	for i := range extra {
		args = append(args, extra[i])
	}

	return slog.Group("voice", args...)
}
