package stt

import "context"

// maxRespBodySize caps HTTP response reads from cloud STT APIs to prevent
// unbounded memory growth on malformed or adversarial responses.
const maxRespBodySize = 1 << 20 // 1 MB

// CloudTranscriber defines the interface for cloud-based speech-to-text services.
type CloudTranscriber interface {
	// Transcribe sends the audio file at wavPath to the cloud service for transcription.
	// The lang parameter specifies the language code (e.g. "ru", "en") or "auto" for auto-detection.
	Transcribe(ctx context.Context, wavPath string, lang string) (string, error)
	// Name returns the name of the transcriber implementation.
	Name() string
}
