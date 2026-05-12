//go:build linux && x11 && x11live

// Tests for X11Hotkey.Listen that exercise the real CGo path
// (XOpenDisplay → XGrabKey → XNextEvent → XUngrabKey → XCloseDisplay).
//
// Opt-in via the x11live build tag: these need a reachable X server and
// xdotool to synthesize key presses, neither of which CI nodes always have.
//
// Prerequisites on the host running the tests:
//   - DISPLAY env points at a reachable X server (Xorg, Xvfb, or Xephyr)
//   - xdotool is installed (used to synthesize key presses into that display)
//
// The suite skips itself when either is missing rather than failing, so the
// same command remains green on a headless host without explicit gating.
//
// Local invocation:
//
//	go test -tags="x11 x11live" -v -run X11HotkeyKeypress ./internal/adapters/hotkey/
//
// Headless invocation:
//
//	xvfb-run -a go test -tags="x11 x11live" -v -run X11HotkeyKeypress ./internal/adapters/hotkey/
//
// We use Mod4+F12 as the test combo: Super is rarely consumed by simple
// X servers/WMs without a desktop environment, and F12 is unlikely to clash
// with default bindings. If a future bug surfaces because some WM steals
// Super+F12 first, switch to a different free combo here.

package hotkey

import (
	"context"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
)

const (
	kpTestKey       = "F12"
	kpGrabSettleMs  = 100 * time.Millisecond
	kpHandlerWaitMs = 2 * time.Second
	kpStopWaitMs    = 1 * time.Second
	kpXdotoolWaitMs = 5 * time.Second
)

type X11HotkeyKeypressSuite struct {
	suite.Suite
}

func TestX11HotkeyKeypressSuite(t *testing.T) {
	suite.Run(t, new(X11HotkeyKeypressSuite))
}

func (s *X11HotkeyKeypressSuite) SetupSuite() {
	if os.Getenv("DISPLAY") == "" {
		s.T().Skip("DISPLAY is empty: no X server reachable; run under Xvfb or Xorg")
	}

	if _, err := exec.LookPath("xdotool"); err != nil {
		s.T().Skip("xdotool not installed: needed to synthesize KeyPress events")
	}
}

// TestListen_RealKeyPress_FiresHandler registers Mod4+F12, starts Listen,
// synthesizes the key via xdotool, and verifies the handler fires.
func (s *X11HotkeyKeypressSuite) TestListen_RealKeyPress_FiresHandler() {
	var fired atomic.Int32

	handler := func(_ context.Context, _ Event) {
		fired.Add(1)
	}

	hk, err := NewX11Hotkey(handler, kpTestKey, Mod4, nil)
	s.Require().NoError(err)

	listenErrCh := make(chan error, 1)

	go func() {
		listenErrCh <- hk.Listen(s.T().Context())
	}()

	// Give Listen a moment to call hkSetup → XGrabKey before we fire the key.
	time.Sleep(kpGrabSettleMs)

	s.runXdotool("key", "--clearmodifiers", "super+F12")

	s.Require().Eventually(
		func() bool { return fired.Load() >= 1 },
		kpHandlerWaitMs,
		20*time.Millisecond,
		"handler must fire within %s after synthetic KeyPress", kpHandlerWaitMs,
	)

	s.Require().NoError(hk.Stop())

	select {
	case listenErr := <-listenErrCh:
		s.Require().NoError(listenErr, "Listen must return nil after Stop")
	case <-time.After(kpStopWaitMs):
		s.T().Fatal("Listen did not return within stop timeout")
	}
}

// TestListen_StopWhileWaiting_ReturnsNil verifies that Stop unblocks a Listen
// that is currently sitting in the poll loop with no events pending.
func (s *X11HotkeyKeypressSuite) TestListen_StopWhileWaiting_ReturnsNil() {
	hk, err := NewX11Hotkey(func(_ context.Context, _ Event) {}, kpTestKey, Mod4, nil)
	s.Require().NoError(err)

	listenErrCh := make(chan error, 1)

	go func() {
		listenErrCh <- hk.Listen(s.T().Context())
	}()

	time.Sleep(kpGrabSettleMs)
	s.Require().NoError(hk.Stop())

	select {
	case listenErr := <-listenErrCh:
		s.Require().NoError(listenErr)
	case <-time.After(kpStopWaitMs):
		s.T().Fatal("Listen did not return after Stop")
	}
}

// TestListen_CtxCancel_ReturnsCanceled verifies that cancelling ctx unblocks
// Listen with context.Canceled (rather than nil from Stop).
func (s *X11HotkeyKeypressSuite) TestListen_CtxCancel_ReturnsCanceled() {
	hk, err := NewX11Hotkey(func(_ context.Context, _ Event) {}, kpTestKey, Mod4, nil)
	s.Require().NoError(err)

	ctx, cancel := context.WithCancel(s.T().Context())

	listenErrCh := make(chan error, 1)

	go func() {
		listenErrCh <- hk.Listen(ctx)
	}()

	time.Sleep(kpGrabSettleMs)
	cancel()

	select {
	case listenErr := <-listenErrCh:
		s.Require().ErrorIs(listenErr, context.Canceled)
	case <-time.After(kpStopWaitMs):
		s.T().Fatal("Listen did not return after ctx cancel")
	}
}

// TestListen_InvalidKeySym_FailsFast verifies that hkSetup rejects a bogus
// keysym name immediately without entering the event loop. Bogus names are
// caught by XStringToKeysym → NoSymbol; this test makes sure the error path
// surfaces a typed sentinel rather than a generic "setup failed".
func (s *X11HotkeyKeypressSuite) TestListen_InvalidKeySym_FailsFast() {
	hk, err := NewX11Hotkey(func(_ context.Context, _ Event) {}, "DefinitelyNotAKey", Mod4, nil)
	s.Require().NoError(err)

	defer func() { _ = hk.Stop() }()

	listenErr := hk.Listen(s.T().Context())
	s.Require().ErrorIs(listenErr, ErrX11InvalidKeySym)
}

// TestListen_GrabIsReleasedAfterStop verifies the cleanup contract: after a
// full Listen → Stop cycle, a fresh X11Hotkey for the SAME combo can grab
// the key again. A leaked grab from the first instance would leave XGrabKey
// returning BadAccess on the second, and the first synthetic press would
// then go to no-one — surfaced here as a missed handler call.
func (s *X11HotkeyKeypressSuite) TestListen_GrabIsReleasedAfterStop() {
	first, err := NewX11Hotkey(func(_ context.Context, _ Event) {}, kpTestKey, Mod4, nil)
	s.Require().NoError(err)

	firstDoneCh := make(chan error, 1)

	go func() { firstDoneCh <- first.Listen(s.T().Context()) }()

	time.Sleep(kpGrabSettleMs)
	s.Require().NoError(first.Stop())

	select {
	case <-firstDoneCh:
	case <-time.After(kpStopWaitMs):
		s.T().Fatal("first Listen did not return after Stop")
	}

	var fired atomic.Int32

	second, err := NewX11Hotkey(func(_ context.Context, _ Event) { fired.Add(1) }, kpTestKey, Mod4, nil)
	s.Require().NoError(err)

	secondDoneCh := make(chan error, 1)

	go func() { secondDoneCh <- second.Listen(s.T().Context()) }()

	time.Sleep(kpGrabSettleMs)
	s.runXdotool("key", "--clearmodifiers", "super+F12")

	s.Require().Eventually(
		func() bool { return fired.Load() >= 1 },
		kpHandlerWaitMs,
		20*time.Millisecond,
		"second instance must receive KeyPress — first instance leaked the grab if not",
	)

	s.Require().NoError(second.Stop())
	<-secondDoneCh
}

// runXdotool synthesizes input via xdotool. Failures are reported as test
// failures rather than skips: at this point the suite has confirmed xdotool
// is available, so an error here is a real problem (display permissions,
// busy synth queue, etc.). A short timeout guards against a hung xdotool
// process pinning the test goroutine indefinitely.
func (s *X11HotkeyKeypressSuite) runXdotool(args ...string) {
	ctx, cancel := context.WithTimeout(s.T().Context(), kpXdotoolWaitMs)
	defer cancel()

	out, err := exec.CommandContext(ctx, "xdotool", args...).CombinedOutput()
	s.Require().NoError(err, "xdotool %v failed: %s", args, string(out))
}
