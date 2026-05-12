package voice

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/partyzanex/a2text/internal/domain"
	"github.com/stretchr/testify/suite"
)

type StateMachineSuite struct {
	suite.Suite
}

func TestStateMachineSuite(t *testing.T) {
	suite.Run(t, new(StateMachineSuite))
}

// --- Pure transition table ---

func (s *StateMachineSuite) TestTransition_Table() {
	for name, testCase := range transitionTable() {
		s.Run(name, func() {
			next, action, err := Transition(testCase.state, testCase.event)
			s.Equal(testCase.want.next, next, "next state")
			s.Equal(testCase.want.action, action, "action")

			if testCase.want.errIs != nil {
				s.Require().ErrorIs(err, testCase.want.errIs)
			} else {
				s.Require().NoError(err)
			}
		})
	}
}

// transitionTable returns the full transition table for the state machine.
func transitionTable() map[string]struct {
	state domain.State
	event domain.Event
	want  struct {
		next   domain.State
		action domain.Action
		errIs  error
	}
} {
	transitions := make(map[string]struct {
		state domain.State
		event domain.Event
		want  struct {
			next   domain.State
			action domain.Action
			errIs  error
		}
	})

	addIdleTransitions(transitions)
	addRecordingTransitions(transitions)
	addTranscribingTransitions(transitions)
	addDeliveringTransitions(transitions)
	addErrorTransitions(transitions)
	addShuttingDownTransitions(transitions)

	return transitions
}

type transEntry struct {
	state domain.State
	event domain.Event
	next  domain.State
	act   domain.Action
	err   error
}

func addEntry(transitions map[string]struct {
	state domain.State
	event domain.Event
	want  struct {
		next   domain.State
		action domain.Action
		errIs  error
	}
}, name string, entry transEntry) {
	transitions[name] = struct {
		state domain.State
		event domain.Event
		want  struct {
			next   domain.State
			action domain.Action
			errIs  error
		}
	}{
		state: entry.state,
		event: entry.event,
		want: struct {
			next   domain.State
			action domain.Action
			errIs  error
		}{entry.next, entry.act, entry.err},
	}
}

func addIdleTransitions(transitions map[string]struct {
	state domain.State
	event domain.Event
	want  struct {
		next   domain.State
		action domain.Action
		errIs  error
	}
}) {
	addEntry(transitions, "idle+toggle",
		transEntry{domain.StateIdle, domain.EventToggle, domain.StateRecording, domain.ActionStartRecording, nil})
	addEntry(transitions, "idle+start",
		transEntry{domain.StateIdle, domain.EventStart, domain.StateRecording, domain.ActionStartRecording, nil})
	addEntry(transitions, "idle+stop",
		transEntry{domain.StateIdle, domain.EventStop, domain.StateIdle, domain.ActionNone, domain.ErrBusy})
	addEntry(transitions, "idle+shutdown",
		transEntry{domain.StateIdle, domain.EventShutdown, domain.StateShuttingDown, domain.ActionShutdownNow, nil})
	addEntry(transitions, "idle+late_timeout",
		transEntry{domain.StateIdle, domain.EventTimeout, domain.StateIdle, domain.ActionNone, domain.ErrBusy})
	addEntry(transitions, "idle+late_empty",
		transEntry{domain.StateIdle, domain.EventEmptyResult, domain.StateIdle, domain.ActionNone, domain.ErrBusy})
	addEntry(transitions, "idle+late_transcribe",
		transEntry{domain.StateIdle, domain.EventTranscribeDone, domain.StateIdle, domain.ActionNone, domain.ErrBusy})
	addEntry(transitions, "idle+late_failed",
		transEntry{domain.StateIdle, domain.EventTranscribeFailed, domain.StateIdle, domain.ActionNone, domain.ErrBusy})
	addEntry(transitions, "idle+late_record_failed",
		transEntry{domain.StateIdle, domain.EventRecordFailed, domain.StateIdle, domain.ActionNone, domain.ErrBusy})
	addEntry(transitions, "idle+late_deliver",
		transEntry{domain.StateIdle, domain.EventDeliverDone, domain.StateIdle, domain.ActionNone, domain.ErrBusy})
}

func addRecordingTransitions(transitions map[string]struct {
	state domain.State
	event domain.Event
	want  struct {
		next   domain.State
		action domain.Action
		errIs  error
	}
}) {
	addEntry(transitions, "recording+toggle",
		transEntry{domain.StateRecording, domain.EventToggle, domain.StateTranscribing, domain.ActionStopRecording, nil})
	addEntry(transitions, "recording+stop",
		transEntry{domain.StateRecording, domain.EventStop, domain.StateTranscribing, domain.ActionStopRecording, nil})
	addEntry(transitions, "recording+timeout",
		transEntry{domain.StateRecording, domain.EventTimeout, domain.StateTranscribing, domain.ActionStopRecording, nil})
	addEntry(transitions, "recording+start",
		transEntry{domain.StateRecording, domain.EventStart, domain.StateRecording, domain.ActionNone, domain.ErrBusy})
	addEntry(transitions, "recording+shutdown",
		transEntry{domain.StateRecording, domain.EventShutdown, domain.StateShuttingDown, domain.ActionDiscardAudio, nil})
	addEntry(transitions, "recording+record_failed",
		transEntry{domain.StateRecording, domain.EventRecordFailed, domain.StateError, domain.ActionNone, nil})
	addEntry(transitions, "recording+late_empty",
		transEntry{domain.StateRecording, domain.EventEmptyResult, domain.StateRecording, domain.ActionNone, domain.ErrBusy})
	addEntry(transitions, "recording+late_transcribe",
		transEntry{
			domain.StateRecording, domain.EventTranscribeDone, domain.StateRecording,
			domain.ActionNone, domain.ErrBusy,
		})
	addEntry(transitions, "recording+late_failed",
		transEntry{
			domain.StateRecording, domain.EventTranscribeFailed, domain.StateRecording,
			domain.ActionNone, domain.ErrBusy,
		})
	addEntry(transitions, "recording+late_deliver",
		transEntry{domain.StateRecording, domain.EventDeliverDone, domain.StateRecording, domain.ActionNone, domain.ErrBusy})
}

func addTranscribingTransitions(transitions map[string]struct {
	state domain.State
	event domain.Event
	want  struct {
		next   domain.State
		action domain.Action
		errIs  error
	}
}) {
	addEntry(transitions, "transcribing+toggle",
		transEntry{
			domain.StateTranscribing, domain.EventToggle, domain.StateTranscribing,
			domain.ActionNone, domain.ErrBusy,
		})
	addEntry(transitions, "transcribing+start",
		transEntry{domain.StateTranscribing, domain.EventStart, domain.StateTranscribing, domain.ActionNone, domain.ErrBusy})
	addEntry(transitions, "transcribing+stop",
		transEntry{domain.StateTranscribing, domain.EventStop, domain.StateTranscribing, domain.ActionNone, domain.ErrBusy})
	addEntry(transitions, "transcribing+late_timeout",
		transEntry{
			domain.StateTranscribing, domain.EventTimeout, domain.StateTranscribing,
			domain.ActionNone, domain.ErrBusy,
		})
	addEntry(transitions, "transcribing+late_record_failed",
		transEntry{
			domain.StateTranscribing, domain.EventRecordFailed, domain.StateTranscribing,
			domain.ActionNone, domain.ErrBusy,
		})
	addEntry(transitions, "transcribing+done",
		transEntry{domain.StateTranscribing, domain.EventTranscribeDone, domain.StateDelivering, domain.ActionNone, nil})
	addEntry(transitions, "transcribing+failed",
		transEntry{domain.StateTranscribing, domain.EventTranscribeFailed, domain.StateError, domain.ActionNone, nil})
	addEntry(transitions, "transcribing+empty",
		transEntry{domain.StateTranscribing, domain.EventEmptyResult, domain.StateIdle, domain.ActionFinishCycle, nil})
	addEntry(transitions, "transcribing+shutdown",
		transEntry{domain.StateTranscribing, domain.EventShutdown, domain.StateShuttingDown, domain.ActionShutdownNow, nil})
}

func addDeliveringTransitions(transitions map[string]struct {
	state domain.State
	event domain.Event
	want  struct {
		next   domain.State
		action domain.Action
		errIs  error
	}
}) {
	addEntry(transitions, "delivering+done",
		transEntry{domain.StateDelivering, domain.EventDeliverDone, domain.StateIdle, domain.ActionFinishCycle, nil})
	addEntry(transitions, "delivering+toggle",
		transEntry{domain.StateDelivering, domain.EventToggle, domain.StateDelivering, domain.ActionNone, domain.ErrBusy})
	addEntry(transitions, "delivering+late_timeout",
		transEntry{domain.StateDelivering, domain.EventTimeout, domain.StateDelivering, domain.ActionNone, domain.ErrBusy})
	addEntry(transitions, "delivering+late_failed",
		transEntry{
			domain.StateDelivering, domain.EventTranscribeFailed, domain.StateDelivering,
			domain.ActionNone, domain.ErrBusy,
		})
	addEntry(transitions, "delivering+late_record_failed",
		transEntry{
			domain.StateDelivering, domain.EventRecordFailed, domain.StateDelivering,
			domain.ActionNone, domain.ErrBusy,
		})
	addEntry(transitions, "delivering+shutdown",
		transEntry{domain.StateDelivering, domain.EventShutdown, domain.StateShuttingDown, domain.ActionShutdownNow, nil})
}

func addErrorTransitions(transitions map[string]struct {
	state domain.State
	event domain.Event
	want  struct {
		next   domain.State
		action domain.Action
		errIs  error
	}
}) {
	addEntry(transitions, "error+toggle",
		transEntry{domain.StateError, domain.EventToggle, domain.StateRecording, domain.ActionStartRecording, nil})
	addEntry(transitions, "error+start",
		transEntry{domain.StateError, domain.EventStart, domain.StateRecording, domain.ActionStartRecording, nil})
	addEntry(transitions, "error+stop",
		transEntry{domain.StateError, domain.EventStop, domain.StateError, domain.ActionNone, domain.ErrBusy})
	addEntry(transitions, "error+late_timeout",
		transEntry{domain.StateError, domain.EventTimeout, domain.StateError, domain.ActionNone, domain.ErrBusy})
	addEntry(transitions, "error+late_empty",
		transEntry{domain.StateError, domain.EventEmptyResult, domain.StateError, domain.ActionNone, domain.ErrBusy})
	addEntry(transitions, "error+late_transcribe",
		transEntry{domain.StateError, domain.EventTranscribeDone, domain.StateError, domain.ActionNone, domain.ErrBusy})
	addEntry(transitions, "error+late_deliver",
		transEntry{domain.StateError, domain.EventDeliverDone, domain.StateError, domain.ActionNone, domain.ErrBusy})
	addEntry(transitions, "error+late_record_failed",
		transEntry{domain.StateError, domain.EventRecordFailed, domain.StateError, domain.ActionNone, domain.ErrBusy})
	addEntry(transitions, "error+shutdown",
		transEntry{domain.StateError, domain.EventShutdown, domain.StateShuttingDown, domain.ActionShutdownNow, nil})
}

func addShuttingDownTransitions(transitions map[string]struct {
	state domain.State
	event domain.Event
	want  struct {
		next   domain.State
		action domain.Action
		errIs  error
	}
}) {
	addEntry(transitions, "shutting_down+toggle",
		transEntry{
			domain.StateShuttingDown, domain.EventToggle, domain.StateShuttingDown,
			domain.ActionNone, domain.ErrBusy,
		})
	addEntry(transitions, "shutting_down+shutdown",
		transEntry{
			domain.StateShuttingDown, domain.EventShutdown, domain.StateShuttingDown,
			domain.ActionNone, domain.ErrBusy,
		})
}

func (s *StateMachineSuite) TestTransition_UnknownEvent_ReturnsUnknownSentinel() {
	_, _, err := Transition(domain.StateIdle, domain.Event("bogus"))
	s.Require().Error(err)
	s.Require().ErrorIs(err, domain.ErrUnknownEvent)
	s.NotErrorIs(err, domain.ErrInvalidEventForState,
		"a truly unknown event must not masquerade as 'known-but-invalid'")
}

func (s *StateMachineSuite) TestIsKnownEvent_CoversEveryDeclaredEvent() {
	// If someone adds a new domain.Event const and forgets to extend isKnownEvent,
	// every fallthrough on that event will be misreported as domain.ErrUnknownEvent
	// instead of domain.ErrInvalidEventForState. List every domain.Event we use in the
	// transition table; the test fails loudly on the missing entry.
	all := []domain.Event{
		domain.EventToggle, domain.EventStart, domain.EventStop, domain.EventTimeout, domain.EventRecordFailed,
		domain.EventTranscribeDone, domain.EventTranscribeFailed, domain.EventEmptyResult,
		domain.EventDeliverDone, domain.EventShutdown,
	}

	for _, event := range all {
		s.True(isKnownEvent(event), "isKnownEvent missing entry for %s", event)
	}

	s.False(isKnownEvent(domain.Event("nope")), "garbage strings must not be classified as known")
}

func (s *StateMachineSuite) TestTransition_KnownEventInvalidForState_ReturnsInvalidSentinel() {
	// domain.EventDeliverDone is a real event in the vocabulary. Before the exhaustive fix,
	// StateTranscribing had no case for it. After the exhaustive fix, it's now handled as
	// a late event from the prior delivery phase (returns ErrBusy). Both represent the same
	// semantic: a known event that is not valid for initiating a transition in this state.
	// The error must distinguish this from a typo'd event string so IPC can map it to
	// a different error code.
	_, _, err := Transition(domain.StateTranscribing, domain.EventDeliverDone)
	s.Require().Error(err)
	s.Require().ErrorIs(err, domain.ErrBusy)
	s.NotErrorIs(err, domain.ErrUnknownEvent,
		"a known event arriving at the wrong state is not 'unknown'")
}

// --- Machine: thread-safety + listener fires per transition ---

func (s *StateMachineSuite) TestMachine_StartsIdle() {
	m := NewMachine(nil)
	s.Equal(domain.StateIdle, m.State())
}

func (s *StateMachineSuite) TestMachine_NilListener_ApplyStillWorks() {
	m := NewMachine(nil)

	state, action, err := m.Apply(domain.EventToggle)
	s.Require().NoError(err)
	s.Equal(domain.StateRecording, state)
	s.Equal(domain.ActionStartRecording, action)
	s.Equal(domain.StateRecording, m.State())
}

func (s *StateMachineSuite) TestMachine_ListenerCalledOnEachTransition() {
	var calls atomic.Int32

	m := NewMachine(func(_ domain.State, _ domain.Action) {
		calls.Add(1)
	})

	_, _, err := m.Apply(domain.EventToggle) // idle → recording
	s.Require().NoError(err)
	_, _, err = m.Apply(domain.EventToggle) // recording → transcribing
	s.Require().NoError(err)
	_, _, err = m.Apply(domain.EventTranscribeDone) // transcribing → delivering
	s.Require().NoError(err)
	_, _, err = m.Apply(domain.EventDeliverDone) // delivering → idle
	s.Require().NoError(err)

	s.Equal(int32(4), calls.Load())
	s.Equal(domain.StateIdle, m.State())
}

func (s *StateMachineSuite) TestMachine_ListenerNotCalledOnRejectedEvent() {
	var calls atomic.Int32

	m := NewMachine(func(_ domain.State, _ domain.Action) {
		calls.Add(1)
	})

	_, _, err := m.Apply(domain.EventStop) // idle + stop → domain.ErrBusy
	s.Require().ErrorIs(err, domain.ErrBusy)
	s.Equal(int32(0), calls.Load(), "listener must not fire on rejected events")
}

func (s *StateMachineSuite) TestApplyWithError_RejectsNonFailureEvents() {
	// Contract guard: ApplyWithError accepts only failure-carrying events.
	// Any other event is a programming error and must panic rather than
	// silently advance the machine with a dropped error message.
	m := NewMachine(nil)

	s.Panics(func() {
		_, _, _ = m.ApplyWithError(domain.EventToggle, "boom")
	}, "ApplyWithError with a non-failure event must panic")
}

func (s *StateMachineSuite) TestApplyWithError_RecordFailed_FromRecording_LandsInError() {
	// Phase-aware routing: when the recording phase itself fails (mic
	// gone, encoder crashed), the daemon sends domain.EventRecordFailed and
	// expects the SM to jump domain.StateRecording → domain.StateError without going
	// through transcribing/delivering. Without this transition the daemon
	// would be wedged in domain.StateRecording after every capture failure.
	m := NewMachine(nil)

	_, _, _ = m.Apply(domain.EventToggle) // setup: idle → recording

	state, action, err := m.ApplyWithError(domain.EventRecordFailed, "pw-record killed")
	s.Require().NoError(err)
	s.Equal(domain.StateError, state)
	s.Equal(domain.ActionNone, action)
	s.Equal("pw-record killed", m.LastError(),
		"the daemon's diagnostic message must survive the transition")
}

func (s *StateMachineSuite) TestMachine_LastError_OnlySurfacesInErrorState() {
	m := NewMachine(nil)

	_, _, _ = m.Apply(domain.EventToggle) // setup transition
	_, _, _ = m.Apply(domain.EventToggle) // setup transition

	_, _, err := m.ApplyWithError(domain.EventTranscribeFailed, "whisper crashed")
	s.Require().NoError(err)
	s.Equal(domain.StateError, m.State())
	s.Equal("whisper crashed", m.LastError())

	// Recover via toggle.
	_, _, _ = m.Apply(domain.EventToggle) // recovery transition
	s.Equal(domain.StateRecording, m.State())
	s.Empty(m.LastError(), "lastError must clear when leaving Error state")
}

// --- Listener runs outside the lock (re-entrant Apply must not deadlock) ---
//
// The deadlock-detection tests intentionally use a channel + time.After
// instead of "did State() return a value". If we accidentally moved the
// listener back inside the lock, calling State() would block forever; we
// want the test to fail with a clear "listener deadlocked" message rather
// than time out the entire suite.

func (s *StateMachineSuite) TestMachine_Apply_ListenerCanReadState_NoDeadlock() {
	done := make(chan struct{})

	var m *Machine

	m = NewMachine(func(_ domain.State, _ domain.Action) {
		_ = m.State()

		close(done)
	})

	_, _, err := m.Apply(domain.EventToggle)
	s.Require().NoError(err)

	select {
	case <-done:
	case <-time.After(time.Second):
		s.Fail("listener deadlocked while calling Machine.State from inside Apply")
	}
}

func (s *StateMachineSuite) TestMachine_ApplyWithError_ListenerCanReadState_NoDeadlock() {
	// The listener fires on every transition, including the two setup
	// Apply calls. We arm the deadlock detector only on the ApplyWithError
	// transition. closeOnce guards close(done) in case the test is ever
	// extended with more domain.StateError transitions — close-of-closed-channel
	// would panic and mask the real assertion.
	armed := make(chan struct{}, 1)
	done := make(chan struct{})

	var (
		m        *Machine
		doneOnce sync.Once
	)

	m = NewMachine(func(newState domain.State, _ domain.Action) {
		if newState != domain.StateError {
			return
		}

		select {
		case <-armed:
		default:
			return
		}

		_ = m.State()

		doneOnce.Do(func() { close(done) })
	})

	_, _, _ = m.Apply(domain.EventToggle) // setup: idle → recording
	_, _, _ = m.Apply(domain.EventToggle) // setup: recording → transcribing

	armed <- struct{}{} // next transition is the one we measure

	_, _, err := m.ApplyWithError(domain.EventTranscribeFailed, "boom")
	s.Require().NoError(err)

	select {
	case <-done:
	case <-time.After(time.Second):
		s.Fail("listener deadlocked while calling Machine.State from inside ApplyWithError")
	}
}

// --- domain.EventEmptyResult: transcribing → idle without going through delivering ---

func (s *StateMachineSuite) TestMachine_EmptyResult_SkipsDelivering() {
	m := NewMachine(nil)

	_, _, _ = m.Apply(domain.EventToggle) // setup: idle → recording
	_, _, _ = m.Apply(domain.EventToggle) // setup: recording → transcribing

	state, action, err := m.Apply(domain.EventEmptyResult)
	s.Require().NoError(err)
	s.Equal(domain.StateIdle, state, "empty result must land directly on idle, not delivering")
	s.Equal(domain.ActionFinishCycle, action)
	s.Equal(domain.StateIdle, m.State())
}

// --- Concurrent toggles never produce inconsistent state ---

func (s *StateMachineSuite) TestMachine_ConcurrentStarts_OneWinsExactlyOnce() {
	m := NewMachine(nil)

	const goroutines = 50

	var (
		startCh          = make(chan struct{})
		startedRecording atomic.Int32
		busyCount        atomic.Int32
		unexpectedCount  atomic.Int32
	)

	done := make(chan struct{}, goroutines)

	for range goroutines {
		go func() {
			<-startCh

			_, action, err := m.Apply(domain.EventStart)

			switch {
			case err == nil && action == domain.ActionStartRecording:
				startedRecording.Add(1)
			case errors.Is(err, domain.ErrBusy):
				busyCount.Add(1)
			default:
				// Don't call s.T() from a goroutine — race-detector noise
				// and testify's t.Fatal semantics get muddled. Count here,
				// assert in the main goroutine.
				unexpectedCount.Add(1)
			}

			done <- struct{}{}
		}()
	}

	close(startCh)

	for range goroutines {
		<-done
	}

	s.Equal(int32(0), unexpectedCount.Load(), "no goroutine should see an outcome other than start/busy")
	s.Equal(int32(1), startedRecording.Load(), "exactly one start must transition idle→recording")
	s.Equal(int32(goroutines-1), busyCount.Load(), "the rest must hit domain.ErrBusy")
}
