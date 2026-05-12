package domain

import (
	"fmt"
	"time"
)

// CyclePhase identifies which step of the dictation pipeline produced an
// error. Used by the daemon to feed the right event into the state machine
// (a recording-phase failure is not the same as a transcription failure).
type CyclePhase string

const (
	PhaseRecord     CyclePhase = "record"
	PhaseTranscribe CyclePhase = "transcribe"
	PhaseDeliver    CyclePhase = "deliver"
)

// RecordOpts describes how the daemon wants this cycle to capture audio.
type RecordOpts struct {
	// MaxDuration caps the recording. The daemon's state machine separately
	// owns a timer to fire EventTimeout if the user does not toggle off in
	// time; this Duration is the belt-and-braces inside Recorder.
	MaxDuration time.Duration
}

// CycleResult is what one dictation cycle produced. Returned to the daemon
// so it can feed the right event into the state machine.
type CycleResult struct {
	// Text is the trimmed transcription that was delivered.
	Text string

	// AudioDuration is a rough estimate of the recorded audio duration,
	// derived from the WAV file size (assumes 16kHz mono s16le = 32000 B/s).
	// Recordings shorter than 1 s appear as 0; WAV header bytes contribute
	// ~1.4 ms of error. Use for logging only — not a precise wall-clock.
	AudioDuration time.Duration

	// STTDuration is how long the transcription step took.
	STTDuration time.Duration
}

// CycleError attaches a phase tag to a pipeline failure. The state machine
// can switch on Phase to decide whether the daemon should land in
// StateError vs surface a softer status.
type CycleError struct {
	Err   error
	Phase CyclePhase
}

func (e *CycleError) Error() string {
	if e == nil {
		return "voice: <nil CycleError>"
	}

	if e.Err == nil {
		return fmt.Sprintf("voice: %s: <nil>", e.Phase)
	}

	return fmt.Sprintf("voice: %s: %s", e.Phase, e.Err.Error())
}

func (e *CycleError) Unwrap() error {
	if e == nil {
		return nil
	}

	return e.Err
}
