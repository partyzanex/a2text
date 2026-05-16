package voice

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCleanStderrTail_FfmpegSigintCascade(t *testing.T) {
	raw := `[aist#0:0/pcm_s16le @ 0x646267f721c0] Guessed Channel Layout: stereo
[aost#0:0/pcm_s16le @ 0x646267f72b40] Error submitting a packet to the muxer: Immediate exit requested
    Last message repeated 1 times
[out#0/s16le @ 0x646267f72380] Error muxing a packet
[out#0/s16le @ 0x646267f72380] Task finished with error code: -1414092869 (Immediate exit requested)
[out#0/s16le @ 0x646267f72380] Terminating thread with return code -1414092869 (Immediate exit requested)
[out#0/s16le @ 0x646267f72380] Error writing trailer: Immediate exit requested
[out#0/s16le @ 0x646267f72380] Error closing file: Immediate exit requested
`

	got := cleanStderrTail(raw)

	assert.Contains(t, got, "Guessed Channel Layout: stereo")
	assert.Contains(t, got, "shutdown via SIGINT")
	assert.NotContains(t, got, "0x", "hex pointer must be stripped")
	assert.NotContains(t, got, "Immediate exit", "shutdown cascade must be collapsed")
}

func TestCleanStderrTail_EmptyInput(t *testing.T) {
	assert.Empty(t, cleanStderrTail(""))
}

func TestCleanStderrTail_PreservesUnknownErrors(t *testing.T) {
	raw := "[input @ 0xdeadbeef] device disappeared\n[input @ 0xdeadbeef] device disappeared\n"

	got := cleanStderrTail(raw)

	assert.Contains(t, got, "device disappeared")
	assert.Contains(t, got, "(x2)")
	assert.NotContains(t, got, "0xdeadbeef")
}
