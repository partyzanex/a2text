package stt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"
)

const backoffMultiplier = 2

// RetryConfig configures the RetryingTranscriber decorator. Zero MaxAttempts
// means "no retries" — the decorator passes Transcribe through unchanged so
// users can leave the wrapper in the chain at all times and gate behaviour
// purely by config.
type RetryConfig struct {
	// IsRetryable classifies an error as worth a retry. nil falls back to
	// DefaultRetryClassifier — covers transient network and 5xx-style errors
	// without retrying on user cancellation, semantic errors, or 4xx.
	IsRetryable func(error) bool

	// Sleep is exposed for tests so they can swap time.Sleep for an instant
	// no-op without burning real wall-clock time. nil means time.Sleep.
	Sleep func(context.Context, time.Duration) error

	// InitialDelay is the wait before the second attempt. Subsequent attempts
	// double the wait until MaxDelay.
	InitialDelay time.Duration

	// MaxDelay caps the exponential backoff. Required when MaxAttempts > 2,
	// otherwise the delay would grow unbounded.
	MaxDelay time.Duration

	// MaxAttempts is the total number of Transcribe attempts including the
	// first call. 1 disables retries; 2 means "one retry on failure".
	MaxAttempts int
}

// RetryingTranscriber wraps an STTBackend with bounded retry on transient
// errors. Non-Transcribe methods pass straight through — LoadModel /
// ReloadModel / DetectLanguage / Close are infrequent and either permanent
// (model bytes on disk) or not worth re-attempting on the hot path.
type RetryingTranscriber struct {
	inner STTBackend
	log   *slog.Logger
	cfg   RetryConfig
}

// NewRetryingTranscriber wraps inner with retry policy. A nil inner panics —
// retries over no backend would be a programming error. A nil log is replaced
// with a discard handler. The cfg is normalised: MaxAttempts ≤ 0 becomes 1
// (effectively no-op), nil IsRetryable falls back to DefaultRetryClassifier,
// nil Sleep falls back to a context-aware time.After.
func NewRetryingTranscriber(inner STTBackend, cfg RetryConfig, log *slog.Logger) *RetryingTranscriber {
	if inner == nil {
		panic("stt: NewRetryingTranscriber: inner must not be nil")
	}

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 1
	}

	if cfg.IsRetryable == nil {
		cfg.IsRetryable = DefaultRetryClassifier
	}

	if cfg.Sleep == nil {
		cfg.Sleep = sleepWithContext
	}

	return &RetryingTranscriber{inner: inner, cfg: cfg, log: log}
}

// Transcribe runs the inner Transcribe up to MaxAttempts times, sleeping with
// exponential backoff between attempts. Stops early on context cancellation
// (returning the context error) or on any error the classifier marks as
// non-retryable. The final error preserves the underlying cause via %w so
// callers can still errors.Is against ErrEmptyResult / context errors.
func (r *RetryingTranscriber) Transcribe(ctx context.Context, wavPath, lang string) (string, error) {
	var lastErr error

	delay := r.cfg.InitialDelay

	for attempt := 1; attempt <= r.cfg.MaxAttempts; attempt++ {
		text, err := r.inner.Transcribe(ctx, wavPath, lang)
		if err == nil {
			if attempt > 1 {
				r.log.InfoContext(ctx, "stt: retry succeeded",
					slog.Int("attempt", attempt),
					slog.Int("max_attempts", r.cfg.MaxAttempts),
				)
			}

			return text, nil
		}

		lastErr = err

		// Context cancellation is the user's intent — never retry past it,
		// regardless of how the underlying client surfaced the cancel.
		if ctx.Err() != nil {
			return "", fmt.Errorf("retry: %w", err)
		}

		if attempt >= r.cfg.MaxAttempts {
			break
		}

		if !r.cfg.IsRetryable(err) {
			r.log.DebugContext(ctx, "stt: error classified non-retryable, giving up",
				slog.Int("attempt", attempt),
				slog.Any("err", err),
			)

			return "", fmt.Errorf("retry: %w", err)
		}

		r.log.WarnContext(ctx, "stt: transient transcribe error, retrying",
			slog.Int("attempt", attempt),
			slog.Int("max_attempts", r.cfg.MaxAttempts),
			slog.Duration("backoff", delay),
			slog.Any("err", err),
		)

		if sleepErr := r.cfg.Sleep(ctx, delay); sleepErr != nil {
			return "", sleepErr
		}

		delay = nextBackoff(delay, r.cfg.MaxDelay)
	}

	return "", fmt.Errorf("stt: exhausted %d attempts: %w", r.cfg.MaxAttempts, lastErr)
}

// LoadModel delegates without retry. Loading is a one-shot startup operation;
// a transient failure here is almost always permanent (file missing, RAM
// shortage) and worth surfacing immediately.
func (r *RetryingTranscriber) LoadModel(path string) error {
	if err := r.inner.LoadModel(path); err != nil {
		return fmt.Errorf("retry: %w", err)
	}

	return nil
}

// ReloadModel delegates without retry — see LoadModel rationale.
func (r *RetryingTranscriber) ReloadModel(path string) error {
	if err := r.inner.ReloadModel(path); err != nil {
		return fmt.Errorf("retry: %w", err)
	}

	return nil
}

// DetectLanguage delegates without retry. The cost of a wrong language tag
// is small (whisper still produces text), so a single attempt is enough.
func (r *RetryingTranscriber) DetectLanguage(ctx context.Context, wavPath string) (string, error) {
	lang, err := r.inner.DetectLanguage(ctx, wavPath)
	if err != nil {
		return lang, fmt.Errorf("retry: %w", err)
	}

	return lang, nil
}

// Close delegates straight through.
func (r *RetryingTranscriber) Close() error {
	err := r.inner.Close()
	if err != nil {
		return fmt.Errorf("retry: %w", err)
	}

	return nil
}

// StreamCapable is the structural interface a streaming-aware inner
// transcriber must satisfy. Exported so the factory layer can dispatch
// retry-with-Stream vs retry-without-Stream without importing
// usecases/voice (which would invert the dependency direction).
type StreamCapable interface {
	Stream(ctx context.Context, pcm io.Reader, lang string) (string, error)
}

// RetryingStreamingTranscriber bundles RetryingTranscriber with a Stream
// pass-through. It is constructed by NewRetryingStreamingTranscriber when
// inner is StreamCapable so the method set of the wrapper matches that of
// inner — without this, Go's structural typing would lose the streaming
// capability the moment a streaming transcriber is wrapped for retry.
type RetryingStreamingTranscriber struct {
	*RetryingTranscriber

	streamer StreamCapable
}

// NewRetryingStreamingTranscriber wraps a streaming inner with retry. The
// stream pass-through is intentionally NOT retried at this layer — the
// WebSocket lifecycle is owned by the streamer and a retry here would
// double-open the connection. Only the file-based Transcribe path uses
// the retry loop.
func NewRetryingStreamingTranscriber(
	inner StreamCapable, asBackend STTBackend, cfg RetryConfig, log *slog.Logger,
) *RetryingStreamingTranscriber {
	return &RetryingStreamingTranscriber{
		RetryingTranscriber: NewRetryingTranscriber(asBackend, cfg, log),
		streamer:            inner,
	}
}

// Stream forwards to the streaming inner without retry.
func (r *RetryingStreamingTranscriber) Stream(
	ctx context.Context, pcm io.Reader, lang string,
) (string, error) {
	text, err := r.streamer.Stream(ctx, pcm, lang)
	if err != nil {
		return text, fmt.Errorf("stream: %w", err)
	}

	return text, nil
}

// nextBackoff doubles the delay, clamped to maxDelay. A zero maxDelay leaves
// the delay unchanged — the constructor enforces that callers with
// MaxAttempts > 2 supply a maxDelay, so unbounded growth is prevented.
func nextBackoff(current, maxDelay time.Duration) time.Duration {
	doubled := current * backoffMultiplier
	if maxDelay > 0 && doubled > maxDelay {
		return maxDelay
	}

	return doubled
}

// sleepWithContext returns early when ctx is cancelled, with the context's
// error. Used as the default RetryConfig.Sleep so callers get cooperative
// cancellation for free.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("retry: %w", ctx.Err())
	}
}

// DefaultRetryClassifier flags an error as retryable when it represents a
// transient transport-level failure: network timeouts, connection resets,
// short reads (io.ErrUnexpectedEOF), or anything matching net.Error.Timeout().
//
// It does NOT retry on:
//   - context.Canceled / context.DeadlineExceeded — the caller drove the abort
//   - plain io.EOF — at this layer EOF means the call returned no bytes at all
//     (peer closed before any response); successful streams surface as
//     (text, nil). Treating bare EOF as transient would also retry permanent
//     "connection closed by peer" cases that really mean "service is down".
//     io.ErrUnexpectedEOF is still retried because it specifically marks a
//     premature cutoff mid-payload, which is the canonical transient case.
//   - any error not satisfying the heuristics above (covers HTTP 4xx, auth,
//     semantic errors like ErrEmptyResult, and whisper.cpp CGo failures which
//     are permanent).
//
// HTTP-status-aware retry (5xx, 429 with Retry-After) is intentionally not in
// the default — backends that want that behaviour should provide their own
// classifier and pass it via RetryConfig.IsRetryable.
func DefaultRetryClassifier(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	var opErr *net.OpError

	return errors.As(err, &opErr)
}
