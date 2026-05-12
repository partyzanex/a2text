//go:build integration && linux

package hotkey_test

import (
	"context"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/a2text/pkg/hotkey"
)

// PortalIntegrationSuite probes the real xdg-desktop-portal GlobalShortcuts
// service via D-Bus. It runs under `make test-integration` and requires:
//
//   - a live session bus ($DBUS_SESSION_BUS_ADDRESS or per-user systemd);
//   - org.freedesktop.portal.GlobalShortcuts registered (modern xdg-desktop-portal);
//   - on GNOME 45+/KDE 5.27+/wlroots with xdg-desktop-portal-wlr.
//
// Hosts that lack any of those skip cleanly so this suite is safe to leave
// in `test-all` and CI: a headless runner with no bus will see Skip, not a
// red build.
//
// The suite does NOT actually press keys (that would require a compositor
// permission grant + a synthetic input source). It validates:
//
//   - IsPortalAvailable agrees with manual introspection;
//   - NewPortalHotkey.Listen connects, CreateSession + BindShortcuts succeed
//     OR fail with a typed error we can act on (user-cancelled grant prompt);
//   - Stop unblocks Listen within a bounded deadline.
type PortalIntegrationSuite struct {
	suite.Suite
}

func TestPortalIntegrationSuite(t *testing.T) {
	suite.Run(t, new(PortalIntegrationSuite))
}

func (s *PortalIntegrationSuite) SetupSuite() {
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		// systemd --user provides a session bus via XDG_RUNTIME_DIR, but the
		// env var is the most reliable signal we have something to talk to.
		// Skip rather than fail so this suite is portable across CI shapes.
		if _, err := dbus.ConnectSessionBus(); err != nil {
			s.T().Skipf("no D-Bus session bus available: %v", err)
		}
	}

	if !hotkey.IsPortalAvailable() {
		const skipMsg = "org.freedesktop.portal.GlobalShortcuts is not registered on this session bus — " +
			"skipping portal integration suite"
		s.T().Skip(skipMsg)
	}
}

// TestIsPortalAvailable_TrueOnHostWithPortal asserts the convenience helper
// matches reality on the host that survived SetupSuite. Tautological at first
// glance, but it pins the helper against regressions where someone changes
// the introspection match string and silently flips it to false.
func (s *PortalIntegrationSuite) TestIsPortalAvailable_TrueOnHostWithPortal() {
	s.True(hotkey.IsPortalAvailable())
}

// TestListen_StopBeforeStart_NoBlock validates the Stop-then-Listen path:
// a caller that signals shutdown before the listener starts must not wedge.
// Listen should return promptly when the close signal is already buffered.
func (s *PortalIntegrationSuite) TestListen_StopBeforeStart_NoBlock() {
	var callCount atomic.Int32

	listener, err := hotkey.NewPortalHotkey(
		func(_ context.Context, _ hotkey.Event) { callCount.Add(1) },
		"F12", []string{"ctrl"},
		slog.New(slog.DiscardHandler),
	)
	s.Require().NoError(err)

	// Stop before Listen → Listen must observe the closed stopCh and return.
	s.Require().NoError(listener.Stop())

	ctx, cancel := context.WithTimeout(s.T().Context(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- listener.Listen(ctx) }()

	select {
	case err := <-errCh:
		// We accept any of three outcomes:
		//   1) nil — Listen unwound on stopCh.
		//   2) a permission-grant error — compositor declined CreateSession.
		//   3) a portal version mismatch — old GNOME without BindShortcuts.
		// All three are "bounded exit, no wedge", which is the invariant.
		s.T().Logf("Listen returned (acceptable): err=%v", err)
	case <-time.After(8 * time.Second):
		s.FailNow("Listen did not return within deadline after Stop")
	}

	s.Zero(callCount.Load(), "handler must not fire when Listen exits without binding")
}

// TestListen_CtxCancel_Unblocks validates the ctx-cancel path: a listener
// that started successfully (CreateSession + BindShortcuts past prompt)
// must observe ctx.Done() and return. Some compositors block CreateSession
// on a user prompt — that surfaces as a long Listen that ctx.Cancel ends.
//
// We give the compositor up to 3s to accept-or-reject the request. If the
// host requires interactive confirmation and no one is at the keyboard,
// CreateSession will eventually time out via portalReplyTimeout (10s); ctx
// expiring at 3s wins that race and Listen returns sooner.
func (s *PortalIntegrationSuite) TestListen_CtxCancel_Unblocks() {
	listener, err := hotkey.NewPortalHotkey(
		func(_ context.Context, _ hotkey.Event) {},
		"F11", nil,
		slog.New(slog.DiscardHandler),
	)
	s.Require().NoError(err)

	ctx, cancel := context.WithTimeout(s.T().Context(), 3*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- listener.Listen(ctx) }()

	select {
	case err := <-done:
		s.T().Logf("Listen returned: err=%v", err)
	case <-time.After(15 * time.Second):
		_ = listener.Stop()

		s.FailNow("Listen did not honor ctx cancellation within 15s")
	}
}
