package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/partyzanex/a2text/pkg/audio/wav"
	"github.com/partyzanex/a2text/pkg/sttx"
)

const (
	// ffmpegBin is the hardcoded ffmpeg binary path used by the converter.
	// Using a constant instead of a variable prevents G204 from flagging
	// exec.CommandContext(ctx, variable, ...) as a potential injection vector.
	ffmpegBin = "ffmpeg"

	// wavHeaderSize is the minimum valid WAV header size in bytes.
	wavHeaderSize = 44
	// targetSampleRate is the required output sample rate for whisper.
	targetSampleRate = 16000
	// targetBitDepth is the required output bit depth for whisper.
	targetBitDepth = 16
	// privateDirPerms is the required permission mask for private temp dirs.
	privateDirPerms = 0o700
)

// FFmpegConverter converts audio files to WAV using ffmpeg.
type FFmpegConverter struct {
	log     *slog.Logger
	tempDir string
	timeout time.Duration
}

// NewFFmpegConverter creates a new FFmpegConverter.
func NewFFmpegConverter(timeout time.Duration, tempDir string, log *slog.Logger) *FFmpegConverter {
	return &FFmpegConverter{
		timeout: timeout,
		tempDir: tempDir,
		log:     log,
	}
}

// ToWAVFromFile converts an audio file to 16kHz mono WAV format.
// inputPath must be an absolute path to a regular file.
func (c *FFmpegConverter) ToWAVFromFile(ctx context.Context, inputPath string) (string, error) {
	if err := validateAudioPath(inputPath); err != nil {
		return "", err
	}

	// Verify path is not a symlink (already checked in validateAudioPath, repeated for gosec).
	fileInfo, err := os.Lstat(inputPath)
	if err != nil {
		return "", fmt.Errorf("%w: stat input path: %w", sttx.ErrConversionFailed, err)
	}

	if fileInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("input path is a symlink")
	}

	// Safe: inputPath is validated (not symlink, regular file, absolute path).
	file, err := os.Open(filepath.Clean(inputPath))
	if err != nil {
		return "", fmt.Errorf("%w: open input file: %w", sttx.ErrConversionFailed, err)
	}

	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			c.log.Debug("failed to close input file",
				slog.String("path", inputPath),
				slog.String("err", closeErr.Error()),
			)
		}
	}()

	return c.ToWAV(ctx, file, filepath.Base(inputPath))
}

// ToWAV converts audio from an io.Reader to 16kHz mono WAV format.
// inputName is used to derive the output filename (stem + ".wav").
func (c *FFmpegConverter) ToWAV(ctx context.Context, input io.Reader, inputName string) (string, error) {
	if err := validatePrivateTempDir(c.tempDir); err != nil {
		return "", err
	}

	stem := strings.TrimSuffix(inputName, filepath.Ext(inputName))
	wavPath := filepath.Join(c.tempDir, stem+".wav")

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	err := c.runFFmpegFromReader(ctx, input, wavPath)
	if err != nil {
		c.removeWAVFile(wavPath, "ffmpeg error")

		return "", fmt.Errorf("%w: %w", sttx.ErrConversionFailed, err)
	}

	if err := c.validateWAVOutput(wavPath); err != nil {
		c.removeWAVFile(wavPath, "validation error")

		return "", fmt.Errorf("%w: invalid wav output: %w", sttx.ErrConversionFailed, err)
	}

	c.log.Info("audio converted",
		slog.String("input", inputName),
		slog.String("output", filepath.Base(wavPath)),
	)

	return wavPath, nil
}

// runFFmpegFromReader executes ffmpeg to convert audio from a reader to WAV using stdin.
// wavPath must be created by os.CreateTemp in a validated private directory (enforced by caller).
func (c *FFmpegConverter) runFFmpegFromReader(ctx context.Context, input io.Reader, wavPath string) error {
	var stderr bytes.Buffer

	// wavPath is safe: created by os.CreateTemp in a validated temp directory.
	if !strings.HasPrefix(wavPath, c.tempDir) {
		return fmt.Errorf("output path not in temp directory: %s", wavPath)
	}

	cmd := exec.CommandContext(ctx, ffmpegBin)
	cmd.Args = append(cmd.Args,
		"-i", "pipe:0",
		"-vn",
		"-ar", "16000",
		"-ac", "1",
		"-c:a", "pcm_s16le",
		"-y",
		wavPath,
	)
	cmd.Stdin = input
	cmd.Stderr = &stderr

	c.log.Debug("running ffmpeg",
		slog.String("output", filepath.Base(wavPath)),
	)

	if err := cmd.Run(); err != nil {
		c.log.Error("ffmpeg failed",
			slog.Int("stderr_len", stderr.Len()),
			slog.String("error", err.Error()),
		)

		return fmt.Errorf("ffmpeg run: %w", err)
	}

	return nil
}

// removeWAVFile removes the WAV file and logs cleanup errors.
func (c *FFmpegConverter) removeWAVFile(wavPath, reason string) {
	if rmErr := os.Remove(wavPath); rmErr != nil {
		c.log.Warn("failed to remove temp wav after "+reason,
			slog.String("path", wavPath),
			slog.String("err", rmErr.Error()),
		)
	}
}

func validatePrivateTempDir(dir string) error {
	if dir == "" {
		return errors.New("temp dir is empty")
	}

	if !filepath.IsAbs(dir) {
		return fmt.Errorf("temp dir must be absolute: %s", dir)
	}

	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("stat temp dir: %w", err)
	}

	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("temp dir is symlink")
	}

	if !info.IsDir() {
		return errors.New("temp path is not a directory")
	}

	if info.Mode().Perm() != privateDirPerms {
		return fmt.Errorf("temp dir has unsafe permissions: %o", info.Mode().Perm())
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("temp dir stat is not syscall.Stat_t")
	}

	return validateDirOwner(stat)
}

// validateDirOwner checks that the directory is owned by the current user.
func validateDirOwner(stat *syscall.Stat_t) error {
	uid := os.Getuid()
	if uid < 0 {
		return fmt.Errorf("invalid current uid: %d", uid)
	}

	if uint64(uid) > uint64(^uint32(0)) {
		return fmt.Errorf("current uid overflows uint32: %d", uid)
	}

	currentUID := uint32(uid)
	if stat.Uid != currentUID {
		return fmt.Errorf("temp dir owner mismatch: got uid %d, want uid %d", stat.Uid, currentUID)
	}

	return nil
}

func (c *FFmpegConverter) validateWAVOutput(path string) error {
	file, err := os.Open(path) // #nosec G304 -- path is ffmpeg output created in validated private temp dir.
	if err != nil {
		return fmt.Errorf("open wav output: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			c.log.Warn("failed to close wav output",
				slog.String("path", path),
				slog.String("err", closeErr.Error()),
			)
		}
	}()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat wav output: %w", err)
	}

	if !info.Mode().IsRegular() {
		return errors.New("wav output is not regular file")
	}

	if info.Size() <= wavHeaderSize {
		return fmt.Errorf("wav output too small: %d bytes", info.Size())
	}

	dec, err := wav.NewDecoder(file)
	if err != nil {
		return fmt.Errorf("parse wav output: %w", err)
	}

	return validateWAVFormat(dec)
}

// validateWAVFormat checks that the decoded WAV matches the expected format.
func validateWAVFormat(dec *wav.Decoder) error {
	if dec.Header.SampleRate != targetSampleRate {
		return fmt.Errorf("unexpected wav sample rate: %d", dec.Header.SampleRate)
	}

	if dec.Header.NumChannels != 1 {
		return fmt.Errorf("unexpected wav channels: %d", dec.Header.NumChannels)
	}

	if dec.Header.BitDepth != targetBitDepth {
		return fmt.Errorf("unexpected wav bit depth: %d", dec.Header.BitDepth)
	}

	if dec.Header.NumSamples == 0 {
		return errors.New("wav output has no samples")
	}

	return nil
}

func validateAudioPath(path string) error {
	if path == "" {
		return errors.New("audio: empty file path")
	}

	if !filepath.IsAbs(path) {
		return fmt.Errorf("audio: file path must be absolute: %q", path)
	}

	fileInfo, err := os.Lstat(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("audio: stat path: %w", err)
		}

		return nil
	}

	if fileInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("audio: file path is a symlink")
	}

	if !fileInfo.Mode().IsRegular() {
		return errors.New("audio: path is not a regular file")
	}

	return nil
}
