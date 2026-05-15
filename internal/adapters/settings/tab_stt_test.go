package settings

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScanWhisperCppModels_Empty verifies that empty directory returns empty slice.
func TestScanWhisperCppModels_Empty(t *testing.T) {
	tmpDir := t.TempDir()
	models := scanWhisperCppModels(tmpDir)
	assert.Empty(t, models)
}

// TestScanWhisperCppModels_NoModels verifies that directory without .bin files returns empty slice.
func TestScanWhisperCppModels_NoModels(t *testing.T) {
	tmpDir := t.TempDir()
	// Create non-.bin files
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "readme.txt"), []byte("test"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "config.yaml"), []byte("test"), 0o644))

	models := scanWhisperCppModels(tmpDir)
	assert.Empty(t, models)
}

// TestScanWhisperCppModels_WithModels verifies that .bin files are detected and sorted.
func TestScanWhisperCppModels_WithModels(t *testing.T) {
	tmpDir := t.TempDir()

	// Create model files in non-alphabetical order
	modelFiles := []string{
		"ggml-large.bin",
		"ggml-base.bin",
		"ggml-small.bin",
		"ggml-medium.bin",
	}
	for _, name := range modelFiles {
		require.NoError(t, os.WriteFile(filepath.Join(tmpDir, name), []byte("model"), 0o644))
	}

	// Add non-model files that should be ignored
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "readme.txt"), []byte("test"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "ggml-small.txt"), []byte("test"), 0o644))

	models := scanWhisperCppModels(tmpDir)

	// Check that only .bin files are returned, sorted alphabetically
	expectedModels := []string{
		"ggml-base.bin",
		"ggml-large.bin",
		"ggml-medium.bin",
		"ggml-small.bin",
	}
	assert.Equal(t, expectedModels, models)
}

// TestScanWhisperCppModels_CaseInsensitive verifies that .BIN files (uppercase) are detected.
func TestScanWhisperCppModels_CaseInsensitive(t *testing.T) {
	tmpDir := t.TempDir()

	// Create files with uppercase extensions
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "ggml-small.BIN"), []byte("model"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "ggml-medium.Bin"), []byte("model"), 0o644))

	models := scanWhisperCppModels(tmpDir)

	assert.Len(t, models, 2)
	assert.Contains(t, models, "ggml-small.BIN")
	assert.Contains(t, models, "ggml-medium.Bin")
}

// TestScanWhisperCppModels_EmptyPath returns empty slice for empty path string.
func TestScanWhisperCppModels_EmptyPath(t *testing.T) {
	models := scanWhisperCppModels("")
	assert.Empty(t, models)
}

// TestScanWhisperCppModels_WhitespacePath returns empty slice for whitespace-only path.
func TestScanWhisperCppModels_WhitespacePath(t *testing.T) {
	models := scanWhisperCppModels("   ")
	assert.Empty(t, models)
}

// TestScanWhisperCppModels_NonexistentPath returns empty slice for non-existent directory.
func TestScanWhisperCppModels_NonexistentPath(t *testing.T) {
	models := scanWhisperCppModels("/nonexistent/path/that/does/not/exist")
	assert.Empty(t, models)
}

// TestScanWhisperCppModels_Subdirectories verifies that subdirectories are ignored.
func TestScanWhisperCppModels_Subdirectories(t *testing.T) {
	tmpDir := t.TempDir()

	// Create model file
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "ggml-small.bin"), []byte("model"), 0o644))

	// Create subdirectory with model file inside (should be ignored)
	subDir := filepath.Join(tmpDir, "subdir")
	require.NoError(t, os.Mkdir(subDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "ggml-large.bin"), []byte("model"), 0o644))

	models := scanWhisperCppModels(tmpDir)

	// Only top-level model should be returned
	assert.Len(t, models, 1)
	assert.Equal(t, "ggml-small.bin", models[0])
}
