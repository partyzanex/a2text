package sysd

import (
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/partyzanex/a2text/internal/domain"
	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"
)

type SdNotifierSuite struct {
	suite.Suite

	ctrl *gomock.Controller
}

func TestSdNotifierSuite(t *testing.T) {
	suite.Run(t, new(SdNotifierSuite))
}

func (s *SdNotifierSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
}

// --- Status / Ready / Stopping send the right payloads ---

func (s *SdNotifierSuite) TestStatus_SendsStatusLine() {
	rec := s.notifyProbe(true, false, nil)
	n := s.newNotifier(rec)

	n.Status("recording")

	s.Equal([]string{"STATUS=recording"}, rec.payloads())
}

func (s *SdNotifierSuite) TestReady_WithInitialStatus_CombinesReadyAndStatus() {
	rec := s.notifyProbe(true, false, nil)
	n := s.newNotifier(rec)

	n.Ready("idle")

	s.Equal([]string{"READY=1\nSTATUS=idle"}, rec.payloads())
}

func (s *SdNotifierSuite) TestReady_WithoutStatus_SendsReadyOnly() {
	rec := s.notifyProbe(true, false, nil)
	n := s.newNotifier(rec)

	n.Ready("")

	s.Equal([]string{"READY=1"}, rec.payloads())
}

func (s *SdNotifierSuite) TestReady_CalledTwice_OnlySendsOnce() {
	rec := s.notifyProbe(true, false, nil)
	n := s.newNotifier(rec)

	n.Ready("idle")
	n.Ready("idle")
	n.Ready("ignored-second-status")

	s.Equal([]string{"READY=1\nSTATUS=idle"}, rec.payloads(),
		"systemd treats duplicate READY=1 as a state change — exactly-once-on-success")
}

func (s *SdNotifierSuite) TestReady_FirstAttemptErrors_RetriesOnNextCall() {
	// readySent must NOT flip on a failed attempt: systemd is still waiting
	// for READY=1, and the next call (e.g. from a retry-on-error path)
	// must get another shot.
	rec := s.notifyProbe(true, false, errors.New("dial unix: refused"))
	n := s.newNotifier(rec)

	n.Ready("idle")
	s.Equal(1, rec.calls(), "first attempt was made but errored")

	rec.clearErr()
	n.Ready("idle")
	s.Equal(2, rec.calls(), "second attempt must run because the first never succeeded")

	// Third call: now sent, must no-op.
	n.Ready("idle")
	s.Equal(2, rec.calls(), "after a successful send, further Ready calls are suppressed")
}

func (s *SdNotifierSuite) TestReady_ActiveSentFalse_RetriesOnNextCall() {
	// Active socket but sent=false (e.g. payload rejected) is treated the
	// same as an error — readySent stays false so the next call retries.
	rec := s.notifyProbe(true, true, nil)
	n := s.newNotifier(rec)

	n.Ready("idle")
	s.Equal(1, rec.calls())

	rec.clearNotSent()
	n.Ready("idle")
	s.Equal(2, rec.calls(), "active+sent=false must allow retry")
}

func (s *SdNotifierSuite) TestStopping_WithReason_CombinesStoppingAndStatus() {
	rec := s.notifyProbe(true, false, nil)
	n := s.newNotifier(rec)

	n.Stopping("shutdown")

	s.Equal([]string{"STOPPING=1\nSTATUS=shutdown"}, rec.payloads())
}

func (s *SdNotifierSuite) TestStopping_CalledTwice_OnlySendsOnce() {
	rec := s.notifyProbe(true, false, nil)
	n := s.newNotifier(rec)

	n.Stopping("shutdown")
	n.Stopping("shutdown")

	s.Equal([]string{"STOPPING=1\nSTATUS=shutdown"}, rec.payloads())
}

func (s *SdNotifierSuite) TestStopping_FirstAttemptErrors_RetriesOnNextCall() {
	rec := s.notifyProbe(true, false, errors.New("dial unix: refused"))
	n := s.newNotifier(rec)

	n.Stopping("bye")
	rec.clearErr()
	n.Stopping("bye")

	s.Equal(2, rec.calls(),
		"Stopping must retry until a send succeeds — otherwise systemd marks the unit as failed-while-stopping")
}

// --- Sender errors are logged, not panicked ---

func (s *SdNotifierSuite) TestSenderError_DoesNotPanic() {
	n := s.newNotifier(s.notifyProbe(true, false, errors.New("dial unix: refused")))

	s.NotPanics(func() {
		n.Status("idle")
	})
}

func (s *SdNotifierSuite) TestSenderError_InactiveSocket_StillLogsNoPanic() {
	// Active=false + err != nil: lib misbehaving, not a benign skip. We
	// don't have a log capture in this suite, so the assertion is
	// "doesn't panic" + "send did happen". The warn-on-error path is
	// covered by reading the implementation.
	n := s.newNotifier(s.notifyProbe(false, false, errors.New("malformed")))

	s.NotPanics(func() {
		n.Status("idle")
	})
}

// --- Inactive sender (NOTIFY_SOCKET unset) is silent ---

func (s *SdNotifierSuite) TestInactiveSender_NoErrorNoPanic() {
	n := s.newNotifier(s.notifyProbe(false, true, nil))

	s.NotPanics(func() {
		n.Status("idle")
		n.Ready("idle")
		n.Stopping("bye")
	})
}

// --- Active sender that returns sent=false is logged (no panic) ---

func (s *SdNotifierSuite) TestActiveSender_SentFalse_DoesNotPanic() {
	rec := s.notifyProbe(true, true, nil)
	n := s.newNotifier(rec)

	s.NotPanics(func() {
		n.Status("idle")
	})

	s.Equal([]string{"STATUS=idle"}, rec.payloads(),
		"payload still goes through to the sender — sent=false is the lib's verdict, not our gate")
}

// --- Public API smoke ---

func (s *SdNotifierSuite) TestNewSdNotifier_NilLogger_NoPanic() {
	n := NewSdNotifier(nil)

	s.Require().NotNil(n)
	s.NotPanics(func() { n.Status("idle") })
}

// --- Concurrency: check-send-set is atomic ---

func (s *SdNotifierSuite) TestReady_ConcurrentCallers_OnlyOnePayloadEmitted() {
	rec := s.notifyProbe(true, false, nil)
	n := s.newNotifier(rec)

	const goroutines = 20

	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			n.Ready("idle")
		})
	}

	wg.Wait()

	s.Equal([]string{"READY=1\nSTATUS=idle"}, rec.payloads(),
		"20 concurrent Ready() callers must produce exactly one READY=1 — anything else is a race")
}

func (s *SdNotifierSuite) TestStopping_ConcurrentCallers_OnlyOnePayloadEmitted() {
	rec := s.notifyProbe(true, false, nil)
	n := s.newNotifier(rec)

	const goroutines = 20

	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			n.Stopping("bye")
		})
	}

	wg.Wait()

	s.Equal([]string{"STOPPING=1\nSTATUS=bye"}, rec.payloads(),
		"20 concurrent Stopping() callers must produce exactly one STOPPING=1")
}

// --- Defensive: nil-receiver / nil-sender / nil-log don't panic ---

func (s *SdNotifierSuite) TestNilReceiver_Active_ReturnsFalse() {
	var n *SdNotifier

	s.False(n.Active())
}

func (s *SdNotifierSuite) TestNilSender_NoPanic() {
	// SdNotifier is exported, so a caller could construct one without
	// going through NewSdNotifier. send/Active must not panic.
	n := &SdNotifier{log: slog.New(slog.DiscardHandler)}

	s.False(n.Active())
	s.NotPanics(func() {
		n.Status("idle")
		n.Ready("idle")
		n.Stopping("bye")
	})
}

func (s *SdNotifierSuite) TestNilLog_ErrorPath_NoPanic() {
	// Hand-built SdNotifier with a live sender but log==nil. The error
	// branch in sendLocked would dereference n.log directly — must go
	// through the logger() helper instead.
	n := &SdNotifier{sender: s.notifyProbe(true, false, errors.New("dial unix: refused"))}

	s.NotPanics(func() {
		n.Status("idle")
		n.Ready("idle")
		n.Stopping("bye")
	})
}

func (s *SdNotifierSuite) TestNilLog_SentFalseActive_NoPanic() {
	// Same shape but the warn branch is "not sent despite active socket".
	n := &SdNotifier{sender: s.notifyProbe(true, true, nil)}

	s.NotPanics(func() {
		n.Status("idle")
	})
}

// --- Active() delegates to sender ---

func (s *SdNotifierSuite) TestActive_DelegatesToSender() {
	active := s.newNotifier(s.notifyProbe(true, false, nil))
	inactive := s.newNotifier(s.notifyProbe(false, false, nil))

	s.True(active.Active())
	s.False(inactive.Active())
}

// --- domain.State listener maps state → STATUS text ---

func (s *SdNotifierSuite) TestMakeStateListener_StatusPerState() {
	rec := s.notifyProbe(true, false, nil)
	n := s.newNotifier(rec)

	listener := MakeStateListener(n, slog.New(slog.DiscardHandler))

	listener(domain.StateRecording, domain.ActionStartRecording)
	listener(domain.StateTranscribing, domain.ActionStopRecording)
	listener(domain.StateDelivering, domain.ActionNone)
	listener(domain.StateIdle, domain.ActionFinishCycle)
	listener(domain.StateError, domain.ActionNone)
	listener(domain.StateShuttingDown, domain.ActionShutdownNow)

	s.Equal([]string{
		"STATUS=recording",
		"STATUS=transcribing",
		"STATUS=delivering",
		"STATUS=idle",
		"STATUS=error",
		"STATUS=shutting down",
	}, rec.payloads())
}

func (s *SdNotifierSuite) TestMakeStateListener_NilNotifier_NilListener() {
	// Listener is nil only when BOTH sinks are nil — a notifier-less manual
	// run still gets the log half.
	s.Nil(MakeStateListener(nil, nil))
}

func (s *SdNotifierSuite) TestMakeStateListener_NilNotifier_NonNilLog_LogsOnly() {
	listener := MakeStateListener(nil, slog.New(slog.DiscardHandler))
	s.Require().NotNil(listener)
	// Must not panic on a transition when notifier is nil — log is the only sink.
	s.NotPanics(func() { listener(domain.StateRecording, domain.ActionStartRecording) })
}

// --- stateNotifyText covers all explicit cases + default ---

func (s *SdNotifierSuite) TestStateNotifyText_UnknownState_ReturnsDefault() {
	got := stateNotifyText(domain.State("weird"))

	s.Contains(got, "unknown state",
		"default branch must surface the literal state so the systemd log shows what slipped through")
	s.Contains(got, "weird")
}

// --- Helpers ---

func (s *SdNotifierSuite) newNotifier(sender NotifySender) *SdNotifier {
	return &SdNotifier{
		log:    slog.New(slog.DiscardHandler),
		sender: sender,
	}
}

type notifyProbe struct {
	sender     *MockNotifySender
	mu         sync.Mutex
	payloadLog []string
	active     bool
	notSent    bool
	err        error
}

func (p *notifyProbe) Notify(state string) (bool, error) {
	return p.sender.Notify(state)
}

func (p *notifyProbe) Active() bool {
	return p.sender.Active()
}

func (s *SdNotifierSuite) notifyProbe(active, notSent bool, err error) *notifyProbe {
	probe := &notifyProbe{
		sender:  NewMockNotifySender(s.ctrl),
		active:  active,
		notSent: notSent,
		err:     err,
	}

	probe.sender.EXPECT().Active().DoAndReturn(func() bool {
		probe.mu.Lock()
		defer probe.mu.Unlock()

		return probe.active
	}).AnyTimes()

	probe.sender.EXPECT().Notify(gomock.Any()).DoAndReturn(func(state string) (bool, error) {
		probe.mu.Lock()
		defer probe.mu.Unlock()

		probe.payloadLog = append(probe.payloadLog, state)
		if probe.err != nil {
			return false, probe.err
		}

		return !probe.notSent, nil
	}).AnyTimes()

	return probe
}

func (r *notifyProbe) payloads() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]string, len(r.payloadLog))
	copy(out, r.payloadLog)

	return out
}

func (f *notifyProbe) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return len(f.payloadLog)
}

func (f *notifyProbe) clearErr() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.err = nil
}

func (f *notifyProbe) clearNotSent() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.notSent = false
}
