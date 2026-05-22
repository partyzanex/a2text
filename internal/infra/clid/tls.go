package clid

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// errTLSPartial is returned when only some of the three mTLS files
// (--cert / --key / --client-ca) are supplied. mTLS requires all
// three; partial configs are an operator mistake and we want a hard
// error rather than silent plaintext fallback.
var errTLSPartial = errors.New(
	"clid: mTLS requires all three of --cert, --key, --client-ca (or none)",
)

// loadServerTLS builds a *tls.Config suitable for a mTLS-only gRPC
// server. Returns (nil, nil) when none of the three flags are set —
// the caller treats that as "run in plaintext". Any other partial
// combination returns errTLSPartial.
//
// The resulting config:
//   - presents certFile / keyFile as the server certificate;
//   - trusts only client certificates signed by the CAs in
//     clientCAFile (no system roots);
//   - requires the client to present a certificate and verifies the
//     full chain (ClientAuth = RequireAndVerifyClientCert);
//   - pins MinVersion to TLS 1.3 — the daemon and the UI ship
//     together so there is no legacy client to accommodate.
func loadServerTLS(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	if certFile == "" && keyFile == "" && clientCAFile == "" {
		return nil, nil //nolint:nilnil // intentional: nil config = plaintext mode signal.
	}

	if certFile == "" || keyFile == "" || clientCAFile == "" {
		return nil, errTLSPartial
	}

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("clid: load server keypair: %w", err)
	}

	pem, err := os.ReadFile(clientCAFile) //nolint:gosec // operator-supplied flag, not user input.
	if err != nil {
		return nil, fmt.Errorf("clid: read client CA bundle %q: %w", clientCAFile, err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("clid: no PEM-encoded certificates found in %q", clientCAFile)
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}
