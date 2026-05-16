package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/partyzanex/a2text/app"
)

// bootstrapUserConfig is the LoadVoice-side hook: when the caller did
// not pass an explicit --config path, drop the embedded default into
// the per-user config dir on first launch. Errors are logged and
// swallowed — a read-only home should not block startup, viper
// discovery + defaults still take over.
func bootstrapUserConfig(explicitPath string) {
	if explicitPath != "" {
		return
	}

	created, dest, err := EnsureUserConfig()
	if err != nil {
		slog.Default().Warn("config: first-run bootstrap skipped",
			slog.Any("err", err))

		return
	}

	if created {
		slog.Default().Info("config: default config written",
			slog.String("path", dest))
	}
}

// defaultConfigYAML returns the embedded template from the app package
// (app/config.yaml at build time). Wrapped in a func so the embed
// source stays a single global (in package app) — the config package
// just forwards the bytes without holding its own variable.
func defaultConfigYAML() []byte { return app.DefaultConfigYAML }

// userConfigSubdir is the per-app directory name appended to
// os.UserConfigDir() (e.g. ~/.config/a2text on Linux, ~/Library/
// Application Support/a2text on macOS, %AppData%\a2text on Windows).
const userConfigSubdir = "a2text"

// userConfigFile is the file name inside that directory.
const userConfigFile = "config.yaml"

// File-mode constants for the bootstrap writes. Config may carry the
// Deepgram API key, so it is owner-readable only.
const (
	userConfigDirMode  = 0o700
	userConfigFileMode = 0o600
)

// EnsureUserConfig creates the user's config directory and writes the
// embedded default config to it if no file is already there. Returns
// (created, path, err): created=true when a new file was written.
// Idempotent: an existing config is left untouched.
//
// Location uses os.UserConfigDir() so the OS-native location is picked
// without per-platform branching:
//
//   - Linux/BSD: $XDG_CONFIG_HOME/a2text/config.yaml,
//     fallback ~/.config/a2text/config.yaml.
//   - macOS:    ~/Library/Application Support/a2text/config.yaml.
//   - Windows:  %AppData%\a2text\config.yaml.
//
// Designed to be called once at startup. Errors are returned to the
// caller (LoadVoice logs and continues) so a read-only home does not
// block the launch — the daemon will run against built-in defaults +
// env vars in that case.
func EnsureUserConfig() (created bool, path string, err error) {
	dir, err := userConfigDir()
	if err != nil {
		return false, "", err
	}

	dest := filepath.Join(dir, userConfigFile)

	if _, statErr := os.Stat(dest); statErr == nil {
		// Already present — nothing to do.
		return false, dest, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return false, dest, fmt.Errorf("stat %s: %w", dest, statErr)
	}

	if err := os.MkdirAll(dir, userConfigDirMode); err != nil {
		return false, dest, fmt.Errorf("mkdir %s: %w", dir, err)
	}

	if err := os.WriteFile(dest, defaultConfigYAML(), userConfigFileMode); err != nil {
		return false, dest, fmt.Errorf("write %s: %w", dest, err)
	}

	return true, dest, nil
}

// UserConfigPath returns the absolute path of the per-user config file,
// regardless of whether it currently exists. Useful for log lines and
// the settings UI "open config in editor" affordance.
func UserConfigPath() (string, error) {
	dir, err := userConfigDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, userConfigFile), nil
}

func userConfigDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}

	return filepath.Join(base, userConfigSubdir), nil
}
