package audio

import (
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/partyzanex/a2text/pkg/audio/wav"
)

// rmsChunkSamples is the buffer size used when streaming a WAV through
// the RMS computation. 4096 float32 samples ≈ 16 KiB — small enough to
// stay on the stack of small caches yet large enough that the read
// syscalls amortise. The exact value is not critical to the result.
const rmsChunkSamples = 4096

// silenceFloorDBFS is the value RMSdBFS returns for absolutely silent
// audio (sumSquares == 0). The exact dBFS value for true silence is
// -infinity; we clamp to a sentinel far below any usable speech
// threshold so callers can compare with < / > without special-casing
// math.Inf.
const silenceFloorDBFS = -120.0

// dbfsReference is the linear amplitude that maps to 0 dBFS. The WAV
// decoder normalises samples to [-1, 1], so full scale is 1.0.
const dbfsReference = 1.0

// dbfsScale converts a power ratio (RMS / reference) into decibels:
// dB = 20 · log10(amplitude_ratio). Named so the multiplier isn't a
// magic number in the formula.
const dbfsScale = 20.0

// RMSdBFS computes the root-mean-square loudness of a WAV file and
// returns it in decibels relative to full scale (dBFS).
//
// Reference points for 16 kHz mono speech recordings:
//
//	  0 dBFS  digital ceiling (clipping)
//	-20 dBFS  loud, close-miked voice
//	-35 dBFS  conversational voice
//	-45 dBFS  whisper or distant voice; usable threshold for silence gate
//	-60 dBFS  room tone / quiet ambient noise
//
// Returns silenceFloorDBFS for empty or perfectly silent audio so that
// the value stays comparable with float64 inequalities.
func RMSdBFS(path string) (retDBFS float64, retErr error) {
	dec, err := wav.Open(path)
	if err != nil {
		return 0, fmt.Errorf("audio: rms: open %s: %w", path, err)
	}

	defer func() {
		if closeErr := dec.Close(); closeErr != nil && retErr == nil {
			retErr = fmt.Errorf("audio: rms: close %s: %w", path, closeErr)
		}
	}()

	dbfs, err := streamRMSDBFS(dec)
	if err != nil {
		return 0, fmt.Errorf("audio: rms: read %s: %w", path, err)
	}

	return dbfs, nil
}

// streamRMSDBFS reads samples from dec in fixed-size chunks and
// accumulates the running sum-of-squares without ever holding the
// whole recording in memory. Replaces the previous ReadAll path —
// pprof showed ReadAll dominating cycle-time allocations because a
// 60-second 16 kHz mono recording materialises ~7.7 MB of float32 per
// call. The streaming version peaks at ~16 KiB regardless of length.
func streamRMSDBFS(dec *wav.Decoder) (float64, error) {
	buf := make([]float32, rmsChunkSamples)

	var (
		sumSquares float64
		count      int64
	)

	for {
		nRead, readErr := dec.Read(buf)
		for i := range nRead {
			amplitude := float64(buf[i])
			sumSquares += amplitude * amplitude
		}

		count += int64(nRead)

		if errors.Is(readErr, io.EOF) {
			break
		}

		if readErr != nil {
			return 0, fmt.Errorf("wav read: %w", readErr)
		}
	}

	if count == 0 {
		return silenceFloorDBFS, nil
	}

	meanSquare := sumSquares / float64(count)
	if meanSquare <= 0 {
		return silenceFloorDBFS, nil
	}

	rms := math.Sqrt(meanSquare)

	return dbfsScale * math.Log10(rms/dbfsReference), nil
}

// rmsDBFSFromSamples is the in-memory computation extracted for testability.
// Operates on float32 PCM samples already normalised to [-1, 1].
func rmsDBFSFromSamples(samples []float32) float64 {
	if len(samples) == 0 {
		return silenceFloorDBFS
	}

	var sumSquares float64

	for _, sample := range samples {
		amplitude := float64(sample)
		sumSquares += amplitude * amplitude
	}

	meanSquare := sumSquares / float64(len(samples))
	if meanSquare <= 0 {
		return silenceFloorDBFS
	}

	rms := math.Sqrt(meanSquare)

	return dbfsScale * math.Log10(rms/dbfsReference)
}

// IsSilent reports whether the WAV at path has an RMS below thresholdDBFS.
// Use a negative threshold (e.g. -45.0); a non-negative threshold is
// rejected because full-scale audio can never be that loud and almost
// certainly indicates a configuration error.
func IsSilent(path string, thresholdDBFS float64) (bool, error) {
	if thresholdDBFS >= 0 {
		return false, fmt.Errorf("audio: silence threshold must be negative, got %.2f", thresholdDBFS)
	}

	dbfs, err := RMSdBFS(path)
	if err != nil {
		return false, err
	}

	return dbfs < thresholdDBFS, nil
}
