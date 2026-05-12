// Cross-platform constants and types for the capture adapter. Kept in a
// build-tag-free file so non-Linux builds (which only ship the unsupported
// stub) can still reference Backend, BackendPipeWire, BackendPulseAudio
// without a build-tag dance.

package capture

import "errors"

// Backend identifies which subprocess utility a SubprocessRecorder uses.
type Backend string

const (
	// BackendPipeWire selects the PipeWire `pw-record` utility.
	BackendPipeWire Backend = "pw-record"

	// BackendPulseAudio selects the PulseAudio `parecord` utility.
	// We deliberately use parecord (file-format aware) rather than the bare
	// `parec` (which defaults to raw PCM and would silently produce a
	// .wav-named file with no RIFF header). See ADR-0011.
	BackendPulseAudio Backend = "parecord"
)

// ErrNoCaptureBackend signals that neither pw-record nor parecord was found
// in PATH. The caller should surface install hints to the user.
var ErrNoCaptureBackend = errors.New(
	"no audio capture backend found: install pipewire-bin (pw-record) " +
		"or pulseaudio-utils (parecord)",
)
