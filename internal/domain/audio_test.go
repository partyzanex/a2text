package domain

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestEstimateAudioDuration(t *testing.T) {
	t.Run("zero size returns zero duration", func(t *testing.T) {
		d := EstimateAudioDuration(0)
		assert.Zero(t, d)
	})

	t.Run("header-only file returns zero duration", func(t *testing.T) {
		d := EstimateAudioDuration(WavHeaderBytes)
		assert.Zero(t, d, "file with only header bytes must give 0 duration")
	})

	t.Run("one second of payload", func(t *testing.T) {
		size := WavHeaderBytes + AudioFilePayloadBytesPerSecond // 44 + 32000
		d := EstimateAudioDuration(size)
		assert.Equal(t, time.Second, d, "32000 payload bytes / 32000 B/s = 1 s")
	})

	t.Run("half second of payload", func(t *testing.T) {
		size := WavHeaderBytes + AudioFilePayloadBytesPerSecond/2 // 44 + 16000
		d := EstimateAudioDuration(size)
		assert.Equal(t, 500*time.Millisecond, d, "16000 payload bytes / 32000 B/s = 500 ms")
	})

	t.Run("two seconds of payload", func(t *testing.T) {
		size := WavHeaderBytes + 2*AudioFilePayloadBytesPerSecond
		d := EstimateAudioDuration(size)
		assert.Equal(t, 2*time.Second, d)
	})

	t.Run("negative size clamped to zero", func(t *testing.T) {
		d := EstimateAudioDuration(-100)
		assert.Zero(t, d, "negative size must be clamped to 0")
	})

	t.Run("size smaller than header clamped to zero", func(t *testing.T) {
		d := EstimateAudioDuration(WavHeaderBytes - 1)
		assert.Zero(t, d, "payload smaller than header must be clamped to 0")
	})
}

func TestAudioConstants(t *testing.T) {
	assert.Equal(t, int64(44), WavHeaderBytes)
	assert.Equal(t, 16000, DefaultRecordSampleRate)
	assert.Equal(t, 1, DefaultRecordChannels)
	assert.Equal(t, int64(32000), AudioFilePayloadBytesPerSecond)
}
