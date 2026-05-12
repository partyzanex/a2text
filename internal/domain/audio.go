package domain

import "time"

// WavHeaderBytes is the standard PCM WAV header size. Subtracting it before
// computing AudioDuration avoids overestimating duration by ~1.4 ms.
const WavHeaderBytes int64 = 44

// Recording defaults locked to 16 kHz mono s16le to match the local
// whisper.cpp pipeline and avoid an extra resampling step in the subprocess
// capture adapter. Cloud STT backends accept other shapes, but converting on
// capture would buy nothing — they re-decode anyway.
const (
	DefaultRecordSampleRate = 16000
	DefaultRecordChannels   = 1
)

// AudioFilePayloadBytesPerSecond is the byte rate for 16kHz mono s16le
// (16000 samples/s × 1 channel × 2 bytes/sample). Used to estimate audio
// duration from a WAV file's payload size.
const AudioFilePayloadBytesPerSecond int64 = 32000

// EstimateAudioDuration returns a rough audio duration derived from the WAV
// file size. The WAV header is subtracted before the division so the estimate
// reflects payload only. Recordings shorter than 1 s appear as 0; the WAV
// header contributes ~1.4 ms of error. Use for logging only — not a precise
// wall-clock measurement.
func EstimateAudioDuration(fileSizeBytes int64) time.Duration {
	payloadSize := max(fileSizeBytes-WavHeaderBytes, 0)

	return time.Duration(payloadSize) * time.Second / time.Duration(AudioFilePayloadBytesPerSecond)
}
