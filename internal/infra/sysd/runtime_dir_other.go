//go:build !linux

package sysd

import (
	"errors"
	"os"
	"os/user"
)

// currentUserID returns a stable per-user identifier suitable for
// embedding in a /tmp fallback directory name. We avoid os.Getuid() here:
// on windows it returns -1, which would yield "a2text-voice--1" — a
// shared, racy path across users. user.Current().Uid is stable on every
// supported OS; if even that fails we fall back to "unknown" so the path
// at least stays well-formed (the daemon stub returns ErrLockUnsupportedOS
// regardless, so the path is never bound to a real socket here).
func currentUserID() string {
	if u, err := user.Current(); err == nil && u.Uid != "" {
		return u.Uid
	}

	return "unknown"
}

// ErrRuntimeDirUnsupportedOS is returned by ensureOwnedByCurrentUser on
// non-Linux platforms. The voice daemon's runtime-dir checks rely on
// syscall.Stat_t which is not portable to Windows; on darwin the type
// exists but the daemon as a whole is Linux-first, so we fail closed.
var ErrRuntimeDirUnsupportedOS = errors.New("cmd: runtime dir verification is only implemented on Linux")

func ensureOwnedByCurrentUser(_ string, _ os.FileInfo) error {
	return ErrRuntimeDirUnsupportedOS
}
