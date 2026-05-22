// Package inject is the usecase behind KeyboardService.Inject: given
// a transcript and the daemon's active output policy, it decides
// which low-level action to perform (Ctrl+V chord on the platform
// virtual keyboard, no-op for clipboard-only delivery, future
// per-character typing) and reports the mode it resolved to plus
// the number of key events the platform driver wrote.
//
// The platform driver is reached through the InputDriver seam
// interface declared in this file (DIP — consumer owns the
// contract). On Linux the infrastructure implementation wraps
// /dev/uinput via bendahl/uinput; macOS and Windows ports will
// supply their own InputDriver implementations.
//
//nolint:godoclint // mockgen file shares package + its own header
package inject

//go:generate go run go.uber.org/mock/mockgen@latest -source=service.go -destination=service_mocks_test.go -package=inject

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	a2textv1 "github.com/partyzanex/a2text/pkg/proto/a2text/v1"
)

// ErrUnsupportedMode is returned when the configured InjectMode is
// neither CLIPBOARD nor PASTE. TYPE is wire-reserved but the daemon
// does not yet honour it, so it ends up here.
var ErrUnsupportedMode = errors.New("inject: unsupported mode")

// InputDriver is the platform-agnostic seam Service uses to inject
// key events. The infrastructure implementation owns the platform
// virtual keyboard (Linux uinput, macOS CGEventTap, Windows
// SendInput) and translates these calls into the matching kernel /
// OS primitives.
type InputDriver interface {
	// PasteChord synthesises a Ctrl+V key chord on the platform
	// virtual keyboard and returns the number of low-level key
	// events it wrote. The OS routes the chord to whatever window
	// currently has focus in the user's session.
	PasteChord(ctx context.Context) (int32, error)
}

// Service is the inject usecase. Safe for concurrent use; the
// underlying driver is expected to be safe under the same.
type Service struct {
	log    *slog.Logger
	mode   a2textv1.InjectMode
	driver InputDriver
}

// New constructs a Service. mode is the daemon's configured output
// policy at startup — until config reload is wired the value is
// fixed for the Service lifetime. driver is required; passing nil is
// a programmer error and will surface as a panic on the first PASTE
// call.
func New(log *slog.Logger, mode a2textv1.InjectMode, driver InputDriver) *Service {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &Service{
		log:    log,
		mode:   mode,
		driver: driver,
	}
}

// Inject performs the delivery action that matches the configured
// mode and returns (resolvedMode, eventsWritten, err).
//
// Behaviour per mode:
//
//   - CLIPBOARD — no platform action; the UI is responsible for
//     delivering the transcript via its local clipboard helper. The
//     text argument is ignored. Returned events_written is zero.
//   - PASTE     — synthesise a Ctrl+V chord on the platform virtual
//     keyboard. The text argument is ignored at this layer; the UI
//     is assumed to have written the transcript to the clipboard
//     beforehand.
//   - anything else — ErrUnsupportedMode (covers INJECT_MODE_TYPE
//     and INJECT_MODE_INVALID until they are implemented).
func (s *Service) Inject(
	ctx context.Context,
	_ string,
) (a2textv1.InjectMode, int32, error) {
	switch s.mode {
	case a2textv1.InjectMode_INJECT_MODE_CLIPBOARD:
		return a2textv1.InjectMode_INJECT_MODE_CLIPBOARD, 0, nil

	case a2textv1.InjectMode_INJECT_MODE_PASTE:
		written, err := s.driver.PasteChord(ctx)
		if err != nil {
			return a2textv1.InjectMode_INJECT_MODE_PASTE, 0, fmt.Errorf("inject: paste chord: %w", err)
		}

		return a2textv1.InjectMode_INJECT_MODE_PASTE, written, nil

	default:
		return a2textv1.InjectMode_INJECT_MODE_INVALID, 0, ErrUnsupportedMode
	}
}
