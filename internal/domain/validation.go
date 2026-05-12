package domain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidateCycleArgs checks the arguments that must be satisfied before any
// I/O begins in a dictation cycle. Returns a descriptive error for each
// violated invariant so the caller can surface it without wrapping.
func ValidateCycleArgs(ctx, recordCtx context.Context, opts RecordOpts, lang string) error {
	if ctx == nil || recordCtx == nil {
		return errors.New("voice: Cycle: ctx and recordCtx must not be nil")
	}

	if strings.TrimSpace(lang) == "" {
		return errors.New("voice: lang must not be empty or whitespace-only")
	}

	if opts.MaxDuration <= 0 {
		return errors.New("voice: MaxDuration must be positive")
	}

	return nil
}

// ValidateRecordedFile stats path and confirms it is a regular file.
// Returns the file size on success. An error means the recorder produced
// an unusable artifact; the caller wraps it as PhaseRecord.
func ValidateRecordedFile(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("voice: %w", err)
	}

	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("recorded audio is not a regular file: %s", filepath.Base(path))
	}

	return info.Size(), nil
}
