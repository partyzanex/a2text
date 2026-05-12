//go:build integration && linux

package tests

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"log/slog"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/a2text/internal/infra/factory"
	"github.com/partyzanex/a2text/internal/usecases/voice"
	"github.com/partyzanex/a2text/pkg/audio"
	"github.com/partyzanex/a2text/pkg/capture"
)

const (
	nullSinkName        = "a2text_test"
	recordDuration      = 3 * time.Second
	goWhisperDefaultURL = "http://localhost:9081"
)

// VoiceRecordIntegrationSuite tests the full microphone capture pipeline using
// a PulseAudio null sink as a loopback device. Audio is played into the sink
// via paplay and recorded from the sink's monitor source by SubprocessRecorder.
//
// Requires pactl, paplay, and at least one of pw-record / parecord in PATH.
// The go-whisper round-trip test additionally requires a reachable go-whisper
// service (GO_WHISPER_URL or http://localhost:9081).
type VoiceRecordIntegrationSuite struct {
	suite.Suite

	testdataDir string
	moduleID    uint32 // PulseAudio module ID for null sink teardown
}

func TestVoiceRecordIntegrationSuite(t *testing.T) {
	suite.Run(t, new(VoiceRecordIntegrationSuite))
}

func (s *VoiceRecordIntegrationSuite) SetupSuite() {
	wd, err := os.Getwd()
	s.Require().NoError(err)
	s.testdataDir = filepath.Join(wd, "testdata")

	for _, tool := range []string{"pactl", "paplay"} {
		if _, lookErr := exec.LookPath(tool); lookErr != nil {
			s.T().Skipf("tool %q not found in PATH — skipping voice capture integration tests", tool)
		}
	}

	// pw-record is tried first by capture.NewSubprocessRecorder; parecord is
	// the fallback. Skip the whole suite only if neither is found.
	_, pwErr := exec.LookPath("pw-record")

	_, paErr := exec.LookPath("parecord")
	if pwErr != nil && paErr != nil {
		s.T().Skip("neither pw-record nor parecord found in PATH — skipping voice capture integration tests")
	}

	// Load a named null sink so we have a deterministic loopback source.
	out, loadErr := exec.Command(
		"pactl", "load-module", "module-null-sink",
		"sink_name="+nullSinkName,
		"sink_properties=device.description=a2text_test_sink",
	).Output()
	s.Require().NoError(loadErr, "pactl load-module module-null-sink failed")

	id, parseErr := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 32)
	s.Require().NoError(parseErr, "unexpected module ID from pactl: %q", string(out))
	s.moduleID = uint32(id)
}

func (s *VoiceRecordIntegrationSuite) TearDownSuite() {
	if s.moduleID > 0 {
		_ = exec.Command("pactl", "unload-module", strconv.FormatUint(uint64(s.moduleID), 10)).Run()
	}
}

// TestRecorder_NullSink_ProducesValidWAV records 3 s from the null sink
// monitor while paplay feeds jfk.wav into the sink. The recorded file must
// pass audio.ValidateAudioFormat — that proves the subprocess finalised the
// WAV header correctly on SIGINT.
func (s *VoiceRecordIntegrationSuite) TestRecorder_NullSink_ProducesValidWAV() {
	// PULSE_SOURCE routes both parecord and pw-record (via PipeWire-PA compat)
	// to the null sink monitor.
	s.T().Setenv("PULSE_SOURCE", nullSinkName+".monitor")

	log := slog.New(slog.DiscardHandler)

	rec, err := capture.NewSubprocessRecorder(log)
	s.Require().NoError(err)

	playCtx, cancelPlay := context.WithCancel(context.Background())
	defer cancelPlay()

	go s.playIntoSink(playCtx, "jfk.wav")

	ctx, cancel := context.WithTimeout(context.Background(), recordDuration+10*time.Second)
	defer cancel()

	outPath, err := rec.RecordToFile(ctx, voice.RecordOptions{
		Duration:   recordDuration,
		SampleRate: 16000,
		Channels:   1,
	})
	s.Require().NoError(err)

	defer func() { _ = os.Remove(outPath) }()

	s.Require().NoError(audio.ValidateAudioFormat(outPath), "recorded file failed WAV validation")
}

// TestRecordOneshotUseCase_GoWhisper is a full end-to-end round-trip:
// record spasibo_ru.ogg played into the null sink, transcribe via go-whisper,
// and verify the output contains "спасибо".
//
// Skipped when go-whisper is not reachable (GO_WHISPER_URL env, default
// http://localhost:9081).
func (s *VoiceRecordIntegrationSuite) TestRecordOneshotUseCase_GoWhisper() {
	goWhisperURL := os.Getenv("GO_WHISPER_URL")
	if goWhisperURL == "" {
		goWhisperURL = goWhisperDefaultURL
	}

	if !s.probeGoWhisper(goWhisperURL) {
		s.T().Skipf("go-whisper not reachable at %s — set GO_WHISPER_URL to override", goWhisperURL)
	}

	s.T().Setenv("PULSE_SOURCE", nullSinkName+".monitor")

	log := slog.New(slog.DiscardHandler)

	rec, err := capture.NewSubprocessRecorder(log)
	s.Require().NoError(err)

	transcriber, err := factory.Build(context.Background(), &factory.Config{
		Provider:       factory.ProviderGoWhisper,
		GoWhisperURL:   goWhisperURL,
		GoWhisperModel: "ggml-small",
	}, log)
	s.Require().NoError(err)

	var captured string

	out := captureOutput(func(_ context.Context, text string) error {
		captured = text

		return nil
	})

	useCase := voice.NewRecordOneshotUseCase(rec, transcriber, out, log)

	// spasibo_ru.ogg is ~1 s, so the 3 s window captures it in full.
	playCtx, cancelPlay := context.WithCancel(context.Background())
	defer cancelPlay()

	go s.playIntoSink(playCtx, "spasibo_ru.ogg")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	s.Require().NoError(useCase.Run(ctx, recordDuration, "ru"))

	s.T().Logf("transcribed: %q", captured)
	s.Contains(strings.ToLower(captured), "спасибо")
}

// probeGoWhisper returns true when the go-whisper model endpoint responds.
func (s *VoiceRecordIntegrationSuite) probeGoWhisper(baseURL string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, baseURL+"/api/whisper/model", nil)
	if err != nil {
		return false
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}

	_ = resp.Body.Close()

	return true
}

// playIntoSink plays filename from testdata into the null sink. The goroutine
// exits when ctx is cancelled or paplay finishes naturally.
func (s *VoiceRecordIntegrationSuite) playIntoSink(ctx context.Context, filename string) {
	cmd := exec.CommandContext(ctx, "paplay",
		"--device="+nullSinkName,
		filepath.Join(s.testdataDir, filename),
	)
	_ = cmd.Run()
}

// captureOutput adapts a function literal to the voice.Output interface so
// tests can collect the delivered text without importing extra packages.
type captureOutput func(ctx context.Context, text string) error

func (fn captureOutput) Deliver(ctx context.Context, text string) error {
	return fn(ctx, text)
}
