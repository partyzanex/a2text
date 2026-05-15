//go:build whisper

package daemon

// whisperCppAvailable returns true in the `whisper` build-tag flavour:
// the binary was linked against libwhisper.so and can run the local
// CGo backend via stt.NewWhisperTranscriber.
func whisperCppAvailable() bool { return true }
