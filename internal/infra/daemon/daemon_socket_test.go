package daemon

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/a2text/internal/adapters/ipc"
	"github.com/partyzanex/a2text/internal/domain"
	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/usecases/voice"
)

//go:generate go run go.uber.org/mock/mockgen@latest -write_package_comment=false -package=daemon -destination=voice_socket_mocks_test.go github.com/partyzanex/a2text/internal/usecases/voice Recorder,Transcriber,Output
//go:generate go run go.uber.org/mock/mockgen@latest -write_package_comment=false -package=daemon -destination=hotkey_mocks_test.go github.com/partyzanex/a2text/internal/usecases/voice HotkeyListener

// e2e timeouts. Centralised so a slow CI box can be tuned in one place.
const (
	e2eReadyTimeout    = 2 * time.Second // socket bound + first Ping ok
	e2eStateTimeout    = 3 * time.Second // ping-polled state convergence
	e2eDeliverTimeout  = 3 * time.Second // Output.Deliver fires after release
	e2eShutdownTimeout = 5 * time.Second // Serve returns after ctx cancel
	e2eClientTimeout   = 2 * time.Second // single ipc.Client RTT cap
	e2ePingPollGap     = 20 * time.Millisecond
)

// DaemonE2ESuite drives a real Daemon through its full unix-socket IPC,
// exercising ping/toggle and every state transition of a successful cycle
// plus the busy and unknown-command error paths. Recorder/Transcriber/Output
// are mocked so the cycle is deterministic and synchronisable from the test;
// everything else (state machine, IPC server, dispatcher, cycle goroutine)
// runs as in production.
type DaemonE2ESuite struct {
	suite.Suite

	ctrl *gomock.Controller

	sockPath string
	daemon   *Daemon

	serveCancel context.CancelFunc
	serveErr    chan error

	client *ipc.Client

	releaseTranscribe chan struct{}
	deliveredCh       chan string

	// recordedPath stores the WAV path returned by the Recorder mock so
	// shutdown-style tests can assert the cycle goroutine's deferred
	// cleanup actually removed it. Pointer-typed so "not yet recorded"
	// is distinguishable from "recorded an empty string" (the latter
	// must never happen — it would be a bug in the mock).
	recordedPath atomic.Pointer[string]

	// transcribeCalls / deliverCalls count invocations of the respective
	// stubs. Used by error-path tests (e.g. SIGTERM during recording) to
	// prove the cycle did NOT advance past the cancelled record phase.
	transcribeCalls atomic.Int32
	deliverCalls    atomic.Int32

	// drainOnce serialises the cancel+wait sequence so TearDownTest and
	// tests that consume serveErr themselves don't race or double-wait.
	drainOnce  sync.Once
	drainedErr error

	recorder    *MockRecorder
	transcriber *MockTranscriber
	output      *MockOutput
}

func TestDaemonE2ESuite(t *testing.T) {
	suite.Run(t, new(DaemonE2ESuite))
}

func (s *DaemonE2ESuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())

	s.initChannels()
	s.initCounters()
	s.initMocks()

	s.sockPath = filepath.Join(s.T().TempDir(), "voice.sock")
	s.serveErr = make(chan error, 1)

	serveCtx, serveCancel := context.WithCancel(context.Background())
	s.serveCancel = serveCancel

	go func() {
		s.serveErr <- s.daemon.Serve(serveCtx, s.sockPath)
	}()

	s.client = ipc.NewClient(s.sockPath, e2eClientTimeout)
	s.waitReady()
}

func (s *DaemonE2ESuite) TearDownTest() {
	// Shutdown first — cycleCancel cascades into the Transcribe stub's ctx
	// and forces it out of its select. Closing releaseTranscribe afterward
	// is a belt-and-braces fallback for the (impossible-in-prod) case where
	// the SM never moved out of recording before TearDown.
	_ = s.drainServe()

	select {
	case <-s.releaseTranscribe:
	default:
		close(s.releaseTranscribe)
	}

	// Wait for the cycle goroutine to fully unwind out of the Transcribe
	// stub before returning. Without this wait the next SetupTest's
	// re-assignment of s.releaseTranscribe races a leftover Transcribe
	// invocation that still reads the field via select. We use WAV file
	// removal as the synchronization signal: voice.Cycle's deferred
	// os.Remove runs after Transcribe returns and before the cycle
	// goroutine's outer defers, so seeing the file gone proves Transcribe
	// has exited.
	if pathPtr := s.recordedPath.Load(); pathPtr != nil {
		deadline := time.Now().Add(e2eShutdownTimeout)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(*pathPtr); errors.Is(err, os.ErrNotExist) {
				return
			}

			time.Sleep(e2ePingPollGap)
		}

		s.FailNow("cycle goroutine did not clean up WAV before TearDown deadline")
	}
}

// --- tests ---

func (s *DaemonE2ESuite) TestPing_InitialState_Idle() {
	resp, err := s.client.Ping(s.T().Context())
	s.Require().NoError(err)
	s.True(resp.OK)
	s.Equal(string(domain.StateIdle), resp.State)
	s.Equal(ipc.ProtocolVersion, resp.Version)
}

func (s *DaemonE2ESuite) TestFullCycle_IdleRecordingTranscribingIdle() {
	// idle → recording
	resp, err := s.client.Toggle(s.T().Context())
	s.Require().NoError(err)
	s.True(resp.OK)
	s.Equal(string(domain.StateRecording), resp.State)

	// Confirm the cycle goroutine actually moved through the Recorder ctx
	// cancellation path by waiting for the state to settle at recording.
	s.waitState(domain.StateRecording)

	// recording → transcribing (domain.ActionStopRecording cancels recordCtx; the
	// goroutine then advances into the blocking Transcribe stub).
	resp, err = s.client.Toggle(s.T().Context())
	s.Require().NoError(err)
	s.True(resp.OK)
	s.Equal(string(domain.StateTranscribing), resp.State)

	s.waitState(domain.StateTranscribing)

	// Release the transcribe stub — daemon delivers and walks the SM
	// through Delivering → Idle via advanceCycleSuccess.
	close(s.releaseTranscribe)

	select {
	case got := <-s.deliveredCh:
		s.Equal("hello world", got)
	case <-time.After(e2eDeliverTimeout):
		s.FailNow("Output.Deliver was not called after transcribe released")
	}

	s.waitState(domain.StateIdle)
}

func (s *DaemonE2ESuite) TestBusy_ToggleWhileTranscribing() {
	// Drive the SM into domain.StateTranscribing where Toggle returns domain.ErrBusy.
	_, err := s.client.Toggle(s.T().Context())
	s.Require().NoError(err)

	_, err = s.client.Toggle(s.T().Context())
	s.Require().NoError(err)

	s.waitState(domain.StateTranscribing)

	resp, err := s.client.Toggle(s.T().Context())
	s.Require().ErrorIs(err, domain.ErrBusy)
	s.False(resp.OK)
	s.Equal(ipc.ErrCodeBusy, resp.ErrorCode)
	s.Equal(string(domain.StateTranscribing), resp.State)

	// Let the cycle finish so TearDown shuts down cleanly.
	close(s.releaseTranscribe)
	s.waitState(domain.StateIdle)
}

func (s *DaemonE2ESuite) TestUnknownCommand_ReturnsErrCodeUnknownCommand() {
	// Bypass ipc.Client (which only sends known commands) by sending a raw
	// Request with a garbage Command. Daemon must reject with the typed
	// ErrCodeUnknownCommand instead of e.g. silently treating it as Toggle.
	conn, err := net.Dial("unix", s.sockPath)
	s.Require().NoError(err)

	defer func() { _ = conn.Close() }()

	s.Require().NoError(conn.SetDeadline(time.Now().Add(e2eClientTimeout)))

	req := ipc.Request{
		Version: ipc.ProtocolVersion,
		ID:      "e2e-unknown-cmd",
		Command: "frobnicate",
	}
	s.Require().NoError(ipc.Encode(conn, req))

	var resp ipc.Response

	s.Require().NoError(ipc.Decode(conn, &resp))

	s.False(resp.OK)
	s.Equal(ipc.ErrCodeUnknownCommand, resp.ErrorCode)
	s.Equal(req.ID, resp.ID)

	// Server pre-rejects unknown commands before dispatch, so domain.State may be
	// empty here (it never reached the daemon's handleIPC where domain.State is
	// populated). Re-ping to verify the SM is still in Idle.
	pingResp, pingErr := s.client.Ping(s.T().Context())
	s.Require().NoError(pingErr)
	s.Equal(string(domain.StateIdle), pingResp.State, "unknown command must not advance the SM")
}

// TestShutdown_DuringRecording_DiscardsAudio simulates a SIGTERM in the
// middle of a recording cycle by cancelling the parent ctx the daemon's
// signal.NotifyContext would have been cancelled by in production
// (runDaemon wires it the same way). Invariants under test:
//
//   - Serve returns nil (graceful shutdown, not an error).
//   - The recorded WAV is removed by the cycle goroutine's deferred cleanup
//     so a SIGTERM mid-record does not leak temp files.
//   - Transcribe is not invoked: the cycle short-circuits on ctx.Err() after
//     RecordToFile returns, before reaching the STT call.
//   - Output.Deliver is not invoked: audio is discarded, not delivered.
//
// The cycle goroutine outlives Serve's return (Shutdown does not block on
// it), so file removal is polled with a bounded deadline.
func (s *DaemonE2ESuite) TestShutdown_DuringRecording_DiscardsAudio() {
	// idle → recording. The Recorder mock writes a real WAV and blocks
	// on recordCtx, so cancelling serveCtx (≈ SIGTERM) will:
	//   1. fire d.machine.Apply(domain.EventShutdown) → domain.StateShuttingDown + domain.ActionDiscardAudio,
	//   2. cancel cycleCancel + recordingCancel,
	//   3. unblock the Recorder, which returns the path,
	//   4. the cycle goroutine sees cycleCtx.Err() before Transcribe and bails,
	//   5. its deferred os.Remove removes the WAV.
	_, err := s.client.Toggle(s.T().Context())
	s.Require().NoError(err)

	s.waitState(domain.StateRecording)

	// Wait for the Recorder mock to actually create the file before we
	// cancel — without this the assertion "file got removed" is vacuous
	// (no file ever existed).
	s.Require().Eventually(func() bool {
		return s.recordedPath.Load() != nil
	}, time.Second, e2ePingPollGap, "recorder mock never created the WAV file")

	wavPath := *s.recordedPath.Load()
	s.Require().FileExists(wavPath)

	// SIGTERM ≈ cancel signalCtx in runDaemon. Serve's ctx-watcher goroutine
	// will then call d.Shutdown() → state machine + LIFO cleanup.
	serveErr := s.drainServe()
	s.Require().NoError(serveErr, "Serve must return nil on graceful shutdown")

	// Cycle goroutine cleanup is asynchronous w.r.t. Shutdown's return,
	// so poll for the WAV removal up to a bounded deadline.
	s.Require().Eventually(func() bool {
		_, statErr := os.Stat(wavPath)

		return errors.Is(statErr, os.ErrNotExist)
	}, 2*time.Second, e2ePingPollGap, "WAV file was not cleaned up after shutdown")

	s.Zero(s.transcribeCalls.Load(), "Transcribe must not run when SIGTERM cancels recording")
	s.Zero(s.deliverCalls.Load(), "Deliver must not run — audio is discarded, not delivered")
}

// TestShutdown_DuringTranscribing_CancelsAndDiscards is the
// transcribing-phase counterpart to the recording-phase shutdown test:
// SIGTERM (modelled as ctx cancel) lands while the daemon is in
// domain.StateTranscribing. Per the SM table domain.EventShutdown from domain.StateTranscribing
// emits domain.ActionShutdownNow; Daemon.Shutdown cancels cycleCancel which
// cascades into Transcribe's ctx. The mock returns ("", ctx.Err()), Cycle
// produces domain.CycleError{Phase: domain.PhaseTranscribe}, and its deferred cleanup
// still removes the WAV file (the defer is registered after validate,
// before the ctx-cancel check before Transcribe).
//
// Invariants:
//   - Serve returns nil (clean shutdown, not error).
//   - Transcribe was invoked exactly once — the daemon DID reach the STT
//     phase before shutdown, not aborted mid-record.
//   - Output.Deliver was NOT called — there is no successful transcription
//     to deliver when ctx wins the race.
//   - The recorded WAV is cleaned up by Cycle's defer despite the
//     post-record error path.
func (s *DaemonE2ESuite) TestShutdown_DuringTranscribing_CancelsAndDiscards() {
	// idle → recording.
	_, err := s.client.Toggle(s.T().Context())
	s.Require().NoError(err)

	s.waitState(domain.StateRecording)

	// Wait for the Recorder mock to actually create the WAV before we
	// progress — same rationale as the record-phase test.
	s.Require().Eventually(func() bool {
		return s.recordedPath.Load() != nil
	}, time.Second, e2ePingPollGap, "recorder mock never created the WAV file")

	wavPath := *s.recordedPath.Load()
	s.Require().FileExists(wavPath)

	// recording → transcribing: domain.ActionStopRecording cancels recordCtx, the
	// recorder unblocks and returns the path, the cycle goroutine advances
	// into Transcribe — which our stub holds indefinitely on ctx.Done()
	// because releaseTranscribe stays unclosed.
	_, err = s.client.Toggle(s.T().Context())
	s.Require().NoError(err)

	s.waitState(domain.StateTranscribing)

	// Trust but verify: the Transcribe stub must have actually been entered
	// before we send the shutdown. Otherwise this test degenerates into the
	// record-phase one.
	s.Require().Eventually(func() bool {
		return s.transcribeCalls.Load() == 1
	}, time.Second, e2ePingPollGap, "Transcribe stub was not entered before shutdown")

	serveErr := s.drainServe()
	s.Require().NoError(serveErr, "Serve must return nil on graceful shutdown from transcribing")

	s.Require().Eventually(func() bool {
		_, statErr := os.Stat(wavPath)

		return errors.Is(statErr, os.ErrNotExist)
	}, 2*time.Second, e2ePingPollGap, "WAV file was not cleaned up after shutdown from transcribing")

	s.Equal(int32(1), s.transcribeCalls.Load(), "Transcribe must have been invoked exactly once")
	s.Zero(s.deliverCalls.Load(), "Deliver must not fire — transcribe was cancelled before returning text")
}

// --- helpers ---

func (s *DaemonE2ESuite) initChannels() {
	releaseTranscribe := make(chan struct{})
	deliveredCh := make(chan string, 1)

	s.releaseTranscribe = releaseTranscribe
	s.deliveredCh = deliveredCh
}

func (s *DaemonE2ESuite) initCounters() {
	s.recordedPath.Store(nil)
	s.transcribeCalls.Store(0)
	s.deliverCalls.Store(0)
	s.drainOnce = sync.Once{}
	s.drainedErr = nil
}

func (s *DaemonE2ESuite) initMocks() {
	s.recorder = NewMockRecorder(s.ctrl)
	s.transcriber = NewMockTranscriber(s.ctrl)
	s.output = NewMockOutput(s.ctrl)

	wavDir := s.T().TempDir()

	s.recorder.EXPECT().
		RecordToFile(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _ voice.RecordOptions) (string, error) {
			wavFile, err := os.CreateTemp(wavDir, "rec-*.wav")
			s.Require().NoError(err)

			_, err = wavFile.Write(make([]byte, 200))
			s.Require().NoError(err)
			s.Require().NoError(wavFile.Close())

			path := wavFile.Name()
			s.recordedPath.Store(&path)

			<-ctx.Done()

			return path, nil
		}).
		AnyTimes()

	releaseTranscribe := s.releaseTranscribe
	deliveredCh := s.deliveredCh

	s.transcriber.EXPECT().
		Transcribe(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _, _ string) (string, error) {
			s.transcribeCalls.Add(1)

			select {
			case <-releaseTranscribe:
				return "hello world", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}).
		AnyTimes()

	s.output.EXPECT().
		Deliver(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, text string) error {
			s.deliverCalls.Add(1)

			deliveredCh <- text

			return nil
		}).
		AnyTimes()

	closer := NewMocktranscribeCloser(s.ctrl)
	closer.EXPECT().Close().Return(nil).AnyTimes()

	s.daemon = s.newStubDaemon(closer)
}

// newStubDaemon creates a Daemon with stub config for e2e tests.
func (s *DaemonE2ESuite) newStubDaemon(closer transcribeCloser) *Daemon {
	cfg := &config.VoiceConfig{
		Provider: "stub",
		Language: "ru",
	}
	cfg.Capture.MaxDuration = 30 * time.Second

	return NewDaemon(&DaemonDeps{
		Cfg:         cfg,
		Log:         slog.New(slog.DiscardHandler),
		Recorder:    s.recorder,
		Transcriber: s.transcriber,
		Closer:      closer,
		Output:      s.output,
	})
}

// drainServe cancels the serve context and waits for Serve to return,
// at most once per test. Tests that need to make assertions on Serve's
// return value (e.g. SIGTERM-style shutdown) call this directly before
// TearDown does; both call sites share the same result via drainOnce.
func (s *DaemonE2ESuite) drainServe() error {
	s.drainOnce.Do(func() {
		s.serveCancel()

		select {
		case s.drainedErr = <-s.serveErr:
		case <-time.After(e2eShutdownTimeout):
			s.FailNow("daemon Serve did not return after cancel")
		}
	})

	return s.drainedErr
}

func (s *DaemonE2ESuite) waitReady() {
	s.T().Helper()

	deadline := time.Now().Add(e2eReadyTimeout)
	for time.Now().Before(deadline) {
		if _, err := s.client.Ping(s.T().Context()); err == nil {
			return
		}

		time.Sleep(e2ePingPollGap)
	}

	s.FailNow("daemon did not become reachable on socket in time")
}

func (s *DaemonE2ESuite) waitState(want domain.State) {
	s.T().Helper()

	deadline := time.Now().Add(e2eStateTimeout)
	for time.Now().Before(deadline) {
		resp, err := s.client.Ping(s.T().Context())
		if err == nil && resp.State == string(want) {
			return
		}

		time.Sleep(e2ePingPollGap)
	}

	s.FailNowf("state did not converge", "want=%s", string(want))
}
