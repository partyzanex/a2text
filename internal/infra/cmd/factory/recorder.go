package factory

import (
	"fmt"
	"log/slog"

	"github.com/partyzanex/a2text/internal/usecases/voice"
	"github.com/partyzanex/a2text/pkg/capture"
)

// BuildRecorder wires a microphone capture backend. Stage I.1 only ships
// the subprocess backend (pw-record / parecord); future stages may swap in
// a CGo PortAudio implementation behind the same interface.
//
// Returning voice.Recorder (not *capture.SubprocessRecorder) keeps RunRecord
// testable — wiring tests can inject a fake without touching the real
// subprocess factory.
//
//nolint:ireturn // factory must return the interface
func BuildRecorder(log *slog.Logger) (voice.Recorder, error) {
	r, err := capture.NewSubprocessRecorder(log)
	if err != nil {
		return nil, fmt.Errorf("recorder: %w", err)
	}

	return r, nil
}
