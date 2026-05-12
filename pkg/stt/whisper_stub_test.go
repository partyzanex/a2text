//go:build !whisper

package stt

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/a2text/pkg/sttx"
)

func TestWhisperDisabled_LoadModel(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWhisperTranscriber(log)

	err := w.LoadModel("/some/model.bin")
	require.Error(t, err)
	assert.ErrorIs(t, err, sttx.ErrTranscribeFailed)
}

func TestWhisperDisabled_Transcribe(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWhisperTranscriber(log)

	_, err := w.Transcribe(context.Background(), "/some/audio.wav", "ru")
	require.Error(t, err)
	assert.ErrorIs(t, err, sttx.ErrTranscribeFailed)
}

func TestWhisperDisabled_Close(t *testing.T) {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	w := NewWhisperTranscriber(log)
	assert.NoError(t, w.Close())
}

// quietLog is a quiet logger for the model-listing tests.
func quietLog() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestWhisperDisabled_ActiveModel_EmptyByDefault(t *testing.T) {
	w := NewWhisperTranscriber(quietLog())
	assert.Empty(t, w.ActiveModel())
}

func TestWhisperDisabled_ActiveModel_AfterLoadModel(t *testing.T) {
	w := NewWhisperTranscriber(quietLog())
	// Disabled-whisper LoadModel records the path even though it returns an error.
	_ = w.LoadModel("/models/ggml-large-v3-turbo.bin")
	assert.Equal(t, "ggml-large-v3-turbo", w.ActiveModel())
}

func TestWhisperDisabled_ListModels_NoModelLoaded_Errors(t *testing.T) {
	w := NewWhisperTranscriber(quietLog())
	_, err := w.ListModels(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, sttx.ErrTranscribeFailed)
}

func TestWhisperDisabled_ListModels_ScansDirOfActiveModel(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"ggml-small.bin", "ggml-large-v3.bin", "readme.md"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}

	w := NewWhisperTranscriber(quietLog())
	_ = w.LoadModel(filepath.Join(dir, "ggml-small.bin"))

	ids, err := w.ListModels(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"ggml-large-v3", "ggml-small"}, ids)
}

func TestWhisperDisabled_ImplementsModelListerSurface(t *testing.T) {
	// Compile-time check via a minimal interface mirroring bot.ModelLister.
	type lister interface {
		ListModels(ctx context.Context) ([]string, error)
		ActiveModel() string
	}

	var _ lister = NewWhisperTranscriber(quietLog())
}
