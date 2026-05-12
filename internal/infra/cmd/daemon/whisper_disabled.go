//go:build !whisper

package daemon

// whisperCppAvailable reports whether this binary was built with the
// whisper-cpp CGo backend linked in. The default build (no `whisper`
// tag) returns false — depcheck surfaces it to the user via a "rebuild
// with -tags whisper" install hint.
func whisperCppAvailable() bool { return false }
