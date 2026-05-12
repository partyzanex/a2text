//go:build linux

package hotkey

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/suite"
)

// PortalUnitSuite covers the pure-function half of portal_linux.go: anything
// that does not touch D-Bus. Constructor validation, accelerator formatting,
// handle-token shape, and Response payload parsing live here. The IO-heavy
// half (CreateSession round-trip, signal dispatch) is covered by
// portal_integration_linux_test.go under build tag `integration`.
type PortalUnitSuite struct {
	suite.Suite
}

func TestPortalUnitSuite(t *testing.T) {
	suite.Run(t, new(PortalUnitSuite))
}

// --- constructor validation ---

func (s *PortalUnitSuite) TestNewPortalHotkey_NilHandler_Errors() {
	_, err := NewPortalHotkey(nil, "D", []string{"super"}, nil)
	s.Require().ErrorIs(err, ErrHandlerNil)
}

func (s *PortalUnitSuite) TestNewPortalHotkey_EmptyKey_Errors() {
	_, err := NewPortalHotkey(noopHandler, "", []string{"super"}, nil)
	s.Require().ErrorIs(err, ErrKeyEmpty)
}

func (s *PortalUnitSuite) TestNewPortalHotkey_NilLog_DoesNotPanic() {
	s.NotPanics(func() {
		hk, err := NewPortalHotkey(noopHandler, "D", []string{"super"}, nil)
		s.Require().NoError(err)
		s.Require().NotNil(hk)
	})
}

func (s *PortalUnitSuite) TestNewPortalHotkey_ModifiersDefensivelyCopied() {
	mods := []string{"super", "shift"}

	listener, err := NewPortalHotkey(noopHandler, "D", mods, nil)
	s.Require().NoError(err)

	// Mutate caller's slice after construction.
	mods[0] = "alt"
	mods[1] = "ctrl"

	// The stored slice must be unchanged — defensive copy invariant lets
	// callers safely reuse their config slice without affecting the listener.
	s.Equal([]string{"super", "shift"}, listener.modifiers)
}

// --- formatAccelerator ---

func (s *PortalUnitSuite) TestFormatAccelerator_Table() {
	cases := map[string]struct {
		key  string
		mods []string
		want string
	}{
		"no modifiers":                         {"D", nil, "d"},
		"single super":                         {"D", []string{"super"}, "SUPER+d"},
		"super shift":                          {"D", []string{"super", "shift"}, "SUPER+SHIFT+d"},
		"mod4 alias":                           {"D", []string{"mod4"}, "SUPER+d"},
		"win alias":                            {"D", []string{"win"}, "SUPER+d"},
		"meta alias":                           {"D", []string{"meta"}, "SUPER+d"},
		"alt mod1 alias":                       {"D", []string{"mod1"}, "ALT+d"},
		"ctrl control alias":                   {"D", []string{"control"}, "CTRL+d"},
		"mixed case":                           {"D", []string{"Super", "SHIFT"}, "SUPER+SHIFT+d"},
		"whitespace trim":                      {"D", []string{"  super  "}, "SUPER+d"},
		"unknown modifier ignored":             {"D", []string{"super", "phaser"}, "SUPER+d"},
		"function key uppercased to lowercase": {"F12", []string{"ctrl"}, "CTRL+f12"},
	}

	for name, tc := range cases {
		s.Run(name, func() {
			s.Equal(tc.want, formatAccelerator(tc.key, tc.mods))
		})
	}
}

// --- handleToken ---

func (s *PortalUnitSuite) TestHandleToken_HasPrefix() {
	tok := handleToken("a2text")
	s.True(strings.HasPrefix(tok, "a2text_"), "token must start with prefix+_")
}

func (s *PortalUnitSuite) TestHandleToken_UniqueEnough() {
	// Two consecutive calls must produce distinct tokens — UnixNano resolution
	// is sufficient because the implementation appends to the prefix. If this
	// ever fires, the portal would multiplex two Request objects on one path
	// and lose one Response.
	a := handleToken("p")
	b := handleToken("p")
	s.NotEqual(a, b)
}

// --- parseResponse ---

func (s *PortalUnitSuite) TestParseResponse_SuccessReturnsResults() {
	results := map[string]dbus.Variant{
		"session_handle": dbus.MakeVariant("/org/freedesktop/portal/desktop/session/test"),
	}
	sig := &dbus.Signal{
		Body: []any{uint32(0), results},
	}

	got, err := parseResponse(sig, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.Equal(results, got)
}

// TestParseResponse_UserCancelled_WrapsPermissionDenied verifies that
// Response code=1 (user dismissed the permission prompt) is both
// recognisable as ErrPortalPermissionDenied and retains "code=1" in the
// message for journal grep.
func (s *PortalUnitSuite) TestParseResponse_UserCancelled_WrapsPermissionDenied() {
	sig := &dbus.Signal{
		Body: []any{uint32(1), map[string]dbus.Variant{}},
	}

	_, err := parseResponse(sig, slog.New(slog.DiscardHandler))
	s.Require().Error(err)
	s.Contains(err.Error(), "code=1", "error must surface the raw status code for journal grep")
	s.Require().ErrorIs(err, ErrPortalPermissionDenied,
		"code=1 must wrap ErrPortalPermissionDenied so callers can errors.Is-check it")
}

// TestParseResponse_OtherError_WrapsBindRejected verifies that Response
// code=2 (compositor-side rejection) wraps ErrPortalBindRejected and
// keeps "code=2" in the error string.
func (s *PortalUnitSuite) TestParseResponse_OtherError_WrapsBindRejected() {
	sig := &dbus.Signal{
		Body: []any{uint32(2), map[string]dbus.Variant{}},
	}

	_, err := parseResponse(sig, slog.New(slog.DiscardHandler))
	s.Require().Error(err)
	s.Contains(err.Error(), "code=2")
	s.Require().ErrorIs(err, ErrPortalBindRejected,
		"code=2 must wrap ErrPortalBindRejected so callers can errors.Is-check it")
}

// TestParseResponse_UnknownNonZeroStatus_WrapsBindRejected verifies that any
// unexpected non-zero code beyond 1 and 2 also wraps ErrPortalBindRejected
// (catch-all "other" bucket) and surfaces the raw code number.
func (s *PortalUnitSuite) TestParseResponse_UnknownNonZeroStatus_WrapsBindRejected() {
	sig := &dbus.Signal{
		Body: []any{uint32(99), map[string]dbus.Variant{}},
	}

	_, err := parseResponse(sig, slog.New(slog.DiscardHandler))
	s.Require().Error(err)
	s.Contains(err.Error(), "code=99")
	s.Require().ErrorIs(err, ErrPortalBindRejected)
}

func (s *PortalUnitSuite) TestParseResponse_ShortBody_Errors() {
	sig := &dbus.Signal{
		Body: []any{uint32(0)}, // missing results dict
	}

	_, err := parseResponse(sig, slog.New(slog.DiscardHandler))
	s.Require().Error(err)
	s.Contains(err.Error(), "body too short")
}

func (s *PortalUnitSuite) TestParseResponse_NonUint32Status_Errors() {
	sig := &dbus.Signal{
		Body: []any{"not-a-uint32", map[string]dbus.Variant{}},
	}

	_, err := parseResponse(sig, slog.New(slog.DiscardHandler))
	s.Require().Error(err)
	s.Contains(err.Error(), "status is")
}

func (s *PortalUnitSuite) TestParseResponse_NonDictResults_ReturnsEmptyMapNotError() {
	// Some portals emit a typed dict that fails the bare assertion; we degrade
	// to an empty map and log a warn rather than fail outright. CreateSession
	// would then fail later with "missing session_handle", which is a clearer
	// caller-side error.
	sig := &dbus.Signal{
		Body: []any{uint32(0), "garbage-not-a-dict"},
	}

	got, err := parseResponse(sig, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.Empty(got)
}

// --- dispatchSignal routing ---

func (s *PortalUnitSuite) TestDispatchSignal_RoutesActivatedToPress() {
	var (
		got    Event
		called bool
	)

	hk := s.newHotkeyWithCapture(&got, &called)
	session := dbus.ObjectPath("/test/session")

	hk.dispatchSignal(s.T().Context(), &dbus.Signal{
		Name: portalShortcutsI + ".Activated",
		Body: []any{session, shortcutID, uint64(0), map[string]dbus.Variant{}},
	}, session)

	s.True(called)
	s.Equal(Press, got)
}

func (s *PortalUnitSuite) TestDispatchSignal_RoutesDeactivatedToRelease() {
	var (
		got    Event
		called bool
	)

	hk := s.newHotkeyWithCapture(&got, &called)
	session := dbus.ObjectPath("/test/session")

	hk.dispatchSignal(s.T().Context(), &dbus.Signal{
		Name: portalShortcutsI + ".Deactivated",
		Body: []any{session, shortcutID, uint64(0), map[string]dbus.Variant{}},
	}, session)

	s.True(called)
	s.Equal(Release, got)
}

func (s *PortalUnitSuite) TestDispatchSignal_WrongSession_Ignored() {
	called := false
	hk := s.newHotkeyWithCapture(nil, &called)

	hk.dispatchSignal(s.T().Context(), &dbus.Signal{
		Name: portalShortcutsI + ".Activated",
		Body: []any{dbus.ObjectPath("/stranger/session"), shortcutID, uint64(0), map[string]dbus.Variant{}},
	}, dbus.ObjectPath("/our/session"))

	s.False(called, "signal for a foreign session must not invoke the handler")
}

func (s *PortalUnitSuite) TestDispatchSignal_WrongShortcutID_Ignored() {
	called := false
	hk := s.newHotkeyWithCapture(nil, &called)
	session := dbus.ObjectPath("/test/session")

	hk.dispatchSignal(s.T().Context(), &dbus.Signal{
		Name: portalShortcutsI + ".Activated",
		Body: []any{session, "different-id", uint64(0), map[string]dbus.Variant{}},
	}, session)

	s.False(called)
}

func (s *PortalUnitSuite) TestDispatchSignal_UnrelatedSignalName_Ignored() {
	called := false
	hk := s.newHotkeyWithCapture(nil, &called)
	session := dbus.ObjectPath("/test/session")

	hk.dispatchSignal(s.T().Context(), &dbus.Signal{
		Name: portalShortcutsI + ".ShortcutsChanged",
		Body: []any{session, []any{}},
	}, session)

	s.False(called, "ShortcutsChanged and unknown signals must be ignored")
}

func (s *PortalUnitSuite) TestDispatchSignal_ShortBody_Ignored() {
	called := false
	hk := s.newHotkeyWithCapture(nil, &called)
	session := dbus.ObjectPath("/test/session")

	hk.dispatchSignal(s.T().Context(), &dbus.Signal{
		Name: portalShortcutsI + ".Activated",
		Body: []any{session}, // missing shortcut_id, timestamp, options
	}, session)

	s.False(called)
}

// --- Stop is idempotent ---

func (s *PortalUnitSuite) TestStop_Idempotent() {
	hk, err := NewPortalHotkey(noopHandler, "D", nil, nil)
	s.Require().NoError(err)

	s.NoError(hk.Stop())
	s.NoError(hk.Stop(), "second Stop must not panic or error — channel already closed")
	s.NoError(hk.Stop())
}

// --- helpers ---

// newHotkeyWithCapture builds a PortalHotkey whose Handler records the
// event it received into the supplied pointers. Pointer-out interface keeps
// test cases concise without per-test channels.
func (s *PortalUnitSuite) newHotkeyWithCapture(eventOut *Event, called *bool) *PortalHotkey {
	s.T().Helper()

	handler := func(_ context.Context, evt Event) {
		*called = true

		if eventOut != nil {
			*eventOut = evt
		}
	}

	hk, err := NewPortalHotkey(handler, "D", nil, nil)
	s.Require().NoError(err)

	return hk
}

// noopHandler is the canonical do-nothing handler for constructor-only tests.
func noopHandler(_ context.Context, _ Event) {}
