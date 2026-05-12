package ipc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/partyzanex/a2text/internal/domain"
)

// Client is a short-lived unix-socket client. Each call opens a connection,
// sends one request, reads one response, closes. No connection pooling: the
// daemon expects single-shot interactions.
type Client struct {
	socketPath string
	timeout    time.Duration
}

// ErrEmptySocketPath is returned when a Client method is invoked on a
// Client whose socketPath is empty. Distinct from domain.ErrDaemonNotRunning
// because "you forgot to wire the path" deserves a louder message than
// "daemon is offline".
var ErrEmptySocketPath = errors.New("ipc: empty socket path")

const defaultClientTimeout = 5 * time.Second

// NewClient returns a client bound to socketPath. Call Ping/Toggle/etc to
// perform operations; the client itself holds no resources.
func NewClient(socketPath string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultClientTimeout
	}

	return &Client{socketPath: socketPath, timeout: timeout}
}

// Ping is the cheapest operation: returns the daemon's current state with
// no side effect. Used by the self-bootstrap path to detect "is a daemon
// already up?" — connection refused / socket missing is mapped to
// domain.ErrDaemonNotRunning so the bootstrap caller can become the daemon
// without printing a confusing error.
func (c *Client) Ping(ctx context.Context) (Response, error) {
	return c.send(ctx, Request{Command: CmdPing})
}

// Toggle is the canonical hotkey action: idle→recording or
// recording→transcribing depending on daemon state.
func (c *Client) Toggle(ctx context.Context) (Response, error) {
	return c.send(ctx, Request{Command: CmdToggle})
}

// Start explicitly enters recording. Useful for scripts.
func (c *Client) Start(ctx context.Context) (Response, error) {
	return c.send(ctx, Request{Command: CmdStart})
}

// Stop explicitly leaves recording.
func (c *Client) Stop(ctx context.Context) (Response, error) {
	return c.send(ctx, Request{Command: CmdStop})
}

func (c *Client) send(ctx context.Context, req Request) (Response, error) {
	if err := c.prepareRequest(&req); err != nil {
		return Response{}, err
	}

	return c.dialAndCommunicate(ctx, req)
}

// prepareRequest initializes Version and ID fields of the request.
func (c *Client) prepareRequest(req *Request) error {
	if c.socketPath == "" {
		return ErrEmptySocketPath
	}

	req.Version = ProtocolVersion

	if req.ID == "" {
		id, err := newRequestID()
		if err != nil {
			return err
		}

		req.ID = id
	}

	return nil
}

// dialAndCommunicate establishes connection and exchanges request/response.
func (c *Client) dialAndCommunicate(ctx context.Context, req Request) (Response, error) {
	dialer := net.Dialer{Timeout: c.timeout}

	dialCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	conn, err := dialer.DialContext(dialCtx, "unix", c.socketPath)
	if err != nil {
		if isDaemonAbsent(err) {
			return Response{}, domain.ErrDaemonNotRunning
		}

		return Response{}, fmt.Errorf("ipc: dial %q: %w", c.socketPath, err)
	}

	defer func() {
		if err := conn.Close(); err != nil {
			_ = err
		}
	}()

	if err := conn.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		return Response{}, fmt.Errorf("ipc: set deadline: %w", err)
	}

	if err := Encode(conn, req); err != nil {
		return Response{}, fmt.Errorf("ipc: encode request: %w", err)
	}

	var resp Response
	if err := Decode(conn, &resp); err != nil {
		return Response{}, fmt.Errorf("ipc: decode response: %w", err)
	}

	if err := validateResponse(&resp, &req); err != nil {
		return resp, err
	}

	return resp, nil
}

func validateResponse(resp *Response, req *Request) error {
	// Version check: the server we shipped always populates Version, so a
	// mismatch means either a future daemon (downgrade) or a malformed
	// response. Surface as a typed error before the OK check so a "good
	// looking" response with the wrong wire format cannot pass through.
	if resp.Version != ProtocolVersion {
		return fmt.Errorf(
			"%w: response version %d, want %d",
			ErrVersionMismatch, resp.Version, ProtocolVersion,
		)
	}

	// ID correlation: protocol echoes the client-generated ID. A mismatch
	// usually means we're talking to a different daemon (rare on a single
	// per-user socket, but possible if a stale handler reordered responses
	// somehow). Always verify so the test exists for free.
	if resp.ID != req.ID {
		return fmt.Errorf("ipc: response id mismatch: got %q, want %q", resp.ID, req.ID)
	}

	if !resp.OK {
		err := mapErrorCode(resp)

		return err
	}

	return nil
}

// mapErrorCode translates a non-OK Response into the typed sentinel error
// callers can errors.Is against. Extracted from send() to keep the dial /
// encode / decode pipeline readable; the switch is the only piece that
// needs to stay in lockstep with protocol.go's ErrCode* constants.
//
// Unknown codes fall through to a generic error so a future-added code is
// at least not silently swallowed as success.
func mapErrorCode(resp *Response) error {
	switch resp.ErrorCode {
	case ErrCodeVersionMismatch:
		return fmt.Errorf("%w: %s", ErrVersionMismatch, resp.Message)
	case ErrCodeBusy:
		return fmt.Errorf("%w: %s", domain.ErrBusy, resp.Message)
	case ErrCodeUnknownCommand:
		return fmt.Errorf("%w: %s", ErrUnknownCommand, resp.Message)
	case ErrCodeDecodeFailed:
		return fmt.Errorf("%w: %s", ErrDecodeFailed, resp.Message)
	case "":
		// Daemon set OK=false without populating ErrorCode. Treat as
		// generic rejection — the Message field carries detail.
		return fmt.Errorf("ipc: daemon rejected request: %s", resp.Message)
	default:
		// Forward-compat: unknown code from a newer daemon. Don't pretend
		// success; surface the raw code so logs and tests spot the drift.
		return fmt.Errorf("ipc: daemon rejected request (code=%s): %s", resp.ErrorCode, resp.Message)
	}
}

// isDaemonAbsent reports whether dial err means "no daemon running here".
// Distinct from "the socket exists but the daemon is hung", which we want
// to surface as a real error.
//
// Detection chain:
//  1. errors.Is(err, os.ErrNotExist) — covers *os.PathError wraps from the
//     net stack when the socket file is missing.
//  2. errors.Is(err, syscall.ENOENT/ECONNREFUSED) — direct syscall errors
//     after explicit unwrap.
//  3. String fallback as last resort for older Go versions where wrapping
//     does not propagate cleanly through *net.OpError. Match patterns are
//     tight enough that they won't false-positive on unrelated errors.
func isDaemonAbsent(err error) bool {
	if errors.Is(err, os.ErrNotExist) {
		return true
	}

	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}

	msg := err.Error()
	if strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "connection refused") {
		return true
	}

	return false
}

// newRequestID generates a short random correlation ID. Not a UUID — just
// 8 hex bytes, plenty unique for one user's local socket traffic.
//
// crypto/rand.Read failing is essentially "the entropy pool is broken or
// /dev/urandom is unavailable" — extremely rare, but if we silently
// returned a string of zeros we'd produce indistinguishable IDs and
// hide the failure. Surfacing the error here is cheap and honest.
func newRequestID() (string, error) {
	var b [8]byte

	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("ipc: generate request id: %w", err)
	}

	return hex.EncodeToString(b[:]), nil
}
