package output

import (
	"context"
	"fmt"
	"log/slog"
)

// LogOutput delivers transcripts as a single structured slog entry at DEBUG
// level. Used as the no-clipboard fallback when running headless or in
// systemd-managed environments where mixing plain-text transcripts into the
// daemon's stdout breaks JSON log consumers.
//
// Privacy contract: transcripts are user speech — potentially sensitive.
// Emitting at DEBUG (not INFO) means production deployments keep them out
// of the journal unless the operator explicitly raises `log_level: debug`
// in config. Matches the existing privacy.log_transcript posture for
// daemon cycle logs.
type LogOutput struct {
	log *slog.Logger
}

// NewLogOutput returns an Output that records the transcript via slog at
// DEBUG level. A nil logger is replaced with a discard handler so the
// constructor never returns an unsafe value.
func NewLogOutput(log *slog.Logger) *LogOutput {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &LogOutput{log: log}
}

// Deliver writes the transcript to slog at DEBUG with the text wrapped as
// a structured field. ctx is observed for cancellation before the log
// emission so a cancelled cycle does not leak a stray DEBUG line.
func (o *LogOutput) Deliver(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("log output: %w", err)
	}

	o.log.LogAttrs(ctx, slog.LevelDebug, "voice: transcript",
		slog.String("text", text),
	)

	return nil
}
