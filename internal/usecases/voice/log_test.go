package voice_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/partyzanex/a2text/internal/domain"
	"github.com/partyzanex/a2text/internal/usecases/voice"
)

// decodeJSONLog parses a single slog JSON line and returns the nested
// `voice` group as a map for assertions.
func decodeJSONLog(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()

	var record map[string]any

	require.NoError(t, json.Unmarshal(buf.Bytes(), &record))

	voiceGroup, ok := record["voice"].(map[string]any)
	require.True(t, ok, "expected 'voice' group in log record, got %v", record)

	return voiceGroup
}

// TestCycleAttrs_DurationsAsHumanString confirms durations are emitted as
// human-readable strings ("3.582s", "1.901s") rather than raw nanoseconds.
// Without this the JSON log shows opaque integers that operators have to
// divide by 1e9 in their head.
func TestCycleAttrs_DurationsAsHumanString(t *testing.T) {
	var buf bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	result := domain.CycleResult{
		AudioDuration: 3582 * time.Millisecond,
		STTDuration:   1901 * time.Millisecond,
	}

	logger.Info("cycle completed", voice.CycleAttrs(result))

	voiceGroup := decodeJSONLog(t, &buf)

	assert.Equal(t, "3.582s", voiceGroup["audio_duration_est"])
	assert.Equal(t, "1.901s", voiceGroup["stt_duration"])
}

// TestCycleAttrs_SubSecondDurations verifies the millisecond range renders
// as "Nms" — short captures should not display as "0s".
func TestCycleAttrs_SubSecondDurations(t *testing.T) {
	var buf bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	result := domain.CycleResult{
		AudioDuration: 250 * time.Millisecond,
		STTDuration:   42 * time.Millisecond,
	}

	logger.Info("cycle completed", voice.CycleAttrs(result))

	voiceGroup := decodeJSONLog(t, &buf)

	assert.Equal(t, "250ms", voiceGroup["audio_duration_est"])
	assert.Equal(t, "42ms", voiceGroup["stt_duration"])
}

// TestCycleAttrs_ExtraAttrsPropagated ensures caller-supplied extras
// (model name, text_len) reach the nested voice group alongside the
// duration fields.
func TestCycleAttrs_ExtraAttrsPropagated(t *testing.T) {
	var buf bytes.Buffer

	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	result := domain.CycleResult{
		AudioDuration: time.Second,
		STTDuration:   500 * time.Millisecond,
	}

	logger.Info("cycle completed",
		voice.CycleAttrs(result,
			slog.String("model", "ggml-small.bin"),
			slog.Int("text_len", 42),
		),
	)

	voiceGroup := decodeJSONLog(t, &buf)

	assert.Equal(t, "ggml-small.bin", voiceGroup["model"])
	assert.EqualValues(t, 42, voiceGroup["text_len"])
}
