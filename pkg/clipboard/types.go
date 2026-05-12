package clipboard

import "errors"

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

// ErrUnsupportedAutopasteBackend signals that the configured autopaste backend
// name is not one the daemon knows about. Treated as a config error, not a
// missing dependency — surfaced separately so depcheck does not advertise an
// "install wtype" hint for a "wytpe" typo.
var ErrUnsupportedAutopasteBackend = errors.New("clipboard: unsupported autopaste backend")
