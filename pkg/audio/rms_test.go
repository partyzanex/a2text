package audio

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRMSDBFSFromSamples_EmptyReturnsFloor(t *testing.T) {
	t.Parallel()
	assert.InDelta(t, silenceFloorDBFS, rmsDBFSFromSamples(nil), 0.001)
	assert.InDelta(t, silenceFloorDBFS, rmsDBFSFromSamples([]float32{}), 0.001)
}

func TestRMSDBFSFromSamples_ZeroSignalReturnsFloor(t *testing.T) {
	t.Parallel()

	samples := make([]float32, 1024) // all zeros
	assert.InDelta(t, silenceFloorDBFS, rmsDBFSFromSamples(samples), 0.001)
}

func TestRMSDBFSFromSamples_FullScaleReturnsZero(t *testing.T) {
	t.Parallel()

	// Square wave at ±1.0 has RMS = 1.0 → 0 dBFS exactly.
	samples := []float32{1, -1, 1, -1, 1, -1, 1, -1}
	assert.InDelta(t, 0.0, rmsDBFSFromSamples(samples), 0.001)
}

func TestRMSDBFSFromSamples_HalfAmplitudeIsMinusSixDB(t *testing.T) {
	t.Parallel()

	// Square wave at ±0.5 has RMS = 0.5 → 20*log10(0.5) ≈ -6.0206 dBFS.
	samples := []float32{0.5, -0.5, 0.5, -0.5}
	assert.InDelta(t, -6.0206, rmsDBFSFromSamples(samples), 0.001)
}

func TestRMSDBFSFromSamples_SineWaveRMS(t *testing.T) {
	t.Parallel()

	// Full-scale sine wave has RMS = 1/sqrt(2) → -3.0103 dBFS.
	const samplesPerCycle = 256

	samples := make([]float32, samplesPerCycle*4) // 4 cycles
	for i := range samples {
		samples[i] = float32(math.Sin(2 * math.Pi * float64(i) / samplesPerCycle))
	}

	assert.InDelta(t, -3.0103, rmsDBFSFromSamples(samples), 0.01)
}

func TestIsSilent_RejectsNonNegativeThreshold(t *testing.T) {
	t.Parallel()

	_, err := IsSilent("/dev/null", 0)
	require.Error(t, err)

	_, err = IsSilent("/dev/null", 10)
	require.Error(t, err)
}
