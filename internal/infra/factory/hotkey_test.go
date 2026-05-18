package factory_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/infra/factory"
	"github.com/partyzanex/a2text/internal/usecases/voice"
)

// noopHandler matches the voice.Handler signature without doing anything.
// Used wherever a real handler is irrelevant to the assertion.
func noopHandler(_ context.Context, _ voice.HotkeyEvent) {}

func TestBuildHotkey_NilCfg_Errors(t *testing.T) {
	t.Parallel()

	_, err := factory.BuildHotkey(nil, nil, noopHandler)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cfg is required")
}

func TestBuildHotkey_NilHandler_Errors(t *testing.T) {
	t.Parallel()

	cfg := &config.VoiceConfig{
		Hotkey: config.VoiceHotkeyConfig{Key: "F4"},
	}

	_, err := factory.BuildHotkey(cfg, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "handler is required")
}

func TestBuildHotkey_EmptyKey_Errors(t *testing.T) {
	t.Parallel()

	cfg := &config.VoiceConfig{
		Hotkey: config.VoiceHotkeyConfig{Key: ""},
	}

	_, err := factory.BuildHotkey(cfg, nil, noopHandler)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hotkey.key")
}

func TestBuildHotkey_Evdev_BuildsListener(t *testing.T) {
	t.Parallel()

	cfg := &config.VoiceConfig{
		Hotkey: config.VoiceHotkeyConfig{
			Key:       "F4",
			Modifiers: []string{"ctrl"},
			Mode:      config.VoiceHotkeyModeHold,
		},
	}

	hk, err := factory.BuildHotkey(cfg, nil, noopHandler)
	require.NoError(t, err)
	require.NotNil(t, hk, "evdev backend must produce a non-nil listener on linux")
}

func TestBuildHotkey_Evdev_UnknownKey_Errors(t *testing.T) {
	t.Parallel()

	cfg := &config.VoiceConfig{
		Hotkey: config.VoiceHotkeyConfig{Key: "ZZZZ"},
	}

	_, err := factory.BuildHotkey(cfg, nil, noopHandler)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "evdev")
}

func TestBuildHotkey_Evdev_UnknownModifier_Errors(t *testing.T) {
	t.Parallel()

	cfg := &config.VoiceConfig{
		Hotkey: config.VoiceHotkeyConfig{
			Key:       "F4",
			Modifiers: []string{"hyper"},
		},
	}

	_, err := factory.BuildHotkey(cfg, nil, noopHandler)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "modifier")
}
