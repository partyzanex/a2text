package settings

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSanitizeDialogPath_ValidPath_PreservesValue verifies that a normal
// absolute path passes through unchanged.
func TestSanitizeDialogPath_ValidPath_PreservesValue(t *testing.T) {
	cases := []string{
		"/home/user/Documents",
		"/tmp",
		"/var/log/a2text",
		"relative/path",
	}

	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			assert.Equal(t, p, sanitizeDialogPath(p))
		})
	}
}

// TestSanitizeDialogPath_TrimsWhitespace verifies leading/trailing spaces
// are stripped while inner content is preserved.
func TestSanitizeDialogPath_TrimsWhitespace(t *testing.T) {
	assert.Equal(t, "/tmp/x", sanitizeDialogPath("  /tmp/x  "))
	assert.Equal(t, "/tmp/x", sanitizeDialogPath("\t/tmp/x\n"))
}

// TestSanitizeDialogPath_EmptyOrWhitespace_FallsBackToHome guards the empty
// and whitespace-only inputs — they must resolve to the user's home, never
// to "" (zenity would treat empty --filename= as flag separator).
func TestSanitizeDialogPath_EmptyOrWhitespace_FallsBackToHome(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	cases := []string{"", "   ", "\t", "\n\n"}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			got := sanitizeDialogPath(p)

			// Home (or "/" fallback) must be non-empty and not start with "-".
			assert.NotEmpty(t, got)
			assert.False(t, strings.HasPrefix(got, "-"))

			if home != "" {
				assert.Equal(t, home, got)
			}
		})
	}
}

// TestSanitizeDialogPath_RejectsLeadingDash defends against flag injection:
// zenity/kdialog interpret values starting with "-" as flags. Function must
// substitute home (or "/") instead.
func TestSanitizeDialogPath_RejectsLeadingDash(t *testing.T) {
	cases := []string{
		"--help",
		"-rf",
		"--file-selection",
		"-h",
	}

	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			got := sanitizeDialogPath(p)
			assert.False(t, strings.HasPrefix(got, "-"),
				"output must not start with '-', got %q for input %q", got, p)
			assert.NotEmpty(t, got)
		})
	}
}
