package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDecideHotkeyRegOp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		key            string
		builtinActive  bool
		unsetupOnEmpty bool
		want           hotkeyRegOp
	}{
		{
			name:           "initial register, empty key → noop",
			key:            "",
			builtinActive:  false,
			unsetupOnEmpty: false,
			want:           hotkeyRegOpNoop,
		},
		{
			name:           "initial register, empty key, evdev active → noop",
			key:            "",
			builtinActive:  true,
			unsetupOnEmpty: false,
			want:           hotkeyRegOpNoop,
		},
		{
			name:           "reload, empty key → unsetup",
			key:            "",
			builtinActive:  false,
			unsetupOnEmpty: true,
			want:           hotkeyRegOpUnsetup,
		},
		{
			name:           "reload, empty key, evdev active → unsetup",
			key:            "",
			builtinActive:  true,
			unsetupOnEmpty: true,
			want:           hotkeyRegOpUnsetup,
		},
		{
			name:           "initial register, key set, no builtin → setup",
			key:            "F4",
			builtinActive:  false,
			unsetupOnEmpty: false,
			want:           hotkeyRegOpSetup,
		},
		{
			name:           "reload, key set, no builtin → setup",
			key:            "F4",
			builtinActive:  false,
			unsetupOnEmpty: true,
			want:           hotkeyRegOpSetup,
		},
		{
			name:           "initial register, key set, builtin active → unsetup (regression fix)",
			key:            "F4",
			builtinActive:  true,
			unsetupOnEmpty: false,
			want:           hotkeyRegOpUnsetup,
		},
		{
			name:           "reload, key set, builtin active → unsetup (regression fix)",
			key:            "F4",
			builtinActive:  true,
			unsetupOnEmpty: true,
			want:           hotkeyRegOpUnsetup,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := decideHotkeyRegOp(tc.key, tc.builtinActive, tc.unsetupOnEmpty)
			assert.Equal(t, tc.want, got)
		})
	}
}
