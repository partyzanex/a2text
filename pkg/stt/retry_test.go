package stt

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	"go.uber.org/mock/gomock"
)

type RetryingTranscriberSuite struct {
	suite.Suite

	ctrl  *gomock.Controller
	inner *MockSTTBackend
	log   *slog.Logger
	sleep *recordingSleeper
}

func TestRetryingTranscriberSuite(t *testing.T) {
	suite.Run(t, new(RetryingTranscriberSuite))
}

func (s *RetryingTranscriberSuite) SetupTest() {
	s.ctrl = gomock.NewController(s.T())
	s.inner = NewMockSTTBackend(s.ctrl)
	s.log = slog.New(slog.DiscardHandler)
	s.sleep = &recordingSleeper{}
}

// --- Behavioural tests for Transcribe ---

func (s *RetryingTranscriberSuite) TestTranscribe_SuccessFirstTry_NoSleep() {
	s.inner.EXPECT().
		Transcribe(gomock.Any(), "/audio.wav", "ru").
		Return("hello", nil)

	retrying := s.newRetrying(RetryConfig{MaxAttempts: 3, InitialDelay: 50 * time.Millisecond})

	text, err := retrying.Transcribe(s.T().Context(), "/audio.wav", "ru")

	s.Require().NoError(err)
	s.Equal("hello", text)
	s.Empty(s.sleep.calls, "no backoff expected on first-try success")
}

func (s *RetryingTranscriberSuite) TestTranscribe_RetryThenSuccess_LogsAndBacksOff() {
	transient := &net.OpError{Op: "dial", Err: errors.New("connection refused")}

	gomock.InOrder(
		s.inner.EXPECT().Transcribe(gomock.Any(), "/audio.wav", "ru").Return("", transient),
		s.inner.EXPECT().Transcribe(gomock.Any(), "/audio.wav", "ru").Return("recovered", nil),
	)

	retrying := s.newRetrying(RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     1 * time.Second,
	})

	text, err := retrying.Transcribe(s.T().Context(), "/audio.wav", "ru")

	s.Require().NoError(err)
	s.Equal("recovered", text)
	s.Require().Len(s.sleep.calls, 1, "exactly one backoff between attempts")
	s.Equal(100*time.Millisecond, s.sleep.calls[0])
}

func (s *RetryingTranscriberSuite) TestTranscribe_RetryExhausted_WrapsLastError() {
	transient := &net.OpError{Op: "dial", Err: errors.New("connection refused")}

	s.inner.EXPECT().
		Transcribe(gomock.Any(), "/audio.wav", "ru").
		Return("", transient).
		Times(3)

	retrying := s.newRetrying(RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 50 * time.Millisecond,
		MaxDelay:     400 * time.Millisecond,
	})

	_, err := retrying.Transcribe(s.T().Context(), "/audio.wav", "ru")

	s.Require().Error(err)
	s.Require().ErrorIs(err, transient, "exhausted error must unwrap to original")
	s.Contains(err.Error(), "exhausted 3 attempts")
	s.Equal([]time.Duration{50 * time.Millisecond, 100 * time.Millisecond}, s.sleep.calls,
		"backoff doubles each attempt, capped to MaxDelay")
}

func (s *RetryingTranscriberSuite) TestTranscribe_NonRetryableError_StopsImmediately() {
	permanent := errors.New("voice: empty transcription result")

	s.inner.EXPECT().
		Transcribe(gomock.Any(), "/audio.wav", "ru").
		Return("", permanent).
		Times(1)

	retrying := s.newRetrying(RetryConfig{
		MaxAttempts:  5,
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
	})

	_, err := retrying.Transcribe(s.T().Context(), "/audio.wav", "ru")

	s.Require().ErrorIs(err, permanent, "non-retryable error returned as-is, no wrap")
	s.Empty(s.sleep.calls, "non-retryable must not sleep")
}

func (s *RetryingTranscriberSuite) TestTranscribe_CtxCancelDuringBackoff_ReturnsCtxErr() {
	transient := &net.OpError{Op: "dial", Err: errors.New("connection refused")}

	s.inner.EXPECT().
		Transcribe(gomock.Any(), "/audio.wav", "ru").
		Return("", transient).
		Times(1)

	cancelCtx, cancel := context.WithCancel(s.T().Context())

	cancellingSleep := func(ctx context.Context, _ time.Duration) error {
		cancel()

		return ctx.Err()
	}

	retrying := s.newRetrying(RetryConfig{
		MaxAttempts:  3,
		InitialDelay: 1 * time.Hour, // would block forever without ctx-cancel
		Sleep:        cancellingSleep,
	})

	_, err := retrying.Transcribe(cancelCtx, "/audio.wav", "ru")

	s.Require().ErrorIs(err, context.Canceled, "sleep cancellation propagates")
}

// --- Pass-through methods ---

func (s *RetryingTranscriberSuite) TestLoadModel_DelegatesWithoutRetry() {
	loadErr := errors.New("model corrupt")

	s.inner.EXPECT().LoadModel("/m.bin").Return(loadErr).Times(1)

	retrying := s.newRetrying(RetryConfig{MaxAttempts: 3, InitialDelay: 1 * time.Millisecond})

	s.Require().ErrorIs(retrying.LoadModel("/m.bin"), loadErr)
}

// TestLoadModel_Success_ReturnsNil guards against the fmt.Errorf("retry: %w", nil)
// anti-pattern: wrapping a nil error produces a non-nil error, so a successful
// LoadModel would falsely report failure to the caller.
func (s *RetryingTranscriberSuite) TestLoadModel_Success_ReturnsNil() {
	s.inner.EXPECT().LoadModel("/m.bin").Return(nil).Times(1)

	retrying := s.newRetrying(RetryConfig{MaxAttempts: 3, InitialDelay: 1 * time.Millisecond})

	s.Require().NoError(retrying.LoadModel("/m.bin"),
		"LoadModel must return nil when inner returns nil, not a wrapped non-nil error")
}

// TestDetectLanguage_Success_ReturnsLangAndNil guards against unconditional
// fmt.Errorf wrapping of the nil error from a successful DetectLanguage call.
func (s *RetryingTranscriberSuite) TestDetectLanguage_Success_ReturnsLangAndNil() {
	s.inner.EXPECT().DetectLanguage(gomock.Any(), "/a.wav").Return("ru", nil).Times(1)

	retrying := s.newRetrying(RetryConfig{MaxAttempts: 3, InitialDelay: 1 * time.Millisecond})

	lang, err := retrying.DetectLanguage(s.T().Context(), "/a.wav")

	s.Require().NoError(err,
		"DetectLanguage must return nil error when inner returns nil, not a wrapped non-nil error")
	s.Equal("ru", lang)
}

// TestDetectLanguage_Error_WrapsAndReturns verifies that a non-nil error from
// inner is propagated (wrapped) by DetectLanguage.
func (s *RetryingTranscriberSuite) TestDetectLanguage_Error_WrapsAndReturns() {
	detectErr := errors.New("language detection failed")

	s.inner.EXPECT().DetectLanguage(gomock.Any(), "/a.wav").Return("", detectErr).Times(1)

	retrying := s.newRetrying(RetryConfig{MaxAttempts: 3, InitialDelay: 1 * time.Millisecond})

	_, err := retrying.DetectLanguage(s.T().Context(), "/a.wav")

	s.Require().ErrorIs(err, detectErr,
		"DetectLanguage must wrap and return inner error")
}

func (s *RetryingTranscriberSuite) TestClose_Delegates() {
	s.inner.EXPECT().Close().Return(nil).Times(1)

	retrying := s.newRetrying(RetryConfig{MaxAttempts: 3})
	s.Require().NoError(retrying.Close())
}

// --- Constructor sanity ---

func (s *RetryingTranscriberSuite) TestNew_NilInner_Panics() {
	s.Panics(func() {
		_ = NewRetryingTranscriber(nil, RetryConfig{MaxAttempts: 1}, s.log)
	})
}

func (s *RetryingTranscriberSuite) TestNew_ZeroMaxAttempts_NormalisedToOne() {
	s.inner.EXPECT().Transcribe(gomock.Any(), "/x.wav", "ru").Return("ok", nil)

	retrying := s.newRetrying(RetryConfig{MaxAttempts: 0})

	out, err := retrying.Transcribe(s.T().Context(), "/x.wav", "ru")
	s.Require().NoError(err)
	s.Equal("ok", out)
}

// --- DefaultRetryClassifier ---

func (s *RetryingTranscriberSuite) TestDefaultClassifier_Verdicts() {
	s.False(DefaultRetryClassifier(nil), "nil error is never retryable")
	s.False(DefaultRetryClassifier(context.Canceled), "ctx.Canceled is user intent")
	s.False(DefaultRetryClassifier(context.DeadlineExceeded), "ctx.DeadlineExceeded is external")
	s.False(DefaultRetryClassifier(errors.New("4xx bad request")), "generic error not retryable")
	s.True(DefaultRetryClassifier(io.ErrUnexpectedEOF), "short read is transient")
	s.False(DefaultRetryClassifier(io.EOF),
		"plain EOF can mean a permanently-down peer; only ErrUnexpectedEOF is treated as transient")
	s.True(DefaultRetryClassifier(&net.OpError{Op: "dial", Err: errors.New("refused")}),
		"dial error is transport-level transient")
}

// --- Helpers ---

func (s *RetryingTranscriberSuite) newRetrying(cfg RetryConfig) *RetryingTranscriber {
	if cfg.Sleep == nil {
		cfg.Sleep = s.sleep.Sleep
	}

	return NewRetryingTranscriber(s.inner, cfg, s.log)
}

// recordingSleeper captures the durations passed to Sleep so tests can assert
// the backoff schedule without burning real wall-clock time.
type recordingSleeper struct {
	calls []time.Duration
	count atomic.Int64
}

func (r *recordingSleeper) Sleep(_ context.Context, d time.Duration) error {
	r.calls = append(r.calls, d)
	r.count.Add(1)

	return nil
}
