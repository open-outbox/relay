package kafka

import (
	"crypto/tls"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// certReloader serves the client certificate from files and reads them again
// when they change, so a rotated certificate is used for new connections
// without restarting the relay.
type certReloader struct {
	certPath, keyPath string

	mu      sync.Mutex
	pair    *tls.Certificate
	version fileVersion
}

type fileVersion struct {
	certMod, keyMod   time.Time
	certSize, keySize int64
}

func newCertReloader(certSource, keySource string) (*certReloader, error) {
	// Inline and base64 values cannot change, so only files are reloaded.
	certPath, certIsFile := strings.CutPrefix(certSource, "file://")
	keyPath, keyIsFile := strings.CutPrefix(keySource, "file://")
	if !certIsFile || !keyIsFile {
		return nil, nil
	}

	// Load once now, so a bad pair still fails at startup.
	r := &certReloader{certPath: certPath, keyPath: keyPath}
	if _, err := r.certificate(); err != nil {
		return nil, err
	}
	return r, nil
}

// GetClientCertificate is called by crypto/tls on each handshake in which
// the broker asks for a client certificate.
func (r *certReloader) GetClientCertificate(
	*tls.CertificateRequestInfo,
) (*tls.Certificate, error) {
	return r.certificate()
}

func (r *certReloader) certificate() (*tls.Certificate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	version, err := r.stat()
	if err == nil && r.pair != nil && version == r.version {
		return r.pair, nil
	}
	if err == nil {
		var pair tls.Certificate
		pair, err = tls.LoadX509KeyPair(r.certPath, r.keyPath)
		if err == nil {
			r.pair, r.version = &pair, version
			return r.pair, nil
		}
	}

	// Changed but unloadable (e.g. one file half written): keep the last good
	// pair, and try again on the next handshake.
	if r.pair != nil {
		return r.pair, nil
	}
	return nil, fmt.Errorf("failed to load mTLS keypair: %w", err)
}

func (r *certReloader) stat() (fileVersion, error) {
	cert, err := os.Stat(r.certPath)
	if err != nil {
		return fileVersion{}, err
	}
	key, err := os.Stat(r.keyPath)
	if err != nil {
		return fileVersion{}, err
	}
	return fileVersion{
		certMod: cert.ModTime(), certSize: cert.Size(),
		keyMod: key.ModTime(), keySize: key.Size(),
	}, nil
}
