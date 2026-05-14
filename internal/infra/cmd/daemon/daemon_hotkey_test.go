package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"
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
