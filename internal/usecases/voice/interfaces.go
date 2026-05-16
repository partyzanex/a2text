package voice

// Interfaces in this file follow the consumer-owns-the-interface rule (DIP):
// they describe what the voice package needs from its dependencies, not what
// those dependencies happen to provide. Adapters in internal/adapters/* satisfy
// them by duck typing.

import (
	"context"

	"github.com/partyzanex/a2text/pkg/capture"
)

// Re-export of the capture library types so existing voice call sites
// keep their familiar names. The interface contract still belongs to
// voice (consumer-owns-the-interface): pkg/capture is just a generic
// recorder library whose shape happens to match voice's needs.
type (
	RecordOptions = capture.Options
	Recorder      = capture.Recorder
)

//go:generate go run go.uber.org/mock/mockgen@latest -write_package_comment=false -source=interfaces.go -destination=interfaces_mocks_test.go -package=voice
//go:generate go run go.uber.org/mock/mockgen@latest -write_package_comment=false -package=voice -destination=recorder_mocks_test.go github.com/partyzanex/a2text/pkg/capture Recorder

// Transcriber turns an audio file (already prepared by Converter) into text.
//
// The exact accepted format is backend-specific: local whisper.cpp wants
// WAV 16kHz mono s16le, while go-whisper and cloud backends accept the
// original input file because they decode server-side. Voice does not
// concern itself with which is which — it just hands over whatever
// Converter produced.
//
// ISP-minimal: voice never asks the implementation to load/swap models or
// detect language. Those operations belong to richer interfaces in the
// adapters that own model lifecycle (e.g. transcribe.Transcriber for the
// telegram bot which hot-reloads models).
type Transcriber interface {
	Transcribe(ctx context.Context, audioPath, lang string) (string, error)
}

// Converter prepares an input audio file for the chosen Transcriber.
//
// Implementations either convert to a temporary file (returning a cleanup
// func that removes that file) or return the input path unchanged with a
// no-op cleanup. The cleanup func is always non-nil and safe to call.
//
// CRITICAL: cleanup must NEVER delete the original input file. Ownership
// of temp files lives with the Converter, ownership of inputs lives with
// the caller. This contract removes the burden of "did the converter
// passthrough?" from the use case.
type Converter interface {
	ToWAV(ctx context.Context, inputPath string) (audioPath string, cleanup func(), err error)
}

// Output delivers a transcription result text to a destination.
// Implementations: stdout (I.0), clipboard (I.2), clipboard+autopaste (I.3).
type Output interface {
	Deliver(ctx context.Context, text string) error
}

// Recorder and RecordOptions are re-exported from pkg/capture above.
// File ownership contract (verbatim, applies to all implementations):
//   - On success the returned path MUST point to a freshly created regular
//     file owned by the recorder (typically under os.TempDir() or
//     opts.OutputPath). Ownership is transferred to the caller, who is
//     responsible for deleting it.
//   - Implementations MUST NOT return a user-provided source file path or
//     any path they did not create themselves. Past bug: the use case
//     deleted a passthrough'd input file. This contract prevents a recurrence
//     in the record pipeline.
//   - On error the returned path MUST be the empty string and the recorder
//     MUST clean up any partial file it produced.
