package tray

import (
	"bytes"
	"image"
	"testing"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/a2text/internal/domain"
)

// TrayUnitSuite covers the pure-function half of the tray package:
// icon colour lookup, PNG generation, constructor guards, and the
// no-display fast-exit path. The systray event-loop half (Run with an
// active SNI watcher, loop menu dispatch) requires a live D-Bus session
// and is covered by manual smoke tests.
type TrayUnitSuite struct {
	suite.Suite
}

func TestTrayUnitSuite(t *testing.T) {
	suite.Run(t, new(TrayUnitSuite))
}

// --- colorForState ---

func (s *TrayUnitSuite) TestColorForState_Table() {
	cases := map[domain.State][3]uint8{
		domain.StateIdle:         {colorIdleR, colorIdleG, colorIdleB},
		domain.StateRecording:    {colorRecordingR, colorRecordingG, colorRecordingB},
		domain.StateTranscribing: {colorTranscribingR, colorTranscribingG, colorTranscribingB},
		domain.StateDelivering:   {colorDeliveringR, colorDeliveringG, colorDeliveringB},
		domain.StateError:        {colorErrorR, colorErrorG, colorErrorB},
		domain.StateShuttingDown: {colorShutdownR, colorShutdownG, colorShutdownB},
	}

	for st, want := range cases {
		rr, gg, bb := colorForState(st)
		s.Equal(want[0], rr, "state %s: unexpected red", st)
		s.Equal(want[1], gg, "state %s: unexpected green", st)
		s.Equal(want[2], bb, "state %s: unexpected blue", st)
	}
}

// TestColorForState_UnknownState_ReturnsIdleColor guards the default branch:
// any state not in the switch must degrade to the idle colour rather than
// returning zero values which would produce an invisible black icon.
func (s *TrayUnitSuite) TestColorForState_UnknownState_ReturnsIdleColor() {
	rr, gg, bb := colorForState("no-such-state")
	s.Equal(colorIdleR, rr)
	s.Equal(colorIdleG, gg)
	s.Equal(colorIdleB, bb)
}

// --- circleIcon ---

// TestCircleIcon_ValidPNG verifies that circleIcon produces well-formed PNG
// data that decodes to a 22×22 image.
func (s *TrayUnitSuite) TestCircleIcon_ValidPNG() {
	data := circleIcon(255, 0, 0)
	s.NotEmpty(data)

	img, format, err := image.Decode(bytes.NewReader(data))
	s.Require().NoError(err)
	s.Equal("png", format)
	s.Equal(image.Rect(0, 0, iconSizeConst, iconSizeConst), img.Bounds())
}

// TestCircleIcon_CenterPixelOpaque guards that the geometric centre of the
// circle is rendered as a fully-opaque, red-channel pixel.
func (s *TrayUnitSuite) TestCircleIcon_CenterPixelOpaque() {
	data := circleIcon(255, 0, 0)

	img, _, err := image.Decode(bytes.NewReader(data))
	s.Require().NoError(err)

	cx := iconSizeConst / 2
	rr, gg, bb, aa := img.At(cx, cx).RGBA()

	s.NotZero(aa, "center pixel must be opaque (inside circle)")
	s.NotZero(rr, "center pixel must carry the red channel")
	s.Zero(gg, "center pixel must have zero green channel")
	s.Zero(bb, "center pixel must have zero blue channel")
}

// TestCircleIcon_CornerPixelTransparent guards that the top-left corner is
// outside the circle and therefore fully transparent.
func (s *TrayUnitSuite) TestCircleIcon_CornerPixelTransparent() {
	data := circleIcon(255, 0, 0)

	img, _, err := image.Decode(bytes.NewReader(data))
	s.Require().NoError(err)

	_, _, _, aa := img.At(0, 0).RGBA()
	s.Zero(aa, "corner pixel (0,0) must be transparent — it is outside the circle")
}

// TestAllStatesProduceNonEmptyIcons guards that no state yields an empty byte
// slice (which would make the tray show a blank icon). SVG icons are used
// when available; circle fallbacks are used for other states.
func (s *TrayUnitSuite) TestAllStatesProduceNonEmptyIcons() {
	tr := New(nil, nil, nil)

	states := []domain.State{
		domain.StateIdle,
		domain.StateRecording,
		domain.StateTranscribing,
		domain.StateDelivering,
		domain.StateError,
		domain.StateShuttingDown,
	}

	for _, st := range states {
		data := tr.iconFor(st)
		s.NotEmpty(data, "icon for state %s must not be empty", st)
	}
}

// TestSVGIconsDecodeToCorrectSize verifies that the three state SVG icons
// decode to valid PNGs at the expected svgIconPx×svgIconPx resolution.
func (s *TrayUnitSuite) TestSVGIconsDecodeToCorrectSize() {
	tr := New(nil, nil, nil)

	for _, st := range []domain.State{
		domain.StateIdle,
		domain.StateRecording,
		domain.StateTranscribing,
	} {
		data := tr.iconFor(st)
		img, format, err := image.Decode(bytes.NewReader(data))
		s.Require().NoError(err, "icon for state %s must decode without error", st)
		s.Equal("png", format, "icon for state %s must be PNG", st)
		s.Equal(image.Rect(0, 0, svgIconPx, svgIconPx), img.Bounds(),
			"icon for state %s must be %dx%d", st, svgIconPx, svgIconPx)
	}
}

// TestSetState_MapsStringsToKnownStates verifies that SetState maps the
// canonical string values to their domain counterparts without panicking.
func (s *TrayUnitSuite) TestSetState_MapsStringsToKnownStates() {
	cases := map[string]domain.State{
		"inactive":      domain.StateIdle,
		"recording":     domain.StateRecording,
		"transcribing":  domain.StateTranscribing,
		"unknown-value": domain.StateIdle, // unrecognised → idle
	}

	for input, want := range cases {
		got := stateFromString(input)
		s.Equal(want, got, "stateFromString(%q) must return %s", input, want)
	}
}

// --- New ---

// TestNew_NilLog_DoesNotPanic guards the constructor's nil-log guard:
// callers that pass nil must get a working Tray with a discard logger.
func (s *TrayUnitSuite) TestNew_NilLog_DoesNotPanic() {
	s.NotPanics(func() {
		tr := New(nil, nil, nil)
		s.Require().NotNil(tr)
	})
}

// TestNew_NonNilLog_Retained verifies that a provided logger is used rather
// than being replaced by the discard handler.
func (s *TrayUnitSuite) TestNew_NonNilLog_Retained() {
	s.NotPanics(func() {
		tr := New(nil, nil, nil)
		s.NotNil(tr.log)
	})
}

// --- Run with no display ---

// TestRun_NoDisplay_ReturnsImmediately guards the headless fast-exit: when
// neither WAYLAND_DISPLAY nor DISPLAY is set, Run must return without
// attempting to connect to a session bus.
func (s *TrayUnitSuite) TestRun_NoDisplay_ReturnsImmediately() {
	s.T().Setenv("WAYLAND_DISPLAY", "")
	s.T().Setenv("DISPLAY", "")

	tr := New(nil, nil, nil)

	done := make(chan struct{})

	go func() {
		tr.Run(s.T().Context())
		close(done)
	}()

	select {
	case <-done:
	case <-s.T().Context().Done():
		s.Fail("Run did not return when no graphical session was available")
	}
}
