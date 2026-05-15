package factory

import (
	"fmt"
	"log/slog"

	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/usecases/voice"
	"github.com/partyzanex/a2text/pkg/hotkey"
)

// buildEvdevHotkey wires the evdev (/dev/input) backend. Linux-only; the
// non-Linux build of pkg/hotkey returns ErrEvdevUnsupported from Listen, so
// the listener is constructed unconditionally here and the error surfaces
// when Daemon.Serve actually starts the listener goroutine.
//
//nolint:ireturn // see BuildHotkey
func buildEvdevHotkey(cfg *config.VoiceConfig, log *slog.Logger, handler voice.Handler) (voice.HotkeyListener, error) {
	hk, err := hotkey.NewEvdevHotkey(handler, cfg.Hotkey.Key, cfg.Hotkey.Modifiers, log)
	if err != nil {
		return nil, fmt.Errorf("cmd: BuildHotkey: create evdev hotkey: %w", err)
	}

	log.Info("voice: hotkey backend=evdev",
		slog.String("key", cfg.Hotkey.Key),
		slog.Any("modifiers", cfg.Hotkey.Modifiers),
		slog.String("mode", string(cfg.Hotkey.Mode)),
	)

	return hk, nil
}
