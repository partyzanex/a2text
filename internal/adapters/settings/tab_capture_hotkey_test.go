package settings

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/a2text/internal/i18n"
	"github.com/partyzanex/a2text/internal/infra/config"
)

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
			i18n.T(i18n.KeyHotkeyModeToggle),
			i18n.T(i18n.KeyHotkeyModeHold),
		},
		ff.hotkeyMode.Options,
	)
}

// TestHotkeyModeLabelRoundtrip confirms label↔config mapping is bijective —
// guards against UI labels drifting from stored config values.
func TestHotkeyModeLabelRoundtrip(t *testing.T) {
	t.Parallel()

	for _, m := range []config.VoiceHotkeyMode{
		config.VoiceHotkeyModeToggle,
		config.VoiceHotkeyModeHold,
	} {
		assert.Equal(t, m, hotkeyModeFromLabel(hotkeyModeLabel(m)))
	}

	assert.Equal(t, config.VoiceHotkeyModeToggle, hotkeyModeFromLabel("unknown"))
}
