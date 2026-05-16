package factory

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/partyzanex/a2text/internal/usecases/voice"
	"github.com/partyzanex/a2text/pkg/audio"
)

const tempAudioFilePermission = 0o600

// passthroughConverter implements voice.Converter without doing any work:
// it returns the input path verbatim with a no-op cleanup. Used when the
// downstream transcriber accepts the original audio file (go-whisper,
// cloud APIs).
//
// Crucially, the cleanup func is non-nil but does NOT touch inputPath —
// owning the input is the caller's responsibility, not ours.
type passthroughConverter struct{}

func (passthroughConverter) ToWAV(
	_ context.Context, inputPath string,
) (audioPath string, cleanup func(), err error) {
	if inputPath == "" {
		return "", nil, errors.New("passthroughConverter: empty input path")
	}

	return inputPath, func() {}, nil
}

// FFmpegInner is the minimal seam used by ffmpegConverter. Declared as an
// interface so tests can inject fakes without depending on exec.Command.
// Production code always passes an adapter that wraps *audio.FFmpegConverter.
//
//go:generate go run go.uber.org/mock/mockgen@latest -write_package_comment=false -package=factory -destination=converter_mocks_test.go -source=converter.go FFmpegInner
type FFmpegInner interface {
	ToWAV(ctx context.Context, inputPath string) (string, error)
}

// ffmpegConverterAdapter wraps audio.FFmpegConverter to implement FFmpegInner.
// It converts the inputPath to an io.Reader before calling ToWAV, avoiding
// subprocess variable issues (G204/G304 gosec violations).
type ffmpegConverterAdapter struct {
	inner *audio.FFmpegConverter
}

func newFFmpegConverterAdapter(inner *audio.FFmpegConverter) *ffmpegConverterAdapter {
	return &ffmpegConverterAdapter{inner: inner}
}

func (a *ffmpegConverterAdapter) ToWAV(ctx context.Context, inputPath string) (string, error) {
	wavPath, err := a.inner.ToWAVFromFile(ctx, inputPath)
	if err != nil {
		return "", fmt.Errorf("convert to wav: %w", err)
	}

	return wavPath, nil
}

// ffmpegConverter wraps an FFmpegInner to fit voice.Converter: it owns the
// produced temp WAV file and exposes a cleanup that removes it.
//
// Fast-path: if inputPath is already a valid WAV 16k mono file, no
// conversion is performed and the input path is returned with a no-op
// cleanup (so we never delete the user's file).
type ffmpegConverter struct {
	inner FFmpegInner
	log   *slog.Logger
}

// newFfmpegConverter constructs an ffmpegConverter. inner must not be nil.
// A nil log is replaced with a discard handler.
func newFfmpegConverter(inner FFmpegInner, log *slog.Logger) (*ffmpegConverter, error) {
	if inner == nil {
		return nil, errors.New("ffmpegConverter: inner converter must not be nil")
	}

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &ffmpegConverter{inner: inner, log: log}, nil
}

func (c *ffmpegConverter) ToWAV(
	ctx context.Context, inputPath string,
) (audioPath string, cleanup func(), err error) {
	if c == nil {
		return "", nil, errors.New("ffmpegConverter: nil receiver")
	}

	if inputPath == "" {
		return "", nil, errors.New("ffmpegConverter: empty input path")
	}

	if audio.ValidateAudioFormat(inputPath) == nil {
		return inputPath, func() {}, nil
	}

	converted, convErr := c.inner.ToWAV(ctx, inputPath)
	if convErr != nil {
		return "", nil, fmt.Errorf("ffmpeg ToWAV: %w", convErr)
	}

	// Guard against a misbehaving inner returning the input path unchanged —
	// cleanup on such a path would delete the user's original file.
	if converted == inputPath {
		return "", nil, errors.New("ffmpegConverter: converter returned input path as output")
	}

	if chmodErr := os.Chmod(converted, tempAudioFilePermission); chmodErr != nil {
		// Fail closed: a temp WAV with wrong permissions must not be used.
		if rmErr := os.Remove(converted); rmErr != nil {
			c.log.Debug("cmd: cleanup of malformed wav failed", slog.Any("err", rmErr))
		}

		return "", nil, fmt.Errorf("cmd: set wav perms 0600: %w", chmodErr)
	}

	cleanup = func() {
		if rmErr := os.Remove(converted); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			c.log.Warn("cmd: temp wav cleanup failed",
				slog.String("path", filepath.Base(converted)),
				slog.Any("err", rmErr),
			)
		}
	}

	return converted, cleanup, nil
}

// Compile-time interface checks.
var (
	_ voice.Converter = passthroughConverter{}
	_ voice.Converter = (*ffmpegConverter)(nil)
)
