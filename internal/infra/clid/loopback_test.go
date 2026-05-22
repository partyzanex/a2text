package clid

import (
	"errors"
	"testing"
)

func TestRequireLoopbackAddr(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		addr    string
		wantErr error
	}{
		{"ipv4 loopback explicit port", "127.0.0.1:47834", nil},
		{"ipv4 loopback kernel-assigned port", "127.0.0.1:0", nil},
		{"ipv4 loopback non-canonical octets", "127.0.0.42:1234", nil},
		{"ipv6 loopback", "[::1]:47834", nil},
		{"ipv6 loopback kernel-assigned port", "[::1]:0", nil},

		{"wildcard ipv4 reject", "0.0.0.0:47834", errNonLoopbackBind},
		{"wildcard ipv6 reject", "[::]:47834", errNonLoopbackBind},
		{"non-loopback ipv4 reject", "10.0.0.1:47834", errNonLoopbackBind},
		{"non-loopback ipv6 reject", "[2001:db8::1]:47834", errNonLoopbackBind},
		{"empty host reject", ":47834", errNonLoopbackBind},
		{"hostname reject", "localhost:47834", errNonLoopbackBind},

		{"missing port", "127.0.0.1", errMissingPort},
		{"garbage", "not-an-addr", errMissingPort},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := requireLoopbackAddr(tc.addr)

			switch {
			case tc.wantErr == nil:
				if err != nil {
					t.Fatalf("expected nil, got %v", err)
				}
			case errors.Is(tc.wantErr, errNonLoopbackBind):
				if !errors.Is(err, errNonLoopbackBind) {
					t.Fatalf("expected errNonLoopbackBind, got %v", err)
				}
			case errors.Is(tc.wantErr, errMissingPort):
				// SplitHostPort surfaces a generic parse error; we
				// only need to know it failed without wrapping
				// errNonLoopbackBind (parse failures are not security
				// gating — they always reject).
				if err == nil {
					t.Fatalf("expected parse error, got nil")
				}

				if errors.Is(err, errNonLoopbackBind) {
					t.Fatalf("parse error must not wrap errNonLoopbackBind: %v", err)
				}
			}
		})
	}
}

// errMissingPort is a marker used only inside this test to
// differentiate parse failures from loopback rejections in the
// table above. requireLoopbackAddr does not export the underlying
// net.SplitHostPort error, so we test by class instead.
var errMissingPort = errors.New("missing port test marker")
