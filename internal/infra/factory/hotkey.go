package factory

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/usecases/voice"
)

// BuildHotkey returns the evdev-based global hotkey listener. evdev is the
// only supported backend: it reads raw key events from /dev/input/event*,
// sees both Press and Release on any Linux session (Wayland, X11, console)
// and requires read access to the device nodes (usually the "input" group).
// The listener is always built — the hotkey is the only way to start
// recording outside the tray UI.
//
//nolint:ireturn // factory: voice.HotkeyListener is the only stable contract
func BuildHotkey(cfg *config.VoiceConfig, log *slog.Logger, handler voice.Handler) (voice.HotkeyListener, error) {
	if cfg == nil {
		return nil, errors.New("cmd: BuildHotkey: cfg is required")
	}

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	if err := validateHotkeyParams(cfg, handler); err != nil {
		return nil, err
	}

	return buildEvdevHotkey(cfg, log, handler)
}

// validateHotkeyParams checks that handler and key are set.
func validateHotkeyParams(cfg *config.VoiceConfig, handler voice.Handler) error {
	if handler == nil {
		return errors.New("cmd: BuildHotkey: handler is required")
	}

	if strings.TrimSpace(cfg.Hotkey.Key) == "" {
		return errors.New("cmd: BuildHotkey: hotkey.key must not be empty")
	}

	return nil
}
