//go:build linux

package sysd

import (
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/suite"
)

type DaemonLockSuite struct {
	suite.Suite
}

func TestDaemonLockSuite(t *testing.T) {
	suite.Run(t, new(DaemonLockSuite))
}

func (s *DaemonLockSuite) TestAcquire_EmptyPath_Errors() {
	_, err := AcquireDaemonLock("")
	s.Require().Error(err)
	s.Contains(err.Error(), "empty lock path")
}

func (s *DaemonLockSuite) TestAcquire_ParentDirMissing_Errors() {
	_, err := AcquireDaemonLock(filepath.Join(s.T().TempDir(), "missing", "lock"))
	s.Require().Error(err)
	s.Contains(err.Error(), "stat runtime dir")
}

func (s *DaemonLockSuite) TestAcquire_ParentIsRegularFile_Errors() {
	// Make a regular file at the path the parent-dir check will Stat. The
	// IsDir() guard in verifyParentDirIsPrivate must reject it.
	parent := filepath.Join(s.T().TempDir(), "not-a-dir")
	s.Require().NoError(os.WriteFile(parent, []byte("file"), 0o600))

	_, err := AcquireDaemonLock(filepath.Join(parent, "lock"))
	s.Require().Error(err)
	// On Linux, Stat'ing a path under a regular-file parent returns ENOTDIR
	// before our IsDir branch fires. Either error path is fine; we just
	// want to confirm Acquire refuses to touch it.
	s.NotEmpty(err.Error())
}

func (s *DaemonLockSuite) TestAcquire_ParentDirWrongPerms_Errors() {
	dir := filepath.Join(s.T().TempDir(), "loose")
	s.Require().NoError(os.Mkdir(dir, 0o750))

	_, err := AcquireDaemonLock(filepath.Join(dir, "lock"))
	s.Require().Error(err)
	s.Contains(err.Error(), "unsafe runtime dir permissions")
}

func (s *DaemonLockSuite) TestAcquire_HappyPath_WritesPID() {
	dir := s.runtimeDir()
	path := filepath.Join(dir, "a2text.lock")

	lock, err := AcquireDaemonLock(path)
	s.Require().NoError(err)
	s.Require().NotNil(lock)

	defer func() { _ = lock.Release() }()

	contents, readErr := os.ReadFile(path)
	s.Require().NoError(readErr)
	s.Equal(strconv.Itoa(os.Getpid())+"\n", string(contents),
		"lock file must contain exactly pid + newline")
}

func (s *DaemonLockSuite) TestAcquire_SecondCaller_GetsErrDaemonAlreadyRunning() {
	dir := s.runtimeDir()
	path := filepath.Join(dir, "a2text.lock")

	lock, err := AcquireDaemonLock(path)
	s.Require().NoError(err)

	defer func() { _ = lock.Release() }()

	_, err = AcquireDaemonLock(path)
	s.Require().Error(err)
	s.Require().ErrorIs(err, ErrDaemonAlreadyRunning)
}

func (s *DaemonLockSuite) TestRelease_LeavesFileOnDisk() {
	dir := s.runtimeDir()
	path := filepath.Join(dir, "a2text.lock")

	lock, err := AcquireDaemonLock(path)
	s.Require().NoError(err)

	s.Require().NoError(lock.Release())

	_, statErr := os.Stat(path)
	s.Require().NoError(statErr, "Release must NOT remove the lock file (race with new daemon)")
}

func (s *DaemonLockSuite) TestRelease_DoubleCall_NoError() {
	dir := s.runtimeDir()
	path := filepath.Join(dir, "a2text.lock")

	lock, err := AcquireDaemonLock(path)
	s.Require().NoError(err)

	s.Require().NoError(lock.Release())
	s.Require().NoError(lock.Release(), "second Release must be a safe no-op")
}

func (s *DaemonLockSuite) TestRelease_ConcurrentCallers_OneSucceedsOthersNoOp() {
	dir := s.runtimeDir()
	path := filepath.Join(dir, "a2text.lock")

	lock, err := AcquireDaemonLock(path)
	s.Require().NoError(err)

	const goroutines = 20

	var (
		wg       sync.WaitGroup
		errCount int
		errMu    sync.Mutex
	)

	for range goroutines {
		wg.Go(func() {
			if releaseErr := lock.Release(); releaseErr != nil {
				errMu.Lock()
				errCount++
				errMu.Unlock()
			}
		})
	}

	wg.Wait()

	s.Equal(0, errCount, "no Release call should report an error under concurrency")
}

func (s *DaemonLockSuite) TestAcquire_ReuseAfterRelease_Works() {
	dir := s.runtimeDir()
	path := filepath.Join(dir, "a2text.lock")

	first, err := AcquireDaemonLock(path)
	s.Require().NoError(err)
	s.Require().NoError(first.Release())

	second, err := AcquireDaemonLock(path)
	s.Require().NoError(err)

	defer func() { _ = second.Release() }()

	// Second acquire should succeed against the leftover file.
	s.NotNil(second)
}

// --- Helpers ---

// runtimeDir creates a fresh dir with the same permissions
// EnsureRuntimeDir would, so AcquireDaemonLock's parent-dir verification
// passes. Tests cannot share the suite-wide tempdir because t.TempDir()
// returns a 0700-ish dir but not necessarily exactly 0700 (depends on
// umask inheritance) — we re-create explicitly.
func (s *DaemonLockSuite) runtimeDir() string {
	dir := filepath.Join(s.T().TempDir(), "rt")
	s.Require().NoError(os.Mkdir(dir, 0o700))
	s.Require().NoError(os.Chmod(dir, 0o700))

	return dir
}
