package stt

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// modelIDFromPath returns the model identifier (basename without ".bin")
// from a model file path. Empty input → empty output.
func modelIDFromPath(path string) string {
	if path == "" {
		return ""
	}

	return strings.TrimSuffix(filepath.Base(path), ".bin")
}

// resolveModelPath maps a button-data model identifier to a full filesystem path.
//
//   - If input contains a path separator, it's treated as a full/relative path
//     and returned unchanged.
//   - Otherwise input is treated as a model ID; ".bin" is appended if missing,
//     and the result is joined with currentDir.
func resolveModelPath(input, currentDir string) string {
	if strings.ContainsRune(input, filepath.Separator) {
		return input
	}

	name := input
	if filepath.Ext(name) != ".bin" {
		name += ".bin"
	}

	return filepath.Join(currentDir, name)
}

// scanModelsDir returns the sorted IDs of all *.bin files in dir
// (basenames without the .bin suffix).
func scanModelsDir(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("whisper models: %w", err)
	}

	ids := make([]string, 0, len(entries))

	for _, e := range entries {
		if e.IsDir() {
			continue
		}

		name := e.Name()
		if !strings.HasSuffix(name, ".bin") {
			continue
		}

		ids = append(ids, strings.TrimSuffix(name, ".bin"))
	}

	sort.Strings(ids)

	return ids, nil
}
