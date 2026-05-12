package sysd

import (
	"fmt"
	"os"
	"path/filepath"
)

// runtimeDirPerm is the strict mode applied to our private runtime directory.
// 0700 prevents other users on the system from reading/writing the unix
// socket or the PID lock — a placement of either in shared /tmp without
// per-user isolation is a TOCTOU/DoS hole.
const runtimeDirPerm os.FileMode = 0o700

// DefaultRuntimeDir returns the per-user directory that holds the daemon's
// runtime artifacts (unix socket, PID lock, future temp files).
//
// Resolution order:
//
//  1. $XDG_RUNTIME_DIR/a2text — preferred. /run/user/$UID is already 0700,
//     tmpfs, and auto-cleaned on logout. We add a subdirectory so we can
//     enforce 0700 on our own files even if the parent gets relaxed.
//  2. os.TempDir()/a2text-voice-$UID — fallback for environments without
//     XDG_RUNTIME_DIR (very old distros, sandboxed installs). The $UID
//     suffix prevents collisions with other users on the same /tmp.
func DefaultRuntimeDir() string {
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "a2text")
	}

	return filepath.Join(os.TempDir(), "a2text-voice-"+currentUserID())
}

// EnsureRuntimeDir creates DefaultRuntimeDir() with 0700 perms if missing,
// then verifies the directory is owned by the current user with the
// expected mode. Returns a descriptive error if anything is off — better
// to refuse to start than to bind a unix socket inside a directory the
// daemon does not actually control.
//
// Note: MkdirAll does NOT fix the mode of an already-existing directory.
// If $XDG_RUNTIME_DIR/a2text was created earlier with looser permissions,
// we fail closed and tell the user to chmod it themselves rather than
// silently relaxing-and-tightening it under their feet.
func EnsureRuntimeDir() error {
	dir := DefaultRuntimeDir()

	if err := os.MkdirAll(dir, runtimeDirPerm); err != nil {
		return fmt.Errorf("create runtime dir %q: %w", dir, err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("stat runtime dir %q: %w", dir, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("runtime path %q is not a directory", dir)
	}

	if info.Mode().Perm() != runtimeDirPerm {
		return fmt.Errorf(
			"unsafe runtime dir permissions %q: got %o, want %o (fix: chmod %o %q)",
			dir, info.Mode().Perm(), runtimeDirPerm, runtimeDirPerm, dir,
		)
	}

	if err := ensureOwnedByCurrentUser(dir, info); err != nil {
		return err
	}

	return nil
}

// DefaultSocketPath returns the unix socket path inside DefaultRuntimeDir.
func DefaultSocketPath() string {
	return filepath.Join(DefaultRuntimeDir(), "a2text-voice.sock")
}

// DefaultLockPath returns the PID lock path inside DefaultRuntimeDir.
func DefaultLockPath() string {
	return filepath.Join(DefaultRuntimeDir(), "a2text-voice.lock")
}
