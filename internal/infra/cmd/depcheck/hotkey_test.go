package depcheck_test

import (
	"github.com/partyzanex/a2text/internal/infra/cmd/depcheck"
	"github.com/partyzanex/a2text/internal/infra/config"
)

// withHotkey clones a base cfg and overrides hotkey settings. Keeps the rest
// of baseGoWhisperCfg intact so unrelated dep groups stay green and we can
// filter the result down to GroupHotkey for the assertions.
func withHotkey(cfg *config.VoiceConfig, enabled bool, backend config.VoiceHotkeyBackend) *config.VoiceConfig {
	cfg.Hotkey = config.VoiceHotkeyConfig{
		Enabled: enabled,
		Key:     "F4",
		Backend: backend,
	}

	return cfg
}

// --- hotkey deps: backend selection ---

func (s *DepCheckSuite) TestHotkeyDeps_Disabled_NoDeps() {
	cfg := withHotkey(baseGoWhisperCfg(), false, config.VoiceHotkeyBackendEvdev)

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(map[string]string{
		"pw-record": "/usr/bin/pw-record",
	}))

	s.Empty(filterGroup(allDeps, depcheck.GroupHotkey),
		"hotkey.enabled=false must not contribute any deps")
}

func (s *DepCheckSuite) TestHotkeyDeps_Auto_NoDeps() {
	cfg := withHotkey(baseGoWhisperCfg(), true, config.VoiceHotkeyBackendAuto)

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(map[string]string{
		"pw-record": "/usr/bin/pw-record",
	}))

	s.Empty(filterGroup(allDeps, depcheck.GroupHotkey),
		"auto backend has no pre-Listen probe — must not emit a dep")
}

func (s *DepCheckSuite) TestHotkeyDeps_None_NoDeps() {
	cfg := withHotkey(baseGoWhisperCfg(), true, config.VoiceHotkeyBackendNone)

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(map[string]string{
		"pw-record": "/usr/bin/pw-record",
	}))

	s.Empty(filterGroup(allDeps, depcheck.GroupHotkey))
}

func (s *DepCheckSuite) TestHotkeyDeps_X11_OneDep() {
	cfg := withHotkey(baseGoWhisperCfg(), true, config.VoiceHotkeyBackendX11)

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(map[string]string{
		"pw-record": "/usr/bin/pw-record",
	}))

	hkDeps := filterGroup(allDeps, depcheck.GroupHotkey)
	s.Require().Len(hkDeps, 1, "x11 backend must contribute exactly one hotkey dep")
	s.Equal("x11_session", hkDeps[0].Name)
}

func (s *DepCheckSuite) TestHotkeyDeps_Evdev_ProbesDevInput() {
	cfg := withHotkey(baseGoWhisperCfg(), true, config.VoiceHotkeyBackendEvdev)

	allDeps, _ := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(map[string]string{
		"pw-record": "/usr/bin/pw-record",
	}))

	hkDeps := filterGroup(allDeps, depcheck.GroupHotkey)
	s.Require().Len(hkDeps, 1, "evdev backend must contribute exactly one hotkey dep")
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

func (s *DepCheckSuite) TestHotkeyDeps_UnknownBackend_FatalWithEvdevInAllowedList() {
	cfg := withHotkey(baseGoWhisperCfg(), true, "wat")

	allDeps, missing := depcheck.CheckMode(s.T().Context(), depcheck.ModeDaemon, cfg, testEnv(map[string]string{
		"pw-record": "/usr/bin/pw-record",
	}))

	hkDeps := filterGroup(allDeps, depcheck.GroupHotkey)
	s.Require().Len(hkDeps, 1)
	s.Equal("backend", hkDeps[0].Name)
	// Regression: the message must enumerate every valid backend including
	// evdev — adding a new backend without updating this string is a common
	// drift, hence the explicit assertion.
	s.Contains(hkDeps[0].InstallHint, "evdev")
	s.Contains(hkDeps[0].InstallHint, "auto")
	s.Contains(hkDeps[0].InstallHint, "x11")
	s.Contains(hkDeps[0].InstallHint, "none")
	s.Contains(hkDeps[0].InstallHint, "wat", "unknown value must be quoted back in the hint")

	// Unknown backend is fatal — Check always returns Found=false, so the
	// dep must surface in `missing`.
	s.Contains(missingNames(missing), "backend")
}
