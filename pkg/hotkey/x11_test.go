//go:build linux && x11

package hotkey

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/suite"
)

type X11HotkeySuite struct {
	suite.Suite

	handler Handler
}

func TestX11HotkeySuite(t *testing.T) {
	suite.Run(t, new(X11HotkeySuite))
}

func (s *X11HotkeySuite) SetupTest() {
	s.handler = func(_ context.Context, _ Event) {}
}

// --- Constructor ---

func (s *X11HotkeySuite) TestNewX11Hotkey_NilHandler_ReturnsErrHandlerNil() {
	hk, err := NewX11Hotkey(nil, "F1", Mod4, nil)
	s.Require().ErrorIs(err, ErrHandlerNil)
	s.Nil(hk)
}

func (s *X11HotkeySuite) TestNewX11Hotkey_EmptyKey_ReturnsErrKeyEmpty() {
	hk, err := NewX11Hotkey(s.handler, "", Mod4, nil)
	s.Require().ErrorIs(err, ErrKeyEmpty)
	s.Nil(hk)
}

func (s *X11HotkeySuite) TestNewX11Hotkey_Valid_Constructs() {
	hk, err := NewX11Hotkey(s.handler, "F1", Mod4, nil)
	s.Require().NoError(err)
	s.NotNil(hk)
}

func (s *X11HotkeySuite) TestNewX11Hotkey_NilLog_DoesNotPanic() {
	s.NotPanics(func() {
		_, err := NewX11Hotkey(s.handler, "F1", Mod4, nil)
		s.Require().NoError(err)
	})
}

func (s *X11HotkeySuite) TestNewX11Hotkey_WithLog_Constructs() {
	hk, err := NewX11Hotkey(s.handler, "space", Mod4|ModControl, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)
	s.NotNil(hk)
}

// --- Stop ---

func (s *X11HotkeySuite) TestStop_Idempotent() {
	hk, err := NewX11Hotkey(s.handler, "F1", Mod4, nil)
	s.Require().NoError(err)

	s.Require().NoError(hk.Stop())
	s.Require().NoError(hk.Stop())
	s.True(hk.stopped)
}

// --- Listen: pure-Go paths (no X11 connection required) ---

// TestListen_AfterStop_ReturnsAlreadyStopped verifies the stopped-guard that
// fires before any CGo call, so it runs without a real X11 server.
func (s *X11HotkeySuite) TestListen_AfterStop_ReturnsAlreadyStopped() {
	hk, err := NewX11Hotkey(s.handler, "F1", Mod4, nil)
	s.Require().NoError(err)

	s.Require().NoError(hk.Stop())

	listenErr := hk.Listen(s.T().Context())
	s.Require().Error(listenErr)
	s.ErrorContains(listenErr, "already stopped")
}

// TestListen_CancelledContextBeforeSetup_ReturnsContextCancelled verifies that
// a pre-cancelled context is detected before any CGo call. This relies on the
// early ctx.Err() check in Listen that runs after the stopped-guard but before
// hkAlloc / hkSetup — so no X11 connection is needed.
func (s *X11HotkeySuite) TestListen_CancelledContextBeforeSetup_ReturnsContextCancelled() {
	hk, err := NewX11Hotkey(s.handler, "F1", Mod4, nil)
	s.Require().NoError(err)

	ctx, cancel := context.WithCancel(s.T().Context())
	cancel()

	s.Require().ErrorIs(hk.Listen(ctx), context.Canceled)
}
