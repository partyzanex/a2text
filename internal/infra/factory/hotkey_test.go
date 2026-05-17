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

func TestBuildHotkey_Disabled_ReturnsNilNil(t *testing.T) {
	t.Parallel()

	cfg := &config.VoiceConfig{
		Hotkey: config.VoiceHotkeyConfig{Enabled: false},
	}

	hk, err := factory.BuildHotkey(cfg, nil, noopHandler)
	require.NoError(t, err)
	assert.Nil(t, hk, "disabled hotkey must return (nil, nil)")
}

func TestBuildHotkey_NilCfg_ReturnsNilNil(t *testing.T) {
	t.Parallel()

	hk, err := factory.BuildHotkey(nil, nil, noopHandler)
	require.NoError(t, err)
	assert.Nil(t, hk)
}

func TestBuildHotkey_NilHandler_Errors(t *testing.T) {
	t.Parallel()

	cfg := &config.VoiceConfig{
		Hotkey: config.VoiceHotkeyConfig{
			Enabled: true,
			Key:     "F4",
			Backend: config.VoiceHotkeyBackendEvdev,
		},
	}

	_, err := factory.BuildHotkey(cfg, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "handler is required")
}

func TestBuildHotkey_EmptyKey_Errors(t *testing.T) {
	t.Parallel()

	cfg := &config.VoiceConfig{
		Hotkey: config.VoiceHotkeyConfig{
			Enabled: true,
			Key:     "",
			Backend: config.VoiceHotkeyBackendEvdev,
		},
	}

	_, err := factory.BuildHotkey(cfg, nil, noopHandler)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hotkey.key")
}

func TestBuildHotkey_Evdev_BuildsListener(t *testing.T) {
	t.Parallel()

	cfg := &config.VoiceConfig{
		Hotkey: config.VoiceHotkeyConfig{
			Enabled:   true,
			Key:       "F4",
			Modifiers: []string{"ctrl"},
			Backend:   config.VoiceHotkeyBackendEvdev,
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
		Hotkey: config.VoiceHotkeyConfig{
			Enabled: true,
			Key:     "ZZZZ",
			Backend: config.VoiceHotkeyBackendEvdev,
		},
	}

	_, err := factory.BuildHotkey(cfg, nil, noopHandler)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "evdev")
}

func TestBuildHotkey_Evdev_UnknownModifier_Errors(t *testing.T) {
	t.Parallel()

	cfg := &config.VoiceConfig{
		Hotkey: config.VoiceHotkeyConfig{
			Enabled:   true,
			Key:       "F4",
			Modifiers: []string{"hyper"},
			Backend:   config.VoiceHotkeyBackendEvdev,
		},
	}

	_, err := factory.BuildHotkey(cfg, nil, noopHandler)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "modifier")
}

// TestBuildHotkey_AutoWayland_BuildsEvdevListener pins the regression fix:
// under XDG_SESSION_TYPE=wayland, backend=auto must produce a working evdev
// listener instead of nil. A nil listener would force the daemon onto the
// DE-shortcut path, which is Press-only and breaks hold mode (Press from
// the DE shortcut starts recording, then GNOME autorepeat keeps firing
// Toggle → start/stop/start/stop while the key is held).
func TestBuildHotkey_AutoWayland_BuildsEvdevListener(t *testing.T) {
	t.Setenv("XDG_SESSION_TYPE", "wayland")

	cfg := &config.VoiceConfig{
		Hotkey: config.VoiceHotkeyConfig{
			Enabled: true,
			Key:     "F4",
			Backend: config.VoiceHotkeyBackendAuto,
			Mode:    config.VoiceHotkeyModeHold,
		},
	}

	hk, err := factory.BuildHotkey(cfg, nil, noopHandler)
	require.NoError(t, err)
	require.NotNil(t, hk,
		"auto on Wayland must fall through to evdev so Press/Release are observed; "+
			"a nil listener would degrade hold mode to toggle-only via the DE shortcut",
	)
}

// TestBuildHotkey_AutoWayland_EvdevUnknownKey_FallsBackToNil verifies that
// when the evdev backend rejects the configured key under auto+Wayland, the
// factory degrades to (nil, nil) rather than returning a hard error. The
// daemon then logs a warning and the user must pick a valid key.
func TestBuildHotkey_AutoWayland_EvdevUnknownKey_FallsBackToNil(t *testing.T) {
	t.Setenv("XDG_SESSION_TYPE", "wayland")

	cfg := &config.VoiceConfig{
		Hotkey: config.VoiceHotkeyConfig{
			Enabled: true,
			Key:     "ZZZZ",
			Backend: config.VoiceHotkeyBackendAuto,
		},
	}

	hk, err := factory.BuildHotkey(cfg, nil, noopHandler)
	require.NoError(t, err, "auto must never return an error from backend choice")
	assert.Nil(t, hk, "evdev rejects unknown key → auto degrades to nil listener")
}

func TestBuildHotkey_UnknownBackend_Errors(t *testing.T) {
	t.Parallel()

	cfg := &config.VoiceConfig{
		Hotkey: config.VoiceHotkeyConfig{
			Enabled: true,
			Key:     "F4",
			Backend: "wat",
		},
	}

	_, err := factory.BuildHotkey(cfg, nil, noopHandler)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown backend")
}
