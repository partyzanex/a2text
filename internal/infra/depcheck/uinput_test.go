package depcheck

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCheckUinputAccess_DeviceMissing reports a clear "kernel module not
// loaded" hint when /dev/uinput cannot be stat'ed.
func TestCheckUinputAccess_DeviceMissing(t *testing.T) {
	env := Env{
		StatFile: func(_ string) (os.FileInfo, error) {
			return nil, errors.New("no such file")
		},
	}

	res := checkUinputAccess(env)

	assert.False(t, res.Found)
	assert.Contains(t, res.Detail, "/dev/uinput missing")
	assert.Contains(t, res.Detail, "kernel uinput module")
}

// TestCheckUinputAccess_DeviceWritable reports success when /dev/uinput is
// readable AND writable by the current process. Skipped when the test host
// does not actually allow uinput writes (most CI machines).
func TestCheckUinputAccess_DeviceWritable(t *testing.T) {
	if _, err := os.Stat("/dev/uinput"); err != nil {
		t.Skip("/dev/uinput unavailable on this host")
	}

	if fd, err := os.OpenFile("/dev/uinput", os.O_WRONLY, 0); err != nil {
		t.Skip("/dev/uinput not writable on this host: " + err.Error())
	} else {
		_ = fd.Close()
	}

	env := Env{
		StatFile: os.Stat,
	}

	res := checkUinputAccess(env)

	assert.True(t, res.Found)
	assert.Contains(t, res.Detail, "writable")
}

// TestUserGroupNames_ReturnsNonEmpty verifies that the helper resolves at
// least one group (the user's primary group) on a normal Linux host. Skipped
// if the OS user lookup fails (containerised builds with no /etc/passwd).
func TestUserGroupNames_ReturnsNonEmpty(t *testing.T) {
	groups, err := userGroupNames()
	if err != nil {
		t.Skip("user lookup not available in this environment: " + err.Error())
	}

	assert.NotEmpty(t, groups, "current user must belong to at least one resolved group")

	for _, g := range groups {
		assert.NotEmpty(t, strings.TrimSpace(g), "group name must not be blank")
	}
}
