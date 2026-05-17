package voice

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/partyzanex/a2text/internal/domain"
)

// Streaming side-channel tuning.
const (
	pcmKillGrace        = 1500 * time.Millisecond
	pcmStderrTailBytes  = 4 * 1024
	pcmStartGracePeriod = 200 * time.Millisecond

	// pcmTailGrace keeps the streaming PCM subprocess (ffmpeg) alive past
	// recordCtx cancellation. When the user hits F4 the main recorder
	// stops at once, but the last spoken word's audio may still be in
	// flight to Deepgram or not yet finalized server-side. Holding the
	// audio channel open for this long lets Deepgram receive that tail
	// audio plus enough silence to emit the closing final.
	pcmTailGrace = 1500 * time.Millisecond
)

// StreamingTranscriber is implemented by transcribers that can consume a
// live PCM io.Reader concurrently with recording. When a Transcriber also
// satisfies this interface, Cycle runs a concurrent record + stream path
// so the bulk of transcription latency is hidden by the recording phase.
//
// pcm must be 16kHz, 16-bit little-endian, mono. Stream returns the
// accumulated final transcript once pcm reaches EOF (or ctx cancels).
type StreamingTranscriber interface {
	Stream(ctx context.Context, pcm io.Reader, lang string) (string, error)
}

// streamingCapableTranscriber asserts the wired Transcriber also speaks
// the streaming protocol; returns nil otherwise. Loads the atomic
// pointer once so a concurrent Swap cannot flip the answer mid-Cycle.
func (uc *VoiceUseCase) streamingCapableTranscriber() StreamingTranscriber {
	transcriber := uc.transcriberLoad()
	if transcriber == nil {
		return nil
	}

	if streamer, ok := transcriber.(StreamingTranscriber); ok {
		return streamer
	}

	return nil
}

// streamingCycle runs the dictation cycle when the wired transcriber
// supports concurrent streaming. Two captures run in parallel:
//
//   - the regular pw-record/parecord recorder produces the persisted WAV
//     used by the archiver and any downstream tooling;
//   - a side-channel ffmpeg/parec subprocess pipes raw PCM straight into
//     the streamer, so finals start arriving while the user is still
//     talking.
//
// pw-record cannot serve as the side channel because libsndfile buffers
// WAV writes until the process exits — that defeats realtime entirely.
// ffmpeg with pulse input flushes packets every few hundred bytes.
func (uc *VoiceUseCase) streamingCycle(
	ctx, recordCtx context.Context,
	streamer StreamingTranscriber,
	opts domain.RecordOpts, lang string,
) (domain.CycleResult, error) {
	uc.log.Info("voice: streamingCycle entered (concurrent record + stream)")

	// PCM subprocess is bound to the OUTER ctx, not recordCtx. recordCtx
	// dies the moment the user toggles off (F4); ffmpeg must outlive that
	// by pcmTailGrace so the trailing word's audio still reaches Deepgram
	// and the server has time to emit the closing final. Outer ctx still
	// kills the subprocess on hard cancel (shutdown / discard).
	pcm, pcmErr := startStreamingPCM(ctx, uc.log)
	if pcmErr != nil {
		uc.log.Warn("voice: streaming side-channel unavailable, sequential fallback",
			slog.Any("err", pcmErr))

		return uc.sequentialCycle(ctx, recordCtx, opts, lang)
	}

	defer uc.closeStreamSilently(pcm)

	streamCh := uc.spawnStreamer(ctx, streamer, pcm, lang)

	// Brief grace before launching the main recorder so the streamer's
	// ffmpeg subprocess has a chance to grab the audio source first. On
	// PipeWire both processes can capture concurrently but the streaming
	// side benefits from being attached before the user starts speaking.
	time.Sleep(pcmStartGracePeriod)

	audioPath, audioSize, recordErr := uc.recordAndValidate(recordCtx, opts)
	if recordErr != nil {
		uc.closeStreamSilently(pcm)
		<-streamCh

		return domain.CycleResult{}, recordErr
	}

	defer uc.archiveAndCleanup(ctx, audioPath)

	// Hold the streaming audio open past recordCtx cancellation so the
	// trailing word and ~1s of silence reach Deepgram. Without this the
	// last spoken word is frequently dropped because ffmpeg dies before
	// flushing it and the server never emits a final for the orphaned
	// utterance.
	uc.waitTailGrace(ctx)

	uc.closeStreamSilently(pcm)

	return uc.finaliseStream(ctx, streamCh, audioSize)
}

// waitTailGrace sleeps pcmTailGrace, or returns sooner if the outer ctx
// is cancelled (shutdown / discard). Logged so operators can see the
// extra latency in the cycle timeline.
func (uc *VoiceUseCase) waitTailGrace(ctx context.Context) {
	uc.log.Debug("voice: holding streaming audio for tail grace",
		slog.Duration("grace", pcmTailGrace))

	select {
	case <-time.After(pcmTailGrace):
	case <-ctx.Done():
	}
}

// closeStreamSilently closes the PCM side-channel and surfaces any error
// at debug level. The streaming subprocess always reports SIGINT as a
// non-zero exit, so closing produces a benign error every time — we never
// want that noise at INFO.
func (uc *VoiceUseCase) closeStreamSilently(pcm io.Closer) {
	if pcm == nil {
		return
	}

	if err := pcm.Close(); err != nil {
		uc.log.Debug("voice: streaming pcm close", slog.Any("err", err))
	}
}

// streamResult bundles the streamer's return values so spawnStreamer can
// hand a single value down the result channel.
type streamResult struct {
	text string
	err  error
	took time.Duration
}

// spawnStreamer launches the streaming transcriber on a goroutine. The
// returned channel receives exactly one value when Stream returns.
func (uc *VoiceUseCase) spawnStreamer(
	ctx context.Context, streamer StreamingTranscriber, pcm io.Reader, lang string,
) <-chan streamResult {
	ch := make(chan streamResult, 1)

	go func() {
		started := time.Now()

		text, err := streamer.Stream(ctx, pcm, lang)

		ch <- streamResult{text: text, err: err, took: time.Since(started)}
	}()

	return ch
}

// archiveAndCleanup runs the kept-audio archiver and removes the temp WAV.
func (uc *VoiceUseCase) archiveAndCleanup(ctx context.Context, audioPath string) {
	uc.runArchiver(ctx, audioPath)

	if rmErr := os.Remove(audioPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		uc.log.Warn("voice: temp recording cleanup failed",
			slog.String("file", filepath.Base(audioPath)),
			slog.Any("err", rmErr),
		)
	}
}

// finaliseStream waits for the streamer to return, delivers the text, and
// assembles the CycleResult.
func (uc *VoiceUseCase) finaliseStream(
	ctx context.Context, streamCh <-chan streamResult, audioSize int64,
) (domain.CycleResult, error) {
	if ctxErr := ctx.Err(); ctxErr != nil {
		<-streamCh

		return domain.CycleResult{}, &domain.CycleError{Phase: domain.PhaseTranscribe, Err: ctxErr}
	}

	result := <-streamCh
	if result.err != nil {
		uc.log.Warn("voice: streaming transcriber returned error",
			slog.Any("err", result.err),
			slog.Int("partial_len", len(result.text)))
	}

	text := strings.TrimSpace(result.text)
	if text == "" {
		return domain.CycleResult{}, &domain.CycleError{Phase: domain.PhaseTranscribe, Err: domain.ErrEmptyResult}
	}

	if ctxErr := ctx.Err(); ctxErr != nil {
		return domain.CycleResult{}, &domain.CycleError{Phase: domain.PhaseDeliver, Err: ctxErr}
	}

	if deliverErr := uc.outputLoad().Deliver(ctx, text); deliverErr != nil {
		return domain.CycleResult{}, &domain.CycleError{Phase: domain.PhaseDeliver, Err: deliverErr}
	}

	return domain.CycleResult{
		Text:          text,
		AudioDuration: domain.EstimateAudioDuration(audioSize),
		STTDuration:   result.took,
	}, nil
}

// sequentialCycle is the original record→transcribe→deliver pipeline. It
// is the fallback when the transcriber does not support streaming OR when
// the streaming side-channel cannot start.
func (uc *VoiceUseCase) sequentialCycle(
	ctx, recordCtx context.Context,
	opts domain.RecordOpts, lang string,
) (domain.CycleResult, error) {
	audioPath, audioSize, err := uc.recordAndValidate(recordCtx, opts)
	if err != nil {
		return domain.CycleResult{}, err
	}

	defer uc.archiveAndCleanup(ctx, audioPath)

	if ctxErr := ctx.Err(); ctxErr != nil {
		return domain.CycleResult{}, &domain.CycleError{Phase: domain.PhaseTranscribe, Err: ctxErr}
	}

	return uc.transcribeAndDeliver(ctx, audioPath, audioSize, lang)
}

// startStreamingPCM spawns the side-channel audio capture and returns a
// ReadCloser yielding raw 16-bit little-endian mono PCM at 16kHz.
// Closing the returned reader terminates the subprocess.
//
// Backend selection (first available wins):
//
//  1. parec --raw — Pulse/PipeWire-compat. Writes raw PCM directly to
//     stdout, flushes per-period.
//  2. ffmpeg -f pulse — universal fallback. Flushes packets aggressively.
//
// pw-record is intentionally NOT used: it relies on libsndfile's WAV
// writer which buffers the data chunk until close, so the entire
// recording is delivered to stdout in a single burst at SIGINT — useless
// for realtime.
func startStreamingPCM(recordCtx context.Context, log *slog.Logger) (io.ReadCloser, error) {
	binary, args, err := pickStreamingPCMCmd()
	if err != nil {
		return nil, err
	}

	//nolint:noctx,gosec // binary is one of parec/ffmpeg; args are constants
	cmd := exec.Command(binary, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("voice: stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("voice: stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("voice: start %s: %w", binary, err)
	}

	go drainStderr(stderr, log, filepath.Base(binary))
	go watchCancel(recordCtx, cmd, log)

	log.Info("voice: streaming pcm capture started",
		slog.String("backend", filepath.Base(binary)))

	return &pcmStream{cmd: cmd, stdout: stdout, log: log}, nil
}

// pcmStream wraps the subprocess + its stdout so the consumer side has a
// single ReadCloser to manage. Closing it stops the subprocess.
//
// Close uses sync.Once so the defer + explicit close in streamingCycle
// only perform the shutdown work once; subsequent calls are no-ops. We
// deliberately keep all the per-step debug logs (stdout close, sigint,
// wait) inside the once-block so they fire exactly once per cycle and
// stay visible at debug level for diagnosis — they reflect real
// teardown steps, not noise, and the user has asked to keep them
// visible.
type pcmStream struct {
	cmd       *exec.Cmd
	stdout    io.ReadCloser
	log       *slog.Logger
	closeOnce sync.Once
}

func (p *pcmStream) Read(buf []byte) (int, error) {
	n, err := p.stdout.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, fmt.Errorf("pcm stdout read: %w", err)
	}

	return n, err //nolint:wrapcheck // io.EOF must propagate unwrapped
}

func (p *pcmStream) Close() error {
	p.closeOnce.Do(p.shutdown)

	return nil
}

func (p *pcmStream) shutdown() {
	if closeErr := p.stdout.Close(); closeErr != nil {
		p.log.Debug("voice: pcm stdout close", slog.Any("err", closeErr))
	}

	if p.cmd.Process != nil {
		if sigErr := p.cmd.Process.Signal(syscall.SIGINT); sigErr != nil {
			p.log.Debug("voice: pcm sigint", slog.Any("err", sigErr))
		}
	}

	done := make(chan struct{})

	go func() {
		if waitErr := p.cmd.Wait(); waitErr != nil {
			p.log.Debug("voice: pcm wait", slog.Any("err", waitErr))
		}

		close(done)
	}()

	select {
	case <-done:
	case <-time.After(pcmKillGrace):
		if p.cmd.Process != nil {
			if killErr := p.cmd.Process.Kill(); killErr != nil {
				p.log.Debug("voice: pcm kill", slog.Any("err", killErr))
			}
		}

		<-done
	}
}

// pickStreamingPCMCmd returns the binary path + argv for the side-channel
// audio capture. parec --raw is preferred for its minimal overhead; ffmpeg
// is the universal fallback.
func pickStreamingPCMCmd() (binaryPath string, args []string, err error) {
	if path, lookErr := exec.LookPath("parec"); lookErr == nil {
		return path, []string{
			"--raw",
			"--rate=16000",
			"--channels=1",
			"--format=s16le",
		}, nil
	}

	if path, lookErr := exec.LookPath("ffmpeg"); lookErr == nil {
		return path, []string{
			"-hide_banner",
			"-loglevel", "warning",
			"-f", "pulse",
			"-i", "default",
			"-ar", "16000",
			"-ac", "1",
			"-f", "s16le",
			"-flush_packets", "1",
			"-",
		}, nil
	}

	return "", nil, errors.New("voice: streaming requires parec or ffmpeg on PATH")
}

// drainStderr reads a small head of the subprocess's stderr so the pipe
// buffer cannot deadlock the child, and surfaces it at debug level.
// Raw ffmpeg-style logs are cleaned via cleanStderrTail before logging so
// the operator does not have to wade through hex pointers and repeated
// shutdown cascades that all mean "SIGINT was honoured".
func drainStderr(stderr io.ReadCloser, log *slog.Logger, backend string) {
	buf := make([]byte, pcmStderrTailBytes)

	n, readErr := io.ReadFull(stderr, buf)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		log.Debug("voice: streaming capture stderr read err",
			slog.String("backend", backend), slog.Any("err", readErr))
	}

	if n == 0 {
		return
	}

	cleaned := cleanStderrTail(string(buf[:n]))
	if cleaned == "" {
		return
	}

	log.Debug("voice: streaming capture stderr",
		slog.String("backend", backend),
		slog.String("tail", cleaned))
}

// ffmpegPointerRE matches the " @ 0x..." memory addresses ffmpeg embeds
// in every per-stream log prefix. They are useless to a human reader and
// just inflate the log line.
var ffmpegPointerRE = regexp.MustCompile(` @ 0x[0-9a-fA-F]+`)

// cleanStderrTail rewrites the subprocess stderr blob into something a
// human can read:
//
//   - drops the hex pointer addresses ffmpeg attaches to every prefix;
//   - collapses duplicate lines into "(xN)" suffixes;
//   - drops the long shutdown cascade that always follows SIGINT
//     (Immediate exit requested → muxer error → trailer error → close
//     error) and replaces it with a single "shutdown via SIGINT" line;
//   - preserves anything else verbatim so unexpected failures still
//     surface clearly.
func cleanStderrTail(raw string) string {
	stripped := ffmpegPointerRE.ReplaceAllString(raw, "")

	var (
		out        []string
		seenSigint bool
		prev       string
		repeats    int
	)

	flushPrev := func() {
		if prev == "" {
			return
		}

		if repeats > 1 {
			out = append(out, fmt.Sprintf("%s (x%d)", prev, repeats))
		} else {
			out = append(out, prev)
		}

		prev = ""
		repeats = 0
	}

	for line := range strings.SplitSeq(stripped, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if isFfmpegSigintCascade(line) {
			if !seenSigint {
				flushPrev()

				out = append(out, "shutdown via SIGINT (normal stop)")
				seenSigint = true
			}

			continue
		}

		if line == prev {
			repeats++

			continue
		}

		flushPrev()

		prev = line
		repeats = 1
	}

	flushPrev()

	return strings.Join(out, "; ")
}

// isFfmpegSigintCascade matches the family of lines ffmpeg emits when it
// honours our SIGINT — all variants of "Immediate exit requested" plus
// the muxer/trailer/close-file errors that derive from it.
func isFfmpegSigintCascade(line string) bool {
	switch {
	case strings.Contains(line, "Immediate exit requested"):
		return true
	case strings.Contains(line, "Error muxing a packet"):
		return true
	case strings.Contains(line, "Error writing trailer"):
		return true
	case strings.Contains(line, "Error closing file"):
		return true
	case strings.Contains(line, "Terminating thread with return code"):
		return true
	case strings.Contains(line, "Task finished with error code"):
		return true
	default:
		return false
	}
}

// watchCancel sends SIGINT to the streaming subprocess once ctx ends so it
// finalises and exits, freeing the audio source for the next cycle.
func watchCancel(ctx context.Context, cmd *exec.Cmd, log *slog.Logger) {
	<-ctx.Done()

	if cmd.Process == nil {
		return
	}

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		log.Debug("voice: SIGINT to streaming capture failed", slog.Any("err", err))
	}
}
