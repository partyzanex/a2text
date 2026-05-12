//go:build !whisper

package stt

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/partyzanex/a2text/pkg/sttx"
)

// WhisperTranscriber is a stub implementation used when whisper.cpp is not compiled in.
type WhisperTranscriber struct {
	log       *slog.Logger
	modelPath string
}

// NewWhisperTranscriber creates a new stub WhisperTranscriber.
func NewWhisperTranscriber(log *slog.Logger) *WhisperTranscriber {
	return &WhisperTranscriber{log: log}
}

// LoadModel is a stub that records the path (for ListModels) but does not actually load.
func (w *WhisperTranscriber) LoadModel(path string) error {
	w.modelPath = path
	w.log.Warn("whisper stub: whisper.cpp not compiled, model not loaded",
		slog.String("path", path),
	)

	return fmt.Errorf("%w: whisper.cpp не собран, используйте тег сборки 'whisper'", sttx.ErrTranscribeFailed)
}

// Transcribe is a stub that always returns an error.
func (w *WhisperTranscriber) Transcribe(_ context.Context, _, _ string) (string, error) {
	return "", fmt.Errorf("%w: whisper.cpp не собран", sttx.ErrTranscribeFailed)
}

// ReloadModel is a stub that always returns an error.
func (w *WhisperTranscriber) ReloadModel(_ string) error {
	return fmt.Errorf("%w: whisper.cpp не собран, hot reload недоступен", sttx.ErrTranscribeFailed)
}

// DetectLanguage is a stub that always returns an error.
func (w *WhisperTranscriber) DetectLanguage(_ context.Context, _ string) (string, error) {
	return "", fmt.Errorf("%w: whisper.cpp не собран", sttx.ErrTranscribeFailed)
}

// Close is a no-op for the stub.
func (w *WhisperTranscriber) Close() error {
	return nil
}

// ActiveModel returns the model ID (basename without .bin) of the currently loaded model.
// Empty if none has been loaded.
func (w *WhisperTranscriber) ActiveModel() string {
	return modelIDFromPath(w.modelPath)
}

// ListModels returns the IDs of *.bin model files in the directory of the currently
// loaded model. Returns an error if no model has been loaded yet (no directory to scan).
func (w *WhisperTranscriber) ListModels(_ context.Context) ([]string, error) {
	if w.modelPath == "" {
		return nil, fmt.Errorf("%w: no model loaded — model directory unknown", sttx.ErrTranscribeFailed)
	}

	dir := filepath.Dir(w.modelPath)

	return scanModelsDir(dir)
}
