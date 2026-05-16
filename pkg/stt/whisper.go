//go:build whisper

package stt

/*
#include <whisper.h>
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unsafe"

	"github.com/partyzanex/a2text/pkg/audio/wav"
	"github.com/partyzanex/a2text/pkg/sttx"
)

// WhisperTranscriber performs speech-to-text using whisper.cpp via CGo.
// All access to w.ctx is serialized by w.mu. whisper_full is a blocking C call
// and cannot be interrupted mid-execution; context cancellation is a best-effort
// gate checked before the call begins.
type WhisperTranscriber struct {
	mu        sync.Mutex
	ctx       *C.struct_whisper_context
	modelPath string // last successfully loaded model path for rollback
	log       *slog.Logger

	// fullHook is a per-instance test seam: when non-nil, Transcribe
	// uses *fullHook as the return code instead of calling whisper_full.
	// Protected by mu — read inside the same lock that guards w.ctx.
	// Replaces the previous package-global hook to keep parallel tests
	// isolated.
	fullHook *int
}

// NewWhisperTranscriber creates a new WhisperTranscriber.
// If log is nil, slog.Default() is used.
func NewWhisperTranscriber(log *slog.Logger) *WhisperTranscriber {
	if log == nil {
		log = slog.Default()
	}

	return &WhisperTranscriber{log: log}
}

// SetWhisperFullHook installs a per-instance test seam: when hook is
// non-nil, Transcribe substitutes *hook for the whisper_full return code
// instead of calling the C function. Pass nil to remove the seam.
// Safe for concurrent use; the hook is read under w.mu.
func (w *WhisperTranscriber) SetWhisperFullHook(hook *int) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.fullHook = hook
}

// LoadModel loads a whisper GGML model from path. It is safe to call while
// another goroutine holds the transcription lock; if a model is already loaded
// it is freed before the new one is initialised.
func (w *WhisperTranscriber) LoadModel(path string) error {
	if path == "" {
		return fmt.Errorf("%w: empty model path", sttx.ErrTranscribeFailed)
	}

	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("%w: model file not found: %s", sttx.ErrTranscribeFailed, path)
	}

	cPath := C.CString(path)

	defer C.free(unsafe.Pointer(cPath))
	w.mu.Lock()
	defer w.mu.Unlock()

	// Free previous model to avoid a memory leak on reload.
	if w.ctx != nil {
		C.whisper_free(w.ctx)
		w.ctx = nil
	}

	newCtx := initWhisperFromFile(cPath)
	if newCtx == nil {
		return fmt.Errorf("%w: failed to load model from %s", sttx.ErrTranscribeFailed, path)
	}

	w.ctx = newCtx
	w.modelPath = path
	w.log.Info("whisper model loaded", slog.String("path", path))

	return nil
}

// ReloadModel reloads the whisper model from newPath. If loading fails the
// previous model is restored. Safe for concurrent calls.
//
// Accepts either a full path (e.g. "/models/ggml-small.bin") or a bare model ID
// (e.g. "ggml-small") which is resolved relative to the directory of the
// currently loaded model.
func (w *WhisperTranscriber) ReloadModel(newPath string) error {
	if newPath == "" {
		return fmt.Errorf("%w: empty model path", sttx.ErrTranscribeFailed)
	}

	w.mu.Lock()
	currentDir := filepath.Dir(w.modelPath)
	w.mu.Unlock()

	newPath = resolveModelPath(newPath, currentDir)

	if _, err := os.Stat(newPath); err != nil {
		return fmt.Errorf("%w: model file not found: %s", sttx.ErrTranscribeFailed, newPath)
	}

	cPath := C.CString(newPath)

	defer C.free(unsafe.Pointer(cPath))
	w.mu.Lock()
	defer w.mu.Unlock()

	prevCtx := w.ctx
	prevPath := w.modelPath

	newCtx := initWhisperFromFile(cPath)
	if newCtx == nil {
		w.log.Error("failed to load new model, keeping previous",
			slog.String("new_path", newPath),
			slog.String("prev_path", prevPath),
		)

		return fmt.Errorf("%w: failed to load model from %s", sttx.ErrTranscribeFailed, newPath)
	}

	if prevCtx != nil {
		C.whisper_free(prevCtx)
	}

	w.ctx = newCtx
	w.modelPath = newPath
	w.log.Info("whisper model reloaded",
		slog.String("new_path", newPath),
		slog.String("prev_path", prevPath),
	)

	return nil
}

func initWhisperFromFile(path *C.char) *C.struct_whisper_context {
	params := C.whisper_context_default_params()

	return C.whisper_init_from_file_with_params(path, params)
}

// Transcribe performs speech-to-text on wavPath (pcm_s16le, 16 kHz, mono).
// wav.Open enforces the format; mismatched audio will return ErrTranscribeFailed.
// Returns ErrEmptyResult if no speech was detected.
func (w *WhisperTranscriber) Transcribe(ctx context.Context, wavPath string, lang string) (string, error) {
	// Reject already-canceled contexts before doing any IO.
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("%w: %w", sttx.ErrTranscribeFailed, err)
	}

	samples, err := readWavSamples(wavPath, w.log)
	if err != nil {
		return "", err
	}

	w.log.Info("transcribing", slog.String("wav", wavPath), slog.String("lang", lang), slog.Int("samples", len(samples)))

	w.mu.Lock()
	defer w.mu.Unlock()

	// Re-check: context may have been canceled while waiting for the lock, or
	// Close may have been called concurrently.
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("%w: %w", sttx.ErrTranscribeFailed, err)
	}

	if w.ctx == nil {
		return "", fmt.Errorf("%w: model not loaded", sttx.ErrTranscribeFailed)
	}

	params := C.whisper_full_default_params(C.WHISPER_SAMPLING_GREEDY)
	params.print_progress = C.bool(false)
	params.print_timestamps = C.bool(false)
	params.single_segment = C.bool(false)

	if lang != "" && lang != langAuto {
		cLang := C.CString(lang)

		defer C.free(unsafe.Pointer(cLang))

		params.language = cLang
	} else {
		params.language = nil
		params.detect_language = C.bool(true)
	}

	// whisper_full blocks until the entire file is processed; ctx cancellation
	// cannot interrupt it once started.
	var ret int
	if w.fullHook != nil {
		ret = *w.fullHook
	} else {
		ret = int(C.whisper_full(w.ctx, params, (*C.float)(unsafe.Pointer(&samples[0])), C.int(len(samples))))
	}

	if ret != 0 {
		return "", fmt.Errorf("%w: whisper_full returned %d", sttx.ErrTranscribeFailed, ret)
	}

	return w.extractTranscriptionResult()
}

// DetectLanguage detects the spoken language in wavPath and returns the language code
// (e.g. "ru", "en"). Uses whisper_pcm_to_mel + whisper_lang_auto_detect.
func (w *WhisperTranscriber) DetectLanguage(ctx context.Context, wavPath string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("%w: %w", sttx.ErrTranscribeFailed, err)
	}

	decoder, err := wav.Open(wavPath)
	if err != nil {
		return "", fmt.Errorf("%w: %w", sttx.ErrTranscribeFailed, err)
	}

	defer func() {
		if closeErr := decoder.Close(); closeErr != nil {
			w.log.Debug("error closing decoder", slog.Any("error", closeErr))
		}
	}()

	samples, err := decoder.ReadAll()
	if err != nil {
		return "", fmt.Errorf("%w: %w", sttx.ErrTranscribeFailed, err)
	}

	if len(samples) == 0 {
		return "", fmt.Errorf("%w: empty audio", sttx.ErrTranscribeFailed)
	}

	w.log.Info("detecting language", slog.String("decoder", wavPath), slog.Int("samples", len(samples)))

	w.mu.Lock()
	defer w.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("%w: %w", sttx.ErrTranscribeFailed, err)
	}

	if w.ctx == nil {
		return "", fmt.Errorf("%w: model not loaded", sttx.ErrTranscribeFailed)
	}

	ret := int(C.whisper_pcm_to_mel(w.ctx, (*C.float)(unsafe.Pointer(&samples[0])), C.int(len(samples)), C.int(1)))
	if ret != 0 {
		return "", fmt.Errorf("%w: pcm_to_mel returned %d", sttx.ErrTranscribeFailed, ret)
	}

	langID := int(C.whisper_lang_auto_detect(w.ctx, C.int(0), C.int(1), nil))

	if langID < 0 {
		return "", fmt.Errorf("%w: language detection failed with code %d", sttx.ErrTranscribeFailed, langID)
	}

	lang := C.GoString(C.whisper_lang_str(C.int(langID)))
	w.log.Info("language detected", slog.String("lang", lang), slog.String("decoder", wavPath))

	return lang, nil
}

// Close releases whisper model resources. Safe to call multiple times.
func (w *WhisperTranscriber) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.ctx != nil {
		C.whisper_free(w.ctx)
		w.ctx = nil
		w.modelPath = ""
		w.log.Info("whisper model released")
	}

	return nil
}

// ActiveModel returns the model ID (basename without .bin) of the currently loaded model.
// Returns "" if no model is loaded.
func (w *WhisperTranscriber) ActiveModel() string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return modelIDFromPath(w.modelPath)
}

// ListModels returns the IDs of *.bin files in the directory of the currently
// loaded model. Returns an error if no model is loaded (directory unknown).
func (w *WhisperTranscriber) ListModels(_ context.Context) ([]string, error) {
	w.mu.Lock()
	dir := filepath.Dir(w.modelPath)
	loaded := w.modelPath != ""
	w.mu.Unlock()

	if !loaded {
		return nil, fmt.Errorf("%w: no model loaded — model directory unknown", sttx.ErrTranscribeFailed)
	}

	return scanModelsDir(dir)
}

func (w *WhisperTranscriber) extractTranscriptionResult() (string, error) {
	nSegments := int(C.whisper_full_n_segments(w.ctx))

	var sb strings.Builder
	for i := range nSegments {
		text := C.GoString(C.whisper_full_get_segment_text(w.ctx, C.int(i)))
		sb.WriteString(text)
	}

	resultText := strings.TrimSpace(sb.String())
	w.log.Info("transcription complete", slog.Int("segments", nSegments), slog.Int("result_len", len(resultText)))

	if resultText == "" {
		return "", fmt.Errorf("%w: no speech detected", sttx.ErrEmptyResult)
	}

	return resultText, nil
}

func readWavSamples(wavPath string, log *slog.Logger) ([]float32, error) {
	decoder, err := wav.Open(wavPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", sttx.ErrTranscribeFailed, err)
	}
	defer func() {
		if closeErr := decoder.Close(); closeErr != nil {
			log.Debug("error closing decoder", slog.Any("error", closeErr))
		}
	}()

	samples, err := decoder.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", sttx.ErrTranscribeFailed, err)
	}

	if len(samples) == 0 {
		return nil, fmt.Errorf("%w: empty audio", sttx.ErrTranscribeFailed)
	}

	return samples, nil
}
