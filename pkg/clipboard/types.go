package clipboard

import (
	"context"
	"errors"
)

// Snapshot captures one MIME-typed payload from the clipboard. Multi-type
// selections collapse to their primary type — the one listed first by
// wl-paste --list-types / xclip TARGETS — because wl-copy and xclip can
// only serve a single MIME-type per invocation (a new invocation replaces
// the selection owner). Round-tripping a full multi-type selection would
// require a long-running selection-owner process; outside the scope of
// this package.
type Snapshot struct {
	// MIME is the primary content type. Empty when Empty == true.
	MIME string
	// Data is the raw bytes returned by the clipboard utility for MIME.
	// May be nil for empty selections; callers must treat nil and zero
	// length identically.
	Data []byte
	// Empty signals that the clipboard held no usable content at snapshot
	// time. A reader returns Empty=true (and nil err) for empty selections
	// so the caller can branch cleanly without comparing strings/lengths.
	Empty bool
}

// ClipboardReader fetches the current clipboard contents. Implementations
// MUST be safe to call concurrently with clipboard writes by the same
// process — wl-paste/xclip launch their own subprocess so isolation is
// implicit.
//
//go:generate go run go.uber.org/mock/mockgen@latest -package=clipboard -destination=reader_mocks_test.go -source=types.go ClipboardReader
type ClipboardReader interface {
	Snapshot(ctx context.Context) (Snapshot, error)
}

// ErrNoBackend signals that no supported clipboard utility was found.
// The Output wrapper translates this into a stdout fallback with a
// WARN log so the user is not left guessing.
var ErrNoBackend = errors.New(
	"no clipboard backend found: install wl-clipboard (Wayland) or xclip/xsel (X11)",
)

// ErrNoAutopasteBackend signals that no supported autopaste binary was found
// in PATH, or that the current platform does not implement autopaste. The
// output wrapper translates this into a clipboard-only fallback with a WARN
// log so the user is not left guessing why autopaste did nothing.
//
// Distinct from ErrUnsupportedAutopasteBackend: "no backend" means "binary
// missing or platform unsupported"; "unsupported" means "config asked for a
// backend the daemon does not know". They require different fixes (apt install
// vs. edit yaml) and a single sentinel would force callers to parse messages.
var ErrNoAutopasteBackend = errors.New("clipboard: no autopaste backend")

// ErrEmptyMIME signals that a CopyTyped call was made without a MIME
// type. wl-copy/xclip cannot accept empty --type; surfacing this as a
// distinct sentinel makes the wiring bug obvious in logs.
var ErrEmptyMIME = errors.New("clipboard: empty MIME type")

// ErrUnsupportedAutopasteBackend signals that the configured autopaste backend
// name is not one the daemon knows about. Treated as a config error, not a
// missing dependency — surfaced separately so depcheck does not advertise an
// "install wtype" hint for a "wytpe" typo.
var ErrUnsupportedAutopasteBackend = errors.New("clipboard: unsupported autopaste backend")
