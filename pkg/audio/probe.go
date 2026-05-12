package audio

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/partyzanex/a2text/pkg/audio/wav"
)

// probeDurationTimeout caps a single ffprobe invocation. Generous enough for
// large files on slow storage while still bounding the goroutine lifetime.
const probeDurationTimeout = 30 * time.Second

// ProbeDuration returns the duration of an audio or video file using ffprobe.
// It is safe to call on any format supported by ffmpeg/ffprobe.
func ProbeDuration(ctx context.Context, path string) (time.Duration, error) {
	if err := validateAudioPath(path); err != nil {
		return 0, err
	}

	var stdout bytes.Buffer

	probeCtx, cancel := context.WithTimeout(ctx, probeDurationTimeout)
	defer cancel()

	// #nosec G204 -- ffprobe is intentionally executed with validated local audio path.
	cmd := exec.CommandContext(probeCtx, "ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		"-i", path,
	)
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("ffprobe: %w", err)
	}

	raw := strings.TrimSpace(stdout.String())
	if raw == "" || raw == "N/A" {
		return 0, errors.New("ffprobe: no duration in output")
	}

	secs, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("ffprobe: parse duration %q: %w", raw, err)
	}

	if math.IsNaN(secs) || math.IsInf(secs, 0) || secs < 0 {
		return 0, fmt.Errorf("ffprobe: invalid duration: %q", raw)
	}

	duration := time.Duration(secs * float64(time.Second))
	if duration <= 0 {
		return 0, fmt.Errorf("ffprobe: invalid duration: %q", raw)
	}

	return duration, nil
}

// ValidateAudioFormat checks that the WAV file at path meets Whisper requirements:
// 16kHz sample rate, mono (1) channel, 16-bit depth.
// Returns nil when the file is valid; a descriptive error otherwise.
func ValidateAudioFormat(path string) error {
	dec, err := wav.Open(path)
	if err != nil {
		return fmt.Errorf("validate audio format: %w", err)
	}

	if err := dec.Close(); err != nil {
		return fmt.Errorf("probe: %w", err)
	}

	return nil
}
