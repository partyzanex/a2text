package factory

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/partyzanex/a2text/internal/infra/config"
	"github.com/partyzanex/a2text/internal/usecases/voice"
	"github.com/partyzanex/a2text/pkg/hotkey"
)

// BuildHotkey is the public factory: it inspects cfg.Hotkey.Backend and
// returns the appropriate voice.HotkeyListener (or nil, nil when the user
// disabled the built-in listener — they will bind via DE shortcut and the
// daemon receives press-only via the bootstrap path).
//
// Backend selection:
//
//   - "" / "auto": portal on Wayland (if available), x11 on Xorg (if the
//     binary was built with -tags=x11), otherwise none.
//   - "portal": force xdg-desktop-portal GlobalShortcuts. Works on any
//     session with a modern xdg-desktop-portal backend (GNOME 45+, KDE
//     5.27+, wlroots with xdg-desktop-portal-wlr).
//   - "x11": force XGrabKey. Requires Xorg session + -tags=x11 build.
//   - "none" / explicit disable: returns (nil, nil).
//
// Errors:
//
//   - explicit "x11" on a binary without the build tag → error (operator
//     asked for the X11 backend, daemon must not silently fall back);
//   - explicit "portal" when the portal interface is missing → error
//     (same rationale: explicit asks must fail loudly);
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
	case config.VoiceHotkeyBackendPortal:
		return buildPortalHotkey(cfg, log, handler)
	case config.VoiceHotkeyBackendX11:
		return buildX11Hotkey(cfg, log, handler)
	case config.VoiceHotkeyBackendAuto:
		return buildAutoHotkey(cfg, log, handler)
	case config.VoiceHotkeyBackendNone:
		return nil, nil //nolint:nilnil // sentinel
	}

	return nil, fmt.Errorf("cmd: BuildHotkey: unknown backend %q", backend)
}

// buildAutoHotkey picks portal on Wayland, x11 on Xorg, none otherwise.
// Each fallback is logged at INFO so the operator can see which backend
// the daemon settled on in the journal.
//
//nolint:ireturn // see BuildHotkey
func buildAutoHotkey(cfg *config.VoiceConfig, log *slog.Logger, handler voice.Handler) (voice.HotkeyListener, error) {
	sessionType := strings.ToLower(strings.TrimSpace(os.Getenv("XDG_SESSION_TYPE")))

	switch sessionType {
	case "wayland":
		hk, err := buildPortalHotkey(cfg, log, handler)
		if err == nil {
			return hk, nil
		}

		log.Warn("voice: hotkey.backend=auto: portal init failed, no fallback for Wayland — use DE shortcut",
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

// buildPortalHotkey constructs the portal-backed listener. Errors at this
// stage are config issues (missing key); the actual D-Bus probe happens
// inside Listen so a missing portal surfaces at daemon-startup log, not
// here.
//
//nolint:ireturn // see BuildHotkey
func buildPortalHotkey(cfg *config.VoiceConfig, log *slog.Logger, handler voice.Handler) (voice.HotkeyListener, error) {
	listener, err := hotkey.NewPortalHotkey(handler, cfg.Hotkey.Key, cfg.Hotkey.Modifiers, log)
	if err != nil {
		return nil, fmt.Errorf("cmd: BuildHotkey: portal: %w", err)
	}

	log.Info("voice: hotkey backend=portal",
		slog.String("key", cfg.Hotkey.Key),
		slog.Any("modifiers", cfg.Hotkey.Modifiers),
		slog.String("mode", string(cfg.Hotkey.Mode)),
	)

	return listener, nil
}
