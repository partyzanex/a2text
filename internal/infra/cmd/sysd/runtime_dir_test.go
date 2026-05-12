//go:build linux

package sysd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/suite"
)

type RuntimeDirSuite struct {
	suite.Suite
}

func TestRuntimeDirSuite(t *testing.T) {
	suite.Run(t, new(RuntimeDirSuite))
}

func (s *RuntimeDirSuite) TestDefaultRuntimeDir_XDGSet_UsesXDGSubdir() {
	xdg := s.T().TempDir()
	s.T().Setenv("XDG_RUNTIME_DIR", xdg)

	got := DefaultRuntimeDir()
	s.Equal(filepath.Join(xdg, "a2text"), got)
}

func (s *RuntimeDirSuite) TestDefaultRuntimeDir_XDGEmpty_FallsBackToTmpWithUID() {
	s.T().Setenv("XDG_RUNTIME_DIR", "")

	got := DefaultRuntimeDir()
	s.True(strings.HasSuffix(got, "a2text-voice-"+strconv.Itoa(os.Getuid())),
		"fallback path must include $UID, got %q", got)
	s.Equal(os.TempDir(), filepath.Dir(got),
		"fallback path must be an immediate child of os.TempDir, got %q (TempDir=%q)", got, os.TempDir())
}

func (s *RuntimeDirSuite) TestDefaultSocketAndLockPath_LiveInsideRuntimeDir() {
	xdg := s.T().TempDir()
	s.T().Setenv("XDG_RUNTIME_DIR", xdg)

	root := DefaultRuntimeDir()
	s.Equal(root, filepath.Dir(DefaultSocketPath()))
	s.Equal(root, filepath.Dir(DefaultLockPath()))
}

func (s *RuntimeDirSuite) TestEnsureRuntimeDir_HappyPath_CreatesPrivateDir() {
	xdg := s.T().TempDir()
	s.T().Setenv("XDG_RUNTIME_DIR", xdg)

	s.Require().NoError(EnsureRuntimeDir())

	info, err := os.Stat(DefaultRuntimeDir())
	s.Require().NoError(err)
	s.True(info.IsDir())
	s.Equal(runtimeDirPerm, info.Mode().Perm())
}

func (s *RuntimeDirSuite) TestEnsureRuntimeDir_LooseExistingPerms_FailsWithChmodHint() {
	xdg := s.T().TempDir()
	s.T().Setenv("XDG_RUNTIME_DIR", xdg)

	dir := DefaultRuntimeDir()
	s.Require().NoError(os.Mkdir(dir, 0o750))
	// Re-chmod to defeat any umask interference between Mkdir + the Stat inside EnsureRuntimeDir.
	s.Require().NoError(os.Chmod(dir, 0o755))

	err := EnsureRuntimeDir()
	s.Require().Error(err)
	s.Contains(err.Error(), "unsafe runtime dir permissions")
	s.Contains(err.Error(), "chmod 700",
		"error must guide the user toward a manual fix instead of leaving them stranded")
}

func (s *RuntimeDirSuite) TestEnsureRuntimeDir_PathExistsAsFile_Errors() {
	xdg := s.T().TempDir()
	s.T().Setenv("XDG_RUNTIME_DIR", xdg)

	// Pre-create the runtime dir target as a regular file. MkdirAll then
	// fails because it cannot create a directory where a file lives.
	target := DefaultRuntimeDir()
	s.Require().NoError(os.WriteFile(target, []byte("squatter"), 0o600))

	err := EnsureRuntimeDir()
	s.Require().Error(err)
	// MkdirAll on a path that exists as a regular file returns ENOTDIR,
	// which we wrap as "create runtime dir". On exotic filesystems Stat may
	// succeed and the IsDir branch fires instead — accept either, but pin
	// the message so a regression to a generic "stat failed" gets caught.
	msg := err.Error()
	s.True(
		strings.Contains(msg, "create runtime dir") ||
			strings.Contains(msg, "is not a directory"),
		"unexpected error message: %q", msg,
	)
}

func (s *RuntimeDirSuite) TestEnsureRuntimeDir_Owner_HappyPath() {
	xdg := s.T().TempDir()
	s.T().Setenv("XDG_RUNTIME_DIR", xdg)

	// First call creates + verifies; second call is the steady-state idempotent path.
	s.Require().NoError(EnsureRuntimeDir())
	s.Require().NoError(EnsureRuntimeDir())
}
