package factory

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/usecases/voice"
)

// BuildHotkey is the public factory: it inspects cfg.Hotkey.Backend and
// returns the appropriate voice.HotkeyListener (or nil, nil when the user
// disabled the built-in listener — they will bind via DE shortcut and the
// daemon receives press-only via the bootstrap path).
//
// Backend selection:
//
//   - "" / "auto": x11 on Xorg (if the binary was built with -tags=x11),
//     otherwise none. Wayland users should bind via DE shortcut.
//   - "x11": force XGrabKey. Requires Xorg session + -tags=x11 build.
//   - "evdev": read raw key events from /dev/input/event* (Linux only).
//     Sees Press AND Release, works under any session. Requires read
//     access to the device nodes (usually the "input" group).
//   - "none" / explicit disable: returns (nil, nil).
//
// Errors:
//
//   - explicit "x11" on a binary without the build tag → error (operator
//     asked for the X11 backend, daemon must not silently fall back);
//   - "auto" never returns an error from backend choice — at worst it
//     returns (nil, nil) and the user uses a DE shortcut.
//
//nolint:ireturn // factory: backend chosen at runtime; voice.HotkeyListener is the only stable contract
func BuildHotkey(cfg *config.VoiceConfig, log *slog.Logger, handler voice.Handler) (voice.HotkeyListener, error) {
	if cfg == nil || !cfg.Hotkey.Enabled {
		return nil, nil //nolint:nilnil // documented "no hotkey" sentinel; callers branch on nil
	}

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	if err := validateHotkeyParams(cfg, handler); err != nil {
		return nil, err
	}

	backend := cfg.Hotkey.Backend
	if backend == "" {
		backend = config.VoiceHotkeyBackendAuto
	}

	return buildHotkeyByBackend(cfg, log, handler, backend)
}

// validateHotkeyParams checks that handler and key are set when hotkey is enabled.
func validateHotkeyParams(cfg *config.VoiceConfig, handler voice.Handler) error {
	if handler == nil {
		return errors.New("cmd: BuildHotkey: handler is required when hotkey.enabled=true")
	}

	if strings.TrimSpace(cfg.Hotkey.Key) == "" {
		return errors.New("cmd: BuildHotkey: hotkey.key must not be empty when hotkey.enabled=true")
	}

	return nil
}

// buildHotkeyByBackend dispatches to the concrete backend builder.
func buildHotkeyByBackend(
	cfg *config.VoiceConfig,
	log *slog.Logger,
	handler voice.Handler,
	backend config.VoiceHotkeyBackend,
) (voice.HotkeyListener, error) {
	if backend == config.VoiceHotkeyBackendNone {
		log.Info("voice: hotkey.backend=none — built-in listener disabled, use DE shortcut")

		return nil, nil //nolint:nilnil // documented "no hotkey" sentinel
	}

	switch backend {
	case config.VoiceHotkeyBackendX11:
		return buildX11Hotkey(cfg, log, handler)
	case config.VoiceHotkeyBackendEvdev:
		return buildEvdevHotkey(cfg, log, handler)
	case config.VoiceHotkeyBackendAuto:
		return buildAutoHotkey(cfg, log, handler)
	case config.VoiceHotkeyBackendNone:
		return nil, nil //nolint:nilnil // sentinel
	}

	return nil, fmt.Errorf("cmd: BuildHotkey: unknown backend %q", backend)
}

// buildAutoHotkey picks x11 on Xorg, none otherwise. Wayland users should
// bind the shortcut at the DE level — no built-in hotkey is available.
// Each fallback is logged at INFO so the operator can see which backend
// the daemon settled on in the journal.
//
//nolint:ireturn // see BuildHotkey
func buildAutoHotkey(cfg *config.VoiceConfig, log *slog.Logger, handler voice.Handler) (voice.HotkeyListener, error) {
	sessionType := strings.ToLower(strings.TrimSpace(os.Getenv("XDG_SESSION_TYPE")))

	switch sessionType {
	case "wayland":
		hk, err := buildEvdevHotkey(cfg, log, handler)
		if err == nil {
			log.Info("voice: hotkey.backend=auto: Wayland session — using evdev for Press/Release")

			return hk, nil
		}

		log.Warn(
			"voice: hotkey.backend=auto: Wayland — evdev unavailable, falling back to DE shortcut (toggle-only)",
			slog.Any("err", err),
		)

		return nil, nil //nolint:nilnil

	case "x11":
		hk, err := buildX11Hotkey(cfg, log, handler)
		if err == nil {
			return hk, nil
		}

		log.Warn("voice: hotkey.backend=auto: X11 init failed, falling back to no built-in hotkey",
			slog.Any("err", err),
		)

		return nil, nil //nolint:nilnil

	default:
		log.Warn("voice: hotkey.backend=auto: XDG_SESSION_TYPE not set, cannot pick a backend — use DE shortcut",
			slog.String("session_type", sessionType),
		)

		return nil, nil //nolint:nilnil
	}
}
