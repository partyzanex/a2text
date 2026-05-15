package sysd

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

// ErrDaemonAlreadyRunning is returned by AcquireDaemonLock when another
// daemon already holds the PID lock. The bootstrap path uses this to
// re-attempt an IPC ping (a parallel daemon may have just won the race)
// before giving up.
var ErrDaemonAlreadyRunning = errors.New("cmd: daemon already running")

// DaemonLock is the exclusive PID-file lock that prevents two daemons
// from binding the same socket simultaneously. Hold it for the lifetime
// of the daemon and Release on shutdown.
//
// The struct is cross-platform; the acquisition path lives in
// lock_linux.go (uses flock + syscall.Stat_t for owner verification) and
// is stubbed on non-Linux in lock_other.go. Release is portable.
type DaemonLock struct {
	file *os.File
	mu   sync.Mutex
}

// Release unlocks the file and closes the descriptor. Idempotent and safe
// for concurrent callers: the inner mutex guarantees we transfer the
// file handle to a single goroutine, so a second (or parallel) Release
// observes nil and no-ops.
//
// We deliberately do NOT remove the lock file: a stale 0-byte PID file
// is harmless on next start (truncated and reused), and removing it can
// race with a brand-new daemon process that just opened it.
func (l *DaemonLock) Release() error {
	l.mu.Lock()
	file := l.file
	l.file = nil
	l.mu.Unlock()

	if file == nil {
		return nil
	}

	releaseFlock(file) // platform-specific best-effort unlock

	if closeErr := file.Close(); closeErr != nil {
		return fmt.Errorf("release daemon lock: close: %w", closeErr)
	}

	return nil
}
