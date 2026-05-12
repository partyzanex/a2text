//go:build linux

package capture

import "io"

const maxStderrBytes = 200

// readStderrTrunc reads up to maxBytes from r and returns it as a string,
// suitable for embedding in error messages. Read errors are swallowed —
// a partial stderr is more useful for diagnostics than no stderr.
func readStderrTrunc(r io.Reader, maxBytes int) string {
	buf, err := io.ReadAll(r)
	_ = err

	if len(buf) <= maxBytes {
		return string(buf)
	}

	return string(buf[:maxBytes]) + "...(truncated)"
}
