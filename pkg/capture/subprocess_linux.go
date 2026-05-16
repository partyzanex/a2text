//go:build linux

// Package capture provides microphone capture adapters for the voice CLI.
//
// Stage I.1 ships a subprocess backend on Linux: pw-record (PipeWire) or
// parecord (PulseAudio). See ADR-0011 for the trade-offs and graceful-stop
// contract.
package capture

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/partyzanex/a2text/pkg/audio"
)

// CommandRunner abstracts process execution so unit tests can intercept it
// without spawning real subprocesses.
//
// Run starts name with args, then orchestrates the stop:
//   - if stopAfter > 0, it sends SIGINT after stopAfter elapses;
//   - if ctx is cancelled, it sends SIGINT immediately;
//   - if the process does not exit within grace, it is SIGKILLed;
//   - returns nil on a clean exit (including SIGINT-induced clean exit
//     when WAV header was finalised — caller validates the file).
//
//go:generate go run go.uber.org/mock/mockgen@latest -write_package_comment=false -package=capture -destination=subprocess_linux_mocks_test.go -source=subprocess_linux.go CommandRunner
type CommandRunner interface {
	LookPath(name string) (string, error)
	Run(
		ctx context.Context,
		stopAfter, grace time.Duration,
		name string, args ...string,
	) error
}

// execRunner is the production runner. It uses os/exec and a manual
// signal-then-wait dance instead of CommandContext, because CommandContext
// SIGKILLs the child — that risks a truncated WAV header.
type execRunner struct{}

func (execRunner) LookPath(name string) (string, error) {
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("capture: %w", err)
	}

	return p, nil
}

func (execRunner) Run(
	ctx context.Context,
	stopAfter, grace time.Duration,
	name string, args ...string,
) error {
	// We deliberately do NOT use exec.CommandContext: it SIGKILLs the child
	// on ctx cancellation, which would truncate the WAV header. Stop is
	// orchestrated below with SIGINT-then-SIGKILL via orchestrateStop.
	//nolint:noctx,gosec // manual SIGINT stop required to keep WAV header intact; args are safe
	cmd := exec.Command(name, args...)

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("attach stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", name, err)
	}

	// Drain stderr in a goroutine so the pipe buffer cannot fill up and
	// deadlock the child. Wait() will block on the goroutine via the channel.
	stderrCh := make(chan string, 1)

	go func() {
		stderrCh <- readStderrTrunc(stderr, maxStderrBytes)
	}()

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	// Stop orchestration: race timer-elapsed vs ctx-cancel vs process-exit.
	stopReason, waitErr := orchestrateStop(cmd, ctx, stopAfter, grace, waitCh)

	stderrTail := <-stderrCh

	// SIGINT-induced "clean" exit comes back as ExitError; we treat it as
	// success because pw-record/parecord finalise the WAV header on SIGINT.
	// The caller revalidates the file via audio.ValidateAudioFormat anyway.
	if stopReason == stopReasonGraceful {
		return nil
	}

	if waitErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("capture: %w", ctxErr)
		}

		return fmt.Errorf("%s exited: %w (stderr: %s)", name, waitErr, stderrTail)
	}

	return nil
}

type stopReason int

const (
	stopReasonNatural  stopReason = iota // process exited on its own
	stopReasonGraceful                   // we sent SIGINT and it exited within grace
	stopReasonForced                     // we had to SIGKILL
)

// orchestrateStop runs the stop dance. The caller starts Wait once and passes
// the result channel in, because os/exec.Cmd.Wait must not be called
// concurrently.
func orchestrateStop(
	cmd *exec.Cmd,
	ctx context.Context,
	stopAfter, grace time.Duration,
	waitCh <-chan error,
) (stopReason, error) {
	if stopAfter <= 0 && ctx.Done() == nil {
		// No stopping mechanism configured; let the caller's Wait block.
		return stopReasonNatural, <-waitCh
	}

	var timerCh <-chan time.Time

	if stopAfter > 0 {
		t := time.NewTimer(stopAfter)
		defer t.Stop()

		timerCh = t.C
	}

	select {
	case err := <-waitCh:
		return stopReasonNatural, err
	case <-timerCh:
		return signalAndKill(cmd, grace, waitCh)
	case <-ctx.Done():
		return signalAndKill(cmd, grace, waitCh)
	}
}

func signalAndKill(
	cmd *exec.Cmd, grace time.Duration, waitCh <-chan error,
) (stopReason, error) {
	// Send SIGINT — pw-record and parecord both finalise the WAV header on
	// this signal. SIGTERM would also work; SIGKILL would not. Errors are
	// ignored: the process may have already exited, in which case waitCh will
	// report the real result.
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		_ = err
	}

	if grace <= 0 {
		grace = defaultStopGrace
	}

	select {
	case err := <-waitCh:
		return stopReasonGraceful, err
	case <-time.After(grace):
		if err := cmd.Process.Kill(); err != nil {
			_ = err
		}

		return stopReasonForced, <-waitCh
	}
}

const defaultStopGrace = 1500 * time.Millisecond

// SubprocessRecorder records mono 16-bit PCM via pw-record or parecord.
//
// Construction picks a backend at NewSubprocessRecorder time (not per-call):
// the binary search is cheap, but stable selection makes logs and metrics
// easier to reason about than "whatever was in PATH this second".
type SubprocessRecorder struct {
	runner     CommandRunner
	log        *slog.Logger
	backend    Backend
	binaryPath string
}

// NewSubprocessRecorder picks a backend by inspecting PATH and returns a
// ready-to-use Recorder. Returns ErrNoCaptureBackend if neither pw-record
// nor parecord is available.
func NewSubprocessRecorder(log *slog.Logger) (*SubprocessRecorder, error) {
	return newSubprocessRecorder(execRunner{}, log)
}

func newSubprocessRecorder(runner CommandRunner, log *slog.Logger) (*SubprocessRecorder, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	for _, backend := range []Backend{BackendPipeWire, BackendPulseAudio} {
		path, err := runner.LookPath(string(backend))
		if err == nil {
			log.Info("voice: capture backend selected",
				slog.String("backend", string(backend)),
				slog.String("path", path),
			)

			return &SubprocessRecorder{
				backend:    backend,
				binaryPath: path,
				runner:     runner,
				log:        log,
			}, nil
		}
	}

	return nil, ErrNoCaptureBackend
}

// Backend reports which utility this recorder will invoke. Useful for tests
// and depcheck output.
func (r *SubprocessRecorder) Backend() Backend {
	return r.backend
}

// RecordToFile writes a WAV (RIFF) file for opts.Duration to opts.OutputPath
// (or a temp file if empty), then validates the resulting WAV header.
//
// Stop semantics: the runner allows the subprocess opts.Duration of capture,
// then sends SIGINT so the WAV header is finalised; the file is rejected if
// it does not pass audio.ValidateAudioFormat afterwards.
func (r *SubprocessRecorder) RecordToFile(
	ctx context.Context, opts Options,
) (string, error) {
	if err := validateOptions(opts); err != nil {
		return "", err
	}

	outPath, err := resolveOutputPath(opts.OutputPath)
	if err != nil {
		return "", err
	}

	args := buildArgs(r.backend, outPath, opts)

	r.log.Debug("voice: starting capture",
		slog.String("backend", string(r.backend)),
		slog.String("file", filepath.Base(outPath)),
		slog.Duration("duration", opts.Duration),
	)

	runErr := r.runner.Run(ctx, opts.Duration, defaultStopGrace, r.binaryPath, args...)
	if runErr != nil {
		r.cleanupFailed(outPath, "subprocess error")

		return "", fmt.Errorf("capture: %w", runErr)
	}

	if err := audio.ValidateAudioFormat(outPath); err != nil {
		r.cleanupFailed(outPath, "invalid wav after capture")

		return "", fmt.Errorf("capture: %w", err)
	}

	return outPath, nil
}

func (r *SubprocessRecorder) cleanupFailed(path, why string) {
	rmErr := os.Remove(path)
	if rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		r.log.Warn("voice: failed to remove partial capture file",
			slog.String("file", filepath.Base(path)),
			slog.String("reason", why),
			slog.Any("err", rmErr),
		)
	}
}

func validateOptions(opts Options) error {
	if opts.SampleRate <= 0 {
		return fmt.Errorf("capture: sample_rate must be positive (got %d)", opts.SampleRate)
	}

	if opts.Channels <= 0 {
		return fmt.Errorf("capture: channels must be positive (got %d)", opts.Channels)
	}

	return nil
}

// resolveOutputPath returns the final WAV path. The .wav extension is
// applied here, NOT later in buildArgs, so the caller and the subprocess
// receive the exact same path — otherwise we would record to FOO.wav
// while returning FOO and orphan the temp file.
func resolveOutputPath(requested string) (string, error) {
	if requested != "" {
		return ensureWavExt(requested), nil
	}

	tempFile, err := os.CreateTemp("", "a2text-voice-*.wav")
	if err != nil {
		return "", fmt.Errorf("create temp wav: %w", err)
	}

	path := tempFile.Name()
	if closeErr := tempFile.Close(); closeErr != nil {
		return "", fmt.Errorf("close temp wav: %w", closeErr)
	}

	return path, nil
}

// buildArgs translates RecordOptions into backend-specific argv. The caller
// must pass an outPath that already has the correct .wav extension —
// resolveOutputPath handles that.
//
// pw-record auto-detects the container from the file extension, so the
// .wav extension is what triggers RIFF wrapping. parecord requires the
// explicit --file-format=wav.
func buildArgs(backend Backend, outPath string, opts Options) []string {
	rate := strconv.Itoa(opts.SampleRate)
	channels := strconv.Itoa(opts.Channels)

	switch backend {
	case BackendPipeWire:
		return []string{
			"--rate=" + rate,
			"--channels=" + channels,
			"--format=s16",
			outPath,
		}

	case BackendPulseAudio:
		return []string{
			"--file-format=wav",
			"--rate=" + rate,
			"--channels=" + channels,
			"--format=s16le",
			outPath,
		}

	default:
		// Defensive: NewSubprocessRecorder picks from a known set, so any
		// new backend that lands here means the caller forgot to update
		// buildArgs. Panic produces a stack pointing at the bug instead of
		// silently spawning with an empty argv.
		panic(fmt.Sprintf("capture: unknown backend %q in buildArgs", backend))
	}
}

func ensureWavExt(path string) string {
	if filepath.Ext(path) == ".wav" {
		return path
	}

	return path + ".wav"
}

// Compile-time interface check.
var _ Recorder = (*SubprocessRecorder)(nil)
