package config_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/a2text/internal/infra/config"
)

// TestExampleConfig_ParsesAndValidates guards against accidental drift between
// the example config shipped in the repo and the loader's schema/validation.
// Any rename of a mapstructure key or new required field will fail this test
// before it can land on operators.
func TestExampleConfig_ParsesAndValidates(t *testing.T) {
	// LoadVoice enforces mode 0700 on temp_dir. t.TempDir() returns a 0755
	// directory, so point the loader at a non-existent subpath it will create
	// itself with the right permissions.
	t.Setenv("A2TEXT_TEMP_DIR", filepath.Join(t.TempDir(), "voice"))
	t.Setenv("A2TEXT_CLOUD_API_KEY", "")

	path, err := filepath.Abs("../../../app/config.yaml")
	require.NoError(t, err)

	cfg, err := config.LoadVoice(path)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	require.Equal(t, config.VoiceProviderGoWhisper, cfg.Provider)
	require.Equal(t, "ru", cfg.Language)

	require.Equal(t, "http://localhost:9081", cfg.GoWhisper.URL)
	require.Equal(t, "/api/whisper", cfg.GoWhisper.Prefix)
	require.Equal(t, "ggml-small", cfg.GoWhisper.Model)
	require.Positive(t, cfg.GoWhisper.Timeout)
	require.True(t, cfg.GoWhisper.AutoDownload)

	require.Equal(t, config.VoiceOutputModeClipboard, cfg.Output.Mode)
	require.Equal(t, config.VoiceAutopasteCommandAuto, cfg.Output.AutopasteCommand)

	require.Equal(t, config.VoiceCaptureBackendAuto, cfg.Capture.Backend)
	require.Equal(t, 16000, cfg.Capture.SampleRate)
	require.Equal(t, 1, cfg.Capture.Channels)
}
