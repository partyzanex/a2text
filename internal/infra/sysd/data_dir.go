package sysd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AppName is the directory leaf used under XDG data roots
// (`$XDG_DATA_HOME/<AppName>/...`). Centralised so move/rename touches
// one constant.
const AppName = "a2text"

// WhisperCppModelsDir returns the conventional per-user directory where
// whisper.cpp .bin models are kept on this machine. Honours
// XDG_DATA_HOME with a fallback to `~/.local/share/<AppName>/models`
// per the freedesktop Base Directory Specification.
//
// Used by:
//   - the settings UI to pre-fill the "models directory" entry on a
//     fresh install (so the user sees the actual default path rather
//     than an empty box);
//   - the daemon bootstrap to know where to auto-download ggml-small.bin
//     when the user has not picked a model yet.
//
// Returns an error only when $HOME is also unresolvable — extremely
// rare; callers should treat the error as "no usable default exists,
// surface the problem rather than guessing /tmp".
func WhisperCppModelsDir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); dir != "" {
		return filepath.Join(dir, AppName, "models"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("sysd: resolve $HOME for models dir: %w", err)
	}

	return filepath.Join(home, ".local", "share", AppName, "models"), nil
}
