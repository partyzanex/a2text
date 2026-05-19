package audio

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildOGGArgs_ForcesOGGMuxer pins the regression fix: the Archiver
// writes the transcoded file to `<final>.ogg.partial` before atomic-renaming
// to its final name. ffmpeg's format detection looks at the file extension;
// `.partial` is not a known muxer, so without `-f ogg` ffmpeg fails with
// "Unable to choose an output format" and the kept-audio archive layer
// reports `exit status 234`.
//
// Reproduced in production logs on 2026-05-19; the test exists so the
// `-f ogg` flag cannot regress silently.
func TestBuildOGGArgs_ForcesOGGMuxer(t *testing.T) {
	t.Parallel()

	args := buildOGGArgs("in.wav", "out.ogg.partial")

	idx := slices.Index(args, "-f")
	require.GreaterOrEqual(t, idx, 0, "ffmpeg arg list must contain -f")
	require.Less(t, idx+1, len(args), "-f must be followed by a value")
	assert.Equal(t, "ogg", args[idx+1],
		"the muxer override must be `ogg` — needed because the dst extension is `.partial`",
	)
}

// TestBuildOGGArgs_PathsPositional checks that the source path follows -i
// and the destination path is the final positional argument. A future edit
// that reorders these would either feed ffmpeg an input it cannot read or
// silently overwrite the source — both surface only at runtime.
func TestBuildOGGArgs_PathsPositional(t *testing.T) {
	t.Parallel()

	args := buildOGGArgs("/tmp/in.wav", "/tmp/out.ogg.partial")

	iIdx := slices.Index(args, "-i")
	require.GreaterOrEqual(t, iIdx, 0, "ffmpeg arg list must contain -i")
	require.Less(t, iIdx+1, len(args), "-i must be followed by the source path")
	assert.Equal(t, "/tmp/in.wav", args[iIdx+1])

	assert.Equal(t, "/tmp/out.ogg.partial", args[len(args)-1],
		"destination path must be the last positional argument",
	)
}

// TestBuildOGGArgs_CodecAndQuality guards the codec choice: libvorbis is
// the ubiquitous one in stock ffmpeg builds (much more than libopus), and
// q:a 5 ≈ 96 kbps voice — the rationale is documented in the package
// godoc. Bumping the quality silently could 4× the kept-audio footprint.
func TestBuildOGGArgs_CodecAndQuality(t *testing.T) {
	t.Parallel()

	args := buildOGGArgs("in.wav", "out.ogg")

	codecIdx := slices.Index(args, "-c:a")
	require.GreaterOrEqual(t, codecIdx, 0, "ffmpeg arg list must contain -c:a")
	require.Less(t, codecIdx+1, len(args))
	assert.Equal(t, "libvorbis", args[codecIdx+1])

	qIdx := slices.Index(args, "-q:a")
	require.GreaterOrEqual(t, qIdx, 0, "ffmpeg arg list must contain -q:a")
	require.Less(t, qIdx+1, len(args))
	assert.Equal(t, "5", args[qIdx+1])
}
