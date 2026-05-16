package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveLauncherPath_NotUnderLibA2text_ReturnsAsIs verifies that paths
// outside the canonical lib/a2text/ layout are returned unchanged.
func TestResolveLauncherPath_NotUnderLibA2text_ReturnsAsIs(t *testing.T) {
	cases := []string{
		"/usr/bin/a2text",
		"/tmp/a2text",
		"/home/user/dev/a2text/bin/a2text",
	}

	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			assert.Equal(t, p, resolveLauncherPath(p))
		})
	}
}

// TestResolveLauncherPath_LibLayoutWithoutWrapper_ReturnsAsIs verifies that
// the input path is returned when the expected wrapper does not exist on disk.
func TestResolveLauncherPath_LibLayoutWithoutWrapper_ReturnsAsIs(t *testing.T) {
	root := t.TempDir()

	libDir := filepath.Join(root, "lib", "a2text")
	require.NoError(t, os.MkdirAll(libDir, 0o755))

	bin := filepath.Join(libDir, "a2text.bin")
	require.NoError(t, os.WriteFile(bin, []byte("elf"), 0o755))

	assert.Equal(t, bin, resolveLauncherPath(bin))
}

// TestResolveLauncherPath_WrapperExists_ReturnsWrapper verifies the canonical
// case: /lib/a2text/a2text.bin → /bin/a2text when the wrapper file exists.
func TestResolveLauncherPath_WrapperExists_ReturnsWrapper(t *testing.T) {
	root := t.TempDir()

	libDir := filepath.Join(root, "lib", "a2text")
	binDir := filepath.Join(root, "bin")

	require.NoError(t, os.MkdirAll(libDir, 0o755))
	require.NoError(t, os.MkdirAll(binDir, 0o755))

	innerBin := filepath.Join(libDir, "a2text.bin")
	wrapper := filepath.Join(binDir, "a2text")

	require.NoError(t, os.WriteFile(innerBin, []byte("elf"), 0o755))
	require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh"), 0o755))

	assert.Equal(t, wrapper, resolveLauncherPath(innerBin))
}

// TestResolveLauncherPath_WrapperIsDirectory_FallsBack guards against the
// stat-but-directory edge case: if /bin/a2text is a directory (impossible in
// practice but defensive), the function falls back to execPath.
func TestResolveLauncherPath_WrapperIsDirectory_FallsBack(t *testing.T) {
	root := t.TempDir()

	libDir := filepath.Join(root, "lib", "a2text")
	binDir := filepath.Join(root, "bin")
	wrapper := filepath.Join(binDir, "a2text")

	require.NoError(t, os.MkdirAll(libDir, 0o755))
	require.NoError(t, os.MkdirAll(wrapper, 0o755))

	innerBin := filepath.Join(libDir, "a2text.bin")
	require.NoError(t, os.WriteFile(innerBin, []byte("elf"), 0o755))

	assert.Equal(t, innerBin, resolveLauncherPath(innerBin))
}
