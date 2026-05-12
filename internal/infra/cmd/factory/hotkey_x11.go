//go:build linux && x11

package factory

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/usecases/voice"
	"github.com/partyzanex/a2text/pkg/hotkey"
)

// buildX11Hotkey wires the X11 (XGrabKey) backend. Called from BuildHotkey
// in hotkey.go when cfg.Hotkey.Backend is "x11" or "auto" resolved to X11.
//
// The actual XOpenDisplay / XGrabKey calls happen inside Listen, not here,
// so a missing DISPLAY env or a busy keysym surfaces only when Daemon.Serve
// starts the listener goroutine — same lazy-error policy as the portal
// backend.
//
//nolint:ireturn // see BuildHotkey
func buildX11Hotkey(cfg *config.VoiceConfig, log *slog.Logger, handler voice.Handler) (voice.HotkeyListener, error) {
	mods, err := parseHotkeyModifiers(cfg.Hotkey.Modifiers)
	if err != nil {
		return nil, fmt.Errorf("cmd: BuildHotkey: parse modifiers: %w", err)
	}

	hk, err := hotkey.NewX11Hotkey(handler, cfg.Hotkey.Key, mods, log)
	if err != nil {
		return nil, fmt.Errorf("cmd: BuildHotkey: create X11 hotkey: %w", err)
	}

	log.Info("voice: hotkey backend=x11",
		slog.String("key", cfg.Hotkey.Key),
		slog.Any("modifiers", cfg.Hotkey.Modifiers),
		slog.String("mode", string(cfg.Hotkey.Mode)),
	)

	return hk, nil
}

// parseHotkeyModifiers maps user-friendly modifier names to the X11 bitmask
// constants exported by adapters/hotkey. Empty entries are ignored so a
// trailing comma in YAML lists does not fail startup.
func parseHotkeyModifiers(mods []string) (uint, error) {
	var result uint

	for _, m := range mods {
		switch strings.ToLower(strings.TrimSpace(m)) {
		case "":
			continue
		case "super", "mod4", "win":
			result |= hotkey.Mod4
		case "alt", "mod1":
			result |= hotkey.Mod1
		case "ctrl", "control":
			result |= hotkey.ModControl
		case "shift":
			result |= hotkey.ModShift
		default:
			return 0, fmt.Errorf("unknown modifier %q (allowed: super/alt/ctrl/shift)", m)
		}
	}

	return result, nil
}
