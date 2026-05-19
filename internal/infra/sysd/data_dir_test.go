package sysd

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKeptAudioDir_HonoursXDG_DATA_HOME pins that an explicit
// $XDG_DATA_HOME wins over $HOME-based defaults so users who relocate
// their XDG data root (e.g. on an external drive) see kept recordings
// land there instead of the literal `~/.local/share/...` fallback.
func TestKeptAudioDir_HonoursXDG_DATA_HOME(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/custom/xdg")

	dir, err := KeptAudioDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/custom/xdg", AppName, "audio"), dir)
}

// TestKeptAudioDir_FallsBackToHome covers the path users on a plain
// Ubuntu install hit: no $XDG_DATA_HOME, so the directory must land in
// `~/.local/share/<AppName>/audio` per the freedesktop Base Directory
// Specification.
func TestKeptAudioDir_FallsBackToHome(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/home/fake")

	dir, err := KeptAudioDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/home/fake", ".local", "share", AppName, "audio"), dir)
}

// TestKeptAudioDir_LeafMatchesAppName guards that the directory tail is
// the AppName constant. A future rename of the project (or a stray
// hardcoded "a2text" in the helper) must not silently fork the layout
// between models/ and audio/.
func TestKeptAudioDir_LeafMatchesAppName(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/x")

	dir, err := KeptAudioDir()
	require.NoError(t, err)

	// dir = /x/<AppName>/audio — parent of the leaf must be AppName.
	parent := filepath.Base(filepath.Dir(dir))
	assert.Equal(t, AppName, parent)
	assert.Equal(t, "audio", filepath.Base(dir))
}
