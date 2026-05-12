package transcribe

import "context"

//go:generate mockgen -source=interfaces.go -destination=interfaces_mocks_test.go -package=transcribe_test

// Converter handles audio format conversion.
type Converter interface {
	ToWAV(ctx context.Context, inputPath string) (wavPath string, err error)
}

// Transcriber performs speech-to-text on audio files.
type Transcriber interface {
	Transcribe(ctx context.Context, wavPath string, lang string) (string, error)
	LoadModel(path string) error
	// ReloadModel reloads the model from newPath. If loading fails the previous
	// model is restored. Safe for concurrent calls.
	ReloadModel(newPath string) error
	// DetectLanguage detects the language of the audio file and returns a 2-letter
	// code (e.g. "ru", "en").
	DetectLanguage(ctx context.Context, wavPath string) (string, error)
	Close() error
}
