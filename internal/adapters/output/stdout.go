// Package output contains adapters that deliver transcription results
// to user-facing destinations (stdout, clipboard, autopaste).
package output

import (
	"context"
	"fmt"
	"io"
	"os"
)

// StdoutOutput writes the transcription text to a configured io.Writer
// (defaults to os.Stdout) followed by a newline.
type StdoutOutput struct {
	w io.Writer
}

// NewStdoutOutput returns an Output that prints text to os.Stdout.
func NewStdoutOutput() *StdoutOutput {
	return &StdoutOutput{w: os.Stdout}
}

// NewStdoutOutputWithWriter is a constructor used in tests to capture output.
func NewStdoutOutputWithWriter(w io.Writer) *StdoutOutput {
	return &StdoutOutput{w: w}
}

// Deliver writes text + "\n" to the underlying writer.
// The context is observed for cancellation before the write.
func (o *StdoutOutput) Deliver(ctx context.Context, text string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("stdout output: %w", err)
	}

	if _, err := fmt.Fprintln(o.w, text); err != nil {
		return fmt.Errorf("stdout deliver: %w", err)
	}

	return nil
}
