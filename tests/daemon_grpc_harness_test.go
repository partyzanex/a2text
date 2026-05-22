//go:build integration

// In-process daemon fixture for the gRPC integration suite. Real
// KeyboardService / SecretService / Hub / cycletoken.Store /
// secret.Store, fake InputDriver, no evdev reader. Each harness
// gets its own loopback port, its own t.TempDir secrets file, and
// its own PKI tree.

package tests

import (
	"context"
	"crypto/tls"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	grpcserver "github.com/partyzanex/a2text/internal/adapters/grpc/server"
	"github.com/partyzanex/a2text/internal/infra/cycletoken"
	infragrpc "github.com/partyzanex/a2text/internal/infra/grpc"
	"github.com/partyzanex/a2text/internal/infra/secret"
	"github.com/partyzanex/a2text/internal/usecases/hotkey"
	"github.com/partyzanex/a2text/internal/usecases/inject"
	a2textv1 "github.com/partyzanex/a2text/pkg/proto/a2text/v1"
)

const (
	defaultTokenTTL  = 5 * time.Second
	callTimeout      = 2 * time.Second
	serveStopGrace   = 50 * time.Millisecond
	serveStopTimeout = 2 * time.Second
)

type daemonHarness struct {
	Addr    string
	Tokens  *cycletoken.Store
	Hub     *hotkey.Hub
	Secrets *secret.Store
	Driver  *fakeInputDriver
	Server  *infragrpc.Server
	PKI     *testPKI

	// ctx is the lifetime owner for the embedded Serve goroutine.
	// Storing it in the struct (instead of passing as a method
	// argument) is the simplest way to drive a single cancel from
	// Close + provide per-call timeouts via callCtx. The cost is
	// that the harness is one-shot — callCtx after Close yields a
	// cancelled context; tests must build a fresh harness per case.
	//
	//nolint:containedctx // see comment above.
	ctx      context.Context
	cancel   context.CancelFunc
	serveErr chan error
}

type harnessConfig struct {
	injectMode a2textv1.InjectMode
	tokenTTL   time.Duration
	driverErr  error
}

type harnessOpt func(*harnessConfig)

func withInjectMode(mode a2textv1.InjectMode) harnessOpt {
	return func(c *harnessConfig) { c.injectMode = mode }
}

func withTokenTTL(ttl time.Duration) harnessOpt {
	return func(c *harnessConfig) { c.tokenTTL = ttl }
}

func withDriverError(err error) harnessOpt {
	return func(c *harnessConfig) { c.driverErr = err }
}

func newDaemon(t *testing.T, opts ...harnessOpt) *daemonHarness {
	t.Helper()

	cfg := harnessConfig{
		injectMode: a2textv1.InjectMode_INJECT_MODE_PASTE,
		tokenTTL:   defaultTokenTTL,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	pki := mkTestPKI(t)

	driver := &fakeInputDriver{err: cfg.driverErr}
	// HOLD chosen so PRESS / RELEASE event kinds are distinguishable
	// in stream assertions; other suites do not care about kind.
	hub := hotkey.New(nil, a2textv1.HotkeyMode_HOTKEY_MODE_HOLD)
	tokens := cycletoken.New(cfg.tokenTTL, time.Now)
	injectSvc := inject.New(nil, cfg.injectMode, driver)

	secretsPath := filepath.Join(t.TempDir(), "secrets.json")
	store, err := secret.New(secretsPath, time.Now)
	require.NoError(t, err, "open secret store")

	kbSvc := grpcserver.NewKeyboardService(nil, tokens, hub, injectSvc, hub)
	secSvc := grpcserver.NewSecretService(nil, store)

	server := infragrpc.NewServer(nil, kbSvc, secSvc, pki.ServerTLS)

	ctx, cancel := context.WithCancel(context.Background())

	addr, err := server.Listen(ctx, "127.0.0.1:0")
	if err != nil {
		cancel()
		t.Fatalf("listen: %v", err)
	}

	serveErr := make(chan error, 1)

	go func() { serveErr <- server.Serve(ctx) }()

	harness := &daemonHarness{
		Addr:     addr,
		Tokens:   tokens,
		Hub:      hub,
		Secrets:  store,
		Driver:   driver,
		Server:   server,
		PKI:      pki,
		ctx:      ctx,
		cancel:   cancel,
		serveErr: serveErr,
	}

	t.Cleanup(func() { harness.Close(t) })

	return harness
}

// Close cancels the server context, then escalates to a hard Stop
// after serveStopGrace so a server-streaming handler waiting on
// stream.Context cannot keep GracefulStop blocked. Idempotent.
func (h *daemonHarness) Close(t *testing.T) {
	t.Helper()

	if h.cancel == nil {
		return
	}

	h.cancel()
	h.cancel = nil

	grace := time.NewTimer(serveStopGrace)
	defer grace.Stop()

	select {
	case err := <-h.serveErr:
		if err != nil {
			t.Errorf("daemonHarness: Serve returned error: %v", err)
		}

		return
	case <-grace.C:
	}

	_ = h.Server.Close()

	select {
	case err := <-h.serveErr:
		if err != nil {
			t.Errorf("daemonHarness: Serve returned error after forceful Close: %v", err)
		}
	case <-time.After(serveStopTimeout):
		t.Errorf("daemonHarness: Serve did not return within %s after forceful Close", serveStopTimeout)
	}
}

// Dial opens a fresh client connection. nil tlsCfg means insecure
// credentials (only used by the negative-handshake case).
func (h *daemonHarness) Dial(t *testing.T, tlsCfg *tls.Config) *grpc.ClientConn {
	t.Helper()

	var creds credentials.TransportCredentials
	if tlsCfg == nil {
		creds = insecure.NewCredentials()
	} else {
		creds = credentials.NewTLS(tlsCfg)
	}

	conn, err := grpc.NewClient(h.Addr, grpc.WithTransportCredentials(creds))
	require.NoError(t, err, "grpc.NewClient")

	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// callCtx derives a bounded context from the harness lifetime so a
// daemon shutdown also cancels the in-flight call.
func callCtx(t *testing.T, h *daemonHarness) (context.Context, context.CancelFunc) {
	t.Helper()

	return context.WithTimeout(h.ctx, callTimeout)
}

type fakeInputDriver struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (f *fakeInputDriver) PasteChord(_ context.Context) (int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++

	if f.err != nil {
		return 0, f.err
	}

	return 4, nil
}

func (f *fakeInputDriver) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}
