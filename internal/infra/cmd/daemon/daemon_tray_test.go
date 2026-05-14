package daemon

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/a2text/internal/domain"
	"github.com/partyzanex/a2text/internal/usecases/voice"
)

// NotifyListenerSuite tests the makeNotifyListener fan-out helper that
// bridges state-machine transitions to sd_notify and to in-process
// consumers (e.g. the tray).
type NotifyListenerSuite struct {
	suite.Suite
}

func TestNotifyListenerSuite(t *testing.T) {
	suite.Run(t, new(NotifyListenerSuite))
}

// TestMakeNotifyListener_CallsSdListener verifies that the wrapped sd_notify
// listener is always invoked with the correct state.
func (s *NotifyListenerSuite) TestMakeNotifyListener_CallsSdListener() {
	ch := make(chan domain.State, 1)

	var (
		got    domain.State
		called bool
	)

	sdListener := func(state domain.State, _ domain.Action) {
		got = state
		called = true
	}

	makeNotifyListener(sdListener, ch)(domain.StateRecording, domain.ActionStartRecording)

	s.True(called, "sdListener must be called on each transition")
	s.Equal(domain.StateRecording, got, "sdListener must receive the transitioned-to state")
}

// TestMakeNotifyListener_SendsToChannel verifies that every transition is
// forwarded to the notification channel.
func (s *NotifyListenerSuite) TestMakeNotifyListener_SendsToChannel() {
	ch := make(chan domain.State, 1)

	makeNotifyListener(nil, ch)(domain.StateTranscribing, domain.ActionNone)

	select {
	case st := <-ch:
		s.Equal(domain.StateTranscribing, st)
	default:
		s.Fail("expected a state on the channel, but it was empty")
	}
}

// TestMakeNotifyListener_NilSdListener_DoesNotPanic guards the nil-listener
// path: sd_notify may be absent (e.g. outside systemd), in which case the
// composite listener must still forward to the channel without panicking.
func (s *NotifyListenerSuite) TestMakeNotifyListener_NilSdListener_DoesNotPanic() {
	ch := make(chan domain.State, 1)

	s.NotPanics(func() {
		makeNotifyListener(nil, ch)(domain.StateIdle, domain.ActionNone)
	})
}

// TestMakeNotifyListener_FullChannel_NonBlocking guards the drop-on-full
// invariant: a lagging consumer must never stall the state machine.
func (s *NotifyListenerSuite) TestMakeNotifyListener_FullChannel_NonBlocking() {
	ch := make(chan domain.State, 1)
	ch <- domain.StateIdle // fill the buffer

	listener := makeNotifyListener(nil, ch)

	done := make(chan struct{})

	go func() {
		listener(domain.StateRecording, domain.ActionStartRecording)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		s.Fail("listener blocked on a full channel — state machine would stall")
	}

	// The original value must remain; the new one was dropped.
	s.Equal(domain.StateIdle, <-ch, "channel must retain its original value after a non-blocking drop")
}

// DaemonToggleSuite tests Daemon.Toggle, the in-process toggle shortcut
// wired to the tray menu item.
type DaemonToggleSuite struct {
	suite.Suite
}

func TestDaemonToggleSuite(t *testing.T) {
	suite.Run(t, new(DaemonToggleSuite))
}

// TestToggle_BusyState_LogsDebugNotWarn guards that calling Toggle when the
// machine rejects EventToggle (e.g. mid-transcription) produces a DEBUG log
// entry, not a WARN. WARN would spam the journal during normal use where
// the user presses the hotkey while the previous cycle is still running.
func (s *DaemonToggleSuite) TestToggle_BusyState_LogsDebugNotWarn() {
	var logBuf bytes.Buffer

	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	dm := s.newDaemonForToggle(log)

	// Advance to Transcribing — Toggle is rejected with ErrBusy there.
	_, _, err := dm.machine.Apply(domain.EventToggle) // idle → recording
	s.Require().NoError(err)

	_, _, err = dm.machine.Apply(domain.EventTimeout) // recording → transcribing
	s.Require().NoError(err)

	s.NotPanics(func() {
		dm.Toggle(s.T().Context())
	})

	out := logBuf.String()
	s.Contains(out, "tray toggle rejected", "rejected toggle must produce a debug entry")
	s.NotContains(out, `"level":"WARN"`, "rejected toggle must NOT be logged at WARN")
	s.NotContains(out, `"level":"ERROR"`, "rejected toggle must NOT be logged at ERROR")
}

// TestToggle_FromRecording_AdvancesToTranscribing verifies the stop-recording
// path: Toggle from Recording produces ActionStopRecording → dispatch calls
// cancelRecordingPhase, which is a no-op when recordingCancel is nil.
// This lets us exercise the full Toggle→dispatch pipeline without a real
// recording cycle (and therefore without a non-nil useCase).
func (s *DaemonToggleSuite) TestToggle_FromRecording_AdvancesToTranscribing() {
	dm := s.newDaemonForToggle(slog.New(slog.DiscardHandler))

	// Advance the machine directly — no cycle is started (no dispatch called).
	_, _, err := dm.machine.Apply(domain.EventToggle) // idle → recording
	s.Require().NoError(err)
	s.Equal(domain.StateRecording, dm.machine.State())

	// Toggle from recording → transcribing + ActionStopRecording.
	// cancelRecordingPhase is safe with nil recordingCancel.
	s.NotPanics(func() {
		dm.Toggle(s.T().Context())
	})

	s.Equal(domain.StateTranscribing, dm.machine.State(),
		"Toggle from recording must advance the machine to transcribing")
}

// newDaemonForToggle creates a minimal Daemon suitable for Toggle tests.
// Only the machine, log, and notifyCh fields are populated; useCase is
// intentionally nil so that dispatch(ActionStartRecording) is a no-op
// (the startCycle guard returns early when cycleCancel is already set, and
// the test does not need a real recording cycle to verify state transitions).
func (s *DaemonToggleSuite) newDaemonForToggle(log *slog.Logger) *Daemon {
	s.T().Helper()

	notifyCh := make(chan domain.State, stateChBufSize)
	machine := voice.NewMachine(makeNotifyListener(nil, notifyCh))

	return &Daemon{
		log:      log,
		machine:  machine,
		notifyCh: notifyCh,
	}
}
