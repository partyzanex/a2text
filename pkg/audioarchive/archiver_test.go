package audioarchive_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/a2text/pkg/audioarchive"
)

// fakeTranscoder records the args it received and either succeeds by
// writing a stub OGG payload or returns the configured error.
type fakeTranscoder struct {
	gotSrc, gotDst string
	gotFormat      audioarchive.Format
	called         int
	err            error
}

func (f *fakeTranscoder) Encode(_ context.Context, src, dst string, format audioarchive.Format) error {
	f.called++
	f.gotSrc = src
	f.gotDst = dst
	f.gotFormat = format

	if f.err != nil {
		return f.err
	}

	// Write a deterministic stub payload so the rename step has
	// something real to move.
	return os.WriteFile(dst, []byte("OggS-stub"), 0o600)
}

func writeFixtureWAV(t *testing.T, dir string) string {
	t.Helper()

	path := filepath.Join(dir, "input.wav")
	require.NoError(t, os.WriteFile(path, []byte("RIFF....WAVEfmt "), 0o600))

	return path
}

func TestArchive_WAV_CopiesBytesIntoTimestampedFile(t *testing.T) {
	t.Parallel()

	src := writeFixtureWAV(t, t.TempDir())

	destDir := filepath.Join(t.TempDir(), "recordings")
	arch := audioarchive.NewArchiver(nil)

	path, err := arch.Archive(context.Background(), src, destDir, audioarchive.FormatWAV)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(filepath.Base(path), "a2text-"))
	require.True(t, strings.HasSuffix(path, ".wav"))

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "RIFF....WAVEfmt ", string(got))
}

func TestArchive_OGG_CallsTranscoderAndRenames(t *testing.T) {
	t.Parallel()

	src := writeFixtureWAV(t, t.TempDir())
	destDir := filepath.Join(t.TempDir(), "recordings")

	stub := &fakeTranscoder{}
	arch := audioarchive.NewArchiver(stub)

	path, err := arch.Archive(context.Background(), src, destDir, audioarchive.FormatOGG)
	require.NoError(t, err)
	require.Equal(t, 1, stub.called)
	require.Equal(t, src, stub.gotSrc)
	require.True(t, strings.HasSuffix(path, ".ogg"))
	require.Equal(t, audioarchive.FormatOGG, stub.gotFormat)

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "OggS-stub", string(body))
}

func TestArchive_OGG_WithoutTranscoder_Errors(t *testing.T) {
	t.Parallel()

	src := writeFixtureWAV(t, t.TempDir())

	arch := audioarchive.NewArchiver(nil)

	_, err := arch.Archive(context.Background(), src, t.TempDir(), audioarchive.FormatOGG)
	require.ErrorContains(t, err, "no transcoder")
}

func TestArchive_OGG_TranscoderFailure_CleansPartial(t *testing.T) {
	t.Parallel()

	src := writeFixtureWAV(t, t.TempDir())
	destDir := t.TempDir()

	stub := &fakeTranscoder{err: errors.New("ffmpeg exit 1")}
	arch := audioarchive.NewArchiver(stub)

	_, err := arch.Archive(context.Background(), src, destDir, audioarchive.FormatOGG)
	require.Error(t, err)

	entries, listErr := os.ReadDir(destDir)
	require.NoError(t, listErr)
	require.Empty(t, entries, "partial file must be cleaned up on transcoder failure")
}

func TestArchive_EmptyFormatDefaultsToWAV(t *testing.T) {
	t.Parallel()

	src := writeFixtureWAV(t, t.TempDir())
	destDir := t.TempDir()

	path, err := audioarchive.NewArchiver(nil).Archive(
		context.Background(), src, destDir, "",
	)
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(path, ".wav"))
}

func TestArchive_RejectsEmptyArgs(t *testing.T) {
	t.Parallel()

	arch := audioarchive.NewArchiver(nil)

	_, err := arch.Archive(context.Background(), "", t.TempDir(), audioarchive.FormatWAV)
	require.Error(t, err)

	_, err = arch.Archive(context.Background(), "/some/path.wav", "", audioarchive.FormatWAV)
	require.Error(t, err)
}

func TestArchive_CreatesDestDirIfMissing(t *testing.T) {
	t.Parallel()

	src := writeFixtureWAV(t, t.TempDir())
	destDir := filepath.Join(t.TempDir(), "deep", "nested", "recordings")

	_, err := audioarchive.NewArchiver(nil).Archive(
		context.Background(), src, destDir, audioarchive.FormatWAV,
	)
	require.NoError(t, err)

	info, err := os.Stat(destDir)
	require.NoError(t, err)
	require.True(t, info.IsDir())
}

func TestArchive_TimestampedNamesAreUnique(t *testing.T) {
	t.Parallel()

	// Pin time.Now to two distinct seconds so the two archives don't
	// collide even at sub-second test speed.
	arch := audioarchive.NewArchiver(nil)

	src := writeFixtureWAV(t, t.TempDir())
	destDir := t.TempDir()

	p1, err := arch.Archive(context.Background(), src, destDir, audioarchive.FormatWAV)
	require.NoError(t, err)

	// Bump by one second so the timestamp differs deterministically.
	time.Sleep(1100 * time.Millisecond)

	p2, err := arch.Archive(context.Background(), src, destDir, audioarchive.FormatWAV)
	require.NoError(t, err)

	require.NotEqual(t, p1, p2)
}
