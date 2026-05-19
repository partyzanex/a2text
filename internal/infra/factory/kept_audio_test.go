package factory

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/infra/sysd"
)

// archiverWith returns a keptAudioArchiver whose only populated dependency
// is cfg. resolveDestDir reads no other state, so the rest can stay nil.
func archiverWith(cfg *config.VoiceConfig) *keptAudioArchiver {
	return &keptAudioArchiver{cfg: cfg}
}

// TestResolveDestDir_ExplicitKeepAudioDirWins pins the highest-priority
// case: when the user typed a path in the "Папка для аудио" field, the
// archiver must honour it verbatim even if XDG / TempDir are populated.
func TestResolveDestDir_ExplicitKeepAudioDirWins(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/should/not/be/used")

	arch := archiverWith(&config.VoiceConfig{
		Privacy: config.VoicePrivacyConfig{KeepAudioDir: "/explicit/path"},
		TempDir: "/should/not/be/used/either",
	})

	assert.Equal(t, "/explicit/path", arch.resolveDestDir())
}

// TestResolveDestDir_FallsBackToXDG covers the post-2026-05-19 behaviour:
// with no explicit path the archive must land in the conventional
// $XDG_DATA_HOME/a2text/audio rather than $TMPDIR (the pre-fix default),
// so kept recordings survive reboots and are findable without grepping
// logs.
func TestResolveDestDir_FallsBackToXDG(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/xdg/data")

	arch := archiverWith(&config.VoiceConfig{
		Privacy: config.VoicePrivacyConfig{KeepAudioDir: ""},
		TempDir: "/some/temp",
	})

	expected, err := sysd.KeptAudioDir()
	require.NoError(t, err)
	assert.Equal(t, expected, arch.resolveDestDir(),
		"with no explicit dir the archiver must prefer XDG over TempDir",
	)
	// Sanity check: expected path is XDG-derived.
	assert.Equal(t, filepath.Join("/xdg/data", "a2text", "audio"), arch.resolveDestDir())
}

// TestResolveDestDir_FallsThroughToTempDir covers the unusual case where
// $XDG_DATA_HOME and $HOME are both unset (typical for systemd system
// units running outside a graphical session). The archiver must still
// produce *some* path rather than panicking; cfg.TempDir is next in line.
func TestResolveDestDir_FallsThroughToTempDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "")

	arch := archiverWith(&config.VoiceConfig{
		Privacy: config.VoicePrivacyConfig{KeepAudioDir: ""},
		TempDir: "/tmp/configured",
	})

	got := arch.resolveDestDir()
	// Either sysd.KeptAudioDir succeeded (some environments still resolve a
	// home dir via NSS even with $HOME unset) or it fell through to
	// cfg.TempDir. Both are documented acceptable outcomes; the assertion
	// guards only against `os.TempDir()` being picked while a configured
	// TempDir exists.
	assert.Contains(t,
		[]string{
			"/tmp/configured",
			filepath.Join("/", "a2text", "audio"),
		},
		got,
		"with no XDG_DATA_HOME and no HOME, fall-through must reach cfg.TempDir or a $HOME-derived audio dir",
	)
}
