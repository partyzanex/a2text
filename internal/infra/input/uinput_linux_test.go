//go:build linux

package input

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/bendahl/uinput"
	"github.com/stretchr/testify/suite"
)

// fakeKeyboard satisfies the unexported keyboard seam and records
// every KeyDown / KeyUp call so the test can assert the chord
// sequence emitted by PasteChord. closeErr / downErrs / upErrs
// let each test inject targeted failures without needing /dev/uinput.
type fakeKeyboard struct {
	downCalls []int
	upCalls   []int

	downErrs []error // popped on each KeyDown; nil tail means no more injected errors.
	upErrs   []error // same shape for KeyUp.

	closed   bool
	closeErr error
}

func (f *fakeKeyboard) KeyDown(key int) error {
	f.downCalls = append(f.downCalls, key)

	if len(f.downErrs) == 0 {
		return nil
	}

	next := f.downErrs[0]
	f.downErrs = f.downErrs[1:]

	return next
}

func (f *fakeKeyboard) KeyUp(key int) error {
	f.upCalls = append(f.upCalls, key)

	if len(f.upErrs) == 0 {
		return nil
	}

	next := f.upErrs[0]
	f.upErrs = f.upErrs[1:]

	return next
}

func (f *fakeKeyboard) Close() error {
	f.closed = true

	return f.closeErr
}

// UinputSuite covers PasteChord ordering, error propagation, and
// Close behaviour without touching real /dev/uinput.
type UinputSuite struct {
	suite.Suite

	driver *UinputDriver
	fake   *fakeKeyboard
}

func (s *UinputSuite) SetupTest() {
	s.fake = &fakeKeyboard{}
	s.driver = &UinputDriver{
		log: slog.New(slog.DiscardHandler),
		kb:  s.fake,
	}
}

// TestPasteChord_HappyPath verifies a successful chord writes the
// expected number of events and exercises every step (Ctrl down,
// V down, V up, Ctrl up) in order.
func (s *UinputSuite) TestPasteChord_HappyPath() {
	got, err := s.driver.PasteChord(context.Background())

	s.Require().NoError(err)
	s.Equal(stepCtrlUp, got)
	s.Equal([]int{uinput.KeyLeftctrl, uinput.KeyV}, s.fake.downCalls)
	s.Equal([]int{uinput.KeyV, uinput.KeyLeftctrl}, s.fake.upCalls)
}

// TestPasteChord_ContextCancelledShortCircuits verifies an already
// cancelled context is detected before any KeyDown is sent.
func (s *UinputSuite) TestPasteChord_ContextCancelledShortCircuits() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := s.driver.PasteChord(ctx)

	s.Require().Error(err)
	s.Require().ErrorIs(err, context.Canceled)
	s.Equal(int32(0), got)
	s.Empty(s.fake.downCalls)
	s.Empty(s.fake.upCalls)
}

// TestPasteChord_CtrlDownFailureReturnsZero verifies that a
// failure on the very first event returns a zero count and no
// partial side effects on the V key.
func (s *UinputSuite) TestPasteChord_CtrlDownFailureReturnsZero() {
	sentinel := errors.New("ctrl down failed")
	s.fake.downErrs = []error{sentinel}

	got, err := s.driver.PasteChord(context.Background())

	s.Require().Error(err)
	s.Require().ErrorIs(err, sentinel)
	s.Equal(int32(0), got)
	s.Equal([]int{uinput.KeyLeftctrl}, s.fake.downCalls)
	s.Empty(s.fake.upCalls)
}

// TestPasteChord_VDownFailureReturnsCtrlDownCount verifies that a
// failure on the V down step reports exactly one event landed
// (the Ctrl down before it).
func (s *UinputSuite) TestPasteChord_VDownFailureReturnsCtrlDownCount() {
	sentinel := errors.New("v down failed")
	s.fake.downErrs = []error{nil, sentinel}

	got, err := s.driver.PasteChord(context.Background())

	s.Require().Error(err)
	s.Require().ErrorIs(err, sentinel)
	s.Equal(stepCtrlDown, got)
}

// TestPasteChord_VUpFailureReturnsTwoCount verifies a failure on
// the V up step reports two events landed.
func (s *UinputSuite) TestPasteChord_VUpFailureReturnsTwoCount() {
	sentinel := errors.New("v up failed")
	s.fake.upErrs = []error{sentinel}

	got, err := s.driver.PasteChord(context.Background())

	s.Require().Error(err)
	s.Require().ErrorIs(err, sentinel)
	s.Equal(stepVDown, got)
}

// TestPasteChord_CtrlUpFailureReturnsThreeCount verifies a failure
// on the Ctrl up step reports three events landed.
func (s *UinputSuite) TestPasteChord_CtrlUpFailureReturnsThreeCount() {
	sentinel := errors.New("ctrl up failed")
	s.fake.upErrs = []error{nil, sentinel}

	got, err := s.driver.PasteChord(context.Background())

	s.Require().Error(err)
	s.Require().ErrorIs(err, sentinel)
	s.Equal(stepVUp, got)
}

// TestClose_ForwardsToKeyboard verifies Close delegates to the
// underlying keyboard.
func (s *UinputSuite) TestClose_ForwardsToKeyboard() {
	s.Require().NoError(s.driver.Close())
	s.True(s.fake.closed)
}

// TestClose_PropagatesKeyboardError verifies a non-nil close error
// is wrapped and returned.
func (s *UinputSuite) TestClose_PropagatesKeyboardError() {
	sentinel := errors.New("keyboard close failed")
	s.fake.closeErr = sentinel

	err := s.driver.Close()
	s.Require().Error(err)
	s.Require().ErrorIs(err, sentinel)
}

// TestClose_NilKeyboardIsNoOp verifies Close on a driver whose
// keyboard never opened returns nil rather than panicking.
func (s *UinputSuite) TestClose_NilKeyboardIsNoOp() {
	driver := &UinputDriver{log: slog.New(slog.DiscardHandler)}
	s.Require().NoError(driver.Close())
}

// TestPasteChord_DelayBetweenEvents verifies the chord actually
// pauses between steps so the compositor sees stable modifier
// state. Asserts only the lower bound (3 × interKeyDelay) — the
// upper bound is intentionally loose because timer granularity on
// busy CI hosts is unreliable.
func (s *UinputSuite) TestPasteChord_DelayBetweenEvents() {
	start := time.Now()

	_, err := s.driver.PasteChord(context.Background())
	s.Require().NoError(err)

	const interKeyGaps = 3 // between 4 chord events.

	elapsed := time.Since(start)
	expected := interKeyDelay * interKeyGaps
	s.GreaterOrEqual(elapsed, expected, "chord must respect inter-key delays")
}

func TestUinputSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(UinputSuite))
}
