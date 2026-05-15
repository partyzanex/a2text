//go:build linux

package sysd

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
)

// currentUserID returns the numeric POSIX uid as a decimal string. Linux
// guarantees os.Getuid() yields a real uid; the helper exists so that
// non-Linux builds can substitute a portable identifier (e.g. user.Current()
// username on windows where Getuid() returns -1).
func currentUserID() string {
	return strconv.Itoa(os.Getuid())
}

// ensureOwnedByCurrentUser refuses to use a directory owned by anyone else.
// Important on the /tmp fallback path where another user could have
// pre-created /tmp/a2text-voice-$YOUR_UID/ with permissive perms.
func ensureOwnedByCurrentUser(path string, info os.FileInfo) error {
	// On Linux info.Sys() is always *syscall.Stat_t; the build tag guarantees
	// this, so a direct assertion is correct — a panic here would be a bug.
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("runtime dir %q: unexpected file info type", path)
	}

	uidInt := os.Getuid()
	if uidInt < 0 || uidInt > 4294967295 {
		return fmt.Errorf("runtime dir: uid %d out of valid range", uidInt)
	}

	uid := uint32(uidInt)
	if stat.Uid != uid {
		return fmt.Errorf(
			"runtime dir %q is owned by uid %d, expected uid %d — refusing to share",
			path, stat.Uid, uid,
		)
	}

	return nil
}
