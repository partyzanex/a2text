package settings

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/a2text/internal/infra/config"
)

// TestBuildHotkeyFieldWidgets_BackendOptionsIncludeEvdev pins the regression
// fix: the hotkey backend select must offer "evdev" alongside "auto"/"none".
// Without an explicit evdev option the user has no way to force Press/Release
// capture when XDG_SESSION_TYPE is unset (auto degrades to nil listener →
// hold mode broken).
func TestBuildHotkeyFieldWidgets_BackendOptionsIncludeEvdev(t *testing.T) {
	t.Parallel()

	w := &Window{
		cfg: &config.VoiceConfig{},
		log: slog.New(slog.DiscardHandler),
	}
	ff := &formFields{}

	w.buildHotkeyFieldWidgets(ff)

	require.NotNil(t, ff.hotkeyBackend, "buildHotkeyFieldWidgets must build hotkeyBackend select")
	assert.Equal(t,
		[]string{
			string(config.VoiceHotkeyBackendAuto),
			string(config.VoiceHotkeyBackendEvdev),
			string(config.VoiceHotkeyBackendNone),
		},
		ff.hotkeyBackend.Options,
		"hotkey backend select must expose evdev so users can force Press/Release capture",
	)
}

// TestBuildHotkeyFieldWidgets_ModeOptions confirms the toggle/hold pair is
// available — the failure mode this test guards against is a future edit
// silently dropping hold.
func TestBuildHotkeyFieldWidgets_ModeOptions(t *testing.T) {
	t.Parallel()

	w := &Window{
		cfg: &config.VoiceConfig{},
		log: slog.New(slog.DiscardHandler),
	}
	ff := &formFields{}

	w.buildHotkeyFieldWidgets(ff)

	require.NotNil(t, ff.hotkeyMode)
	assert.Equal(t,
		[]string{
			string(config.VoiceHotkeyModeToggle),
			string(config.VoiceHotkeyModeHold),
		},
		ff.hotkeyMode.Options,
	)
}
