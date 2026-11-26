package kafka

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writePair writes a cert and key to dir and bumps their modification time,
// so a rewrite within the same second still counts as a change.
func writePair(t *testing.T, dir, cert, key string, mod time.Time) (string, string) {
	t.Helper()
	certPath, keyPath := filepath.Join(dir, "client.crt"), filepath.Join(dir, "client.key")
	require.NoError(t, os.WriteFile(certPath, []byte(cert), 0o600))
	require.NoError(t, os.WriteFile(keyPath, []byte(key), 0o600))
	require.NoError(t, os.Chtimes(certPath, mod, mod))
	require.NoError(t, os.Chtimes(keyPath, mod, mod))
	return certPath, keyPath
}

func TestCertReloader(t *testing.T) {
	t.Run("only file sources are reloaded", func(t *testing.T) {
		cert, key := generateValidMockCert(t)
		r, err := newCertReloader(cert, key)
		require.NoError(t, err)
		assert.Nil(t, r)

		dir := t.TempDir()
		certPath, _ := writePair(t, dir, cert, key, time.Now())
		r, err = newCertReloader("file://"+certPath, key)
		require.NoError(t, err)
		assert.Nil(t, r, "both cert and key must be files")
	})

	t.Run("a bad pair fails at startup", func(t *testing.T) {
		dir := t.TempDir()
		certPath, keyPath := writePair(t, dir, "not a cert", "not a key", time.Now())

		_, err := newCertReloader("file://"+certPath, "file://"+keyPath)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to load mTLS keypair")
	})

	t.Run("serves the rotated pair once the files change", func(t *testing.T) {
		dir := t.TempDir()
		cert1, key1 := generateValidMockCert(t)
		certPath, keyPath := writePair(t, dir, cert1, key1, time.Now().Add(-time.Hour))

		r, err := newCertReloader("file://"+certPath, "file://"+keyPath)
		require.NoError(t, err)
		first, err := r.GetClientCertificate(nil)
		require.NoError(t, err)

		again, err := r.GetClientCertificate(nil)
		require.NoError(t, err)
		assert.Same(t, first, again, "unchanged files must not be reloaded")

		cert2, key2 := generateValidMockCert(t)
		writePair(t, dir, cert2, key2, time.Now())

		rotated, err := r.GetClientCertificate(nil)
		require.NoError(t, err)
		assert.NotEqual(t, first.Certificate[0], rotated.Certificate[0])
	})

	t.Run("keeps the last good pair when the new files are broken", func(t *testing.T) {
		dir := t.TempDir()
		cert, key := generateValidMockCert(t)
		certPath, keyPath := writePair(t, dir, cert, key, time.Now().Add(-time.Hour))

		r, err := newCertReloader("file://"+certPath, "file://"+keyPath)
		require.NoError(t, err)
		good, err := r.GetClientCertificate(nil)
		require.NoError(t, err)

		writePair(t, dir, cert, "half-written key", time.Now())
		got, err := r.GetClientCertificate(nil)
		require.NoError(t, err)
		assert.Same(t, good, got)

		require.NoError(t, os.Remove(keyPath))
		got, err = r.GetClientCertificate(nil)
		require.NoError(t, err)
		assert.Same(t, good, got, "a missing file must not break new connections")
	})
}

func TestNew_FileCertsUseReloader(t *testing.T) {
	dir := t.TempDir()
	cert, key := generateValidMockCert(t)
	certPath, keyPath := writePair(t, dir, cert, key, time.Now())

	k, err := New(Config{
		Brokers: []string{"localhost:9092"},
		TLSCert: "file://" + certPath,
		TLSKey:  "file://" + keyPath,
	})
	require.NoError(t, err)

	assert.NotNil(t, k.dialer.TLS.GetClientCertificate)
	assert.Empty(t, k.dialer.TLS.Certificates)
	assert.Same(t, k.dialer.TLS, k.transport.TLS)
}
