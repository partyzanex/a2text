//go:build whisper

package depcheck

// defaultWhisperCppAvailable returns true in the "whisper" build-tag flavour:
// the binary was linked against libwhisper.so and can run the local CGo backend.
func defaultWhisperCppAvailable() bool { return true }
