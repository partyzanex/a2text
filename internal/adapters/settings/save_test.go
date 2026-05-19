package settings

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/a2text/internal/infra/config"
)

// TestApplyOutputToMap_RestoreClipboard verifies that the restore_clipboard
// setting is persisted to the map when saving config.
func TestApplyOutputToMap_RestoreClipboard(t *testing.T) {
	tests := []struct {
		name     string
		restore  bool
		expected bool
	}{
		{
			name:     "restore enabled",
			restore:  true,
			expected: true,
		},
		{
			name:     "restore disabled",
			restore:  false,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.VoiceConfig{
				Output: config.VoiceOutputConfig{
					Mode:             config.VoiceOutputModeClipboardAutopaste,
					AutopasteCommand: config.VoiceAutopasteCommandUinput,
					RestoreClipboard: tt.restore,
				},
			}

			dst := make(map[string]any)
			applyOutputToMap(dst, cfg)

			output, ok := dst["output"].(map[string]any)
			assert.True(t, ok, "output key must be a map")

			restore, ok := output["restore_clipboard"].(bool)
			assert.True(t, ok, "restore_clipboard must be a bool")
			assert.Equal(t, tt.expected, restore)
		})
	}
}

// TestApplyOutputToMap_AllFields verifies that all Output fields are persisted.
func TestApplyOutputToMap_AllFields(t *testing.T) {
	cfg := &config.VoiceConfig{
		Output: config.VoiceOutputConfig{
			Mode:             config.VoiceOutputModeClipboardAutopaste,
			AutopasteCommand: config.VoiceAutopasteCommandXdotool,
			RestoreClipboard: true,
		},
	}

	dst := make(map[string]any)
	applyOutputToMap(dst, cfg)

	output, ok := dst["output"].(map[string]any)
	assert.True(t, ok)
	assert.Equal(t, config.VoiceOutputModeClipboardAutopaste, output["mode"])
	assert.Equal(t, config.VoiceAutopasteCommandXdotool, output["autopaste_command"])
	assert.Equal(t, true, output["restore_clipboard"])
}

// TestApplyPrivacyToMap_PersistsKeptAudioFields pins the regression fix
// for the "kept-audio dir/format reset on restart" bug observed
// 2026-05-19: the in-memory cfg was updated by the form but the
// persistence layer dropped keep_audio_dir and keep_audio_format on
// disk write, so viper repopulated the defaults at next launch.
func TestApplyPrivacyToMap_PersistsKeptAudioFields(t *testing.T) {
	t.Parallel()

	cfg := &config.VoiceConfig{
		Privacy: config.VoicePrivacyConfig{
			LogTranscript:   true,
			KeepAudio:       true,
			KeepAudioDir:    "/home/user/Music/a2text",
			KeepAudioFormat: config.VoiceKeepAudioFormatOGG,
		},
	}

	dst := make(map[string]any)
	applyPrivacyToMap(dst, cfg)

	privacy, ok := dst["privacy"].(map[string]any)
	require.True(t, ok, "privacy key must be a map")

	assert.Equal(t, true, privacy["log_transcript"])
	assert.Equal(t, true, privacy["keep_audio"])
	assert.Equal(t, "/home/user/Music/a2text", privacy["keep_audio_dir"],
		"keep_audio_dir must round-trip — bug 2026-05-19: setting reset after restart")
	assert.Equal(t, config.VoiceKeepAudioFormatOGG, privacy["keep_audio_format"],
		"keep_audio_format must round-trip — bug 2026-05-19: setting reset after restart")
}

// TestApplyPrivacyToMap_EmptyKeptAudioDir guards the "blank path = use
// default" case: the field must be emitted (as an empty string) so a
// previous non-empty value in the on-disk YAML is cleared, not preserved.
func TestApplyPrivacyToMap_EmptyKeptAudioDir(t *testing.T) {
	t.Parallel()

	cfg := &config.VoiceConfig{
		Privacy: config.VoicePrivacyConfig{
			KeepAudio:    true,
			KeepAudioDir: "",
		},
	}

	dst := map[string]any{
		"privacy": map[string]any{
			"keep_audio_dir": "/old/stale/path",
		},
	}

	applyPrivacyToMap(dst, cfg)

	privacy := dst["privacy"].(map[string]any)
	assert.Empty(t, privacy["keep_audio_dir"],
		"empty in-memory value must overwrite the stale path on disk",
	)
}

// TestApplyOutputToMap_PreservesExisting verifies that existing keys are not
// overwritten when they're not part of the Output config.
func TestApplyOutputToMap_PreservesExisting(t *testing.T) {
	cfg := &config.VoiceConfig{
		Output: config.VoiceOutputConfig{
			Mode:             config.VoiceOutputModeClipboard,
			AutopasteCommand: config.VoiceAutopasteCommandAuto,
		},
	}

	dst := map[string]any{
		"output": map[string]any{
			"extra_field": "should be preserved",
		},
	}

	applyOutputToMap(dst, cfg)

	output := dst["output"].(map[string]any)
	assert.Equal(t, "should be preserved", output["extra_field"])
}
