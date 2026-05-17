package stt

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/partyzanex/a2text/pkg/audio/wav"
)

// Provider names used in AuditEvent.Provider. Kept as constants so the
// transcribers and the audit consumer cannot drift on string literals.
const (
	AuditProviderOpenAI   = "openai"
	AuditProviderDeepgram = "deepgram"
)

// Outcome string constants for AuditEvent.Outcome. The bucket model is
// intentionally coarse — fine-grained taxonomy belongs in the message
// field or in upstream logs.
const (
	AuditOutcomeOK         = "ok"
	AuditOutcomeEmpty      = "empty"
	AuditOutcomeBuildErr   = "build_err"
	AuditOutcomeNetworkErr = "network_err"
	AuditOutcomeDecodeErr  = "decode_err"
	AuditOutcome4xx        = "4xx"
	AuditOutcome5xx        = "5xx"
	AuditOutcomeNon2xx     = "non_2xx"
)

// AuditEvent records one cloud STT request for the post-incident audit
// trail. Stored metadata only — never the audio bytes or the transcript
// text. Hashes let an operator correlate against local archives without
// the audit log itself becoming a privacy leak.
type AuditEvent struct {
	// Timestamp is when the request was issued (RFC3339).
	Timestamp time.Time `json:"timestamp"`
	// Provider is the cloud backend name: "openai", "deepgram".
	Provider string `json:"provider"`
	// Endpoint is the upstream URL the daemon talked to.
	Endpoint string `json:"endpoint"`
	// Model is the requested STT model identifier (provider-specific).
	Model string `json:"model,omitempty"`
	// Lang is the language hint sent with the request.
	Lang string `json:"lang,omitempty"`
	// AudioBytes is the size of the WAV file in bytes.
	AudioBytes int64 `json:"audio_bytes"`
	// AudioDuration is the wall-clock length of the audio (best-effort —
	// zero when the duration could not be read from the WAV header).
	AudioDuration time.Duration `json:"audio_duration,omitempty"`
	// AudioSHA256 is the hex sha256 of the WAV file contents before upload.
	AudioSHA256 string `json:"audio_sha256,omitempty"`
	// TextLen is the byte length of the returned transcript (0 on failure).
	TextLen int `json:"text_len"`
	// TextSHA256 is the hex sha256 of the returned transcript.
	TextSHA256 string `json:"text_sha256,omitempty"`
	// ElapsedMs is how long the HTTP round-trip took.
	ElapsedMs int64 `json:"elapsed_ms"`
	// Outcome is "ok" / "network_err" / "4xx" / "5xx" / "empty".
	Outcome string `json:"outcome"`
	// HTTPStatus is the provider's status code (0 if request never reached
	// the server).
	HTTPStatus int `json:"http_status,omitempty"`
	// RequestID is the provider-supplied correlation ID (e.g. the
	// X-Request-ID header), useful for support tickets.
	RequestID string `json:"request_id,omitempty"`
}

// AuditLogger is the seam between cloud transcribers and the on-disk
// audit log. Implementations must be safe for concurrent use. The event
// is passed by pointer to keep the per-call allocation small even as
// the struct grows (currently ~190 B).
type AuditLogger interface {
	LogEvent(event *AuditEvent)
}

// NoopAuditLogger silently drops every event. Returned to callers that
// did not wire up an audit log so the cloud transcribers can call
// LogEvent unconditionally.
type NoopAuditLogger struct{}

// LogEvent is a no-op.
func (NoopAuditLogger) LogEvent(*AuditEvent) {}

// JSONLAuditLogger writes one JSON object per line to an io.Writer.
// Safe for concurrent LogEvent calls — a single mutex protects each
// write so two goroutines cannot interleave bytes.
type JSONLAuditLogger struct {
	mu sync.Mutex
	w  io.Writer
}

// NewJSONLAuditLogger wraps w in a line-delimited JSON audit logger.
// Pass an *os.File opened append-only / 0o600 for production use; in
// tests pass a *bytes.Buffer.
func NewJSONLAuditLogger(w io.Writer) *JSONLAuditLogger {
	return &JSONLAuditLogger{w: w}
}

// LogEvent marshals event as JSON, appends a newline, and writes it to
// the underlying io.Writer. Write errors are silently dropped: an audit
// log that wedges the daemon on disk-full would be worse than a missing
// audit entry. Future work could surface this via a counter.
func (l *JSONLAuditLogger) LogEvent(event *AuditEvent) {
	if event == nil {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	raw, err := json.Marshal(event)
	if err != nil {
		return
	}

	raw = append(raw, '\n')

	_, _ = l.w.Write(raw) //nolint:errcheck // audit log is best-effort; see godoc above
}

// hashAudioFile streams the WAV at path through sha256 and returns the
// hex digest plus the byte count. Reads in one pass to avoid loading
// the whole file into memory.
func hashAudioFile(path string) (digest string, size int64, err error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", 0, fmt.Errorf("audit: open %q: %w", path, err)
	}

	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("audit: close %q: %w", path, closeErr)
		}
	}()

	hasher := sha256.New()

	n, err := io.Copy(hasher, file)
	if err != nil {
		return "", n, fmt.Errorf("audit: read %q: %w", path, err)
	}

	return hex.EncodeToString(hasher.Sum(nil)), n, nil
}

// hashString returns the hex sha256 of s. Used to record a transcript's
// fingerprint without storing the transcript itself.
func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))

	return hex.EncodeToString(sum[:])
}

// wavDuration returns the WAV's playback duration. Best-effort — a
// missing or malformed header yields 0 and a non-nil error which the
// caller can log and continue past.
func wavDuration(path string) (time.Duration, error) {
	dec, err := wav.Open(path)
	if err != nil {
		return 0, fmt.Errorf("audit: open wav %q: %w", path, err)
	}

	dur := dec.Header.Duration

	if closeErr := dec.Close(); closeErr != nil {
		return dur, fmt.Errorf("audit: close wav %q: %w", path, closeErr)
	}

	return dur, nil
}

// httpStatusBucket coarsens an HTTP status into the audit Outcome field:
// "4xx" / "5xx" / "non_2xx". Keeps the schema small while still letting
// a downstream consumer group rows.
func httpStatusBucket(status int) string {
	switch {
	case status >= 400 && status < 500:
		return AuditOutcome4xx
	case status >= 500 && status < 600:
		return AuditOutcome5xx
	default:
		return AuditOutcomeNon2xx
	}
}

// captureAudioMetrics gathers everything the audit event needs to record
// about the input file: size, duration, sha256. Returns zero values for
// any field that could not be computed; never blocks the transcription
// path on a hashing or header-read error.
//
// Errors are intentionally not surfaced — a flaky disk read on the WAV
// would otherwise tank the transcription itself.
func captureAudioMetrics(path string) (size int64, dur time.Duration, digest string) {
	digest, size, hashErr := hashAudioFile(path)
	_ = hashErr // intentional drop, see godoc

	dur, durErr := wavDuration(path)
	_ = durErr

	return size, dur, digest
}
