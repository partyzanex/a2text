//go:build !whisper

package factory

import (
	"fmt"
	"log/slog"

	"github.com/partyzanex/a2text/internal/usecases/transcribe"
)

//nolint:ireturn // returns transcribe.Transcriber defined in usecases (consumer owns the interface, per DIP)
func buildWhisperCpp(_ *Config, _ *slog.Logger) (transcribe.Transcriber, error) {
	return nil, fmt.Errorf("stt: provider %q requires building with -tags whisper", ProviderWhisperCpp)
}
