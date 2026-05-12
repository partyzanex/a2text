//go:build linux

package sysd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

const lockFilePermission = 0o600

// AcquireDaemonLock takes an exclusive flock on the PID file at lockPath,
// writes our PID into it, and returns the handle. The lock is released
// when DaemonLock.Release is called or when the process exits.
//
// AcquireDaemonLock requires the parent directory to already exist with
// 0700 permissions — call EnsureRuntimeDir() first. We re-verify both
// here rather than trusting "the caller said so": the verifier is cheap
// and a misconfigured runtime dir is a security regression we want to
// fail closed on.
//
// Returns ErrDaemonAlreadyRunning if another process holds the lock —
// that's a normal race between concurrent CLI invocations, not an error.
//
//nolint:cyclop // complexity unavoidable: lock setup requires multiple validations
func AcquireDaemonLock(lockPath string) (*DaemonLock, error) {
	if lockPath == "" {
		return nil, errors.New("acquire daemon lock: empty lock path")
	}

	if err := verifyParentDirIsPrivate(lockPath); err != nil {
		return nil, err
	}

	if err := validateLockPath(lockPath); err != nil {
		return nil, fmt.Errorf("acquire daemon lock: %w", err)
	}

	// Safe: path validated by validateLockPath above (absolute, not symlink) and parent dir verified private.
	file, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, lockFilePermission) //nolint:gosec // path validated above
	if err != nil {
		return nil, fmt.Errorf("acquire daemon lock: open %q: %w", lockPath, err)
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeFile(file)

		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrDaemonAlreadyRunning
		}

		return nil, fmt.Errorf("acquire daemon lock: flock: %w", err)
	}

	// We hold the exclusive flock for the remainder of Acquire, so the
	// Truncate→WriteString→Sync sequence is invisible to any other process
	// that calls AcquireDaemonLock concurrently — it will block on flock
	// until we return or release the lock on error.
	if err := file.Truncate(0); err != nil {
		closeAndUnlockFile(file)

		return nil, fmt.Errorf("acquire daemon lock: truncate: %w", err)
	}

	if _, err := file.WriteString(strconv.Itoa(os.Getpid()) + "\n"); err != nil {
		closeAndUnlockFile(file)

		return nil, fmt.Errorf("acquire daemon lock: write pid: %w", err)
	}

	if err := file.Sync(); err != nil {
		closeAndUnlockFile(file)

		return nil, fmt.Errorf("acquire daemon lock: sync: %w", err)
	}

	return &DaemonLock{file: file}, nil
}

func verifyParentDirIsPrivate(lockPath string) error {
	dir := filepath.Dir(lockPath)

	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("acquire daemon lock: stat runtime dir %q: %w", dir, err)
	}

	if !info.IsDir() {
		return fmt.Errorf("acquire daemon lock: runtime path %q is not a directory", dir)
	}

	if info.Mode().Perm() != runtimeDirPerm {
		return fmt.Errorf(
			"acquire daemon lock: unsafe runtime dir permissions %q: got %o, want %o (fix: chmod %o %q)",
			dir, info.Mode().Perm(), runtimeDirPerm, runtimeDirPerm, dir,
		)
	}

	// Owner check: even with 0700 permissions, a directory pre-created by
	// another user with our $UID in the name (think /tmp fallback) is not
	// safe to reuse. os.Stat would normally fail with EACCES on a 0700 dir
	// we don't own, but if for any reason it didn't, surface a typed error
	// rather than trusting permission bits alone.
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("acquire daemon lock: cannot inspect owner of %q (unsupported platform)", dir)
	}

	uidInt := os.Getuid()
	if uidInt < 0 || uidInt > 4294967295 {
		return fmt.Errorf("acquire daemon lock: uid %d out of valid range", uidInt)
	}

	uid := uint32(uidInt)
	if stat.Uid != uid {
		return fmt.Errorf(
			"acquire daemon lock: runtime dir %q owned by uid %d, expected uid %d",
			dir, stat.Uid, uid,
		)
	}

	return nil
}

// validateLockPath ensures the lock path is safe to use.
// It checks that the path is not a symlink and is within safe bounds.
func validateLockPath(lockPath string) error {
	// Check for symlink to prevent symlink attacks.
	fileInfo, err := os.Lstat(lockPath)
	if err == nil && fileInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("lock path is a symlink, rejecting: %q", lockPath)
	}

	// os.OpenFile with O_CREATE will follow symlinks if the file doesn't exist,
	// but Lstat will catch if a malicious symlink was placed there.
	// Additional: verify path is absolute to prevent relative path traversal.
	if !filepath.IsAbs(lockPath) {
		return fmt.Errorf("lock path must be absolute: %q", lockPath)
	}

	return nil
}

// closeFile closes the file without unlocking. Used on error paths where
// flock was never acquired, so there is no lock to release.
func closeFile(file *os.File) {
	closeErr := file.Close()
	_ = closeErr
}

// closeAndUnlockFile explicitly releases the flock before closing.
func closeAndUnlockFile(file *os.File) {
	flockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = flockErr

	closeErr := file.Close()
	_ = closeErr
}

// releaseFlock is the platform hook invoked by DaemonLock.Release.
func releaseFlock(file *os.File) {
	flockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = flockErr
}
