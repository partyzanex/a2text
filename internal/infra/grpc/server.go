// Package grpc is the gRPC transport wiring for a2textd. It owns the
// loopback listener and the underlying *grpc.Server, and registers
// the wire-service adapters created at bootstrap. The service-level
// logic lives in internal/adapters/grpc/server.
//
// mTLS is optional: when NewServer is given a non-nil *tls.Config
// the server requires mTLS and rejects plaintext / untrusted-client
// handshakes at the transport layer. A nil config keeps the server
// in plaintext mode for local development.
//
// The external "google.golang.org/grpc" library is imported under
// the alias `googlegrpc` to avoid colliding with this package's own
// name.
package grpc

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	googlegrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/status"

	a2textv1 "github.com/partyzanex/a2text/pkg/proto/a2text/v1"
)

// errServeBeforeListen is returned when Serve is called on a Server
// whose Listen has not been invoked (or has failed).
var errServeBeforeListen = errors.New("grpc: Serve called before Listen")

// Server is the infrastructure-side gRPC server. It binds a listener,
// owns the grpc.Server, and registers the wire-service adapters.
type Server struct {
	log *slog.Logger

	grpc     *googlegrpc.Server
	listener net.Listener
}

// NewServer constructs a Server and registers the supplied adapters
// against the underlying grpc.Server. The adapters are taken as
// interface types from the generated proto package so this layer
// never imports the concrete adapter implementations directly.
//
// A single-client guard is installed on every Server: the first
// incoming connection becomes the owner and gets every RPC handled
// normally; every other connection has its RPCs rejected with
// AlreadyExists. This enforces the "one UI per daemon, ever"
// invariant at the transport layer.
//
// tlsConfig, when non-nil, switches the server to mTLS mode. The
// caller is responsible for populating Certificates, ClientCAs and
// setting ClientAuth = RequireAndVerifyClientCert. A nil value keeps
// the server in plaintext mode (loopback dev only).
func NewServer(
	log *slog.Logger,
	keyboard a2textv1.KeyboardServiceServer,
	secret a2textv1.SecretServiceServer,
	tlsConfig *tls.Config,
) *Server {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	guard := &clientGuard{}

	opts := []googlegrpc.ServerOption{
		googlegrpc.StatsHandler(guard),
		googlegrpc.UnaryInterceptor(guard.unary),
		googlegrpc.StreamInterceptor(guard.stream),
	}

	if tlsConfig != nil {
		opts = append(opts, googlegrpc.Creds(credentials.NewTLS(tlsConfig)))
		log.Info("grpc: mTLS enabled",
			slog.Int("server_certs", len(tlsConfig.Certificates)),
		)
	} else {
		log.Warn("grpc: mTLS disabled — server running in plaintext (loopback dev only)")
	}

	srv := googlegrpc.NewServer(opts...)
	a2textv1.RegisterKeyboardServiceServer(srv, keyboard)
	a2textv1.RegisterSecretServiceServer(srv, secret)

	return &Server{
		log:  log,
		grpc: srv,
	}
}

// Listen binds the gRPC listener to addr (e.g. "127.0.0.1:0" for a
// kernel-assigned port) and returns the actual bound address so the
// caller can advertise it via the port-discovery file. It does NOT
// start serving — call Serve afterwards.
func (s *Server) Listen(ctx context.Context, addr string) (string, error) {
	var lc net.ListenConfig

	lis, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("grpc listen %q: %w", addr, err)
	}

	s.listener = lis

	bound := lis.Addr().String()
	s.log.Info("grpc bound",
		slog.String("address", bound),
	)

	return bound, nil
}

// Serve blocks until ctx is cancelled or the underlying server
// stops. It must be called after a successful Listen.
func (s *Server) Serve(ctx context.Context) error {
	if s.grpc == nil || s.listener == nil {
		return errServeBeforeListen
	}

	errCh := make(chan error, 1)

	go func() {
		errCh <- s.grpc.Serve(s.listener)
	}()

	select {
	case <-ctx.Done():
		s.log.Info("grpc: context done, initiating graceful stop")
		s.grpc.GracefulStop()

		return nil
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("grpc serve: %w", err)
		}

		return nil
	}
}

// Close stops the gRPC server immediately. Intended for use from the
// shutdown manager (partyzanex/shutdown) when graceful stop is not
// appropriate.
func (s *Server) Close() error {
	if s.grpc != nil {
		s.grpc.Stop()
	}

	return nil
}

// --- single-client guard ----------------------------------------------------
//
// The guard enforces "one client at a time" at the gRPC transport
// layer. The first TCP connection to reach the listener claims the
// active-owner slot in HandleConn(ConnBegin); subsequent connections
// keep their own per-conn id and have every RPC rejected with
// AlreadyExists by the interceptors. When the owner disconnects the
// slot is freed in HandleConn(ConnEnd) so the next reconnect can
// take ownership.

// clientGuard is the per-server state: a monotonic id counter and
// the id of the currently-active connection (0 = free).
type clientGuard struct {
	mu       sync.Mutex
	nextID   uint64
	activeID uint64
}

// connStateCtxKey tags the value clientGuard places on the per-conn
// context. Unexported so no other package can fake it.
type connStateCtxKey struct{}

// connState is the value stored under connStateCtxKey.
type connState struct {
	id uint64
}

// TagConn assigns a fresh id to the incoming connection and stores
// it on the per-conn context. The id is later read by HandleConn (to
// claim or release ownership) and by verify (to gate RPC dispatch).
func (g *clientGuard) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	g.mu.Lock()
	g.nextID++
	id := g.nextID
	g.mu.Unlock()

	return context.WithValue(ctx, connStateCtxKey{}, connState{id: id})
}

// HandleConn claims the active-owner slot on ConnBegin (if free) and
// releases it on ConnEnd (if held). Any other lifecycle event is
// ignored.
func (g *clientGuard) HandleConn(ctx context.Context, info stats.ConnStats) {
	state, ok := ctx.Value(connStateCtxKey{}).(connState)
	if !ok {
		return
	}

	switch info.(type) {
	case *stats.ConnBegin:
		g.mu.Lock()

		if g.activeID == 0 {
			g.activeID = state.id
		}

		g.mu.Unlock()

	case *stats.ConnEnd:
		g.mu.Lock()

		if g.activeID == state.id {
			g.activeID = 0
		}

		g.mu.Unlock()
	}
}

// TagRPC and HandleRPC satisfy stats.Handler but the per-RPC hooks
// are not used — gating happens at the interceptor layer.
func (g *clientGuard) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context {
	return ctx
}

func (g *clientGuard) HandleRPC(_ context.Context, _ stats.RPCStats) {}

// verify returns nil when the calling RPC's connection currently
// owns the active-client slot, or a gRPC status error otherwise.
func (g *clientGuard) verify(ctx context.Context) error {
	state, ok := ctx.Value(connStateCtxKey{}).(connState)
	if !ok {
		return status.Errorf(codes.Internal, "grpc: missing connection state")
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.activeID == state.id {
		return nil
	}

	return status.Errorf(
		codes.AlreadyExists,
		"another client is already attached; this daemon serves a single client at a time",
	)
}

// unary gates every unary RPC through verify.
func (g *clientGuard) unary(
	ctx context.Context,
	req any,
	_ *googlegrpc.UnaryServerInfo,
	handler googlegrpc.UnaryHandler,
) (any, error) {
	if err := g.verify(ctx); err != nil {
		return nil, err
	}

	return handler(ctx, req)
}

// stream gates every streaming RPC through verify.
func (g *clientGuard) stream(
	srv any,
	ss googlegrpc.ServerStream,
	_ *googlegrpc.StreamServerInfo,
	handler googlegrpc.StreamHandler,
) error {
	if err := g.verify(ss.Context()); err != nil {
		return err
	}

	return handler(srv, ss)
}
