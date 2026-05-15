//go:build whisper

package stt

type whisperSeams struct {
	fullHook *int
}

var seams *whisperSeams

func init() {
	seams = &whisperSeams{}
}

// SetWhisperFullHook sets the test seam for whisper_full.
// If non-nil, the hook's integer value is used as the return code instead of calling the real C function.
// Used by tests to simulate inference failures without importing "C" in test files.
func SetWhisperFullHook(hook *int) {
	seams.fullHook = hook
}

func getWhisperFullHook() *int {
	return seams.fullHook
}
