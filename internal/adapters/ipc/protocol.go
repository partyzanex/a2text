package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxMessageBytes caps the JSON payload size (newline excluded) we read
// off a single connection. The protocol's largest legitimate request is
// a few hundred bytes; 64KiB leaves comfortable headroom while preventing
// local DoS via unbounded allocations from a misbehaving local client.
const MaxMessageBytes = 64 * 1024

// messageBytesBudget adds 2 bytes to the max message size: 1 for the newline,
// 1 to detect when payload reaches exactly the limit.
const messageBytesBudget = MaxMessageBytes + 2

// Command is the verb the client wants the daemon to execute.
type Command string

const (
	// CmdToggle is the primary command: idle→recording or
	// recording→transcribing. Sent when the user invokes `a2text` with no
	// arguments (typically from a GNOME custom shortcut).
	CmdToggle Command = "toggle"

	// CmdStart explicitly enters recording. Used by scripts and hidden
	// dev modes that need start/stop split rather than a single toggle.
	CmdStart Command = "start"

	// CmdStop explicitly leaves recording, kicking transcription.
	CmdStop Command = "stop"

	// CmdPing returns immediately with the current state. Used by the
	// self-bootstrap path to decide "is a daemon already running?".
	CmdPing Command = "ping"
)

// Request is what the client sends. JSON over a single connection, one
// request per connection — no streaming, no multiplexing.
type Request struct {
	// Version is the wire format the client speaks. Server rejects
	// versions outside [MinSupportedVersion, ProtocolVersion].
	Version int `json:"version"`

	// ID is a client-generated correlation token. Server echoes it back
	// in Response. We require non-empty so debug grep through journal
	// works ("which request produced this error?").
	ID string `json:"id"`

	// Command is the verb. Unknown commands → ok=false with a descriptive
	// message rather than a connection-level error.
	Command Command `json:"command"`

	// Payload is reserved for future per-command data. Ignored at v=1.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Response is what the daemon sends back exactly once before closing the
// connection.
type Response struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	OK      bool   `json:"ok"`

	// State is the daemon state at the moment the response was produced.
	// Populated for daemon-handled responses (use NewResponseFor in new
	// code to never forget it). Server-level protocol errors that fire
	// before dispatch (decode failure, version mismatch on a stranger)
	// MAY leave State empty — the client treats "" as "unknown".
	State string `json:"state"`

	// Message is human-readable, English. Empty on success unless the
	// daemon wants to surface a hint.
	Message string `json:"message,omitempty"`

	// LastError is the message attached to the most recent transition into
	// the error state, if any. Useful for `ping` to surface why the daemon
	// is stuck.
	LastError string `json:"last_error,omitempty"`

	// ErrorCode is a machine-readable failure category. Empty on OK
	// responses; one of the ErrCode* constants when OK is false. Lets the
	// client errors.Is to a sentinel without parsing Message strings.
	ErrorCode string `json:"error_code,omitempty"`
}

// Error codes carried in Response.ErrorCode. Stable across protocol
// versions: clients pin against these strings, not against Message.
const (
	ErrCodeVersionMismatch = "ipc_version_mismatch"
	ErrCodeUnknownCommand  = "ipc_unknown_command"
	ErrCodeDecodeFailed    = "ipc_decode_failed"
	ErrCodeBusy            = "voice_busy"
)

// Encode writes one request and a trailing newline. We use newline as
// frame delimiter so debugging with `socat` is trivial.
func Encode(w io.Writer, v any) error {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return fmt.Errorf("ipc: encode: %w", err)
	}

	return nil
}

// Decode reads exactly one newline-terminated JSON message from r and
// unmarshals it into dst. Messages MUST be single-line JSON; unterminated
// input is rejected. MaxMessageBytes bounds the JSON payload (excluding
// '\n') so a misbehaving local client cannot trigger unbounded allocation.
func Decode(r io.Reader, dst any) error {
	line, err := readLine(r)
	if err != nil {
		return err
	}

	if err := json.Unmarshal(line, dst); err != nil {
		return fmt.Errorf("ipc: decode: %w", err)
	}

	return nil
}

// readLine reads a single newline-terminated line from r, bounded by
// MaxMessageBytes. Returns the line without the trailing '\n'.
func readLine(r io.Reader) ([]byte, error) {
	br := bufio.NewReader(io.LimitReader(r, int64(messageBytesBudget)))

	line, err := br.ReadBytes('\n')

	switch {
	case err == nil:
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
		}
	case errors.Is(err, io.EOF) && len(line) > 0:
	case errors.Is(err, io.EOF):
		return nil, fmt.Errorf("ipc: read message: %w", io.ErrUnexpectedEOF)
	default:
		return nil, fmt.Errorf("ipc: read message: %w", err)
	}

	return validateLine(line, err)
}

// validateLine checks the decoded line against protocol constraints.
func validateLine(line []byte, readErr error) ([]byte, error) {
	if len(line) > MaxMessageBytes {
		return nil, fmt.Errorf("ipc: message exceeds %d bytes", MaxMessageBytes)
	}

	if readErr != nil && errors.Is(readErr, io.EOF) {
		return nil, errors.New("ipc: read message: missing newline terminator")
	}

	return line, nil
}

// IsKnownCommand reports whether c is a command the daemon recognises at
// this protocol version. Used for early rejection before dispatch.
func IsKnownCommand(c Command) bool {
	switch c {
	case CmdToggle, CmdStart, CmdStop, CmdPing:
		return true
	}

	return false
}

// NewResponseFor constructs a Response prefilled with the protocol fields
// every reply must carry: Version and the request's ID. State is required
// from the caller — the choice of "what state are we in?" is contextual.
//
// Callers that already populate Version/ID by hand can keep doing so;
// this helper exists so future call sites do not silently forget to echo
// the ID, which the client uses for correlation.
func NewResponseFor(req Request, state string) Response {
	return Response{
		Version: ProtocolVersion,
		ID:      req.ID,
		State:   state,
	}
}
