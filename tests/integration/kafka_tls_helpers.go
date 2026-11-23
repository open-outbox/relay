//go:build integration

package integration

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	kafka_go "github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/kafka"
)

const kafkaSecretsDir = "/etc/kafka/secrets"

// kafkaTLSCerts holds the PEM file paths (on the host) of a throwaway PKI
// generated for a single test: a CA, a broker certificate and a client
// certificate, both signed by that CA.
type kafkaTLSCerts struct {
	CAFile         string
	ClientCertFile string
	ClientKeyFile  string

	// serverPEMFile holds the broker's private key followed by its
	// certificate, which is the layout Kafka expects for a PEM keystore.
	serverPEMFile string

	caPool     *x509.CertPool
	clientPair tls.Certificate
}

// ClientTLSConfig returns a TLS config that trusts the test CA and presents
// the client certificate, for test-side producers/consumers/admin calls.
func (c *kafkaTLSCerts) ClientTLSConfig() *tls.Config {
	return &tls.Config{
		RootCAs:      c.caPool,
		Certificates: []tls.Certificate{c.clientPair},
		MinVersion:   tls.VersionTLS12,
	}
}

// Dialer returns a kafka-go dialer using ClientTLSConfig.
func (c *kafkaTLSCerts) Dialer() *kafka_go.Dialer {
	return &kafka_go.Dialer{
		Timeout:   10 * time.Second,
		DualStack: true,
		TLS:       c.ClientTLSConfig(),
	}
}

// generateKafkaTLSCerts creates a CA, a broker certificate valid for
// localhost/127.0.0.1/::1, and a client certificate, and writes them as PEM
// files into a per-test temp directory.
func generateKafkaTLSCerts(t *testing.T) *kafkaTLSCerts {
	t.Helper()
	dir := t.TempDir()
	now := time.Now()

	newKey := func() *ecdsa.PrivateKey {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		require.NoError(t, err)
		return k
	}
	serial := func() *big.Int {
		n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
		require.NoError(t, err)
		return n
	}

	// CA
	caKey := newKey()
	caTmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "openoutbox-test-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	issue := func(cn string, usage x509.ExtKeyUsage, dns []string, ips []net.IP) ([]byte, *ecdsa.PrivateKey) {
		key := newKey()
		tmpl := &x509.Certificate{
			SerialNumber: serial(),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    now.Add(-time.Hour),
			NotAfter:     now.Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{usage},
			DNSNames:     dns,
			IPAddresses:  ips,
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
		require.NoError(t, err)
		return der, key
	}

	serverDER, serverKey := issue("localhost", x509.ExtKeyUsageServerAuth,
		[]string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")})
	clientDER, clientKey := issue("openoutbox-relay", x509.ExtKeyUsageClientAuth, nil, nil)

	certPEM := func(der []byte) []byte {
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	}
	// Kafka's PEM keystore only accepts PKCS#8 ("BEGIN PRIVATE KEY").
	keyPEM := func(k *ecdsa.PrivateKey) []byte {
		der, err := x509.MarshalPKCS8PrivateKey(k)
		require.NoError(t, err)
		return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	}
	write := func(name string, parts ...[]byte) string {
		var data []byte
		for _, p := range parts {
			data = append(data, p...)
		}
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, data, 0o600))
		return path
	}

	certs := &kafkaTLSCerts{
		CAFile:         write("ca.pem", certPEM(caDER)),
		ClientCertFile: write("client.crt", certPEM(clientDER)),
		ClientKeyFile:  write("client.key", keyPEM(clientKey)),
		serverPEMFile:  write("server.pem", keyPEM(serverKey), certPEM(serverDER)),
	}

	certs.caPool = x509.NewCertPool()
	certs.caPool.AddCert(caCert)
	certs.clientPair, err = tls.X509KeyPair(certPEM(clientDER), keyPEM(clientKey))
	require.NoError(t, err)

	return certs
}

// setupKafkaTLS starts a Kafka broker whose client-facing listener only
// speaks TLS, using a freshly generated CA. With requireClientCert the
// broker also enforces mTLS. The topic is created over the TLS listener.
// It returns the broker address and the generated certificates.
func setupKafkaTLS(t *testing.T, topic string, requireClientCert bool) (string, *kafkaTLSCerts) {
	t.Helper()
	ctx := context.Background()
	certs := generateKafkaTLSCerts(t)

	clientAuth := "none"
	if requireClientCert {
		clientAuth = "required"
	}

	// The module advertises its host-mapped listener under the name
	// "PLAINTEXT"; remapping that name to the SSL protocol turns it into a
	// TLS listener while leaving the internal BROKER/CONTROLLER listeners
	// (and the module's advertised-listener wiring) untouched.
	kafkaContainer, err := kafka.Run(ctx, "confluentinc/confluent-local:7.5.0",
		testcontainers.WithFiles(
			testcontainers.ContainerFile{
				HostFilePath:      certs.serverPEMFile,
				ContainerFilePath: kafkaSecretsDir + "/server.pem",
				FileMode:          0o644,
			},
			testcontainers.ContainerFile{
				HostFilePath:      certs.CAFile,
				ContainerFilePath: kafkaSecretsDir + "/ca.pem",
				FileMode:          0o644,
			},
		),
		testcontainers.WithEnv(map[string]string{
			"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP": "BROKER:PLAINTEXT,PLAINTEXT:SSL,CONTROLLER:PLAINTEXT",
			"KAFKA_SSL_KEYSTORE_TYPE":              "PEM",
			"KAFKA_SSL_KEYSTORE_LOCATION":          kafkaSecretsDir + "/server.pem",
			"KAFKA_SSL_TRUSTSTORE_TYPE":            "PEM",
			"KAFKA_SSL_TRUSTSTORE_LOCATION":        kafkaSecretsDir + "/ca.pem",
			"KAFKA_SSL_CLIENT_AUTH":                clientAuth,
		}),
	)
	t.Cleanup(func() {
		if kafkaContainer != nil {
			_ = kafkaContainer.Terminate(ctx)
		}
	})
	require.NoError(t, err)

	brokers, err := kafkaContainer.Brokers(ctx)
	require.NoError(t, err)
	broker := brokers[0]

	createKafkaTopicWithDialer(t, certs.Dialer(), broker, topic)

	return broker, certs
}

// createKafkaTopicWithDialer is createKafkaTopic for brokers that need a
// custom (e.g. TLS) dialer.
func createKafkaTopicWithDialer(t *testing.T, dialer *kafka_go.Dialer, broker, topic string) {
	t.Helper()
	conn, err := dialer.Dial("tcp", broker)
	require.NoError(t, err)
	defer conn.Close()

	require.NoError(t, conn.CreateTopics(kafka_go.TopicConfig{
		Topic:             topic,
		NumPartitions:     1,
		ReplicationFactor: 1,
	}))
}
