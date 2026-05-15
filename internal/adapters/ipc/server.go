package ipc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"
)

// Handler is the daemon-supplied callback that converts a Command into a
// Response. It is invoked once per accepted connection. The handler runs
// inside a per-connection goroutine; the daemon is responsible for
// thread-safety on its state machine (Machine.Apply already is).
//
// The handler must NOT close the connection — the server owns the lifecycle.
// On failure, return ok=false and a Message; never panic.
type Handler interface {
	Handle(ctx context.Context, req Request) Response
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, req Request) Response

func (f HandlerFunc) Handle(ctx context.Context, req Request) Response {
	return f(ctx, req)
}

// Server accepts unix-socket connections and dispatches one Request →
// Response per connection.
//
// Construction binds the socket; Serve runs the accept loop; Shutdown
// releases the socket file. The lifecycle is split so the daemon can
// register the socket cleanup with the global shutdown manager before
// entering the accept loop.
type Server struct {
	listener     net.Listener
	log          *slog.Logger
	handler      Handler
	shutdownErr  error
	socketPath   string
	connWG       sync.WaitGroup
	shutdownOnce sync.Once
}

// NewServer binds a unix socket at socketPath. If a stale socket file
// exists, it is removed first — daemon startup races flock-against-PID,
// so by the time we get here we already know no other daemon owns the
// socket. We refuse to remove anything that is not actually a unix socket
// to avoid accidentally deleting an unrelated file with a similar path.
func NewServer(ctx context.Context, socketPath string, handler Handler, log *slog.Logger) (*Server, error) {
	if handler == nil {
		return nil, errors.New("ipc: handler is required")
	}

	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	if err := cleanupStaleSocket(socketPath); err != nil {
		return nil, err
	}

	var lc net.ListenConfig

	listener, err := lc.Listen(ctx, "unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("ipc: listen %q: %w", socketPath, err)
	}

	const socketPermission = 0o600
	if chmodErr := os.Chmod(socketPath, socketPermission); chmodErr != nil {
		if closeErr := listener.Close(); closeErr != nil {
			log.Warn("ipc: failed to close listener during initialization",
				slog.String("err", closeErr.Error()),
			)
		}

		if rmErr := os.Remove(socketPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			log.Warn("ipc: failed to remove socket during initialization",
				slog.String("path", socketPath),
				slog.String("err", rmErr.Error()),
			)
		}

		return nil, fmt.Errorf("ipc: chmod socket %q: %w", socketPath, chmodErr)
	}

	return &Server{
		socketPath: socketPath,
		listener:   listener,
		log:        log,
		handler:    handler,
	}, nil
}

// cleanupStaleSocket removes any non-socket file at socketPath.
func cleanupStaleSocket(socketPath string) error {
	info, statErr := os.Lstat(socketPath)
	if statErr != nil {
		// Path doesn't exist, no cleanup needed
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}

		return fmt.Errorf("ipc: stat socket path %q: %w", socketPath, statErr)
	}

	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf(
			"ipc: refusing to remove non-socket path %q (mode=%s)",
			socketPath, info.Mode(),
		)
	}

	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("ipc: remove stale socket %q: %w", socketPath, err)
	}

	return nil
}

// SocketPath returns the path the listener is bound to. Used by the daemon
// for sd_notify status and depcheck output.
func (s *Server) SocketPath() string {
	return s.socketPath
}

// Serve runs the accept loop until ctx is done or Shutdown is called.
// A goroutine watches ctx; on cancel it triggers Shutdown which closes
// the listener and unblocks Accept. Returns nil on a clean shutdown.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()

		if shutdownErr := s.Shutdown(); shutdownErr != nil {
			s.log.Debug("ipc: shutdown error",
				slog.String("err", shutdownErr.Error()),
			)
		}
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}

			return fmt.Errorf("ipc: accept: %w", err)
		}

		s.connWG.Add(1)

		go s.handleConn(ctx, conn)
	}
}

// Shutdown closes the listener, waits for in-flight connections (bounded
// by per-connection deadlines), and removes the socket file. Idempotent
// via sync.Once: the ctx-cancel watcher in Serve and an outside Shutdown
// call may both fire — second invocation returns the cached first result.
func (s *Server) Shutdown() error {
	s.shutdownOnce.Do(func() {
		closeErr := s.listener.Close()

		// Wait for in-flight connections to finish, but do not wait forever:
		// if a handler goroutine is stuck, we still want to remove the socket
		// so that subsequent daemon invocations can bind the path cleanly.
		done := make(chan struct{})

		go func() { s.connWG.Wait(); close(done) }()

		select {
		case <-done:
		case <-time.After(connDeadline + time.Second):
			s.log.Warn("ipc: timed out waiting for in-flight connections during shutdown")
		}

		if rmErr := os.Remove(s.socketPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
			s.log.Warn("ipc: failed to remove socket on shutdown",
				slog.String("path", s.socketPath),
				slog.String("err", rmErr.Error()),
			)
		}

		if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			s.shutdownErr = fmt.Errorf("ipc: close listener: %w", closeErr)
		}
	})

	return s.shutdownErr
}

// connDeadline caps how long a single client connection can occupy the
// daemon. Toggle requests should be sub-millisecond; 5s leaves room for
// ping while never letting a stuck client wedge the accept loop.
const connDeadline = 5 * time.Second

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer s.connWG.Done()
	defer func() {
		if err := conn.Close(); err != nil {
			_ = err
		}
	}()

	if err := conn.SetDeadline(time.Now().Add(connDeadline)); err != nil {
		s.log.Warn("ipc: set deadline failed", slog.String("err", err.Error()))

		return
	}

	var req Request
	if err := Decode(conn, &req); err != nil {
		s.encodeErrorResponse(conn, &Response{
			Version:   ProtocolVersion,
			OK:        false,
			ErrorCode: ErrCodeDecodeFailed,
			Message:   "decode request: " + err.Error(),
		}, "decode error")

		return
	}

	if validationResp := s.validateRequest(req); validationResp != nil {
		s.encodeErrorResponse(conn, validationResp, "validation error")

		return
	}

	resp := s.handler.Handle(ctx, req)
	resp.Version = ProtocolVersion
	resp.ID = req.ID

	if err := Encode(conn, resp); err != nil {
		s.log.Warn("ipc: write response failed",
			slog.String("id", req.ID),
			slog.String("err", err.Error()),
		)
	}
}

// validateRequest checks version, ID, and command. Returns nil if valid.
func (s *Server) validateRequest(req Request) *Response {
	if resp, ok := versionCheck(req); !ok {
		return &resp
	}

	if req.ID == "" {
		return &Response{
			Version:   ProtocolVersion,
			OK:        false,
			ErrorCode: ErrCodeDecodeFailed,
			Message:   "missing request id (correlation field is required)",
		}
	}

	if !IsKnownCommand(req.Command) {
		resp := NewResponseFor(req, "")
		resp.OK = false
		resp.ErrorCode = ErrCodeUnknownCommand
		resp.Message = fmt.Sprintf("unknown command %q", req.Command)

		return &resp
	}

	return nil
}

// encodeErrorResponse encodes and sends an error response, logging encode failures.
func (s *Server) encodeErrorResponse(conn net.Conn, resp *Response, ctx string) {
	if encErr := Encode(conn, *resp); encErr != nil {
		s.log.Debug("ipc: failed to encode "+ctx+" response",
			slog.String("err", encErr.Error()),
		)
	}
}

// versionCheck enforces the supported version range. Returns (response, ok)
// where ok=false means the request must be rejected with the response.
//
// Clients identify version-mismatch by ErrorCode == ErrCodeVersionMismatch,
// not by parsing Message — that's the whole point of the code field.
func versionCheck(req Request) (Response, bool) {
	if req.Version > ProtocolVersion {
		resp := NewResponseFor(req, "")
		resp.OK = false
		resp.ErrorCode = ErrCodeVersionMismatch
		resp.Message = fmt.Sprintf(
			"client too new (v=%d), daemon supports up to v=%d",
			req.Version, ProtocolVersion,
		)

		return resp, false
	}

	if req.Version < MinSupportedVersion {
		resp := NewResponseFor(req, "")
		resp.OK = false
		resp.ErrorCode = ErrCodeVersionMismatch
		resp.Message = fmt.Sprintf(
			"client too old (v=%d), daemon requires at least v=%d",
			req.Version, MinSupportedVersion,
		)

		return resp, false
	}

	return Response{}, true
}
