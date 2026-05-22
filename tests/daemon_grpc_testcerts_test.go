//go:build integration

// In-memory PKI for the daemon gRPC integration suite. Pure
// crypto/ecdsa + crypto/x509; no openssl, no disk, no fixtures.
// Each call produces fresh material so parallel runs cannot share
// trust anchors by accident.

package tests

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type testPKI struct {
	CAPool *x509.CertPool

	ServerTLS *tls.Config
	ClientTLS *tls.Config

	// BadClientTLS is signed by an untrusted CA so the handshake
	// failure path can be exercised without forging certificates.
	BadClientTLS *tls.Config

	// NoClientCertTLS trusts the server but presents no client
	// cert — exercises the RequireAndVerifyClientCert posture.
	NoClientCertTLS *tls.Config
}

const testCertValidity = time.Hour

func mkTestPKI(t *testing.T) *testPKI {
	t.Helper()

	trustedCA, trustedCAKey := mkCA(t, "a2text-test-ca")
	foreignCA, foreignCAKey := mkCA(t, "a2text-test-foreign-ca")

	caPool := x509.NewCertPool()
	caPool.AddCert(trustedCA)

	serverCert := issueLeaf(t, trustedCA, trustedCAKey, leafSpec{
		commonName:  "a2textd",
		dnsNames:    []string{"localhost"},
		ipAddresses: []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
		extKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})

	clientCert := issueLeaf(t, trustedCA, trustedCAKey, leafSpec{
		commonName:  "a2text-ui",
		extKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})

	badClientCert := issueLeaf(t, foreignCA, foreignCAKey, leafSpec{
		commonName:  "a2text-ui-rogue",
		extKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})

	return &testPKI{
		CAPool: caPool,
		ServerTLS: &tls.Config{
			Certificates: []tls.Certificate{serverCert},
			ClientCAs:    caPool,
			ClientAuth:   tls.RequireAndVerifyClientCert,
			MinVersion:   tls.VersionTLS13,
		},
		ClientTLS: &tls.Config{
			Certificates: []tls.Certificate{clientCert},
			RootCAs:      caPool,
			ServerName:   "localhost",
			MinVersion:   tls.VersionTLS13,
		},
		BadClientTLS: &tls.Config{
			Certificates: []tls.Certificate{badClientCert},
			RootCAs:      caPool,
			ServerName:   "localhost",
			MinVersion:   tls.VersionTLS13,
		},
		NoClientCertTLS: &tls.Config{
			RootCAs:    caPool,
			ServerName: "localhost",
			MinVersion: tls.VersionTLS13,
		},
	}
}

func mkCA(t *testing.T, commonName string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err, "generate CA key")

	tmpl := &x509.Certificate{
		SerialNumber:          mkSerial(t),
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"a2text-test"}},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(testCertValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err, "self-sign CA")

	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err, "parse CA DER")

	return cert, key
}

type leafSpec struct {
	commonName  string
	dnsNames    []string
	ipAddresses []net.IP
	extKeyUsage []x509.ExtKeyUsage
}

func issueLeaf(
	t *testing.T,
	ca *x509.Certificate,
	caKey *ecdsa.PrivateKey,
	spec leafSpec,
) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err, "generate leaf key")

	tmpl := &x509.Certificate{
		SerialNumber: mkSerial(t),
		Subject:      pkix.Name{CommonName: spec.commonName, Organization: []string{"a2text-test"}},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(testCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  spec.extKeyUsage,
		DNSNames:     spec.dnsNames,
		IPAddresses:  spec.ipAddresses,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	require.NoError(t, err, "sign leaf %q", spec.commonName)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err, "marshal leaf key")

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err, "assemble keypair %q", spec.commonName)

	return pair
}

const serialBits = 128

func mkSerial(t *testing.T) *big.Int {
	t.Helper()

	limit := new(big.Int).Lsh(big.NewInt(1), serialBits)

	n, err := rand.Int(rand.Reader, limit)
	require.NoError(t, err, "random serial")

	return n
}
