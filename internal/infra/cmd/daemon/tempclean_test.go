package daemon_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	daemon "github.com/partyzanex/a2text/internal/infra/cmd/daemon"
)

type TempCleanSuite struct {
	suite.Suite

	log *slog.Logger
}

func TestTempCleanSuite(t *testing.T) {
	suite.Run(t, new(TempCleanSuite))
}

func (s *TempCleanSuite) SetupTest() {
	s.log = slog.New(slog.DiscardHandler)
}

// --- MakeSessionDir ---

func (s *TempCleanSuite) TestMakeSessionDir_Creates0700() {
	base := s.T().TempDir()

	dir, err := daemon.MakeSessionDir(base)
	s.Require().NoError(err)
	s.Require().NotEmpty(dir)
	s.DirExists(dir)
	s.Contains(filepath.Base(dir), "a2text-")

	info, err := os.Stat(dir)
	s.Require().NoError(err)
	s.Equal(os.FileMode(0o700), info.Mode().Perm())
}

func (s *TempCleanSuite) TestMakeSessionDir_EmptyBase_FallsBackToOsTempDir() {
	// Fallback to os.TempDir() is an intentional safety net when no cfg.TempDir
	// is set. The created dir is 0700 (owner-only), so it is safe even in
	// shared /tmp. A caller that cares about isolation should always pass an
	// explicit baseDir (e.g. a runtime-dir under /run/user/<uid>).
	dir, err := daemon.MakeSessionDir("")
	s.Require().NoError(err)
	s.DirExists(dir)

	info, statErr := os.Stat(dir)
	s.Require().NoError(statErr)
	s.Equal(os.FileMode(0o700), info.Mode().Perm(), "fallback dir must still be 0700")

	// Cleanup so we don't leave a stray dir in os.TempDir.
	_ = os.RemoveAll(dir)
}

// --- CleanupSession ---

func (s *TempCleanSuite) TestCleanupSession_EmptyDir_NoPanic() {
	s.NotPanics(func() {
		daemon.CleanupSession("", false, nil, nil)
	})
}

func (s *TempCleanSuite) TestCleanupSession_NilLog_NoPanic() {
	base := s.T().TempDir()
	dir, err := daemon.MakeSessionDir(base)
	s.Require().NoError(err)

	s.NotPanics(func() {
		daemon.CleanupSession(dir, false, nil, nil)
	})
}

func (s *TempCleanSuite) TestCleanupSession_RemovesDir() {
	base := s.T().TempDir()
	dir, err := daemon.MakeSessionDir(base)
	s.Require().NoError(err)

	daemon.CleanupSession(dir, false, s.log, nil)

	s.NoDirExists(dir)
}

func (s *TempCleanSuite) TestCleanupSession_KeepAudio_DoesNotRemoveDir() {
	base := s.T().TempDir()
	dir, err := daemon.MakeSessionDir(base)
	s.Require().NoError(err)

	defer func() { _ = os.RemoveAll(dir) }()

	var buf bytes.Buffer

	daemon.CleanupSession(dir, true, s.log, &buf)

	s.DirExists(dir)
	s.Contains(buf.String(), "saved audio:")
	s.Contains(buf.String(), dir)
}

func (s *TempCleanSuite) TestCleanupSession_NonA2textPrefix_DirNoRemove() {
	base := s.T().TempDir()

	// Create a directory without the a2text- prefix.
	other := filepath.Join(base, "other-app-dir")
	s.Require().NoError(os.MkdirAll(other, 0o700))

	daemon.CleanupSession(other, false, s.log, nil)

	s.DirExists(other, "non-a2text- dir must not be removed")
}

func (s *TempCleanSuite) TestCleanupSession_NonA2textPrefix_FileNoRemove() {
	base := s.T().TempDir()

	// A regular file — CleanupSession must not treat it as a session dir.
	file := filepath.Join(base, "other-file.txt")
	s.Require().NoError(os.WriteFile(file, []byte("x"), 0o600))

	daemon.CleanupSession(file, false, s.log, nil)

	s.FileExists(file, "non-a2text- file must not be removed")
}

// --- WithSessionDir ---

func (s *TempCleanSuite) TestWithSessionDir_CleanupOnSuccess() {
	base := s.T().TempDir()

	var captured string

	err := daemon.WithSessionDir(base, false, s.log, nil, func(dir string) error {
		captured = dir
		s.DirExists(dir)

		return nil
	})

	s.Require().NoError(err)
	s.NoDirExists(captured)
}

func (s *TempCleanSuite) TestWithSessionDir_CleanupOnError() {
	base := s.T().TempDir()

	var captured string

	sentinel := errors.New("pipeline failed")

	err := daemon.WithSessionDir(base, false, s.log, nil, func(dir string) error {
		captured = dir

		return sentinel
	})

	s.Require().ErrorIs(err, sentinel)
	s.NoDirExists(captured)
}

func (s *TempCleanSuite) TestWithSessionDir_CleanupOnCancel() {
	base := s.T().TempDir()

	var captured string

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := daemon.WithSessionDir(base, false, s.log, nil, func(dir string) error {
		captured = dir

		return ctx.Err()
	})

	s.Require().ErrorIs(err, context.Canceled)
	s.NoDirExists(captured)
}

func (s *TempCleanSuite) TestWithSessionDir_CleanupOnPanic() {
	base := s.T().TempDir()

	var captured string

	s.Panics(func() {
		_ = daemon.WithSessionDir(base, false, s.log, nil, func(dir string) error {
			captured = dir

			panic("test pipeline panic")
		})
	})

	s.NoDirExists(captured)
}

func (s *TempCleanSuite) TestWithSessionDir_KeepAudio_PreservesOnSuccess() {
	base := s.T().TempDir()

	var (
		captured string
		buf      bytes.Buffer
	)

	err := daemon.WithSessionDir(base, true, s.log, &buf, func(dir string) error {
		captured = dir

		return nil
	})

	s.Require().NoError(err)
	s.DirExists(captured)
	s.Contains(buf.String(), "saved audio:")

	_ = os.RemoveAll(captured)
}

func (s *TempCleanSuite) TestWithSessionDir_KeepAudio_PreservesOnError() {
	base := s.T().TempDir()

	var captured string

	err := daemon.WithSessionDir(base, true, s.log, nil, func(dir string) error {
		captured = dir

		return errors.New("pipeline failed")
	})

	s.Require().Error(err)
	s.DirExists(captured, "keepAudio=true must preserve dir even on error")
	_ = os.RemoveAll(captured)
}

// --- CleanOrphanDirs ---

func (s *TempCleanSuite) TestCleanOrphanDirs_NilLog_NoPanic() {
	base := s.T().TempDir()

	s.NotPanics(func() {
		daemon.CleanOrphanDirs(base, nil)
	})
}

func (s *TempCleanSuite) TestCleanOrphanDirs_EmptyBaseDir_NoPanic() {
	// Fallback to os.TempDir(); must not panic and must not error.
	s.NotPanics(func() {
		daemon.CleanOrphanDirs("", s.log)
	})
}

func (s *TempCleanSuite) TestCleanOrphanDirs_RemovesOldDirs() {
	base := s.T().TempDir()

	dir := filepath.Join(base, "a2text-orphan")
	s.Require().NoError(os.MkdirAll(dir, 0o700))

	old := time.Now().Add(-25 * time.Hour)
	s.Require().NoError(os.Chtimes(dir, old, old))

	daemon.CleanOrphanDirs(base, s.log)

	s.NoDirExists(dir)
}

func (s *TempCleanSuite) TestCleanOrphanDirs_SkipsNewDirs() {
	base := s.T().TempDir()

	dir := filepath.Join(base, "a2text-fresh")
	s.Require().NoError(os.MkdirAll(dir, 0o700))
	// mtime is now — well within orphanMaxAge (24h).

	daemon.CleanOrphanDirs(base, s.log)

	s.DirExists(dir)
}

func (s *TempCleanSuite) TestCleanOrphanDirs_SkipsNonA2textPrefixOldDirs() {
	base := s.T().TempDir()

	other := filepath.Join(base, "other-old")
	s.Require().NoError(os.MkdirAll(other, 0o700))

	old := time.Now().Add(-25 * time.Hour)
	s.Require().NoError(os.Chtimes(other, old, old))

	daemon.CleanOrphanDirs(base, s.log)

	s.DirExists(other, "non-a2text- dirs must never be removed")
}

func (s *TempCleanSuite) TestCleanOrphanDirs_SkipsWorldWritableDirs() {
	base := s.T().TempDir()

	// A dir with loose permissions — not created by MakeSessionDir (which
	// always uses 0700). CleanOrphanDirs must skip it.
	loose := filepath.Join(base, "a2text-loose")
	s.Require().NoError(os.MkdirAll(loose, 0o750))

	old := time.Now().Add(-25 * time.Hour)
	s.Require().NoError(os.Chtimes(loose, old, old))

	daemon.CleanOrphanDirs(base, s.log)

	s.DirExists(loose, "world-writable a2text- dir must not be removed")
}

func (s *TempCleanSuite) TestCleanOrphanDirs_SkipsSymlinks() {
	base := s.T().TempDir()

	// Create a real directory and a symlink with the a2text- prefix.
	target := filepath.Join(base, "real-dir")
	s.Require().NoError(os.MkdirAll(target, 0o700))

	link := filepath.Join(base, "a2text-symlink")
	s.Require().NoError(os.Symlink(target, link))

	// CleanOrphanDirs uses Lstat: the link appears as ModeSymlink (not IsDir()),
	// so it must be skipped regardless of age — no need to backdate.
	daemon.CleanOrphanDirs(base, s.log)

	_, lstatErr := os.Lstat(link)
	s.Require().NoError(lstatErr, "symlink must not be removed by CleanOrphanDirs")
	s.DirExists(target)
}
