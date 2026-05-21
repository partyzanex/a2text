package domain

import "errors"

// Sentinel errors for the voice dictation domain. IPC- and infrastructure-
// specific errors live next to their respective adapters/infrastructure
// (e.g. ipc.ErrVersionMismatch, depcheck.DependencyMissingError).
var (
	// ErrEmptyResult is returned when the transcriber produced no text.
	// Treated as an error so the caller knows the audio yielded nothing
	// instead of silently succeeding with an empty string.
	ErrEmptyResult = errors.New("voice: empty transcription result")

	// ErrFileNotFound is returned when the input audio file does not exist.
	ErrFileNotFound = errors.New("voice: input file not found")

	// ErrNotRegularFile is returned when the input path exists but is not a
	// regular file (directory, socket, device, etc.).
	ErrNotRegularFile = errors.New("voice: input is not a regular file")

	// ErrBusy is returned by the state machine when an event arrives in a
	// state that cannot service it (e.g. toggle during transcription).
	// Hotkey/tray dispatchers surface this as a no-op rather than a hard
	// failure.
	ErrBusy = errors.New("voice: daemon is busy")

	// ErrProviderUnavailable is returned when the chosen STT provider
	// cannot service requests (network failure, model not loaded, etc.).
	ErrProviderUnavailable = errors.New("voice: stt provider unavailable")

	// ErrCaptureUnavailable is returned when no microphone capture backend
	// can be initialised. The CLI prints the install hint.
	ErrCaptureUnavailable = errors.New("voice: audio capture unavailable")

	// ErrCycleInFlight is returned by the cycle trigger when a new
	// cycle is requested while another one is already running. The
	// gRPC adapter surfaces this as FAILED_PRECONDITION.
	ErrCycleInFlight = errors.New("voice: recording cycle in flight")

	// ErrUnknownEvent is returned when the event string is not part of the
	// declared Event vocabulary at all. Distinct from ErrInvalidEventForState
	// (event is known but the current state does not accept it) and from
	// ErrBusy (event is known and accepted but rejected with a busy
	// signal). IPC clients can differentiate these via ErrorCode.
	ErrUnknownEvent = errors.New("voice: unknown event")

	// ErrInvalidEventForState is returned when an event is part of the Event
	// vocabulary but has no transition defined for the current state — e.g.
	// EventTranscribeDone while StateIdle. Surfacing this separately from
	// ErrUnknownEvent lets callers distinguish "client sent garbage" from
	// "internal pipeline emitted a late event after a state change".
	ErrInvalidEventForState = errors.New("voice: event invalid for current state")
)
