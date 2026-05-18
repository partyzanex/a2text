package settings

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
			string(config.VoiceHotkeyModeToggle),
			string(config.VoiceHotkeyModeHold),
		},
		ff.hotkeyMode.Options,
	)
}
