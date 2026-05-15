package settings

import (
	"testing"

	"github.com/stretchr/testify/assert"

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
