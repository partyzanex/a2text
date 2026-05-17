package daemon

// hotkeyRegOp describes what to do with the desktop-level (GNOME custom
// keybinding) registration when the daemon (re)configures its hotkey wiring.
type hotkeyRegOp int

const (
	hotkeyRegOpNoop hotkeyRegOp = iota
	// hotkeyRegOpSetup installs or refreshes the GNOME custom binding so the
	// DE spawns the CLI on Press → IPC toggle.
	hotkeyRegOpSetup
	// hotkeyRegOpUnsetup removes any existing GNOME custom binding.
	hotkeyRegOpUnsetup
)

// decideHotkeyRegOp picks the action for the desktop-level binding.
//
// Inputs:
//
//   - key: the configured hotkey key (cfg.Hotkey.Key). Empty means the user
//     intentionally has no key bound.
//   - builtinActive: true when the daemon already runs a built-in listener
//     (evdev) that reads Press/Release directly from /dev/input.
//   - unsetupOnEmpty: true for the reload path (user just cleared the key in
//     settings — actively remove the binding); false for the initial register
//     path (nothing to undo on first start with no key).
//
// Decision table:
//
//	key=""         + unsetupOnEmpty=false → Noop      (initial register, no key)
//	key=""         + unsetupOnEmpty=true  → Unsetup   (reload: user cleared field)
//	key!="" + builtinActive               → Unsetup   (evdev drives the daemon;
//	                                                   a GNOME binding would spawn
//	                                                   the CLI on every Press and
//	                                                   race with evdev — see the
//	                                                   hold-mode regression fix)
//	key!="" + !builtinActive              → Setup     (DE shortcut path)
func decideHotkeyRegOp(key string, builtinActive, unsetupOnEmpty bool) hotkeyRegOp {
	if key == "" {
		if unsetupOnEmpty {
			return hotkeyRegOpUnsetup
		}

		return hotkeyRegOpNoop
	}

	if builtinActive {
		return hotkeyRegOpUnsetup
	}

	return hotkeyRegOpSetup
}
