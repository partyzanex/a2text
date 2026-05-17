package daemon

import (
	"bytes"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/a2text/internal/domain"
	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/usecases/voice"
)

// ToggleDebounceSuite covers the key-repeat guard wired to all three Toggle
// entry points: acceptToggle (unit), Toggle (tray), HotkeyHandler (hotkey
// press in toggle mode), and handleIPC (IPC Toggle command).
type ToggleDebounceSuite struct {
	suite.Suite
}

func TestToggleDebounceSuite(t *testing.T) {
	suite.Run(t, new(ToggleDebounceSuite))
}

// --- acceptToggle unit tests ---

// TestAcceptToggle_FirstCall_Accepted guards that a newly created Daemon
// always accepts the first toggle (zero lastToggleAt → interval elapsed).
func (s *ToggleDebounceSuite) TestAcceptToggle_FirstCall_Accepted() {
	s.True(s.newSilentDaemon().acceptToggle(), "first toggle must be accepted")
}

// TestAcceptToggle_ImmediateRepeat_Rejected guards the key-repeat path: a
// second call from non-Recording state within toggleMinInterval must be blocked.
func (s *ToggleDebounceSuite) TestAcceptToggle_ImmediateRepeat_Rejected() {
	dm := s.newSilentDaemon()

	s.True(dm.acceptToggle(), "first call accepted")
	s.False(dm.acceptToggle(), "immediate repeat from non-Recording state must be rejected")
}

// TestAcceptToggle_FromRecording_AlwaysAccepted verifies that Toggle from
// domain.StateRecording is never debounced: the user must always be able to
// stop an in-progress recording regardless of how recently it started.
func (s *ToggleDebounceSuite) TestAcceptToggle_FromRecording_AlwaysAccepted() {
	dm := s.newSilentDaemon()
	dm.acceptToggle() // consume the debounce slot

	// Advance machine to Recording so acceptToggle sees StateRecording.
	_, _, err := dm.machine.Apply(domain.EventToggle)
	s.Require().NoError(err)
	s.Require().Equal(domain.StateRecording, dm.machine.State())

	s.True(dm.acceptToggle(), "stop-recording toggle must bypass the debounce")
}

// TestAcceptToggle_AfterInterval_Accepted verifies that once
// toggleMinInterval has elapsed, the next toggle is accepted again.
// Back-dating lastToggleAt avoids sleeping in the test.
func (s *ToggleDebounceSuite) TestAcceptToggle_AfterInterval_Accepted() {
	dm := s.newSilentDaemon()
	dm.acceptToggle()

	dm.toggleMu.Lock()
	dm.lastToggleAt = time.Now().Add(-(toggleMinInterval + time.Millisecond))
	dm.toggleMu.Unlock()

	s.True(dm.acceptToggle(), "toggle must be accepted after the debounce interval")
}

// TestAcceptToggle_Concurrent_OnlyOneWins guards the mutex: when two
// goroutines race on the same unfired debounce exactly one must win.
func (s *ToggleDebounceSuite) TestAcceptToggle_Concurrent_OnlyOneWins() {
	dm := s.newSilentDaemon()

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []bool
	)

	for range 2 {
		wg.Go(func() {
			res := dm.acceptToggle()

			mu.Lock()
			results = append(results, res) //nolint:wsl_v5 // append inside goroutine body — no shared variable above
			mu.Unlock()
		})
	}

	wg.Wait()

	trueCount := 0

	for _, ok := range results {
		if ok {
			trueCount++
		}
	}

	s.Equal(1, trueCount, "exactly one concurrent acceptToggle must return true")
}

// --- Toggle (tray path) ---

// TestToggle_Debounced_MachineNotAdvanced verifies that a debounced Toggle
// call leaves the state machine unchanged.
func (s *ToggleDebounceSuite) TestToggle_Debounced_MachineNotAdvanced() {
	dm := s.newSilentDaemon()
	dm.acceptToggle()

	before := dm.machine.State()

	dm.Toggle(s.T().Context())

	s.Equal(before, dm.machine.State(), "debounced Toggle must not change machine state")
}

// TestToggle_Debounced_LogsDebugNotWarn ensures the log entry for a
// debounced toggle is at DEBUG — WARN would spam the journal during
// GNOME key-repeat.
func (s *ToggleDebounceSuite) TestToggle_Debounced_LogsDebugNotWarn() {
	var logBuf bytes.Buffer

	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	dm := s.newDaemon(log)
	dm.acceptToggle()
	dm.Toggle(s.T().Context())

	out := logBuf.String()
	s.Contains(out, "tray toggle debounced", "debounced toggle must produce a debug message")
	s.NotContains(out, `"level":"WARN"`, "debounced toggle must NOT appear at WARN level")
}

// --- HotkeyHandler (toggle mode) ---

// TestHotkeyHandler_ToggleMode_StopRecording_NotDebounced verifies that even
// when the debounce slot was just consumed, Toggle from Recording is always
// accepted — the user must be able to stop a recording immediately.
//
// cycleCancel is pre-set to a no-op so startCycle returns immediately without
// a real useCase; the machine still advances normally through Apply.
func (s *ToggleDebounceSuite) TestHotkeyHandler_ToggleMode_StopRecording_NotDebounced() {
	dm := s.newSilentDaemon()

	dm.cycleMu.Lock()
	dm.cycleCancel = func() {} // block startCycle without a real useCase
	dm.cycleMu.Unlock()

	handler := dm.HotkeyHandler()
	ctx := s.T().Context()

	handler(ctx, voice.HotkeyPress) // accepted: idle → recording

	s.Require().Equal(domain.StateRecording, dm.machine.State())

	// Second press arrives immediately (simulates intentional stop within debounce
	// window). From Recording state debounce is bypassed.
	handler(ctx, voice.HotkeyPress) // recording → transcribing

	s.Equal(domain.StateTranscribing, dm.machine.State(),
		"stop-recording press must not be blocked by the debounce window")
}

// TestHotkeyHandler_ToggleMode_RapidRestart_FromIdle_Debounced verifies that
// the debounce blocks a key-repeat restart attempt from Idle: after the
// initial Toggle is accepted (Idle → Recording) and recording completes
// (manual SM advance to Idle), a rapid second Toggle is rejected.
func (s *ToggleDebounceSuite) TestHotkeyHandler_ToggleMode_RapidRestart_FromIdle_Debounced() {
	dm := s.newSilentDaemon()

	dm.cycleMu.Lock()
	dm.cycleCancel = func() {} // block startCycle without a real useCase
	dm.cycleMu.Unlock()

	handler := dm.HotkeyHandler()
	ctx := s.T().Context()

	handler(ctx, voice.HotkeyPress) // accepted: idle → recording (debounce timer starts)

	s.Require().Equal(domain.StateRecording, dm.machine.State())

	// Manually walk SM back to Idle so the next Toggle would be a fresh start.
	_, _, err := dm.machine.Apply(domain.EventTimeout)
	s.Require().NoError(err)

	_, _, err = dm.machine.Apply(domain.EventEmptyResult)
	s.Require().NoError(err)

	s.Require().Equal(domain.StateIdle, dm.machine.State())

	before := dm.machine.State()

	handler(ctx, voice.HotkeyPress) // key-repeat within debounce window — must be blocked

	s.Equal(before, dm.machine.State(),
		"key-repeat restart from Idle within debounce window must be blocked")
}

// --- advanceCycleSuccess: transcript log includes model ---

// TestAdvanceCycleSuccess_TranscriptLog_IncludesModel verifies that the
// "voice: transcript" DEBUG entry carries the model name so operators can
// correlate transcription quality to the model without grepping other lines.
func (s *ToggleDebounceSuite) TestAdvanceCycleSuccess_TranscriptLog_IncludesModel() {
	var logBuf bytes.Buffer

	log := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	notifyCh := make(chan domain.State, stateChBufSize)
	machine := voice.NewMachine(makeNotifyListener(nil, notifyCh))

	dm := &Daemon{
		log:      log,
		machine:  machine,
		notifyCh: notifyCh,
		cfg: &config.VoiceConfig{
			Provider: "go-whisper",
			Privacy:  config.VoicePrivacyConfig{LogTranscript: true},
			GoWhisper: config.VoiceGoWhisperConfig{
				Model: "ggml-small",
			},
		},
	}

	// Advance SM to Recording; advanceCycleSuccess's EventTimeout bridge will
	// be a no-op (ErrBusy expected and swallowed) once it fires.
	_, _, err := dm.machine.Apply(domain.EventToggle) // idle → recording
	s.Require().NoError(err)

	result := domain.CycleResult{Text: "тестовая фраза"}
	dm.advanceCycleSuccess(result)

	out := logBuf.String()
	s.Contains(out, `"msg":"voice: transcript"`, "transcript log line must be emitted")
	s.Contains(out, `"model":"ggml-small"`, "transcript log must include the model name")
	s.Contains(out, `"text":"тестовая фраза"`, "transcript log must include the transcribed text")
}

// newDaemon returns a Daemon with just enough state for debounce tests: a
// real machine (so state transitions are observable), a capturing logger,
// and a zero lastToggleAt so the first call is always accepted.
func (s *ToggleDebounceSuite) newDaemon(log *slog.Logger) *Daemon {
	s.T().Helper()

	notifyCh := make(chan domain.State, stateChBufSize)
	machine := voice.NewMachine(makeNotifyListener(nil, notifyCh))

	return &Daemon{
		log:      log,
		machine:  machine,
		notifyCh: notifyCh,
	}
}

func (s *ToggleDebounceSuite) newSilentDaemon() *Daemon {
	return s.newDaemon(slog.New(slog.DiscardHandler))
}
