package stt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelIDFromPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"ggml-small.bin", "ggml-small"},
		{"./ggml-large-v3.bin", "ggml-large-v3"},
		{"/usr/share/whisper/ggml-medium.bin", "ggml-medium"},
		{"models/ggml-large-v3-turbo-q5_0.bin", "ggml-large-v3-turbo-q5_0"},
		{"file.txt", "file.txt"}, // not a .bin — basename returned as-is
	}
	for _, c := range cases {
		assert.Equal(t, c.want, modelIDFromPath(c.in), "input: %q", c.in)
	}
}

func TestResolveModelPath_FullPath_ReturnedAsIs(t *testing.T) {
	// Anything with a separator is treated as path.
	got := resolveModelPath("/tmp/models/ggml-medium.bin", "/other/dir")
	assert.Equal(t, "/tmp/models/ggml-medium.bin", got)

	got = resolveModelPath("./local/ggml-small.bin", "/cwd")
	assert.Equal(t, "./local/ggml-small.bin", got)
}

func TestResolveModelPath_BareID_JoinedWithDir(t *testing.T) {
	got := resolveModelPath("ggml-large-v3-turbo", "/models")
	assert.Equal(t, filepath.Join("/models", "ggml-large-v3-turbo.bin"), got)
}

func TestResolveModelPath_BareIDWithBinExt_NotDoubled(t *testing.T) {
	got := resolveModelPath("ggml-large.bin", "/models")
	assert.Equal(t, filepath.Join("/models", "ggml-large.bin"), got)
}

func TestResolveModelPath_EmptyDir_AppendsBin(t *testing.T) {
	got := resolveModelPath("ggml-tiny", "")
	assert.Equal(t, "ggml-tiny.bin", got)
}

func TestScanModelsDir_OnlyBinFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"ggml-small.bin",
		"ggml-medium.bin",
		"ggml-large-v3.bin",
		"readme.txt",  // ignored
		"random.gguf", // ignored
		".hidden.bin", // included (matches *.bin)
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600))
	}

	require.NoError(t, os.Mkdir(filepath.Join(dir, "subdir.bin"), 0o750)) // dir, must be skipped

	ids, err := scanModelsDir(dir)
	require.NoError(t, err)

	assert.Equal(t, []string{".hidden", "ggml-large-v3", "ggml-medium", "ggml-small"}, ids,
		"must return only .bin files (sorted), excluding directories")
}

func TestScanModelsDir_EmptyDir_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	ids, err := scanModelsDir(dir)
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestScanModelsDir_NonExistent_ReturnsError(t *testing.T) {
	_, err := scanModelsDir("/definitely/does/not/exist/here")
	assert.Error(t, err)
}
