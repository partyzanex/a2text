//go:build !linux

package sysd

import (
	"errors"
	"os"
)

// ErrLockUnsupportedOS is returned by AcquireDaemonLock on non-Linux
// platforms. The voice daemon uses syscall.Flock + syscall.Stat_t for
// owner verification; both are Linux-only in this codebase. The stub
// keeps the type-name resolvable so cross-package references compile
// cleanly under GOOS=darwin/windows for editor tooling.
var ErrLockUnsupportedOS = errors.New("cmd: daemon lock is only implemented on Linux")

// AcquireDaemonLock is a non-functional stub on non-Linux platforms.
func AcquireDaemonLock(_ string) (*DaemonLock, error) {
	return nil, ErrLockUnsupportedOS
}

// releaseFlock is a no-op on non-Linux platforms — Release still closes
// the file descriptor afterwards.
func releaseFlock(_ *os.File) {}
