//go:build linux

package capture

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"
)

type SubprocessRecorderSuite struct {
	suite.Suite

	log  *slog.Logger
	ctrl *gomock.Controller
}

func TestSubprocessRecorderSuite(t *testing.T) {
	suite.Run(t, new(SubprocessRecorderSuite))
}

func (s *SubprocessRecorderSuite) SetupTest() {
	s.log = slog.New(slog.DiscardHandler)
	s.ctrl = gomock.NewController(s.T())
}

// --- Backend selection ---

func (s *SubprocessRecorderSuite) TestNew_PrefersPipeWireOverPulseAudio() {
	runner := NewMockCommandRunner(s.ctrl)
	runner.EXPECT().LookPath("pw-record").Return("/usr/bin/pw-record", nil)

	rec, err := newSubprocessRecorder(runner, s.log)
	s.Require().NoError(err)
	s.Equal(BackendPipeWire, rec.Backend())
}

func (s *SubprocessRecorderSuite) TestNew_FallsBackToParecord() {
	runner := NewMockCommandRunner(s.ctrl)
	runner.EXPECT().LookPath("pw-record").Return("", errors.New("not found"))
	runner.EXPECT().LookPath("parecord").Return("/usr/bin/parecord", nil)

	rec, err := newSubprocessRecorder(runner, s.log)
	s.Require().NoError(err)
	s.Equal(BackendPulseAudio, rec.Backend())
}

func (s *SubprocessRecorderSuite) TestNew_NoBackend_ReturnsErrNoCaptureBackend() {
	runner := NewMockCommandRunner(s.ctrl)
	runner.EXPECT().LookPath("pw-record").Return("", errors.New("not found"))
	runner.EXPECT().LookPath("parecord").Return("", errors.New("not found"))

	rec, err := newSubprocessRecorder(runner, s.log)
	s.Require().Error(err)
	s.Require().ErrorIs(err, ErrNoCaptureBackend)
	s.Nil(rec)
}

func (s *SubprocessRecorderSuite) TestNew_NilLog_DoesNotPanic() {
	runner := NewMockCommandRunner(s.ctrl)
	runner.EXPECT().LookPath("pw-record").Return("/usr/bin/pw-record", nil)

	rec, err := newSubprocessRecorder(runner, nil)
	s.Require().NoError(err)
	s.NotNil(rec)
}

// --- Argv composition ---

func (s *SubprocessRecorderSuite) TestBuildArgs_PipeWire_FixedShape() {
	args := buildArgs(BackendPipeWire, "/tmp/out.wav", Options{
		SampleRate: 16000,
		Channels:   1,
	})

	s.Equal([]string{
		"--rate=16000",
		"--channels=1",
		"--format=s16",
		"/tmp/out.wav",
	}, args)
}

func (s *SubprocessRecorderSuite) TestBuildArgs_Parecord_RequiresExplicitFileFormat() {
	args := buildArgs(BackendPulseAudio, "/tmp/out.wav", Options{
		SampleRate: 16000,
		Channels:   1,
	})

	s.Equal([]string{
		"--file-format=wav",
		"--rate=16000",
		"--channels=1",
		"--format=s16le",
		"/tmp/out.wav",
	}, args)
}

func (s *SubprocessRecorderSuite) TestBuildArgs_DoesNotMutateExtension() {
	// Ext correction is the responsibility of resolveOutputPath, not buildArgs.
	// If buildArgs added .wav here, the Recorder would record to FOO.wav
	// while returning FOO and orphan the file.
	args := buildArgs(BackendPipeWire, "/tmp/no-ext", Options{
		SampleRate: 16000,
		Channels:   1,
	})

	s.Equal("/tmp/no-ext", args[len(args)-1])
}

func (s *SubprocessRecorderSuite) TestBuildArgs_UnknownBackend_Panics() {
	s.Panics(func() {
		buildArgs(Backend("garbage"), "/tmp/out.wav", Options{
			SampleRate: 16000,
			Channels:   1,
		})
	})
}

// --- resolveOutputPath: extension is applied here, exactly once ---

func (s *SubprocessRecorderSuite) TestResolveOutputPath_AddsWavExt() {
	p, err := resolveOutputPath("/tmp/foo")
	s.Require().NoError(err)
	s.Equal("/tmp/foo.wav", p)
}

func (s *SubprocessRecorderSuite) TestResolveOutputPath_KeepsExistingExt() {
	p, err := resolveOutputPath("/tmp/foo.wav")
	s.Require().NoError(err)
	s.Equal("/tmp/foo.wav", p)
}

func (s *SubprocessRecorderSuite) TestResolveOutputPath_TempFileHasWavExt() {
	p, err := resolveOutputPath("")
	s.Require().NoError(err)
	s.T().Cleanup(func() { _ = os.Remove(p) })
	s.Equal(".wav", filepath.Ext(p))
}

// --- Validation ---

func (s *SubprocessRecorderSuite) TestRecord_RejectsZeroSampleRate() {
	runner := NewMockCommandRunner(s.ctrl)
	runner.EXPECT().LookPath("pw-record").Return("/usr/bin/pw-record", nil)
	rec := s.recorderWith(runner)

	_, err := rec.RecordToFile(context.Background(), Options{Channels: 1})
	s.Require().Error(err)
	s.Contains(err.Error(), "sample_rate")
}

func (s *SubprocessRecorderSuite) TestRecord_RejectsZeroChannels() {
	runner := NewMockCommandRunner(s.ctrl)
	runner.EXPECT().LookPath("pw-record").Return("/usr/bin/pw-record", nil)
	rec := s.recorderWith(runner)

	_, err := rec.RecordToFile(context.Background(), Options{SampleRate: 16000})
	s.Require().Error(err)
	s.Contains(err.Error(), "channels")
}

// --- Happy path ---

func (s *SubprocessRecorderSuite) TestRecord_WritesValidWav_AtRequestedPath() {
	outPath := filepath.Join(s.T().TempDir(), "rec.wav")

	runner := NewMockCommandRunner(s.ctrl)
	runner.EXPECT().LookPath("pw-record").Return("/usr/bin/pw-record", nil)
	runner.EXPECT().
		Run(
			gomock.Any(), 100*time.Millisecond, defaultStopGrace, "/usr/bin/pw-record",
			gomock.Any(), gomock.Any(), gomock.Any(), outPath,
		).
		DoAndReturn(func(_ context.Context, _, _ time.Duration, _ string, args ...string) error {
			// Final positional arg is the destination — write a minimal but
			// valid RIFF/WAVE header so audio.ValidateAudioFormat accepts it.
			return writeMinimalWav(args[len(args)-1])
		})

	rec := s.recorderWith(runner)

	got, err := rec.RecordToFile(context.Background(), Options{
		Duration:   100 * time.Millisecond,
		OutputPath: outPath,
		SampleRate: 16000,
		Channels:   1,
	})
	s.Require().NoError(err)
	s.Equal(outPath, got)

	stat, statErr := os.Stat(outPath)
	s.Require().NoError(statErr)
	s.Positive(stat.Size())
}

func (s *SubprocessRecorderSuite) TestRecord_PathReturnedMatchesPathRecordedTo() {
	// Catch the original ext-mismatch bug: requested path without .wav ext
	// must be normalised, AND the same normalised path must be returned.
	dir := s.T().TempDir()
	requested := filepath.Join(dir, "rec") // no .wav
	wantPath := requested + ".wav"

	runner := NewMockCommandRunner(s.ctrl)
	runner.EXPECT().LookPath("pw-record").Return("/usr/bin/pw-record", nil)
	runner.EXPECT().
		Run(
			gomock.Any(), 100*time.Millisecond, defaultStopGrace, "/usr/bin/pw-record",
			gomock.Any(), gomock.Any(), gomock.Any(), wantPath,
		).
		DoAndReturn(func(_ context.Context, _, _ time.Duration, _ string, args ...string) error {
			// Subprocess is told where to write; we mimic it by writing there.
			recordPath := args[len(args)-1]
			s.Equal(wantPath, recordPath, "buildArgs and return path must agree on extension")

			return writeMinimalWav(recordPath)
		})

	rec := s.recorderWith(runner)

	got, err := rec.RecordToFile(context.Background(), Options{
		Duration:   100 * time.Millisecond,
		OutputPath: requested,
		SampleRate: 16000,
		Channels:   1,
	})
	s.Require().NoError(err)
	s.Equal(wantPath, got)
}

// --- Failure paths ---

func (s *SubprocessRecorderSuite) TestRecord_SubprocessError_RemovesPartialFile() {
	outPath := filepath.Join(s.T().TempDir(), "rec.wav")
	s.Require().NoError(os.WriteFile(outPath, []byte("stale"), 0o600))

	runner := NewMockCommandRunner(s.ctrl)
	runner.EXPECT().LookPath("pw-record").Return("/usr/bin/pw-record", nil)
	runner.EXPECT().
		Run(
			gomock.Any(), gomock.Any(), gomock.Any(), "/usr/bin/pw-record",
			gomock.Any(), gomock.Any(), gomock.Any(), outPath,
		).
		DoAndReturn(func(_ context.Context, _, _ time.Duration, _ string, _ ...string) error {
			return errors.New("pw-record died")
		})

	rec := s.recorderWith(runner)

	_, err := rec.RecordToFile(context.Background(), Options{
		OutputPath: outPath,
		SampleRate: 16000,
		Channels:   1,
	})
	s.Require().Error(err)
	s.Contains(err.Error(), "capture")

	_, statErr := os.Stat(outPath)
	s.Require().ErrorIs(statErr, os.ErrNotExist, "failed capture must not leave files behind")
}

func (s *SubprocessRecorderSuite) TestRecord_InvalidWavAfterSuccess_Rejected() {
	// Subprocess "succeeds" but writes garbage — Recorder must reject and clean up.
	outPath := filepath.Join(s.T().TempDir(), "rec.wav")

	runner := NewMockCommandRunner(s.ctrl)
	runner.EXPECT().LookPath("pw-record").Return("/usr/bin/pw-record", nil)
	runner.EXPECT().
		Run(
			gomock.Any(), gomock.Any(), gomock.Any(), "/usr/bin/pw-record",
			gomock.Any(), gomock.Any(), gomock.Any(), outPath,
		).
		DoAndReturn(func(_ context.Context, _, _ time.Duration, _ string, args ...string) error {
			// Not a WAV: no RIFF magic.
			return os.WriteFile(args[len(args)-1], []byte("garbage"), 0o600)
		})

	rec := s.recorderWith(runner)

	_, err := rec.RecordToFile(context.Background(), Options{
		OutputPath: outPath,
		SampleRate: 16000,
		Channels:   1,
	})
	s.Require().Error(err)
	s.Contains(err.Error(), "capture")

	_, statErr := os.Stat(outPath)
	s.Require().ErrorIs(statErr, os.ErrNotExist, "garbage capture must not be returned")
}

// --- execRunner stop orchestration ---

func (s *SubprocessRecorderSuite) TestExecRunner_SIGINTExitWithinGrace_ReturnsNil() {
	start := time.Now()

	err := execRunner{}.Run(
		context.Background(),
		20*time.Millisecond,
		500*time.Millisecond,
		"/bin/sh",
		"-c",
		"trap 'exit 0' INT; while :; do :; done",
	)

	s.Require().NoError(err)
	s.Less(time.Since(start), 300*time.Millisecond, "must not wait for grace after clean SIGINT exit")
}

// --- Helpers ---

func (s *SubprocessRecorderSuite) recorderWith(runner CommandRunner) *SubprocessRecorder {
	rec, err := newSubprocessRecorder(runner, s.log)
	s.Require().NoError(err)

	return rec
}

// writeMinimalWav emits a valid-but-empty RIFF/WAVE file: header + fmt chunk
// + empty data chunk. Enough to satisfy audio.ValidateAudioFormat without
// running a real recorder.
func writeMinimalWav(path string) error {
	const (
		sampleRate = 16000
		bitDepth   = 16
		channels   = 1
	)

	byteRate := sampleRate * channels * (bitDepth / 8)
	blockAlign := uint16(channels * (bitDepth / 8))

	header := make([]byte, 0, 44)
	header = append(header, []byte("RIFF")...)
	header = binary.LittleEndian.AppendUint32(header, 36) // file size - 8
	header = append(header, []byte("WAVE")...)
	header = append(header, []byte("fmt ")...)
	header = binary.LittleEndian.AppendUint32(header, 16) // fmt chunk size (PCM)
	header = binary.LittleEndian.AppendUint16(header, 1)  // PCM
	header = binary.LittleEndian.AppendUint16(header, channels)
	header = binary.LittleEndian.AppendUint32(header, sampleRate)
	header = binary.LittleEndian.AppendUint32(header, uint32(byteRate))
	header = binary.LittleEndian.AppendUint16(header, blockAlign)
	header = binary.LittleEndian.AppendUint16(header, bitDepth)
	header = append(header, []byte("data")...)
	header = binary.LittleEndian.AppendUint32(header, 0) // empty data

	return os.WriteFile(path, header, 0o600)
}
