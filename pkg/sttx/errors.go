// Package sttx contains shared sentinel errors used by speech-to-text
// adapters (pkg/stt) and audio processing (pkg/audio). Consumers compare
// against these with errors.Is to react to typical failure modes without
// depending on a specific backend.
package sttx

import "errors"

var (
	// ErrConversionFailed indicates that audio could not be transcoded
	// to the required format (e.g. ffmpeg returned a non-zero code).
	ErrConversionFailed = errors.New("audio conversion failed")
	// ErrTranscribeFailed is the generic transcription failure. Backends
	// wrap their concrete errors with %w into this sentinel so callers
	// can branch on category without parsing strings.
	ErrTranscribeFailed = errors.New("transcription failed")
	// ErrServiceUnavailable is returned when the transcription backend
	// (local daemon or cloud API) is unreachable.
	ErrServiceUnavailable = errors.New("transcription service unavailable")
	// ErrNotFound is returned when a referenced resource (model file,
	// remote object) does not exist.
	ErrNotFound = errors.New("not found")
	// ErrAudioTooLong indicates the input audio exceeds backend limits.
	ErrAudioTooLong = errors.New("audio too long")
	// ErrFileTooLarge indicates the input audio file size exceeds limits.
	ErrFileTooLarge = errors.New("file too large")
	// ErrUnsupportedFormat indicates an unsupported audio format.
	ErrUnsupportedFormat = errors.New("unsupported format")
	// ErrEmptyResult indicates the backend returned an empty transcription.
	ErrEmptyResult = errors.New("empty transcription result")
	// ErrDownloadFailed indicates a model/resource download failure.
	ErrDownloadFailed = errors.New("download failed")
)
