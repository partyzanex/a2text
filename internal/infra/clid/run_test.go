package clid

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/partyzanex/a2text/internal/infra/config"
	a2textv1 "github.com/partyzanex/a2text/pkg/proto/a2text/v1"
)

func TestMapHotkeyMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   config.VoiceHotkeyMode
		want a2textv1.HotkeyMode
	}{
		{"hold", config.VoiceHotkeyModeHold, a2textv1.HotkeyMode_HOTKEY_MODE_HOLD},
		{"toggle", config.VoiceHotkeyModeToggle, a2textv1.HotkeyMode_HOTKEY_MODE_TOGGLE},
		{"unknown defaults to toggle", "anything-else", a2textv1.HotkeyMode_HOTKEY_MODE_TOGGLE},
		{"empty defaults to toggle", "", a2textv1.HotkeyMode_HOTKEY_MODE_TOGGLE},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, mapHotkeyMode(tc.in))
		})
	}
}

func TestMapInjectMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		in       string
		want     a2textv1.InjectMode
		wantWarn bool
	}{
		{"autopaste", config.VoiceOutputModeClipboardAutopaste, a2textv1.InjectMode_INJECT_MODE_PASTE, false},
		{"clipboard", config.VoiceOutputModeClipboard, a2textv1.InjectMode_INJECT_MODE_CLIPBOARD, false},
		{"stdout (legacy)", config.VoiceOutputModeStdout, a2textv1.InjectMode_INJECT_MODE_CLIPBOARD, false},
		{"unknown warns", "weird-mode", a2textv1.InjectMode_INJECT_MODE_CLIPBOARD, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var buf bytes.Buffer

			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

			got := mapInjectMode(tc.in, log)
			require.Equal(t, tc.want, got)

			gotWarn := strings.Contains(buf.String(), "unknown output mode")
			require.Equal(t, tc.wantWarn, gotWarn,
				"warn-log expectation mismatch; log=%q", buf.String())
		})
	}
}

func TestEnsureParentDir_CreatesMissingDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "a2text", "secrets.json")

	require.NoError(t, ensureParentDir(target, 0o700))

	info, err := os.Stat(filepath.Dir(target))
	require.NoError(t, err)
	require.True(t, info.IsDir())
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestEnsureParentDir_TightensExisting(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	parent := filepath.Join(root, "wide")
	require.NoError(t, os.MkdirAll(parent, 0o755))

	target := filepath.Join(parent, "secrets.json")
	require.NoError(t, ensureParentDir(target, 0o700))

	info, err := os.Stat(parent)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm(),
		"parent must be chmod'd down to requested mode")
}

func TestEnsureParentDir_KeepsStricterExisting(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	parent := filepath.Join(root, "tight")
	require.NoError(t, os.MkdirAll(parent, 0o700))

	require.NoError(t, ensureParentDir(filepath.Join(parent, "x"), 0o755))

	info, err := os.Stat(parent)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm(),
		"existing tighter mode must be preserved")
}

func TestEnsureParentDir_RefusesNonDirParent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	parent := filepath.Join(root, "not-a-dir")
	require.NoError(t, os.WriteFile(parent, []byte("regular file"), 0o600))

	err := ensureParentDir(filepath.Join(parent, "x"), 0o700)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a directory")
}

func TestTightenDirMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		startMode   os.FileMode
		want        os.FileMode
		expectChmod bool
	}{
		{"already strict", 0o700, 0o700, false},
		{"stricter than want", 0o500, 0o500, false}, // preserve operator's stricter choice
		{"broader than want", 0o755, 0o700, true},
		{"world-writable broader", 0o777, 0o700, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dir := filepath.Join(t.TempDir(), "d")
			require.NoError(t, os.Mkdir(dir, tc.startMode))

			require.NoError(t, tightenDirMode(dir, tc.startMode, 0o700))

			info, err := os.Stat(dir)
			require.NoError(t, err)

			got := info.Mode().Perm()

			if tc.expectChmod {
				require.Equal(t, tc.want, got, "should tighten")
			} else {
				require.Equal(t, tc.startMode, got, "should preserve")
			}
		})
	}
}

func TestResolveSecretsPath_FlagWins(t *testing.T) {
	t.Parallel()

	override := filepath.Join(t.TempDir(), "custom", "secrets.json")

	got, err := resolveSecretsPath(override)
	require.NoError(t, err)
	require.Equal(t, override, got)

	info, err := os.Stat(filepath.Dir(override))
	require.NoError(t, err)
	require.True(t, info.IsDir())
}

func TestResolveSecretsPath_FallsBackToXDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)

	got, err := resolveSecretsPath("")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dir, "a2text", "secrets.json"), got)
}
