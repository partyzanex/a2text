package clid

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoadServerTLS_AllEmptyReturnsDisabled(t *testing.T) {
	t.Parallel()

	cfg, err := loadServerTLS("", "", "")
	require.Nil(t, cfg)
	require.ErrorIs(t, err, errTLSDisabled)
}

func TestLoadServerTLS_PartialReturnsError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		cert, key, ca string
	}{
		{"only cert", "cert.pem", "", ""},
		{"only key", "", "key.pem", ""},
		{"only ca", "", "", "ca.pem"},
		{"cert+key missing ca", "cert.pem", "key.pem", ""},
		{"cert+ca missing key", "cert.pem", "", "ca.pem"},
		{"key+ca missing cert", "", "key.pem", "ca.pem"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := loadServerTLS(tc.cert, tc.key, tc.ca)
			require.Nil(t, cfg)
			require.ErrorIs(t, err, errTLSPartial)
		})
	}
}

func TestLoadServerTLS_HappyPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	certPath, keyPath, caPath := writeTestPKI(t, dir, 0o600)

	cfg, err := loadServerTLS(certPath, keyPath, caPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.Len(t, cfg.Certificates, 1)
	require.NotNil(t, cfg.ClientCAs)
	require.Equal(t, uint16(0x0304), cfg.MinVersion, "must pin TLS 1.3")
}

func TestLoadServerTLS_RejectsLooseKeyPerms(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	certPath, keyPath, caPath := writeTestPKI(t, dir, 0o644)

	cfg, err := loadServerTLS(certPath, keyPath, caPath)
	require.Nil(t, cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be 0600 or stricter")
}

func TestLoadServerTLS_AcceptsStricterKeyPerms(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	certPath, keyPath, caPath := writeTestPKI(t, dir, 0o400)

	cfg, err := loadServerTLS(certPath, keyPath, caPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
}

func TestRequireKeyFileMode_MissingFile(t *testing.T) {
	t.Parallel()

	err := requireKeyFileMode(filepath.Join(t.TempDir(), "does-not-exist.key"))
	require.Error(t, err)
	require.True(t, errors.Is(err, os.ErrNotExist) || err != nil)
}

func TestRequireKeyFileMode_TooPermissive(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "k")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o644))

	err := requireKeyFileMode(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "0644")
}

func TestRequireKeyFileMode_OwnerOnly(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "k")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))

	require.NoError(t, requireKeyFileMode(path))
}

func TestLoadClientCABundle_NoPEMCertificates(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(path, []byte("not a pem cert"), 0o644))

	pool, err := loadClientCABundle(path)
	require.Nil(t, pool)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no PEM-encoded certificates")
}

// writeTestPKI emits a self-signed CA used both as the daemon's
// server cert and as the client-CA bundle. Mode is the permission
// applied to the private key file; cert / CA are 0644.
func writeTestPKI(t *testing.T, dir string, keyMode os.FileMode) (cert, key, ca string) {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "clid-test"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:              []string{"localhost"},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	require.NoError(t, err)

	keyDER, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	cert = filepath.Join(dir, "cert.pem")
	key = filepath.Join(dir, "key.pem")
	ca = filepath.Join(dir, "ca.pem")

	require.NoError(t, os.WriteFile(cert, certPEM, 0o644))
	require.NoError(t, os.WriteFile(key, keyPEM, keyMode))
	require.NoError(t, os.WriteFile(ca, certPEM, 0o644))

	return cert, key, ca
}
