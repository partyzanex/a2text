package grpc_test

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"
	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	infragrpc "github.com/partyzanex/a2text/internal/infra/grpc"
	a2textv1 "github.com/partyzanex/a2text/pkg/proto/a2text/v1"
)

// stubKeyboard satisfies a2textv1.KeyboardServiceServer through the
// generated Unimplemented mixin — every method returns Unimplemented.
// The transport tests only need the registration plumbing to compile
// and route; the actual behaviour is covered by the adapter suites.
type stubKeyboard struct {
	a2textv1.UnimplementedKeyboardServiceServer
}

// stubSecret is the SecretServiceServer counterpart of stubKeyboard.
type stubSecret struct {
	a2textv1.UnimplementedSecretServiceServer
}

// ServerSuite covers internal/infra/grpc.Server: listener binding,
// graceful shutdown, the Serve-before-Listen guard, and the
// single-client transport guard that rejects every connection
// beyond the first.
type ServerSuite struct {
	suite.Suite

	srv    *infragrpc.Server
	addr   string
	ctx    context.Context //nolint:containedctx // suite-scoped lifecycle
	cancel context.CancelFunc
	done   chan error
}

// SetupTest builds a fresh server bound to a kernel-assigned port
// on the loopback interface and starts Serve in a goroutine.
func (s *ServerSuite) SetupTest() {
	s.srv = infragrpc.NewServer(slog.New(slog.DiscardHandler), &stubKeyboard{}, &stubSecret{}, nil)

	s.ctx, s.cancel = context.WithCancel(context.Background())

	addr, err := s.srv.Listen(s.ctx, "127.0.0.1:0")
	s.Require().NoError(err)
	s.addr = addr

	s.done = make(chan error, 1)
	go func() {
		s.done <- s.srv.Serve(s.ctx)
	}()

	s.waitServerReady()
}

// TearDownTest cancels the server context, waits for Serve to
// return, and falls back to Close() if graceful shutdown takes too
// long.
func (s *ServerSuite) TearDownTest() {
	s.cancel()

	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		_ = s.srv.Close()
	}
}

// TestListen_ReturnsLoopbackAddress verifies the bound address is on
// the IPv4 loopback interface (no accidental wildcard binding).
func (s *ServerSuite) TestListen_ReturnsLoopbackAddress() {
	s.Require().True(
		strings.HasPrefix(s.addr, "127.0.0.1:"),
		"bound address %q must be on the IPv4 loopback", s.addr,
	)
}

// TestSingleClientGuard_FirstClientPassesThrough verifies the very
// first connection's RPCs reach the underlying handler — a stubbed
// Unimplemented return rather than the guard's AlreadyExists.
func (s *ServerSuite) TestSingleClientGuard_FirstClientPassesThrough() {
	conn := s.dial()
	defer func() { _ = conn.Close() }()

	client := a2textv1.NewKeyboardServiceClient(conn)
	_, err := client.Inject(s.ctx, &a2textv1.InjectRequest{InjectToken: "t"})

	s.Require().Error(err)
	s.Equal(codes.Unimplemented, status.Code(err), "first client must reach handler, got %v", err)
}

// TestSingleClientGuard_SecondClientRejected verifies that while the
// first connection is alive, a second connection's RPCs are blocked
// by the guard with AlreadyExists.
func (s *ServerSuite) TestSingleClientGuard_SecondClientRejected() {
	conn1 := s.dial()
	defer func() { _ = conn1.Close() }()

	client1 := a2textv1.NewKeyboardServiceClient(conn1)
	_, err := client1.Inject(s.ctx, &a2textv1.InjectRequest{InjectToken: "t"})
	s.Require().Error(err)
	s.Equal(codes.Unimplemented, status.Code(err))

	conn2 := s.dial()
	defer func() { _ = conn2.Close() }()

	client2 := a2textv1.NewKeyboardServiceClient(conn2)
	_, err = client2.Inject(s.ctx, &a2textv1.InjectRequest{InjectToken: "t"})

	s.Require().Error(err)
	s.Equal(codes.AlreadyExists, status.Code(err), "second client must be guarded, got %v", err)
}

// TestSingleClientGuard_PromotesAfterFirstDisconnect verifies the
// guard releases the active slot when the first connection ends so
// a fresh connection takes ownership.
func (s *ServerSuite) TestSingleClientGuard_PromotesAfterFirstDisconnect() {
	conn1 := s.dial()

	client1 := a2textv1.NewKeyboardServiceClient(conn1)
	_, err := client1.Inject(s.ctx, &a2textv1.InjectRequest{InjectToken: "t"})
	s.Require().Error(err)
	s.Equal(codes.Unimplemented, status.Code(err))

	_ = conn1.Close()

	// gRPC's stats.Handler delivers ConnEnd after a brief delay
	// because the server-side conn goroutine has to wind down.
	// Poll until the guard accepts a fresh connection.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn2 := s.dial()
		client2 := a2textv1.NewKeyboardServiceClient(conn2)

		_, err = client2.Inject(s.ctx, &a2textv1.InjectRequest{InjectToken: "t"})
		if status.Code(err) == codes.Unimplemented {
			_ = conn2.Close()

			return
		}

		_ = conn2.Close()

		time.Sleep(20 * time.Millisecond)
	}

	s.FailNow("second client never promoted after first disconnect")
}

// dial returns a new insecure gRPC client connection to the suite's
// server. Each call yields an independent TCP connection so the
// single-client guard sees distinct conn ids.
func (s *ServerSuite) dial() *googlegrpc.ClientConn {
	conn, err := googlegrpc.NewClient(s.addr, googlegrpc.WithTransportCredentials(insecure.NewCredentials()))
	s.Require().NoError(err)

	return conn
}

// waitServerReady polls the bound address until a TCP connection
// succeeds or a short deadline fires. Serve runs in its own
// goroutine, so the test must avoid racing the listener-accept
// transition.
func (s *ServerSuite) waitServerReady() {
	deadline := time.Now().Add(2 * time.Second)

	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", s.addr, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()

			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	s.FailNow("server never became ready at " + s.addr)
}

// TestServerSuite is the standard testify entry point for the
// suite-driven cases.
func TestServerSuite(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(ServerSuite))
}

// TestServeBeforeListenErrors covers the stand-alone error path
// outside the suite: a freshly constructed Server whose Listen has
// not been invoked must refuse Serve with errServeBeforeListen.
func TestServeBeforeListenErrors(t *testing.T) {
	t.Parallel()

	srv := infragrpc.NewServer(slog.New(slog.DiscardHandler), &stubKeyboard{}, &stubSecret{}, nil)

	err := srv.Serve(context.Background())
	if err == nil {
		t.Fatalf("Serve before Listen must error, got nil")
	}

	if !strings.Contains(err.Error(), "Serve called before Listen") {
		t.Fatalf("unexpected error message: %v", err)
	}
}
