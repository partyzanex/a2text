package domain

// State enumerates the daemon's mutually-exclusive lifecycle phases. Strings
// are stable: they appear in IPC responses and sd_notify STATUS lines.
type State string

const (
	StateIdle         State = "idle"
	StateRecording    State = "recording"
	StateTranscribing State = "transcribing"
	StateDelivering   State = "delivering"
	StateError        State = "error"
	StateShuttingDown State = "shutting_down"
)

// Event is what an outside agent (IPC, timer, internal pipeline step) feeds
// into the state machine. Naming mirrors the IPC command vocabulary plus
// internal-only completion events.
type Event string

const (
	EventToggle           Event = "toggle"
	EventStart            Event = "start"
	EventStop             Event = "stop"
	EventTimeout          Event = "timeout"
	EventRecordFailed     Event = "record_failed"
	EventTranscribeDone   Event = "transcribe_done"
	EventTranscribeFailed Event = "transcribe_failed"
	EventEmptyResult      Event = "empty_result"
	EventDeliverDone      Event = "deliver_done"
	EventShutdown         Event = "shutdown"
)

// Action is the side effect the daemon must perform after a transition. The
// state machine itself is pure — it returns Action so the daemon can
// dispatch the actual I/O outside the lock.
type Action string

const (
	ActionNone           Action = "none"
	ActionStartRecording Action = "start_recording"
	ActionStopRecording  Action = "stop_recording"
	ActionDiscardAudio   Action = "discard_audio"
	ActionFinishCycle    Action = "finish_cycle"
	ActionShutdownNow    Action = "shutdown_now"
)

// TransitionListener is invoked after every successful state transition.
// Use it for sd_notify breadcrumbs, metrics, or test assertions.
type TransitionListener func(newState State, action Action)
