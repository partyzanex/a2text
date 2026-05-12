package capture

import (
	"context"
	"time"
)

// Options configures a single Recorder.RecordToFile call.
type Options struct {
	// Duration is the wall-clock recording length. A zero value means
	// "record until ctx is cancelled" — caller-driven push-to-talk.
	Duration time.Duration

	// OutputPath is the absolute path the WAV file will be written to.
	// Empty means "create a temp file under os.TempDir()".
	OutputPath string

	// SampleRate in Hz. Whisper expects 16000.
	SampleRate int

	// Channels: 1 for mono (whisper), 2 for stereo.
	Channels int
}

// Recorder captures microphone audio into a WAV file. The returned path
// points to a freshly created regular file owned by the recorder;
// ownership is transferred to the caller, who must delete it.
type Recorder interface {
	RecordToFile(ctx context.Context, opts Options) (path string, err error)
}
