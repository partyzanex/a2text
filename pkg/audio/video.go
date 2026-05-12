package audio

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/partyzanex/a2text/pkg/audio/wav"
	"github.com/partyzanex/a2text/pkg/sttx"
)

// supportedVideoExt is the set of video container extensions handled by ExtractAudioFromVideo.
var supportedVideoExt = map[string]bool{ //nolint:gochecknoglobals
	".mp4":  true,
	".avi":  true,
	".mkv":  true,
	".mov":  true,
	".webm": true,
	".flv":  true,
	".wmv":  true,
	".mpeg": true,
	".mpg":  true,
	".m4v":  true,
	".3gp":  true,
}

// ExtractAudioFromVideo extracts the audio track from a video file, converting
// it to 16kHz mono 16-bit PCM WAV suitable for Whisper transcription.
//
// Supported containers: mp4, avi, mkv, mov, webm, flv, wmv, mpeg, mpg, m4v, 3gp.
// Returns the path to the temporary WAV file and its duration.
// The caller is responsible for removing the WAV file when done.
func (c *FFmpegConverter) ExtractAudioFromVideo(
	ctx context.Context, videoPath string,
) (audioPath string, duration time.Duration, err error) {
	ext := strings.ToLower(filepath.Ext(videoPath))
	if !supportedVideoExt[ext] {
		return "", 0, fmt.Errorf("%w: unsupported video container %q", sttx.ErrConversionFailed, ext)
	}

	wavPath, err := c.ToWAVFromFile(ctx, videoPath)
	if err != nil {
		return "", 0, err
	}

	dur, err := wavFileDuration(wavPath)
	if err != nil {
		if rmErr := os.Remove(wavPath); rmErr != nil {
			c.log.Debug("failed to remove wav after duration read error",
				slog.String("path", wavPath),
				slog.String("error", rmErr.Error()),
			)
		}

		return "", 0, fmt.Errorf("%w: read duration: %w", sttx.ErrConversionFailed, err)
	}

	return wavPath, dur, nil
}

// wavFileDuration reads the audio duration from a WAV file header.
func wavFileDuration(path string) (time.Duration, error) {
	dec, err := wav.Open(path)
	if err != nil {
		return 0, fmt.Errorf("video: %w", err)
	}

	dur := dec.Header.Duration

	if closeErr := dec.Close(); closeErr != nil {
		return dur, fmt.Errorf("video: %w", closeErr)
	}

	return dur, nil
}
