package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/partyzanex/a2text/pkg/sttx"
)

// PassthroughConverter is a Converter implementation that returns the input
// path unchanged. It is intended for the go-whisper provider, which decodes
// audio/video formats internally via ffmpeg — running our own ffmpeg pass
// would be redundant work.
type PassthroughConverter struct{}

// NewPassthroughConverter constructs a PassthroughConverter.
func NewPassthroughConverter() *PassthroughConverter { return &PassthroughConverter{} }

// ToWAVFromFile returns inputPath unchanged. The downstream Transcriber is expected
// to accept the original container format.
func (PassthroughConverter) ToWAVFromFile(_ context.Context, inputPath string) (string, error) {
	if _, err := os.Stat(inputPath); err != nil {
		return "", fmt.Errorf("%w: input file not found: %w", sttx.ErrConversionFailed, err)
	}

	return inputPath, nil
}

// ToWAV is not supported for passthrough converter. Use ToWAVFromFile instead.
func (PassthroughConverter) ToWAV(_ context.Context, _ io.Reader, _ string) (string, error) {
	return "", errors.New("passthrough converter does not support io.Reader input")
}
