//go:build integration && linux

package tests

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/a2text/internal/adapters/ipc"
)

const (
	shutdownNullSinkName    = "a2text_shutdown_test"
	binaryBuildTimeout      = 90 * time.Second
	daemonReadyTimeout      = 5 * time.Second
	daemonReadyPollGap      = 50 * time.Millisecond
	recordStartupSettleTime = 400 * time.Millisecond
	processWaitTimeout      = 10 * time.Second
)

// VoiceShutdownIntegrationSuite covers the full real-binary SIGTERM flow:
//
//   - go build cmd/a2text → produces a daemon binary with no test-only fakes;
//   - the binary starts as --daemon, captures from a PulseAudio null sink
//     monitor (so we are not at the mercy of the host's real microphone);
//   - a Toggle drives the daemon into StateRecording with a live recorder;
//   - SIGTERM is delivered to the process, the daemon's signal.NotifyContext
//     fires, partyzanex/shutdown drains the LIFO, and the binary exits 0.
//
// This is intentionally heavier than the in-process DaemonE2ESuite: the
// in-process suite proves the state machine + cycle goroutine + cleanup
// contract; this one proves the signal wiring + real recorder + real exit
// code that systemd consumes.
type VoiceShutdownIntegrationSuite struct {
	suite.Suite

	binaryPath string
	moduleID   uint32 // pactl module ID for the null sink, 0 if not loaded
}

func TestVoiceShutdownIntegrationSuite(t *testing.T) {
	suite.Run(t, new(VoiceShutdownIntegrationSuite))
}

func (s *VoiceShutdownIntegrationSuite) SetupSuite() {
	// Tie suite-shared lifetime (built binary, loaded null sink) to the
	// suite's parent T. testing.T.Context() is cancelled when the suite
	// finishes — that's the right scope for setup/teardown resources.
	for _, tool := range []string{"pactl", "go"} {
		if _, lookErr := exec.LookPath(tool); lookErr != nil {
			s.T().Skipf("tool %q not found in PATH — skipping shutdown integration suite", tool)
		}
	}

	// SubprocessRecorder needs at least one of pw-record/parecord. Skip the
	// whole suite when neither is available — without a working recorder
	// Toggle would never reach StateRecording and the SIGTERM-mid-record
	// invariant is uninteresting.
	if _, pwErr := exec.LookPath("pw-record"); pwErr != nil {
		if _, paErr := exec.LookPath("parecord"); paErr != nil {
			s.T().Skip("neither pw-record nor parecord found in PATH — skipping shutdown integration suite")
		}
	}

	s.binaryPath = s.buildBinary()

	out, err := exec.Command(
		"pactl", "load-module", "module-null-sink",
		"sink_name="+shutdownNullSinkName,
		"sink_properties=device.description=a2text_shutdown_test_sink",
	).Output()
	s.Require().NoError(err, "pactl load-module module-null-sink failed")

	id, parseErr := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 32)
	s.Require().NoError(parseErr, "unexpected module ID from pactl: %q", string(out))
	s.moduleID = uint32(id)
}

func (s *VoiceShutdownIntegrationSuite) TearDownSuite() {
	if s.moduleID == 0 {
		return
	}

	// Best-effort cleanup. Use Assert (not Require) so a stuck pactl does
	// not panic out of teardown — but DO surface the error so a leaked null
	// sink is visible at CI time instead of silently accumulating.

	if err := exec.Command("pactl", "unload-module", strconv.FormatUint(uint64(s.moduleID), 10)).Run(); err != nil {
		s.T().Logf("pactl unload-module %d failed: %v (null sink may persist until logout)", s.moduleID, err)
	}
}

// daemonProc bundles a spawned daemon's exec.Cmd, an ipc.Client bound to
// its socket, and a channel that yields the daemon's full stderr once it
// exits. Returned by launchDaemon so individual tests share lifecycle code.
type daemonProc struct {
	cmd      *exec.Cmd
	client   *ipc.Client
	stderrCh <-chan []byte
}

// TestSIGTERM_DuringRecording_ExitsZero spawns the daemon, drives it into
// StateRecording via a real Toggle, sends SIGTERM mid-record, and asserts
// the process exits with code 0. This is the contract systemd consumes:
// a non-zero exit on graceful stop would trigger Restart=on-failure loops.
func (s *VoiceShutdownIntegrationSuite) TestSIGTERM_DuringRecording_ExitsZero() {
	proc := s.launchDaemon()

	// Toggle → recording. The daemon spawns its cycle goroutine which calls
	// the live SubprocessRecorder against PULSE_SOURCE (null sink monitor).
	s.toggleAndExpect(proc.client, "recording")

	// Let the real recorder settle into capture mode. Too short and SIGTERM
	// might race the recorder's startup; too long and the recording's
	// MaxDuration could elapse on its own.
	time.Sleep(recordStartupSettleTime)

	s.Require().NoError(proc.cmd.Process.Signal(syscall.SIGTERM), "failed to send SIGTERM")

	s.assertExitZero(proc, "recording")
}

// TestSIGTERM_DuringTranscribing_ExitsZero proves the cloud STT adapter
// propagates context cancellation through the HTTP layer end-to-end. The
// daemon is pointed at a fake OpenAI endpoint that hangs every request
// until the client disconnects; the test drives the daemon into
// StateTranscribing (Toggle → record → Toggle → transcribe), waits until
// the fake server has the request in flight, then sends SIGTERM. The
// daemon must cancel the in-flight HTTP call, run LIFO shutdown, and exit
// 0 — same systemd contract as the record-phase test.
func (s *VoiceShutdownIntegrationSuite) TestSIGTERM_DuringTranscribing_ExitsZero() {
	requestArrived := make(chan struct{}, 1)
	cleanup := make(chan struct{})

	defer close(cleanup)

	fakeOpenAI := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		// Drain the multipart upload so the daemon's HTTP client doesn't
		// stall writing while our handler is still parking on Done.
		_, _ = io.Copy(io.Discard, r.Body)

		select {
		case requestArrived <- struct{}{}:
		default:
		}

		// Hang until the daemon cancels its request (SIGTERM path) OR the
		// test tears down. Without this the daemon's STT call returns
		// instantly and the SM races out of transcribing before SIGTERM
		// has a chance to land.
		select {
		case <-r.Context().Done():
		case <-cleanup:
		}
	}))
	defer fakeOpenAI.Close()

	proc := s.launchDaemon("A2TEXT_CLOUD_BASE_URL=" + fakeOpenAI.URL)

	s.toggleAndExpect(proc.client, "recording")
	time.Sleep(recordStartupSettleTime)

	// Toggle → transcribing. Cycle goroutine: recorder finalises the WAV,
	// then the cloud STT adapter dials our fake server.
	s.toggleAndExpect(proc.client, "transcribing")

	// Wait for the daemon's HTTP request to actually reach our handler —
	// SM state alone isn't enough, the cycle goroutine has work between
	// the transition and the HTTP dial. Without this signal SIGTERM could
	// land before an in-flight HTTP call exists.
	select {
	case <-requestArrived:
	case <-time.After(5 * time.Second):
		s.FailNow("fake OpenAI server never received the transcription request")
	}

	s.Require().NoError(proc.cmd.Process.Signal(syscall.SIGTERM), "failed to send SIGTERM")

	s.assertExitZero(proc, "transcribing")
}

// launchDaemon writes a config, spawns `a2text --daemon`, waits for the
// socket to come online, and returns the running process plus an ipc.Client
// already pointed at it. Tests can pass extraEnv to override or add env
// vars (e.g. A2TEXT_CLOUD_BASE_URL for the fake-OpenAI test).
//
// The Cleanup hook kills the process if it survived past the test's
// natural exit (e.g. a FailNow before cmd.Wait ran), so a daemon never
// leaks across tests.
func (s *VoiceShutdownIntegrationSuite) launchDaemon(extraEnv ...string) *daemonProc {
	s.T().Helper()

	runtimeDir := s.T().TempDir()
	tempDir := s.T().TempDir()
	configPath := s.writeConfig(tempDir)

	env := append(os.Environ(),
		"XDG_RUNTIME_DIR="+runtimeDir,
		"PULSE_SOURCE="+shutdownNullSinkName+".monitor",
		// Env doubles as override so the test fails fast if the YAML on
		// disk is somehow shadowed by a stale local override.
		"A2TEXT_PROVIDER=cloud",
		"A2TEXT_CLOUD_PROVIDER=openai",
		"A2TEXT_CLOUD_API_KEY=fake-key-for-shutdown-test",
		"A2TEXT_LANGUAGE=ru",
		"A2TEXT_TEMP_DIR="+tempDir,
	)
	env = append(env, extraEnv...)

	cmd := exec.CommandContext(
		s.T().Context(), s.binaryPath, "--daemon", "--config", configPath,
	)
	cmd.Env = env

	stderr, err := cmd.StderrPipe()
	s.Require().NoError(err)

	stderrCh := make(chan []byte, 1)

	go func() {
		buf, _ := io.ReadAll(stderr)
		stderrCh <- buf
	}()

	s.Require().NoError(cmd.Start(), "failed to start daemon binary")

	s.T().Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	socketPath := filepath.Join(runtimeDir, "a2text", "a2text-voice.sock")
	client := s.waitDaemonReady(socketPath)

	return &daemonProc{cmd: cmd, client: client, stderrCh: stderrCh}
}

// assertExitZero waits for the daemon to exit and asserts a 0 exit code,
// surfacing stderr on failure so a non-graceful shutdown is debuggable.
func (s *VoiceShutdownIntegrationSuite) assertExitZero(proc *daemonProc, scenario string) {
	s.T().Helper()

	waitErr := s.waitProcess(proc.cmd)

	var stderrBuf []byte
	select {
	case stderrBuf = <-proc.stderrCh:
	case <-time.After(time.Second):
	}

	s.Require().NoErrorf(waitErr,
		"daemon must exit 0 on SIGTERM during %s (got %v).\nstderr:\n%s",
		scenario, waitErr, stderrBuf,
	)
	s.Zero(proc.cmd.ProcessState.ExitCode(),
		"exit code must be 0 for systemd-friendly graceful stop")
}

// toggleAndExpect sends one Toggle with a bounded per-call timeout and
// asserts the post-toggle state. Fresh ctx per call avoids carrying over
// cancellation from a previous iteration.
func (s *VoiceShutdownIntegrationSuite) toggleAndExpect(client *ipc.Client, wantState string) {
	s.T().Helper()

	ctx, cancel := context.WithTimeout(s.T().Context(), 2*time.Second)
	defer cancel()

	resp, err := client.Toggle(ctx)
	s.Require().NoError(err)
	s.Equal(wantState, resp.State)
}

// writeConfig produces a minimal voice config the binary can load. Cloud
// provider with a fake key means BuildTranscriber succeeds without any
// network round-trip (no startup probe), and SIGTERM lands well before
// any transcribe call would actually need the key.
func (s *VoiceShutdownIntegrationSuite) writeConfig(tempDir string) string {
	s.T().Helper()

	// Config lives in a dir separate from tempDir (which becomes
	// A2TEXT_TEMP_DIR for the daemon). Mixing them would have the daemon
	// scan its own config file as a candidate temp artifact.
	path := filepath.Join(s.T().TempDir(), "voice.yaml")

	body := fmt.Sprintf(`provider: "cloud"
cloud_provider: "openai"
cloud_api_key: "fake-key-for-shutdown-test"
language: "ru"
temp_dir: %q
output:
  mode: "stdout"
capture:
  backend: "auto"
  max_duration: 30s
`, tempDir)

	s.Require().NoError(os.WriteFile(path, []byte(body), 0o600))

	return path
}

// buildBinary builds cmd/a2text into a per-suite temp dir without any
// optional build tags (so the cloud-only path is exercised). Errors fail
// the suite immediately — there is no point running the shutdown test
// against a binary that did not compile.
func (s *VoiceShutdownIntegrationSuite) buildBinary() string {
	s.T().Helper()

	tmpDir := s.T().TempDir()
	binary := filepath.Join(tmpDir, "a2text")

	ctx, cancel := context.WithTimeout(s.T().Context(), binaryBuildTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/a2text")
	// Move out of tests/ so go knows where the module root is.
	cmd.Dir = repoRoot(s.T())

	out, err := cmd.CombinedOutput()
	s.Require().NoErrorf(err, "go build failed: %s", string(out))

	return binary
}

// waitDaemonReady polls the socket until Ping returns OK or the deadline
// elapses. Polling instead of fs.Notify keeps the test simple — daemon
// readiness is sub-second in practice.
func (s *VoiceShutdownIntegrationSuite) waitDaemonReady(socketPath string) *ipc.Client {
	s.T().Helper()

	client := ipc.NewClient(socketPath, 2*time.Second)

	deadline := time.Now().Add(daemonReadyTimeout)
	for time.Now().Before(deadline) {
		if pingOnce(s.T().Context(), client) == nil {
			return client
		}

		time.Sleep(daemonReadyPollGap)
	}

	s.FailNow("daemon did not become reachable on socket within deadline", "path=%s", socketPath)

	return nil
}

// waitProcess waits for cmd.Wait() to return, with a deadline. A SIGTERM'd
// daemon that wedges past 10s is a real bug — partyzanex/shutdown caps the
// LIFO at 30s but the test should never get anywhere near that.
func (s *VoiceShutdownIntegrationSuite) waitProcess(cmd *exec.Cmd) error {
	s.T().Helper()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return err
	case <-time.After(processWaitTimeout):
		_ = cmd.Process.Kill()

		<-done

		s.FailNow("daemon did not exit within deadline after SIGTERM")

		return errors.New("daemon did not exit within deadline")
	}
}

// pingOnce wraps one Ping with a fresh per-iteration timeout. Important:
// each iteration must NOT reuse a cancelled context from the previous
// attempt, otherwise the second Ping returns "context canceled" before
// even dialing and the poll never recovers from a single transient
// network blip during socket bind.
func pingOnce(parent context.Context, client *ipc.Client) error {
	ctx, cancel := context.WithTimeout(parent, 500*time.Millisecond)
	defer cancel()

	_, err := client.Ping(ctx)

	return err
}

// repoRoot walks up from the tests/ directory to the module root. We need
// the module root so `go build ./cmd/a2text` resolves the relative package.
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}

		dir = parent
	}
}
