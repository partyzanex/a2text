package inject_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"

	"github.com/partyzanex/a2text/internal/usecases/inject"
	a2textv1 "github.com/partyzanex/a2text/pkg/proto/a2text/v1"
)

// ServiceSuite covers inject.Service: the mode-dispatch matrix
// (CLIPBOARD / PASTE / TYPE / INVALID), the driver-error pass-through,
// and the contractual ignoring of the text argument in every mode
// currently implemented.
type ServiceSuite struct {
	suite.Suite

	ctrl   *gomock.Controller
	driver *inject.MockInputDriver
}

// SetupTest builds a fresh mock driver per test case so test ordering
// and mock state never bleed across cases.
func (s *ServiceSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.driver = inject.NewMockInputDriver(s.ctrl)
}

// TearDownTest finalises the mock controller and surfaces any unmet
// expectations as a test failure.
func (s *ServiceSuite) TearDownTest() {
	s.ctrl.Finish()
}

// TestInject_ClipboardIsNoOp verifies CLIPBOARD mode never touches
// the platform driver and reports zero events_written.
func (s *ServiceSuite) TestInject_ClipboardIsNoOp() {
	svc := s.newService(a2textv1.InjectMode_INJECT_MODE_CLIPBOARD)

	mode, written, err := svc.Inject(context.Background(), "transcript-text")

	s.Require().NoError(err)
	s.Equal(a2textv1.InjectMode_INJECT_MODE_CLIPBOARD, mode)
	s.Equal(int32(0), written)
}

// TestInject_PasteCallsDriver verifies PASTE mode delegates to the
// driver's PasteChord and surfaces its written-count.
func (s *ServiceSuite) TestInject_PasteCallsDriver() {
	svc := s.newService(a2textv1.InjectMode_INJECT_MODE_PASTE)

	s.driver.EXPECT().
		PasteChord(gomock.Any()).
		Return(int32(4), nil)

	mode, written, err := svc.Inject(context.Background(), "transcript-text")

	s.Require().NoError(err)
	s.Equal(a2textv1.InjectMode_INJECT_MODE_PASTE, mode)
	s.Equal(int32(4), written)
}

// TestInject_PasteDriverErrorIsWrapped verifies a driver failure is
// wrapped (still errors.Is-compatible with the original sentinel)
// and the response carries mode=PASTE with zero events_written.
func (s *ServiceSuite) TestInject_PasteDriverErrorIsWrapped() {
	svc := s.newService(a2textv1.InjectMode_INJECT_MODE_PASTE)

	sentinel := errors.New("uinput closed")

	s.driver.EXPECT().
		PasteChord(gomock.Any()).
		Return(int32(0), sentinel)

	mode, written, err := svc.Inject(context.Background(), "transcript-text")

	s.Require().Error(err)
	s.Require().ErrorIs(err, sentinel)
	s.Equal(a2textv1.InjectMode_INJECT_MODE_PASTE, mode)
	s.Equal(int32(0), written)
}

// TestInject_TypeReturnsUnsupported verifies TYPE mode (wire-reserved,
// daemon does not honour) maps to INVALID + ErrUnsupportedMode.
func (s *ServiceSuite) TestInject_TypeReturnsUnsupported() {
	svc := s.newService(a2textv1.InjectMode_INJECT_MODE_TYPE)

	mode, written, err := svc.Inject(context.Background(), "transcript-text")

	s.Require().ErrorIs(err, inject.ErrUnsupportedMode)
	s.Equal(a2textv1.InjectMode_INJECT_MODE_INVALID, mode)
	s.Equal(int32(0), written)
}

// TestInject_InvalidReturnsUnsupported verifies the proto-zero
// INVALID mode (would-be result of an uninitialised config) maps to
// ErrUnsupportedMode too — defensive against a forgotten config
// path.
func (s *ServiceSuite) TestInject_InvalidReturnsUnsupported() {
	svc := s.newService(a2textv1.InjectMode_INJECT_MODE_INVALID)

	mode, written, err := svc.Inject(context.Background(), "")

	s.Require().ErrorIs(err, inject.ErrUnsupportedMode)
	s.Equal(a2textv1.InjectMode_INJECT_MODE_INVALID, mode)
	s.Equal(int32(0), written)
}

// TestInject_TextArgumentIgnored verifies the text argument is not
// inspected — every text value, including empty, yields the same
// behaviour for the current mode. Guards against accidental
// validation creeping in alongside the dead parameter.
func (s *ServiceSuite) TestInject_TextArgumentIgnored() {
	svc := s.newService(a2textv1.InjectMode_INJECT_MODE_PASTE)

	s.driver.EXPECT().
		PasteChord(gomock.Any()).
		Return(int32(4), nil).
		Times(2)

	mode1, written1, err1 := svc.Inject(context.Background(), "first")
	mode2, written2, err2 := svc.Inject(context.Background(), "")

	s.Require().NoError(err1)
	s.Require().NoError(err2)
	s.Equal(mode1, mode2)
	s.Equal(written1, written2)
}

// TestNew_NilLogIsTolerated verifies the constructor replaces a nil
// logger with the discard handler so the daemon never crashes on a
// missing logger.
func (s *ServiceSuite) TestNew_NilLogIsTolerated() {
	svc := inject.New(nil, a2textv1.InjectMode_INJECT_MODE_CLIPBOARD, s.driver)

	mode, _, err := svc.Inject(context.Background(), "")
	s.Require().NoError(err)
	s.Equal(a2textv1.InjectMode_INJECT_MODE_CLIPBOARD, mode)
}

// newService builds a Service with the suite's mock driver pinned to
// the requested mode. Helper placed at the bottom of the file so the
// unexported method stays after every exported suite method
// (funcorder rule).
func (s *ServiceSuite) newService(mode a2textv1.InjectMode) *inject.Service {
	return inject.New(slog.New(slog.DiscardHandler), mode, s.driver)
}

// TestServiceSuite is the standard testify entry point.
func TestServiceSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(ServiceSuite))
}
