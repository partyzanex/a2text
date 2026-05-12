package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/a2text/pkg/hotkey"
)

// RunHotkeySuite tests the runHotkey error-classification logic.
// Each case asserts the correct log level for a given Listen return value.
type RunHotkeySuite struct {
	suite.Suite

	ctrl *gomock.Controller
	hk   *MockHotkeyListener
	logs *bytes.Buffer
	log  *slog.Logger
}

func TestRunHotkeySuite(t *testing.T) {
	suite.Run(t, new(RunHotkeySuite))
}

func (s *RunHotkeySuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.hk = NewMockHotkeyListener(s.ctrl)
	s.logs = &bytes.Buffer{}
	s.log = slog.New(slog.NewJSONHandler(s.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestRunHotkey_PortalBindRejected_LogsInfo guards against the regression
// where compositor-side rejection (Response code=2) was logged at WARN.
// This is a known-good "compositor does not support GlobalShortcuts" path —
// the daemon must degrade gracefully and hint the operator to use DE shortcuts.
func (s *RunHotkeySuite) TestRunHotkey_PortalBindRejected_LogsInfo() {
	bindErr := fmt.Errorf("hotkey: bind portal shortcut: BindShortcuts response: portal Response code=2: %w",
		hotkey.ErrPortalBindRejected)

	s.hk.EXPECT().Listen(gomock.Any()).Return(bindErr)

	s.newDaemon().runHotkey(s.T().Context())

	out := s.logs.String()
	s.Contains(out, `"voice: portal hotkey not available`,
		"bind-rejected path must log the DE-shortcut hint message")
	s.NotContains(out, `"level":"WARN"`,
		"ErrPortalBindRejected must NOT produce a WARN — it is graceful degradation")
}

// TestRunHotkey_PortalPermissionDenied_LogsInfo guards against the user-deny
// path (Response code=1) being treated as an operator error.
func (s *RunHotkeySuite) TestRunHotkey_PortalPermissionDenied_LogsInfo() {
	deniedErr := fmt.Errorf("hotkey: bind portal shortcut: BindShortcuts response: portal Response code=1: %w",
		hotkey.ErrPortalPermissionDenied)

	s.hk.EXPECT().Listen(gomock.Any()).Return(deniedErr)

	s.newDaemon().runHotkey(s.T().Context())

	out := s.logs.String()
	s.Contains(out, `"voice: portal hotkey not available`,
		"permission-denied path must log the DE-shortcut hint message")
	s.NotContains(out, `"level":"WARN"`,
		"ErrPortalPermissionDenied must NOT produce a WARN")
}

// TestRunHotkey_PortalUnavailable_LogsInfo verifies that a missing portal
// interface (no xdg-desktop-portal on the bus) is also graceful degradation.
func (s *RunHotkeySuite) TestRunHotkey_PortalUnavailable_LogsInfo() {
	unavailErr := fmt.Errorf("hotkey: listen: %w", hotkey.ErrPortalUnavailable)

	s.hk.EXPECT().Listen(gomock.Any()).Return(unavailErr)

	s.newDaemon().runHotkey(s.T().Context())

	out := s.logs.String()
	s.Contains(out, `"voice: portal hotkey not available`)
	s.NotContains(out, `"level":"WARN"`)
}

// TestRunHotkey_UnknownError_LogsWarn ensures that genuinely unexpected errors
// (e.g. D-Bus connection failure mid-loop) are still surfaced as WARN.
func (s *RunHotkeySuite) TestRunHotkey_UnknownError_LogsWarn() {
	s.hk.EXPECT().Listen(gomock.Any()).Return(errors.New("unexpected hotkey error"))

	s.newDaemon().runHotkey(s.T().Context())

	out := s.logs.String()
	s.Contains(out, `"level":"WARN"`)
	s.Contains(out, "voice: hotkey listener exited with error")
}

// TestRunHotkey_NilError_NoWarn verifies the clean-exit path produces no warning.
func (s *RunHotkeySuite) TestRunHotkey_NilError_NoWarn() {
	s.hk.EXPECT().Listen(gomock.Any()).Return(nil)

	s.newDaemon().runHotkey(s.T().Context())

	s.NotContains(s.logs.String(), `"level":"WARN"`)
}

// TestRunHotkey_CtxCanceled_NoWarn verifies that context cancellation (normal
// daemon shutdown) does not produce a warning.
func (s *RunHotkeySuite) TestRunHotkey_CtxCanceled_NoWarn() {
	s.hk.EXPECT().Listen(gomock.Any()).Return(context.Canceled)

	s.newDaemon().runHotkey(s.T().Context())

	s.NotContains(s.logs.String(), `"level":"WARN"`)
}

// newDaemon returns a Daemon wired with the mock listener and capturing logger.
// Only the fields touched by runHotkey are populated — this keeps the test
// isolated from socket / transcriber / IPC machinery.
func (s *RunHotkeySuite) newDaemon() *Daemon {
	return &Daemon{log: s.log, hotkey: s.hk}
}
