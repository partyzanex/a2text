package clid

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

const (
	pprofReadHeaderTimeout = 5 * time.Second
	pprofShutdownTimeout   = 2 * time.Second
)

// startPprof launches an HTTP server on addr exposing the standard
// net/http/pprof endpoints. The server runs in its own goroutine and
// is shut down when ctx is cancelled.
//
// Empty addr returns immediately with nil — the caller does not have
// to branch on the --pprof flag, it can call this unconditionally.
//
// addr is passed straight to net.Listen("tcp", addr); use
// "127.0.0.1:6060" to expose only on loopback. Do not bind to a
// non-loopback interface without a clear reason — pprof exposes
// arbitrary heap and goroutine state.
func startPprof(ctx context.Context, addr string, log *slog.Logger) error {
	if addr == "" {
		return nil
	}

	var lc net.ListenConfig

	listener, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("pprof: listen %q: %w", addr, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: pprofReadHeaderTimeout,
	}

	go func() {
		log.Info("pprof: endpoint listening",
			slog.String("addr", listener.Addr().String()),
			slog.String("heap_url", "http://"+listener.Addr().String()+"/debug/pprof/heap"),
		)

		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			log.Warn("pprof: server stopped with error", slog.Any("err", serveErr))
		}
	}()

	go func() {
		<-ctx.Done()

		// Detached shutdown context: ctx is already cancelled at this
		// point, passing it to Shutdown would force-abort connections
		// instead of letting them drain gracefully. WithoutCancel
		// preserves any values while detaching from the cancellation
		// chain.
		shutdownCtx, shutdownCancel := context.WithTimeout(
			context.WithoutCancel(ctx), pprofShutdownTimeout)
		defer shutdownCancel()

		if shErr := server.Shutdown(shutdownCtx); shErr != nil {
			log.Debug("pprof: shutdown returned error", slog.Any("err", shErr))
		}
	}()

	return nil
}
