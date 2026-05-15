//go:build !whisper

package depcheck

// defaultWhisperCppAvailable returns false in the default build (no "whisper" tag):
// the binary was not linked against libwhisper.so.
func defaultWhisperCppAvailable() bool { return false }
