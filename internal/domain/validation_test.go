package domain

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateCycleArgs(t *testing.T) {
	validOpts := RecordOpts{MaxDuration: 5_000_000_000} // 5s in nanoseconds
	validCtx := context.Background()

	t.Run("valid args", func(t *testing.T) {
		err := ValidateCycleArgs(validCtx, validCtx, validOpts, "ru")
		require.NoError(t, err)
	})

	t.Run("nil ctx", func(t *testing.T) {
		err := ValidateCycleArgs(nil, validCtx, validOpts, "ru")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nil")
	})

	t.Run("nil recordCtx", func(t *testing.T) {
		err := ValidateCycleArgs(validCtx, nil, validOpts, "ru")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "nil")
	})

	t.Run("empty lang", func(t *testing.T) {
		err := ValidateCycleArgs(validCtx, validCtx, validOpts, "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "lang")
	})

	t.Run("whitespace-only lang", func(t *testing.T) {
		err := ValidateCycleArgs(validCtx, validCtx, validOpts, "   \t\n  ")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "lang")
	})

	t.Run("zero MaxDuration", func(t *testing.T) {
		err := ValidateCycleArgs(validCtx, validCtx, RecordOpts{MaxDuration: 0}, "ru")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "MaxDuration")
	})

	t.Run("negative MaxDuration", func(t *testing.T) {
		err := ValidateCycleArgs(validCtx, validCtx, RecordOpts{MaxDuration: -1}, "ru")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "MaxDuration")
	})
}

func TestValidateRecordedFile(t *testing.T) {
	t.Run("regular file returns size", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.wav")
		require.NoError(t, os.WriteFile(path, []byte("hello"), 0o600))

		size, err := ValidateRecordedFile(path)
		require.NoError(t, err)
		assert.Equal(t, int64(5), size)
	})

	t.Run("missing file returns error", func(t *testing.T) {
		_, err := ValidateRecordedFile("/nonexistent/a2text-rec.wav")
		require.Error(t, err)
		assert.ErrorIs(t, err, os.ErrNotExist)
	})

	t.Run("directory returns not-regular error", func(t *testing.T) {
		dir := t.TempDir()

		_, err := ValidateRecordedFile(dir)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a regular file")
	})
}
