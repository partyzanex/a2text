package voice

import (
	"errors"
	"fmt"
	"sync"

	"github.com/partyzanex/a2text/internal/domain"
)

// isKnownEvent reports whether e is part of the declared domain.Event vocabulary.
// Keep this in sync with the const block above.
// errEventNotHandled is a sentinel error returned by transition helper functions
// when an event is not handled by a state. The main Transition function uses this
// to distinguish between unknown events and invalid events for the current state.
var errEventNotHandled = errors.New("event not handled")

func isKnownEvent(e domain.Event) bool {
	switch e {
	case domain.EventToggle, domain.EventStart, domain.EventStop, domain.EventTimeout, domain.EventRecordFailed,
		domain.EventTranscribeDone, domain.EventTranscribeFailed, domain.EventEmptyResult,
		domain.EventDeliverDone, domain.EventShutdown:
		return true
	}

	return false
}

// Transition is the pure transition table. Returns the next state, the
// action the daemon should perform, and an error if the event is invalid
// or rejected.
//
// Side effects (start mic, run STT, deliver to clipboard, log) live in the
// Daemon and are dispatched by the returned Action.
func transitionFromIdle(event domain.Event) (domain.State, domain.Action, error) {
	// Late completion events from a cancelled/discarded cycle.
	// Not a programming error, just a race the caller can ignore.
	return transitionToRecordingOrReject(domain.StateIdle, event)
}

func transitionFromRecording(event domain.Event) (domain.State, domain.Action, error) {
	switch event {
	case domain.EventToggle, domain.EventStop, domain.EventTimeout:
		return domain.StateTranscribing, domain.ActionStopRecording, nil
	case domain.EventStart:
		return domain.StateRecording, domain.ActionNone, domain.ErrBusy
	case domain.EventRecordFailed:
		// Capture itself broke (mic gone, subprocess crashed, encoder
		// failed). Skip transcribe/deliver entirely — there is no
		// audio to process — and land in domain.StateError so the next
		// toggle can either retry or surface the message.
		return domain.StateError, domain.ActionNone, nil
	case domain.EventEmptyResult, domain.EventTranscribeDone, domain.EventTranscribeFailed, domain.EventDeliverDone:
		// Pipeline-completion events that arrived while a NEW cycle is
		// already in its recording phase. Stale; ignore.
		return domain.StateRecording, domain.ActionNone, domain.ErrBusy
	case domain.EventShutdown:
		return domain.StateShuttingDown, domain.ActionDiscardAudio, nil
	}

	return domain.StateRecording, domain.ActionNone, errEventNotHandled
}

func transitionFromTranscribing(event domain.Event) (domain.State, domain.Action, error) {
	switch event {
	case domain.EventToggle, domain.EventStart, domain.EventStop:
		return domain.StateTranscribing, domain.ActionNone, domain.ErrBusy
	case domain.EventTimeout, domain.EventRecordFailed:
		// Late events from the prior recording phase that fired while
		// we were already transcribing. Drop silently.
		return domain.StateTranscribing, domain.ActionNone, domain.ErrBusy
	case domain.EventTranscribeDone:
		// Cycle already delivered the text inside the use case; this
		// transition exists purely so sd_notify briefly shows
		// "delivering" before the daemon fires domain.EventDeliverDone.
		return domain.StateDelivering, domain.ActionNone, nil
	case domain.EventTranscribeFailed:
		return domain.StateError, domain.ActionNone, nil
	case domain.EventEmptyResult:
		// STT succeeded but produced no text. Skip the delivery phase
		// entirely — there is nothing to put on the clipboard. Going
		// through domain.StateDelivering with a placeholder would lie to
		// sd_notify/IPC about what the daemon is doing.
		return domain.StateIdle, domain.ActionFinishCycle, nil
	case domain.EventDeliverDone:
		// Late event from the prior delivery phase.
		return domain.StateTranscribing, domain.ActionNone, domain.ErrBusy
	case domain.EventShutdown:
		return domain.StateShuttingDown, domain.ActionShutdownNow, nil
	}

	return domain.StateTranscribing, domain.ActionNone, errEventNotHandled
}

func transitionFromDelivering(event domain.Event) (domain.State, domain.Action, error) {
	switch event {
	case domain.EventDeliverDone:
		return domain.StateIdle, domain.ActionFinishCycle, nil
	case domain.EventToggle, domain.EventStart, domain.EventStop:
		return domain.StateDelivering, domain.ActionNone, domain.ErrBusy
	case domain.EventTimeout, domain.EventEmptyResult, domain.EventTranscribeDone,
		domain.EventTranscribeFailed, domain.EventRecordFailed:
		// Late events from the prior transcribe phase.
		return domain.StateDelivering, domain.ActionNone, domain.ErrBusy
	case domain.EventShutdown:
		return domain.StateShuttingDown, domain.ActionShutdownNow, nil
	}

	return domain.StateDelivering, domain.ActionNone, errEventNotHandled
}

func transitionFromError(event domain.Event) (domain.State, domain.Action, error) {
	// Toggle/Start in domain.StateError doubles as "recover and start a fresh
	// cycle". Documented as the only path out of domain.StateError besides
	// Shutdown — there is no separate EventReset because the user-
	// facing keybind is the same physical action.
	return transitionToRecordingOrReject(domain.StateError, event)
}

// transitionToRecordingOrReject is the shared transition logic for
// domain.StateIdle and domain.StateError. Both states accept Start/Toggle
// (→ recording), reject late pipeline completion events (→ busy allow the caller
// to ignore the race), and accept Shutdown. The currentState parameter is
// used as the default when the event is not handled or returns busy.
func transitionToRecordingOrReject(currentState domain.State, event domain.Event) (domain.State, domain.Action, error) {
	switch event {
	case domain.EventToggle, domain.EventStart:
		return domain.StateRecording, domain.ActionStartRecording, nil
	case domain.EventStop, domain.EventTimeout, domain.EventEmptyResult,
		domain.EventTranscribeDone, domain.EventTranscribeFailed,
		domain.EventRecordFailed, domain.EventDeliverDone:
		return currentState, domain.ActionNone, domain.ErrBusy
	case domain.EventShutdown:
		return domain.StateShuttingDown, domain.ActionShutdownNow, nil
	}

	return currentState, domain.ActionNone, errEventNotHandled
}

func transitionFromShuttingDown(event domain.Event) (domain.State, domain.Action, error) {
	// Once we're shutting down, every further event is rejected.
	return domain.StateShuttingDown, domain.ActionNone, domain.ErrBusy
}

func Transition(state domain.State, event domain.Event) (domain.State, domain.Action, error) {
	var (
		newState domain.State
		action   domain.Action
		err      error
	)

	switch state {
	case domain.StateIdle:
		newState, action, err = transitionFromIdle(event)
	case domain.StateRecording:
		newState, action, err = transitionFromRecording(event)
	case domain.StateTranscribing:
		newState, action, err = transitionFromTranscribing(event)
	case domain.StateDelivering:
		newState, action, err = transitionFromDelivering(event)
	case domain.StateError:
		newState, action, err = transitionFromError(event)
	case domain.StateShuttingDown:
		newState, action, err = transitionFromShuttingDown(event)
	}

	// If the state handler didn't recognize the event, distinguish between
	// unknown and invalid-for-state. Unknown events are not in the vocabulary
	// at all; invalid ones are known but not valid for this state.
	if errors.Is(err, errEventNotHandled) {
		if !isKnownEvent(event) {
			return state, domain.ActionNone, fmt.Errorf("%w: state=%s event=%s", domain.ErrUnknownEvent, state, event)
		}

		return state, domain.ActionNone, fmt.Errorf("%w: state=%s event=%s", domain.ErrInvalidEventForState, state, event)
	}

	return newState, action, err
}

// Machine wraps Transition with a mutex so concurrent IPC handlers can
// safely advance the state. The current state is read by the IPC server
// to populate Response.State on every reply. Apply/ApplyWithError run the
// listener synchronously *after* unlocking so a slow or misbehaving
// listener cannot stall further IPC traffic.
type Machine struct {
	listener domain.TransitionListener
	state    domain.State
	lastErr  string // last error message, exposed via Status when state == domain.StateError
	mu       sync.Mutex
}

// NewMachine returns a machine starting in domain.StateIdle.
func NewMachine(listener domain.TransitionListener) *Machine {
	return &Machine{state: domain.StateIdle, listener: listener}
}

// State reports the current state. Safe to call concurrently.
func (m *Machine) State() domain.State {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.state
}

// LastError returns the message attached to the most recent transition into
// domain.StateError, or "" if state has changed since.
func (m *Machine) LastError() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state == domain.StateError {
		return m.lastErr
	}

	return ""
}

// Apply feeds an event and returns the resulting state and action. If the
// transition rejects the event (e.g. domain.ErrBusy), the state is unchanged.
//
// The listener — if any — is invoked AFTER releasing m.mu so it may safely
// read State() and cannot block state readers while doing external work.
// Listeners must not call Apply/ApplyWithError themselves (see
// domain.TransitionListener).
func (m *Machine) Apply(event domain.Event) (domain.State, domain.Action, error) {
	m.mu.Lock()

	next, action, err := Transition(m.state, event)
	if err != nil {
		state := m.state
		m.mu.Unlock()

		return state, domain.ActionNone, err
	}

	m.state = next
	if next != domain.StateError {
		m.lastErr = ""
	}

	listener := m.listener
	m.mu.Unlock()

	if listener != nil {
		listener(next, action)
	}

	return next, action, nil
}

// ApplyWithError feeds a failure event with an error message that gets
// stored as lastErr. The contract is intentionally narrow — only events
// that semantically carry a failure payload are accepted — so an
// accidental `ApplyWithError(domain.EventToggle, "boom")` cannot silently
// advance the state and drop the error message on the floor. Use Apply
// for every other event. Listener invocation is outside the lock, same
// rationale as Apply.
//
// Accepted events:
//   - domain.EventTranscribeFailed: STT or delivery phase produced an error.
//   - domain.EventRecordFailed:     capture broke before any audio reached STT.
func (m *Machine) ApplyWithError(event domain.Event, errMsg string) (domain.State, domain.Action, error) {
	if event != domain.EventTranscribeFailed && event != domain.EventRecordFailed {
		// Violation of the function's precondition: only failure events carry
		// an errMsg payload. Any other event here is a programming mistake —
		// not a runtime condition — so we panic rather than silently return.
		panic(fmt.Sprintf(
			"ApplyWithError: accepted events are %s and %s, got %s",
			domain.EventTranscribeFailed, domain.EventRecordFailed, event,
		))
	}

	m.mu.Lock()

	next, action, err := Transition(m.state, event)
	if err != nil {
		state := m.state
		m.mu.Unlock()

		return state, domain.ActionNone, err
	}

	m.state = next

	if next == domain.StateError {
		m.lastErr = errMsg
	} else {
		m.lastErr = ""
	}

	listener := m.listener
	m.mu.Unlock()

	if listener != nil {
		listener(next, action)
	}

	return next, action, nil
}
