// Package setup implements the `a2text setup` subcommand, which registers
// (or removes) a global keyboard shortcut in the current desktop environment.
//
// Currently only GNOME is supported. The shortcut is stored in dconf via
// gsettings at the path:
//
//	/org/gnome/settings-daemon/plugins/media-keys/custom-keybindings/a2text/
//
// On non-GNOME sessions RunSetup and RunUnsetup return ErrDesktopUnsupported
// immediately without touching any system state.
package setup

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/partyzanex/a2text/internal/infra/config"
)

// gsettings path / schema constants.
const (
	gnomeBindingPath  = "/org/gnome/settings-daemon/plugins/media-keys/custom-keybindings/a2text/"
	gnomeListSchema   = "org.gnome.settings-daemon.plugins.media-keys"
	gnomeListKey      = "custom-keybindings"
	gnomeDetailSchema = "org.gnome.settings-daemon.plugins.media-keys.custom-keybinding"
	bindingName       = "a2text voice"
	gsettingsBin      = "gsettings"

	// modNameSuper is the "super" modifier name as written in config.
	// Extracted to avoid goconst hitting the 3-occurrence threshold.
	modNameSuper = "super"
)

// ErrDesktopUnsupported is returned by RunSetup / RunUnsetup when the current
// desktop environment does not support automated shortcut registration.
var ErrDesktopUnsupported = errors.New("setup: desktop environment does not support automated shortcut registration")

// ErrMissingKey is returned when hotkey.key is empty in config.
var ErrMissingKey = errors.New("setup: hotkey.key must be set in config")

// runner abstracts exec.CommandContext so tests can inject a fake.
type runner interface {
	run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// execRunner is the production runner backed by os/exec.
type execRunner struct{}

func (execRunner) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	//nolint:gosec // name is always the "gsettings" constant at all call sites
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("exec %s: %w", name, err)
	}

	return out, nil
}

// RunSetup registers the a2text hotkey in the current desktop environment.
// Reads the hotkey key + modifiers from cfg. Returns ErrDesktopUnsupported on
// non-GNOME sessions and ErrMissingKey when hotkey.key is blank.
func RunSetup(ctx context.Context, cfg *config.VoiceConfig, log *slog.Logger) error {
	return runSetup(ctx, cfg, log, execRunner{})
}

// RunUnsetup removes the a2text hotkey binding that was registered by RunSetup.
// Returns ErrDesktopUnsupported on non-GNOME sessions.
func RunUnsetup(ctx context.Context, log *slog.Logger) error {
	return runUnsetup(ctx, log, execRunner{})
}

func runSetup(ctx context.Context, cfg *config.VoiceConfig, log *slog.Logger, rn runner) error {
	if !isGNOME() {
		return ErrDesktopUnsupported
	}

	if cfg.Hotkey.Key == "" {
		return ErrMissingKey
	}

	binding := buildBinding(cfg.Hotkey.Key, cfg.Hotkey.Modifiers)

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("setup: resolve binary path: %w", err)
	}

	if err := setGNOMEBinding(ctx, rn, execPath, binding); err != nil {
		return err
	}

	if err := addBindingToGNOMEList(ctx, rn); err != nil {
		return err
	}

	log.InfoContext(ctx, "voice: hotkey registered",
		slog.String("binding", binding),
		slog.String("command", execPath),
	)

	return nil
}

func runUnsetup(ctx context.Context, log *slog.Logger, rn runner) error {
	if !isGNOME() {
		return ErrDesktopUnsupported
	}

	if err := removeBindingFromGNOMEList(ctx, rn); err != nil {
		return err
	}

	log.InfoContext(ctx, "voice: hotkey unregistered")

	return nil
}

// setGNOMEBinding writes name, command, and binding properties into dconf at
// our fixed custom-keybinding path.
func setGNOMEBinding(ctx context.Context, rn runner, execPath, binding string) error {
	schema := gnomeDetailSchema + ":" + gnomeBindingPath

	if err := gsettingsSet(ctx, rn, schema, "name", bindingName); err != nil {
		return fmt.Errorf("setup: set name: %w", err)
	}

	if err := gsettingsSet(ctx, rn, schema, "command", execPath); err != nil {
		return fmt.Errorf("setup: set command: %w", err)
	}

	if err := gsettingsSet(ctx, rn, schema, "binding", binding); err != nil {
		return fmt.Errorf("setup: set binding: %w", err)
	}

	return nil
}

func addBindingToGNOMEList(ctx context.Context, rn runner) error {
	current, err := gsettingsGet(ctx, rn, gnomeListSchema, gnomeListKey)
	if err != nil {
		return fmt.Errorf("setup: read keybindings list: %w", err)
	}

	paths := addToList(parseGSettingsList(current), gnomeBindingPath)

	if err := gsettingsSet(ctx, rn, gnomeListSchema, gnomeListKey, formatGSettingsList(paths)); err != nil {
		return fmt.Errorf("setup: update keybindings list: %w", err)
	}

	return nil
}

func removeBindingFromGNOMEList(ctx context.Context, rn runner) error {
	current, err := gsettingsGet(ctx, rn, gnomeListSchema, gnomeListKey)
	if err != nil {
		return fmt.Errorf("setup: read keybindings list: %w", err)
	}

	paths := removeFromList(parseGSettingsList(current), gnomeBindingPath)

	if err := gsettingsSet(ctx, rn, gnomeListSchema, gnomeListKey, formatGSettingsList(paths)); err != nil {
		return fmt.Errorf("setup: update keybindings list: %w", err)
	}

	return nil
}

func gsettingsSet(ctx context.Context, rn runner, schema, key, value string) error {
	if _, err := rn.run(ctx, gsettingsBin, "set", schema, key, value); err != nil {
		return fmt.Errorf("gsettings set %s %s: %w", schema, key, err)
	}

	return nil
}

func gsettingsGet(ctx context.Context, rn runner, schema, key string) (string, error) {
	out, err := rn.run(ctx, gsettingsBin, "get", schema, key)
	if err != nil {
		return "", fmt.Errorf("gsettings get %s %s: %w", schema, key, err)
	}

	return strings.TrimSpace(string(out)), nil
}

// isGNOME reports whether the current session is GNOME-based.
// Checks XDG_CURRENT_DESKTOP (covers "GNOME" and "ubuntu:GNOME") and the
// legacy GNOME_DESKTOP_SESSION_ID variable.
func isGNOME() bool {
	desktop := strings.ToUpper(os.Getenv("XDG_CURRENT_DESKTOP"))
	if strings.Contains(desktop, "GNOME") {
		return true
	}

	return os.Getenv("GNOME_DESKTOP_SESSION_ID") != ""
}

// buildBinding assembles the GLib accelerator string from the config key and
// modifier list. Example: key="Q", modifiers=["super"] → "<Super>q".
// Returns an empty string when key is empty.
func buildBinding(key string, modifiers []string) string {
	if key == "" {
		return ""
	}

	var sb strings.Builder

	for _, mod := range modifiers {
		sb.WriteString(modifierToGNOME(mod))
	}

	sb.WriteString(normalizeKey(key))

	return sb.String()
}

// modifierToGNOME converts a modifier name (as written in config) to the
// GLib accelerator notation used by GNOME.
func modifierToGNOME(mod string) string {
	switch strings.ToLower(mod) {
	case modNameSuper, "win", "meta":
		return "<Super>"
	case "ctrl", "control":
		return "<Control>"
	case "alt":
		return "<Alt>"
	case "shift":
		return "<Shift>"
	default:
		return mod
	}
}

// normalizeKey returns the key name in the form GNOME expects: single-letter
// keys are lowercased; function keys and named keys are passed through as-is.
func normalizeKey(key string) string {
	if len(key) == 1 {
		return strings.ToLower(key)
	}

	return key
}

// parseGSettingsList parses the output of `gsettings get … custom-keybindings`
// into a slice of D-Bus object paths.
//
// Recognised forms:
//
//	@as []                   → nil
//	['path1', 'path2', …]    → ["path1", "path2", …]
func parseGSettingsList(raw string) []string {
	raw = strings.TrimSpace(raw)

	if raw == "" || raw == "@as []" {
		return nil
	}

	raw = strings.TrimPrefix(raw, "[")
	raw = strings.TrimSuffix(raw, "]")
	raw = strings.TrimSpace(raw)

	if raw == "" {
		return nil
	}

	parts := strings.Split(raw, ", ")
	paths := make([]string, 0, len(parts))

	for _, part := range parts {
		part = strings.Trim(strings.TrimSpace(part), "'")
		if part != "" {
			paths = append(paths, part)
		}
	}

	return paths
}

// formatGSettingsList serialises a path slice back to the gsettings array
// literal format.  Empty slice → "@as []".
func formatGSettingsList(paths []string) string {
	if len(paths) == 0 {
		return "@as []"
	}

	return "['" + strings.Join(paths, "', '") + "']"
}

// addToList returns paths with path appended if it is not already present
// (idempotent).
func addToList(paths []string, path string) []string {
	if slices.Contains(paths, path) {
		return paths
	}

	return append(paths, path)
}

// removeFromList returns a new slice with all occurrences of path removed.
func removeFromList(paths []string, path string) []string {
	result := make([]string, 0, len(paths))

	for _, pp := range paths {
		if pp != path {
			result = append(result, pp)
		}
	}

	return result
}
