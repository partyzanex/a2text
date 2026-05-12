package ipc_test

import (
	"context"
	"log/slog"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/partyzanex/a2text/internal/adapters/ipc"
	"github.com/partyzanex/a2text/internal/domain"
)

type IPCSuite struct {
	suite.Suite
}

func TestIPCSuite(t *testing.T) {
	suite.Run(t, new(IPCSuite))
}

// --- Round-trip: client → server → handler → response ---

func (s *IPCSuite) TestPing_RoundTrip() {
	socketPath := s.tempSocket()

	server := s.startServer(socketPath, ipc.HandlerFunc(func(_ context.Context, req ipc.Request) ipc.Response {
		s.Equal(ipc.CmdPing, req.Command)

		return ipc.Response{OK: true, State: "idle"}
	}))
	defer func() { _ = server.Shutdown() }()

	client := ipc.NewClient(socketPath, time.Second)

	resp, err := client.Ping(context.Background())
	s.Require().NoError(err)
	s.True(resp.OK)
	s.Equal("idle", resp.State)
	s.Equal(ipc.ProtocolVersion, resp.Version)
	s.NotEmpty(resp.ID, "server must echo client-generated ID")
}

func (s *IPCSuite) TestToggle_HandlerSeesCommand() {
	socketPath := s.tempSocket()

	var seen atomic.Value

	server := s.startServer(socketPath, ipc.HandlerFunc(func(_ context.Context, req ipc.Request) ipc.Response {
		seen.Store(req.Command)

		return ipc.Response{OK: true, State: "recording"}
	}))
	defer func() { _ = server.Shutdown() }()

	client := ipc.NewClient(socketPath, time.Second)

	resp, err := client.Toggle(context.Background())
	s.Require().NoError(err)
	s.True(resp.OK)
	s.Equal("recording", resp.State)

	s.Equal(ipc.CmdToggle, seen.Load())
}

func (s *IPCSuite) TestRequestID_EchoedBack() {
	socketPath := s.tempSocket()

	server := s.startServer(socketPath, ipc.HandlerFunc(func(_ context.Context, _ ipc.Request) ipc.Response {
		return ipc.Response{OK: true, State: "idle"}
	}))
	defer func() { _ = server.Shutdown() }()

	client := ipc.NewClient(socketPath, time.Second)

	resp, err := client.Ping(context.Background())
	s.Require().NoError(err)
	s.NotEmpty(resp.ID, "server must echo client-generated ID")
}

// --- domain.ErrDaemonNotRunning when socket is missing ---

func (s *IPCSuite) TestPing_NoSocket_ReturnsErrDaemonNotRunning() {
	client := ipc.NewClient(filepath.Join(s.T().TempDir(), "nope.sock"), 200*time.Millisecond)

	_, err := client.Ping(context.Background())
	s.Require().Error(err)
	s.Require().ErrorIs(err, domain.ErrDaemonNotRunning)
}

// --- Version mismatch handling ---

func (s *IPCSuite) TestServer_RejectsTooNewClient() {
	socketPath := s.tempSocket()

	called := atomic.Bool{}

	server := s.startServer(socketPath, ipc.HandlerFunc(func(_ context.Context, _ ipc.Request) ipc.Response {
		called.Store(true)

		return ipc.Response{OK: true}
	}))
	defer func() { _ = server.Shutdown() }()

	resp, err := s.sendRaw(socketPath, ipc.Request{
		Version: ipc.ProtocolVersion + 999,
		ID:      "test-1",
		Command: ipc.CmdPing,
	})
	s.Require().NoError(err)
	s.False(resp.OK)
	s.Equal(ipc.ErrCodeVersionMismatch, resp.ErrorCode)
	s.False(called.Load(), "handler must NOT see version-mismatched requests")
}

func (s *IPCSuite) TestServer_RejectsTooOldClient() {
	socketPath := s.tempSocket()

	server := s.startServer(socketPath, ipc.HandlerFunc(func(_ context.Context, _ ipc.Request) ipc.Response {
		return ipc.Response{OK: true}
	}))
	defer func() { _ = server.Shutdown() }()

	resp, err := s.sendRaw(socketPath, ipc.Request{
		Version: 0,
		ID:      "test-1",
		Command: ipc.CmdPing,
	})
	s.Require().NoError(err)
	s.False(resp.OK)
	s.Equal(ipc.ErrCodeVersionMismatch, resp.ErrorCode)
}

// --- Unknown command rejected with ok=false ---

func (s *IPCSuite) TestServer_RejectsUnknownCommand() {
	socketPath := s.tempSocket()

	called := atomic.Bool{}

	server := s.startServer(socketPath, ipc.HandlerFunc(func(_ context.Context, _ ipc.Request) ipc.Response {
		called.Store(true)

		return ipc.Response{OK: true}
	}))
	defer func() { _ = server.Shutdown() }()

	resp, err := s.sendRaw(socketPath, ipc.Request{
		Version: ipc.ProtocolVersion,
		ID:      "test-1",
		Command: ipc.Command("eat-cake"),
	})
	s.Require().NoError(err)
	s.False(resp.OK)
	s.Equal(ipc.ErrCodeUnknownCommand, resp.ErrorCode)
	s.False(called.Load(), "handler must NOT see unknown commands")
}

// --- ID correlation: client rejects mismatched response IDs ---
//
// The well-behaved Server always echoes req.ID into resp.ID, so to test
// the client's mismatch defence we need a hand-rolled server that ignores
// that contract.

func (s *IPCSuite) TestClient_RejectsIDMismatch() {
	socketPath := s.tempSocket()

	listener, err := net.Listen("unix", socketPath)
	s.Require().NoError(err)

	defer func() { _ = listener.Close() }()

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}

		defer func() { _ = conn.Close() }()

		// Drain request.
		var req ipc.Request

		_ = ipc.Decode(conn, &req)

		// Reply with a deliberately wrong ID.
		_ = ipc.Encode(conn, ipc.Response{
			Version: ipc.ProtocolVersion,
			ID:      "wrong-id",
			OK:      true,
			State:   "idle",
		})
	}()

	client := ipc.NewClient(socketPath, time.Second)

	_, err = client.Ping(context.Background())
	s.Require().Error(err)
	s.Contains(err.Error(), "id mismatch")
}

// --- Empty socket path is rejected before dial ---

func (s *IPCSuite) TestClient_EmptySocketPath_ReturnsErrEmptySocketPath() {
	client := ipc.NewClient("", time.Second)

	_, err := client.Ping(context.Background())
	s.Require().Error(err)
	s.Require().ErrorIs(err, ipc.ErrEmptySocketPath)
}

// --- IsKnownCommand ---

func (s *IPCSuite) TestIsKnownCommand() {
	for _, c := range []ipc.Command{ipc.CmdPing, ipc.CmdToggle, ipc.CmdStart, ipc.CmdStop} {
		s.True(ipc.IsKnownCommand(c), "%s must be known", c)
	}

	s.False(ipc.IsKnownCommand(ipc.Command("garbage")))
}

// --- Helpers ---

func (s *IPCSuite) tempSocket() string {
	return filepath.Join(s.T().TempDir(), "ipc.sock")
}

func (s *IPCSuite) startServer(path string, h ipc.Handler) *ipc.Server {
	srv, err := ipc.NewServer(context.Background(), path, h, slog.New(slog.DiscardHandler))
	s.Require().NoError(err)

	go func() {
		_ = srv.Serve(context.Background())
	}()

	s.waitSocketReady(path)

	return srv
}

// waitSocketReady polls the unix socket with short dials until one
// succeeds, replacing a brittle time.Sleep(5ms). The first successful
// dial is closed immediately — IPC handlers tolerate clients that
// disconnect without writing.
func (s *IPCSuite) waitSocketReady(path string) {
	deadline := time.Now().Add(time.Second)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", path, 10*time.Millisecond)
		if err == nil {
			_ = conn.Close()

			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	s.T().Fatalf("socket %q did not become ready within deadline", path)
}

// sendRaw bypasses Client to inject arbitrary Request values (e.g. wrong
// versions, unknown commands) that the public Client API would never send.
func (s *IPCSuite) sendRaw(socketPath string, req ipc.Request) (ipc.Response, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return ipc.Response{}, err
	}

	defer func() { _ = conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(time.Second))

	if err := ipc.Encode(conn, req); err != nil {
		return ipc.Response{}, err
	}

	var resp ipc.Response
	if err := ipc.Decode(conn, &resp); err != nil {
		return ipc.Response{}, err
	}

	return resp, nil
}
