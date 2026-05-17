package stt

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNoopAuditLogger_DoesNothing(t *testing.T) {
	t.Parallel()

	var l NoopAuditLogger
	// Should not panic and should accept arbitrary events without side effects.
	l.LogEvent(&AuditEvent{Provider: "openai"})
}

func TestJSONLAuditLogger_WritesOneLinePerEvent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	logger := NewJSONLAuditLogger(&buf)

	logger.LogEvent(&AuditEvent{
		Timestamp: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
		Provider:  "openai",
		Endpoint:  "https://api.openai.com/v1/audio/transcriptions",
		Outcome:   "ok",
	})
	logger.LogEvent(&AuditEvent{
		Timestamp: time.Date(2026, 5, 18, 12, 1, 0, 0, time.UTC),
		Provider:  "deepgram",
		Outcome:   "5xx",
	})

	lines := bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte{'\n'})
	require.Len(t, lines, 2, "each LogEvent must write exactly one line")

	var first, second AuditEvent
	require.NoError(t, json.Unmarshal(lines[0], &first))
	require.NoError(t, json.Unmarshal(lines[1], &second))

	assert.Equal(t, "openai", first.Provider)
	assert.Equal(t, "ok", first.Outcome)
	assert.Equal(t, "deepgram", second.Provider)
	assert.Equal(t, "5xx", second.Outcome)
}

func TestJSONLAuditLogger_ConcurrentWritesDoNotInterleave(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	logger := NewJSONLAuditLogger(&buf)

	const (
		writers   = 16
		perWriter = 50
	)

	var wg sync.WaitGroup

	wg.Add(writers)

	for i := range writers {
		go func(id int) {
			defer wg.Done()

			for j := range perWriter {
				logger.LogEvent(&AuditEvent{
					Provider: "openai",
					TextLen:  id*1000 + j,
				})
			}
		}(i)
	}

	wg.Wait()

	lines := bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte{'\n'})
	require.Len(t, lines, writers*perWriter, "every LogEvent must produce one line")

	for i, line := range lines {
		var event AuditEvent
		require.NoErrorf(t, json.Unmarshal(line, &event),
			"line %d is not valid JSON, concurrent writes interleaved: %s", i, line)
	}
}

func TestHashString_StableAndHex(t *testing.T) {
	t.Parallel()

	got := hashString("привет, мир")

	// 64 hex chars = 32-byte sha256.
	assert.Len(t, got, 64)
	_, err := hex.DecodeString(got)
	require.NoError(t, err, "must be valid hex")

	again := hashString("привет, мир")
	assert.Equal(t, got, again, "hash must be deterministic")

	different := hashString("привет, мир!")
	assert.NotEqual(t, got, different, "different inputs must hash differently")
}

func TestHttpStatusBucket_Coarsens(t *testing.T) {
	t.Parallel()

	cases := map[int]string{
		200: "non_2xx", // 2xx is the success path, not a bucket
		301: "non_2xx",
		400: "4xx",
		404: "4xx",
		499: "4xx",
		500: "5xx",
		503: "5xx",
		599: "5xx",
	}

	for status, want := range cases {
		assert.Equal(t, want, httpStatusBucket(status), "status %d", status)
	}
}
