//go:build integration && linux

package tests

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/partyzanex/a2text/internal/adapters/ipc"
)

// cycleSettleTimeout caps how long the test waits for the cycle to walk
// from Transcribing → Delivering → Idle after the fake OpenAI replies.
// Delivery is just an stdout write so the realistic budget is sub-second;
// 5s leaves comfortable headroom for a loaded CI box.
const cycleSettleTimeout = 5 * time.Second

// TestFullCycle_IdleRecordingTranscribingDeliveringIdle is the real-binary
// counterpart of the in-process DaemonE2ESuite full-cycle test. It spawns
// `a2text --daemon` with a fake OpenAI endpoint, drives a complete
// dictation cycle through the unix socket, and verifies the daemon walks
// every state of the SM and returns to idle on its own.
//
// Setup:
//   - Real SubprocessRecorder against the suite's PulseAudio null sink.
//   - Real cloud STT adapter pointed at an httptest fake that responds
//     immediately with a valid `{"text": "..."}` payload.
//   - Output mode "stdout" — Deliver is a trivial os.Stdout write, so
//     the cycle completes fast without external clipboard/autopaste deps.
//
// What this proves over the in-process suite:
//   - The real binary boots, binds the socket, and answers IPC.
//   - The cycle goroutine survives an HTTP round-trip end-to-end.
//   - SM advances Transcribing → Delivering → Idle without test help.
//   - Exactly one HTTP request reaches the STT endpoint per Toggle pair.
func (s *VoiceShutdownIntegrationSuite) TestFullCycle_IdleRecordingTranscribingDeliveringIdle() {
	var requestCount atomic.Int32

	fakeOpenAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain the multipart upload — required before responding so the
		// daemon's HTTP client doesn't see a half-read connection.
		_, _ = io.Copy(io.Discard, r.Body)

		requestCount.Add(1)

		// Minimal OpenAI Whisper response shape: just the text field.
		// internal/adapters/stt/openai.go decodes this into the result.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "real-binary cycle ok"})
	}))
	defer fakeOpenAI.Close()

	proc := s.launchDaemon("A2TEXT_CLOUD_BASE_URL=" + fakeOpenAI.URL)

	// 1. Initial ping → idle. Daemon is up, socket is bound, SM is fresh.
	s.expectState(proc.client, "idle")

	// 2. Toggle → recording. Cycle goroutine starts SubprocessRecorder
	//    against the null sink monitor; SM moves to domain.StateRecording.
	s.toggleAndExpect(proc.client, "recording")

	// Real recorder needs a moment to spawn pw-record/parecord and start
	// writing the WAV. Without this, the next Toggle could land before
	// the recorder process even exists, racing the cycle's ctx wiring.
	time.Sleep(recordStartupSettleTime)

	// 3. Toggle → transcribing. domain.ActionStopRecording cancels recordCtx, the
	//    recorder finalises its WAV (subprocess receives SIGINT and writes
	//    the header), and the cycle goroutine proceeds to STT. The HTTP
	//    POST goes to our fake server which replies immediately.
	s.toggleAndExpect(proc.client, "transcribing")

	// 4. Poll Ping until the SM walks itself back to idle: the cycle
	//    goroutine fires Apply(domain.EventTranscribeDone) → domain.StateDelivering,
	//    Deliver writes to stdout, then Apply(domain.EventDeliverDone) → domain.StateIdle.
	//    All driven by the daemon itself — the test only observes.
	s.waitDaemonState(proc.client, "idle", cycleSettleTimeout)

	// 5. Sanity: exactly one HTTP request hit the fake STT for one cycle.
	//    A higher count would point at a retry-loop regression.
	s.Equal(int32(1), requestCount.Load(),
		"fake OpenAI must have served exactly one transcription request")

	// 6. After the cycle, a second Toggle must still drive the SM forward
	//    — the daemon shouldn't be wedged in idle after delivering.
	s.toggleAndExpect(proc.client, "recording")
	time.Sleep(recordStartupSettleTime)
	s.toggleAndExpect(proc.client, "transcribing")
	s.waitDaemonState(proc.client, "idle", cycleSettleTimeout)

	s.Equal(int32(2), requestCount.Load(),
		"second cycle must produce exactly one additional STT request")
}

// raceLoserTimeout caps how long we wait for the lock-losing process to
// finish its waitAndToggle and exit. raceRetryDeadline inside RunBootstrap
// is 3s; 10s here covers a worst-case slow CI plus headroom for the
// fork/exec + initial config load.
const raceLoserTimeout = 10 * time.Second

// TestRaceStart_FlockMakesOneDaemon_OtherExitsZero spawns two `a2text`
// binaries side-by-side against a shared XDG_RUNTIME_DIR so both contend
// for the same lock file and socket path. The contract under test is the
// RunBootstrap flow's structural invariant:
//
//   - Exactly one process becomes the daemon (binds the socket, blocks
//     in daemon.Serve until signalled).
//   - The other process completes a single Toggle round-trip via
//     waitAndToggle and exits with code 0.
//
// Whether the loser hit the explicit "lock taken" branch
// (AcquireDaemonLock → ErrDaemonAlreadyRunning → waitAndToggle) or the
// faster tryToggle-against-running-daemon branch depends on timing —
// both are correct behaviours of RunBootstrap, and the asserted
// invariant ("one becomes daemon, the other exits 0") covers both.
// A `time.Sleep` barrier before cmd.Start() narrows the spawn delta to
// goroutine-scheduling noise to maximise the chance the explicit race
// fires, but the test is not flaky if it doesn't.
func (s *VoiceShutdownIntegrationSuite) TestRaceStart_FlockMakesOneDaemon_OtherExitsZero() {
	fakeOpenAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "race-cycle ok"})
	}))
	defer fakeOpenAI.Close()

	runtimeDir := s.T().TempDir()
	tempDir := s.T().TempDir()
	configPath := s.writeConfig(tempDir)

	env := s.raceEnv(runtimeDir, tempDir, fakeOpenAI.URL)

	procs, stderrs := s.spawnRacePair(configPath, env)

	s.T().Cleanup(func() {
		for _, p := range procs {
			if p.ProcessState == nil {
				_ = p.Process.Kill()
				_, _ = p.Process.Wait()
			}
		}
	})

	loser, winnerIdx := s.waitRaceLoser(procs)

	loserStderr := drainStderr(stderrs[loser.idx])
	s.Require().NoErrorf(loser.err,
		"loser must exit 0 after delivering Toggle to daemon (got %v).\nstderr:\n%s",
		loser.err, loserStderr,
	)

	s.assertWinnerAlive(runtimeDir, procs, stderrs, winnerIdx)
}

// raceEnv builds the environment for the race test.
func (s *VoiceShutdownIntegrationSuite) raceEnv(runtimeDir, tempDir, openAIURL string) []string {
	return append(os.Environ(),
		"XDG_RUNTIME_DIR="+runtimeDir,
		"PULSE_SOURCE="+shutdownNullSinkName+".monitor",
		"A2TEXT_PROVIDER=cloud",
		"A2TEXT_CLOUD_PROVIDER=openai",
		"A2TEXT_CLOUD_API_KEY=fake-key-for-shutdown-test",
		"A2TEXT_LANGUAGE=ru",
		"A2TEXT_TEMP_DIR="+tempDir,
		"A2TEXT_CLOUD_BASE_URL="+openAIURL,
	)
}

// spawnRacePair spawns two a2text binaries with a barrier for the race test.
func (s *VoiceShutdownIntegrationSuite) spawnRacePair(
	configPath string,
	env []string,
) ([]*exec.Cmd, []<-chan []byte) {
	startBarrier := make(chan struct{})

	type spawnResult struct {
		cmd      *exec.Cmd
		stderrCh <-chan []byte
		err      error
	}

	resultCh := make(chan spawnResult, 2)

	var wg sync.WaitGroup

	wg.Add(2)

	for range 2 {
		go func() {
			defer wg.Done()

			<-startBarrier

			cmd, stderrCh, err := s.spawnRaceCmd(configPath, env)
			resultCh <- spawnResult{cmd: cmd, stderrCh: stderrCh, err: err}
		}()
	}

	time.Sleep(20 * time.Millisecond)
	close(startBarrier)
	wg.Wait()
	close(resultCh)

	procs := make([]*exec.Cmd, 0, 2)
	stderrs := make([]<-chan []byte, 0, 2)

	for r := range resultCh {
		s.Require().NoError(r.err, "spawning daemon-race process must succeed")
		procs = append(procs, r.cmd)
		stderrs = append(stderrs, r.stderrCh)
	}

	s.Require().Len(procs, 2)

	return procs, stderrs
}

// spawnRaceCmd starts a single a2text process for the race test.
func (s *VoiceShutdownIntegrationSuite) spawnRaceCmd(
	configPath string,
	env []string,
) (*exec.Cmd, <-chan []byte, error) {
	cmd := exec.CommandContext(context.Background(), s.binaryPath, "--config", configPath)
	cmd.Env = env

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}

	stderrCh := make(chan []byte, 1)

	go func() {
		buf, _ := io.ReadAll(stderr)
		stderrCh <- buf
	}()

	return cmd, stderrCh, cmd.Start()
}

// raceExitInfo holds the result of a race process exit.
type raceExitInfo struct {
	idx int
	err error
}

// waitRaceLoser waits for the first of two processes to exit and returns
// its exit info along with the winner's index.
func (s *VoiceShutdownIntegrationSuite) waitRaceLoser(procs []*exec.Cmd) (raceExitInfo, int) {
	exitCh := make(chan raceExitInfo, 2)

	for idx, p := range procs {
		go func() { exitCh <- raceExitInfo{idx: idx, err: p.Wait()} }()
	}

	var loser raceExitInfo

	select {
	case loser = <-exitCh:
	case <-time.After(raceLoserTimeout):
		s.FailNow("neither process exited within race deadline — both are blocked?")
	}

	return loser, 1 - loser.idx
}

// assertWinnerAlive checks the winner is serving and tears it down cleanly.
func (s *VoiceShutdownIntegrationSuite) assertWinnerAlive(
	runtimeDir string,
	procs []*exec.Cmd,
	stderrs []<-chan []byte,
	winnerIdx int,
) {
	socketPath := filepath.Join(runtimeDir, "a2text", "a2text-voice.sock")
	client := ipc.NewClient(socketPath, 2*time.Second)

	s.Require().NoError(pingClient(context.Background(), client),
		"winner daemon must still answer Ping after race resolved")

	winner := procs[winnerIdx]
	s.Require().NoError(winner.Process.Signal(syscall.SIGTERM))

	exitCh := make(chan raceExitInfo, 1)

	go func() { exitCh <- raceExitInfo{idx: winnerIdx, err: winner.Wait()} }()

	winnerExit := <-exitCh
	s.Equal(winnerIdx, winnerExit.idx, "second exit must belong to the winner")

	winnerStderr := drainStderr(stderrs[winnerIdx])
	s.Require().NoErrorf(winnerExit.err,
		"winner must exit 0 on SIGTERM after race (got %v).\nstderr:\n%s",
		winnerExit.err, winnerStderr,
	)
	s.Zero(winner.ProcessState.ExitCode(),
		"winner exit code must be 0 for systemd-friendly graceful stop")
}

// pingClient wraps one Ping with a bounded timeout — alias of pingOnce
// kept locally so the race test reads as "ping the winner" without the
// indirection through a daemon-readiness-poll helper.
func pingClient(parent context.Context, client *ipc.Client) error {
	return pingOnce(parent, client)
}

// drainStderr reads whatever the goroutine in spawnCmd / launchDaemon
// has buffered so far. Bounded by a short deadline so a test asserting
// stderr does not hang waiting for the kernel to flush the pipe when the
// process is already dead.
func drainStderr(ch <-chan []byte) []byte {
	select {
	case buf := <-ch:
		return buf
	case <-time.After(time.Second):
		return nil
	}
}

// expectState issues one Ping and asserts the post-call state. Used as a
// cheap "what's the SM doing right now?" probe without driving a
// transition.
func (s *VoiceShutdownIntegrationSuite) expectState(client *ipc.Client, wantState string) {
	s.T().Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := client.Ping(ctx)
	s.Require().NoError(err)
	s.Equal(wantState, resp.State)
}

// waitDaemonState polls Ping until resp.State matches want or the
// deadline elapses. Used to observe SM transitions the daemon drives by
// itself (TranscribeDone → DeliverDone → Idle) without nudging via IPC.
func (s *VoiceShutdownIntegrationSuite) waitDaemonState(client *ipc.Client, want string, timeout time.Duration) {
	s.T().Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		state, err := pingState(context.Background(), client)
		if err == nil && state == want {
			return
		}

		time.Sleep(daemonReadyPollGap)
	}

	s.FailNowf("daemon state never reached want", "want=%s", want)
}

// pingState wraps one Ping with a bounded timeout and returns the
// observed state. Sibling of pingOnce — pingOnce only cares whether the
// daemon is reachable, this one extracts the state field too.
func pingState(parent context.Context, client *ipc.Client) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 500*time.Millisecond)
	defer cancel()

	resp, err := client.Ping(ctx)
	if err != nil {
		return "", err
	}

	return resp.State, nil
}
