package ipc

import "errors"

// IPC-protocol sentinels. Kept next to the wire format so changes to
// protocol semantics surface in the same package.
var (
	// ErrVersionMismatch is returned when client and server speak different
	// protocol versions. The CLI prints the two versions so the user knows
	// which side to upgrade.
	ErrVersionMismatch = errors.New("voice: ipc protocol version mismatch")

	// ErrUnknownCommand is returned by the client when the daemon rejected
	// a command it did not recognise (typically a CLI invoking a verb the
	// daemon is too old to understand). Distinct from version-mismatch:
	// here the wire format agrees, only the command vocabulary disagrees.
	ErrUnknownCommand = errors.New("voice: ipc unknown command")

	// ErrDecodeFailed is returned by the client when the daemon failed to
	// decode the request. Almost always a programmer bug (malformed JSON,
	// missing required field) — surfaced as a typed error so tests and
	// scripts can assert on it rather than grepping Message text.
	ErrDecodeFailed = errors.New("voice: ipc decode failed")
)
