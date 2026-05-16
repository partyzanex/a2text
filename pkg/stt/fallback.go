package stt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/partyzanex/a2text/pkg/sttx"
)

// STTBackend is an internal interface for transcription backends.
// It mirrors usecases/transcribe.Transcriber but is defined here to avoid circular imports.
//
//go:generate go run go.uber.org/mock/mockgen@latest -write_package_comment=false -package=stt -destination=fallback_mocks_test.go -source=fallback.go STTBackend
type STTBackend interface {
	LoadModel(path string) error
	ReloadModel(newPath string) error
	Transcribe(ctx context.Context, wavPath string, lang string) (string, error)
	DetectLanguage(ctx context.Context, wavPath string) (string, error)
	Close() error
}

// FallbackTranscriber wraps a primary and a secondary backend.
// If the primary fails and the context is still alive, the secondary is tried.
type FallbackTranscriber struct {
	primary   STTBackend
	secondary STTBackend
	log       *slog.Logger
}

// NewFallbackTranscriber creates a FallbackTranscriber.
func NewFallbackTranscriber(primary, secondary STTBackend, log *slog.Logger) *FallbackTranscriber {
	if log == nil {
		log = slog.Default()
	}

	return &FallbackTranscriber{primary: primary, secondary: secondary, log: log}
}

// LoadModel delegates to the primary backend only.
func (f *FallbackTranscriber) LoadModel(path string) error {
	if err := f.primary.LoadModel(path); err != nil {
		return fmt.Errorf("fallback: %w", err)
	}

	return nil
}

// ReloadModel delegates to the primary backend only.
func (f *FallbackTranscriber) ReloadModel(path string) error {
	if err := f.primary.ReloadModel(path); err != nil {
		return fmt.Errorf("fallback: %w", err)
	}

	return nil
}

// DetectLanguage tries primary; on failure (if ctx is still live) falls back to secondary.
func (f *FallbackTranscriber) DetectLanguage(ctx context.Context, wavPath string) (string, error) {
	lang, err := f.primary.DetectLanguage(ctx, wavPath)
	if err == nil {
		return lang, nil
	}

	if ctx.Err() != nil {
		return "", fmt.Errorf("fallback: %w", err)
	}

	f.log.Warn("primary DetectLanguage failed, trying secondary", slog.String("error", err.Error()))

	lang, err = f.secondary.DetectLanguage(ctx, wavPath)
	if err != nil {
		return "", fmt.Errorf("fallback: %w", err)
	}

	return lang, nil
}

// Transcribe tries primary; on failure (if ctx is still live) falls back to secondary.
func (f *FallbackTranscriber) Transcribe(ctx context.Context, wavPath, lang string) (string, error) {
	result, err := f.primary.Transcribe(ctx, wavPath, lang)
	if err == nil {
		return result, nil
	}

	if ctx.Err() != nil {
		return "", fmt.Errorf("fallback: %w", err)
	}

	f.log.Warn("primary transcriber failed, trying secondary",
		slog.String("error", err.Error()),
	)

	primaryErr := err

	result, err = f.secondary.Transcribe(ctx, wavPath, lang)
	if err != nil {
		return "", fmt.Errorf("%w: primary: %w; secondary: %w", sttx.ErrTranscribeFailed, primaryErr, err)
	}

	return result, nil
}

// Close closes both backends, joining their errors.
func (f *FallbackTranscriber) Close() error {
	return errors.Join(f.primary.Close(), f.secondary.Close())
}
