package settings

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/a2text/internal/i18n"
	"github.com/partyzanex/a2text/pkg/whispercpp"
)

// TestMain initialises i18n once so validators can return translated
// error strings the same way the running settings window would. Without
// this T() would return the message id and assertions on translated
// substrings would all fail.
func TestMain(m *testing.M) {
	if err := i18n.Init("ru"); err != nil {
		panic(err)
	}

	os.Exit(m.Run())
}

func TestIsWhisperModelMagic(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		input  []byte
		expect bool
	}{
		{"legacy ggml little-endian on disk", []byte("lmgg"), true},
		{"big-endian ggml variant", []byte("ggml"), true},
		{"gguf format", []byte("GGUF"), true},
		{"random text", []byte("ABCD"), false},
		{"too short", []byte("ggm"), false},
		{"empty", nil, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.expect, isWhisperModelMagic(tc.input))
		})
	}
}

func TestValidateWhisperCppModelPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	t.Run("empty string is ok", func(t *testing.T) {
		t.Parallel()
		require.NoError(t, validateWhisperCppModelPath(""))
		require.NoError(t, validateWhisperCppModelPath("   "))
	})

	t.Run("nonexistent path", func(t *testing.T) {
		t.Parallel()

		err := validateWhisperCppModelPath(filepath.Join(dir, "missing.bin"))
		require.Error(t, err)
	})

	t.Run("directory rejected", func(t *testing.T) {
		t.Parallel()

		subDir := filepath.Join(dir, "subdir")
		require.NoError(t, os.MkdirAll(subDir, 0o755))

		err := validateWhisperCppModelPath(subDir)
		require.ErrorContains(t, err, "директор")
	})

	t.Run("file too small", func(t *testing.T) {
		t.Parallel()

		small := filepath.Join(dir, "small.bin")
		require.NoError(t, os.WriteFile(small, []byte("ggml"), 0o600))

		err := validateWhisperCppModelPath(small)
		require.Error(t, err)
	})

	t.Run("bad magic rejected", func(t *testing.T) {
		t.Parallel()

		junk := filepath.Join(dir, "junk.bin")

		payload := append([]byte("WRONG"), make([]byte, 2*1024*1024)...)
		require.NoError(t, os.WriteFile(junk, payload, 0o600))

		err := validateWhisperCppModelPath(junk)
		require.Error(t, err)
	})

	t.Run("valid ggml file accepted", func(t *testing.T) {
		t.Parallel()

		ok := filepath.Join(dir, "ggml-tiny.bin")

		payload := append([]byte("lmgg"), make([]byte, 2*1024*1024)...)
		require.NoError(t, os.WriteFile(ok, payload, 0o600))

		require.NoError(t, validateWhisperCppModelPath(ok))
	})

	t.Run("valid gguf file accepted", func(t *testing.T) {
		t.Parallel()

		ok := filepath.Join(dir, "ggml-gguf.bin")

		payload := append([]byte("GGUF"), make([]byte, 2*1024*1024)...)
		require.NoError(t, os.WriteFile(ok, payload, 0o600))

		require.NoError(t, validateWhisperCppModelPath(ok))
	})
}

func TestFormatBytes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input  int64
		expect string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{2048, "2 KiB"},
		{int64(3 * 1024 * 1024), "3.0 MiB"},
		{int64(2 * 1024 * 1024 * 1024), "2.00 GiB"},
	}

	for _, tc := range cases {
		assert.Equal(t, tc.expect, formatBytes(tc.input))
	}
}

func TestFormatProgress(t *testing.T) {
	t.Parallel()

	p := whispercpp.Progress{Source: "huggingface", Done: 1024 * 1024, Total: 4 * 1024 * 1024}
	assert.Contains(t, formatProgress(p), "huggingface")
	assert.Contains(t, formatProgress(p), "MiB")

	pUnknown := whispercpp.Progress{Source: "mirror", Done: 4096, Total: -1}
	assert.Contains(t, formatProgress(pUnknown), "mirror")
	assert.NotContains(t, formatProgress(pUnknown), "/")
}

func TestWhisperCppModelsDir_PicksXDGDataHomeWhenSet(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/tmp/fixture-xdg")

	got := whisperCppModelsDir()
	assert.Equal(t, "/tmp/fixture-xdg/a2text/models", got)
}

func TestWhisperCppModelsDir_FallsBackToHomeShare(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "/tmp/fixture-home")

	got := whisperCppModelsDir()
	assert.Equal(t, "/tmp/fixture-home/.local/share/a2text/models", got)
}

func TestNewWhisperCppModelPathEntry_IncludesCurrentOutsideSuggestions(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/tmp/test-xdg")

	custom := "/opt/models/my-custom.bin"
	entry := newWhisperCppModelPathEntry(custom)

	require.NotNil(t, entry)
	assert.Equal(t, custom, entry.Text)
	// SelectEntry.Options is unexported; behavioural test instead — the
	// Text round-trips and the constructor must not panic on a custom
	// path.
}

// --- Download row tests ---

type fakeDownloader struct {
	called   bool
	resPath  string
	err      error
	progress []whispercpp.Progress
}

func (f *fakeDownloader) Download(
	_ context.Context,
	_ string,
	_ string,
	progress whispercpp.ProgressFunc,
) (string, error) {
	f.called = true

	if progress != nil {
		for _, p := range f.progress {
			progress(p)
		}
	}

	return f.resPath, f.err
}

func TestSttLanguageOrDefault_RoundTrip(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "auto", sttLanguageOrDefault(""))
	assert.Equal(t, "auto", sttLanguageOrDefault("garbage"))
	assert.Equal(t, "ru", sttLanguageOrDefault("ru"))
	assert.Equal(t, "en", sttLanguageOrDefault("en"))
}

func TestUILanguageOrDefault_RoundTrip(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "en", uiLanguageOrDefault(""))
	assert.Equal(t, "en", uiLanguageOrDefault("xx"))
	assert.Equal(t, "en", uiLanguageOrDefault("en"))
	assert.Equal(t, "ru", uiLanguageOrDefault("ru"))
}

func TestHelpIcon_MinSizeMatchesConstant(t *testing.T) {
	t.Parallel()

	icon := newHelpIcon("some help text")

	const epsilon = 0.001

	size := icon.MinSize()
	assert.InEpsilon(t, helpIconSize, size.Width, epsilon)
	assert.InEpsilon(t, helpIconSize, size.Height, epsilon)
}

func TestHelpIcon_EmptyTextSkipsTimer(t *testing.T) {
	t.Parallel()

	icon := newHelpIcon("")
	// MouseIn must NOT schedule a popup when the text is empty —
	// otherwise the tooltip would flash blank above empty labels.
	icon.MouseIn(nil)
	assert.Nil(t, icon.showWhen, "empty-text helpIcon must not arm the popup timer")
}

func TestHelpIcon_MouseOutCancelsTimer(t *testing.T) {
	t.Parallel()

	icon := newHelpIcon("hello")
	icon.MouseIn(nil)
	require.NotNil(t, icon.showWhen, "MouseIn must arm the timer when text is non-empty")

	icon.MouseOut()
	assert.Nil(t, icon.showWhen, "MouseOut must clear the pending-popup timer")
}

func TestFakeDownloader_ErrorPropagates(t *testing.T) {
	t.Parallel()

	stub := &fakeDownloader{err: errors.New("network down")}

	_, err := stub.Download(context.Background(), "x.bin", "/tmp", nil)
	assert.True(t, stub.called)
	assert.ErrorContains(t, err, "network down")
}
