package depcheck_test

import (
	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/infra/depcheck"
)

// withHotkey clones a base cfg and overrides hotkey settings.
func withHotkey(cfg *config.VoiceConfig) *config.VoiceConfig {
	cfg.Hotkey = config.VoiceHotkeyConfig{
		Key: "F4",
	}

	return cfg
}

// TestHotkeyDeps_ProbesDevInput verifies that the evdev hotkey listener
// always contributes a /dev/input probe — the backend is the only
// implementation and is always active.
func (s *DepCheckSuite) TestHotkeyDeps_ProbesDevInput() {
	cfg := withHotkey(baseGoWhisperCfg())

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(map[string]string{
		"pw-record": "/usr/bin/pw-record",
	}))

	hkDeps := filterGroup(allDeps, depcheck.GroupHotkey)
	s.Require().Len(hkDeps, 1, "hotkey deps must contribute exactly one evdev probe")
	s.Equal("evdev", hkDeps[0].Name)
	s.Equal(depcheck.GroupHotkey, hkDeps[0].Group)
	s.Contains(hkDeps[0].InstallHint, "input")
	s.Contains(hkDeps[0].RequiredFor, "evdev")

	// The Check function probes /dev/input/event* on the host. On a Linux
	// dev/CI box at least one event node exists; on systems where the user
	// has read access (input group or ACL) Found=true with a path detail,
	// otherwise Found=false with a "no readable" message. Both are valid
	// outcomes — assert only that the probe RAN to completion and produced
	// either a detail (path) or a not-found explanation.
	res := hkDeps[0].Check(s.T().Context(), testEnv(nil))
	s.NotEmpty(res.Detail, "evdev probe must always populate Detail (path or reason)")
}
