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

// errTLSDisabled is returned by loadServerTLS when no mTLS material
// is configured at all. It is a normal signal, not a failure: the
// caller is expected to inspect it via errors.Is and pass a nil
// *tls.Config to the server constructor so the listener runs in
// plaintext (loopback dev only).
//
// Sentinel-instead-of-(nil,nil) keeps the function's return shape
// honest — every success path returns a non-nil config, every
// non-success path returns a non-nil error.
var errTLSDisabled = errors.New("clid: mTLS disabled (no cert / key / client-ca supplied)")

// loadServerTLS builds a *tls.Config suitable for an mTLS-only gRPC
// server. Returns errTLSDisabled when none of the three flags are
// set; the caller is expected to recognise that sentinel and switch
// to plaintext mode. Any other partial combination is an operator
// mistake and surfaces as errTLSPartial.
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
	switch {
	case certFile == "" && keyFile == "" && clientCAFile == "":
		return nil, errTLSDisabled
	case certFile == "" || keyFile == "" || clientCAFile == "":
		return nil, errTLSPartial
	}

	if err := requireKeyFileMode(keyFile); err != nil {
		return nil, err
	}

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("clid: load server keypair: %w", err)
	}

	pool, err := loadClientCABundle(clientCAFile)
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

func loadClientCABundle(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path) //nolint:gosec // operator-supplied flag, not user input.
	if err != nil {
		return nil, fmt.Errorf("clid: read client CA bundle %q: %w", path, err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("clid: no PEM-encoded certificates found in %q", path)
	}

	return pool, nil
}

// keyFileMaxMode is the most permissive set of bits we tolerate on
// the server TLS private key. Group/world read or write bits are
// refused; only owner-read (0400) and owner-rw (0600) pass.
const keyFileMaxMode os.FileMode = 0o600

// requireKeyFileMode refuses to load a private key whose file
// permissions are broader than 0600. The check is a defense in
// depth — Go's tls.LoadX509KeyPair will happily read a world-
// readable key, and operators sometimes commit "convenience"
// permissions during dev that survive into production.
func requireKeyFileMode(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("clid: stat key file %q: %w", path, err)
	}

	mode := info.Mode().Perm()
	if mode&^keyFileMaxMode != 0 {
		return fmt.Errorf(
			"clid: key file %q has permissions %#o — must be %#o or stricter (chmod 600 %s)",
			path, mode, keyFileMaxMode, path,
		)
	}

	return nil
}
