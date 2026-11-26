package kafka

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/segmentio/kafka-go/compress"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func generateValidMockCert(t *testing.T) (string, string) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"OpenOutbox Test"},
		},
		NotBefore: time.Now().Add(-1 * time.Hour),
		NotAfter:  time.Now().Add(1 * time.Hour),
		KeyUsage:  x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certBytes, err := x509.CreateCertificate(
		rand.Reader,
		&template,
		&template,
		&priv.PublicKey,
		priv,
	)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certBytes})

	keyBytes, err := x509.MarshalECPrivateKey(priv)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	return string(certPEM), string(keyPEM)
}

func TestNew_TransportConfigurations(t *testing.T) {
	mockCACert, mockClientKey := generateValidMockCert(t)
	mockClientCert := mockCACert

	tests := []struct {
		name          string
		cfg           Config
		expectErr     bool
		errContains   string
		assertDetails func(t *testing.T, k *Publisher)
	}{
		{
			name: "Error: No Brokers Provided",
			cfg: Config{
				Brokers: []string{},
			},
			expectErr:   true,
			errContains: "no broker addresses provided",
		},
		{
			name: "Valid Plaintext Default Configuration",
			cfg: Config{
				Brokers: []string{"localhost:9092"},
			},
			expectErr: false,
			assertDetails: func(t *testing.T, k *Publisher) {
				assert.Nil(t, k.dialer.TLS)
				assert.Nil(t, k.transport.TLS)
				assert.Nil(t, k.dialer.SASLMechanism)
				assert.Nil(t, k.transport.SASL)
				assert.NotNil(t, k.transport.Dial, "transport must use a configured dialer")
			},
		},
		{
			name: "Valid Standard One-Way TLS (Insecure Skip Verify Option)",
			cfg: Config{
				Brokers:  []string{"localhost:9092"},
				Insecure: true,
			},
			expectErr: false,
			assertDetails: func(t *testing.T, k *Publisher) {
				require.NotNil(t, k.dialer.TLS)
				assert.True(t, k.dialer.TLS.InsecureSkipVerify)
				assert.Nil(t, k.dialer.TLS.RootCAs)
			},
		},
		{
			name: "Valid TLS Enabled without CA uses system trust store",
			cfg: Config{
				Brokers:    []string{"localhost:9092"},
				TLSEnabled: true,
				TLSVersion: tls.VersionTLS12,
			},
			expectErr: false,
			assertDetails: func(t *testing.T, k *Publisher) {
				require.NotNil(t, k.dialer.TLS)
				assert.Nil(t, k.dialer.TLS.RootCAs, "nil RootCAs means system roots")
				assert.False(t, k.dialer.TLS.InsecureSkipVerify)
				assert.Empty(t, k.dialer.TLS.Certificates)
				assert.Equal(t, uint16(tls.VersionTLS12), k.dialer.TLS.MinVersion)
				assert.Same(t, k.dialer.TLS, k.transport.TLS)
			},
		},
		{
			name: "Valid One-Way TLS with Explicit CA Inline/PEM Data",
			cfg: Config{
				Brokers:    []string{"localhost:9092"},
				TLSCA:      mockCACert,
				ServerName: "kafka.production.internal",
				TLSVersion: tls.VersionTLS13,
			},
			expectErr: false,
			assertDetails: func(t *testing.T, k *Publisher) {
				require.NotNil(t, k.dialer.TLS)
				assert.NotNil(t, k.dialer.TLS.RootCAs)
				assert.Equal(t, "kafka.production.internal", k.dialer.TLS.ServerName)
				assert.Equal(t, uint16(tls.VersionTLS13), k.dialer.TLS.MinVersion)
				assert.Empty(t, k.dialer.TLS.Certificates) // No client certs yet
				assert.Same(t, k.dialer.TLS, k.transport.TLS)
			},
		},
		{
			name: "Valid Strict mTLS (CA + Cert + Key via Inline Base64 Data)",
			cfg: Config{
				Brokers: []string{"localhost:9092"},
				TLSCA:   "base64://" + base64.StdEncoding.EncodeToString([]byte(mockCACert)),
				TLSCert: "base64://" + base64.StdEncoding.EncodeToString([]byte(mockClientCert)),
				TLSKey:  "base64://" + base64.StdEncoding.EncodeToString([]byte(mockClientKey)),
			},
			expectErr: false,
			assertDetails: func(t *testing.T, k *Publisher) {
				require.NotNil(t, k.dialer.TLS)
				assert.NotNil(t, k.dialer.TLS.RootCAs)
				assert.Len(
					t,
					k.dialer.TLS.Certificates,
					1,
					"Should contain exactly one compiled mTLS private keypair",
				)
			},
		},
		{
			name: "Error mTLS: Missing Key component entirely",
			cfg: Config{
				Brokers: []string{"localhost:9092"},
				TLSCert: mockClientCert,
			},
			expectErr:   true,
			errContains: "incomplete mTLS configuration: both TLSCert and TLSKey must be provided together",
		},
		{
			name: "Error TLS: Invalid Corrupted CA input layout",
			cfg: Config{
				Brokers: []string{"localhost:9092"},
				TLSCA:   "-----BEGIN CERTIFICATE-----\ninvalid-truncated-junk-data",
			},
			expectErr:   true,
			errContains: "malformed PEM data",
		},
		{
			name: "Valid Auth: SASL Plain authentication layout configuration",
			cfg: Config{
				Brokers:       []string{"localhost:9092"},
				SASLMechanism: "plain",
				Username:      "openoutbox-admin",
				Password:      "secure-password-string",
			},
			expectErr: false,
			assertDetails: func(t *testing.T, k *Publisher) {
				require.NotNil(t, k.dialer.SASLMechanism)
				assert.Equal(t, "PLAIN", k.dialer.SASLMechanism.Name())
				require.NotNil(t, k.transport.SASL, "writer transport must authenticate")
				assert.Equal(t, "PLAIN", k.transport.SASL.Name())
			},
		},
		{
			name: "Valid Auth: SASL SCRAM-SHA-256 layout configuration",
			cfg: Config{
				Brokers:       []string{"localhost:9092"},
				SASLMechanism: "scram-sha-256",
				Username:      "openoutbox-scram",
				Password:      "scram-secret-password",
			},
			expectErr: false,
			assertDetails: func(t *testing.T, k *Publisher) {
				require.NotNil(t, k.dialer.SASLMechanism)
				assert.Equal(t, "SCRAM-SHA-256", k.dialer.SASLMechanism.Name())
				require.NotNil(t, k.transport.SASL, "writer transport must authenticate")
				assert.Equal(t, "SCRAM-SHA-256", k.transport.SASL.Name())
			},
		},
		{
			name: "Valid Auth: SASL mechanism is case and whitespace insensitive",
			cfg: Config{
				Brokers:       []string{"localhost:9092"},
				SASLMechanism: " SCRAM-SHA-512 ",
				Username:      "openoutbox-scram",
				Password:      "scram-secret-password",
			},
			expectErr: false,
			assertDetails: func(t *testing.T, k *Publisher) {
				require.NotNil(t, k.transport.SASL)
				assert.Equal(t, "SCRAM-SHA-512", k.transport.SASL.Name())
			},
		},
		{
			name: "Error Auth: Unsupported SASL Configuration Engine",
			cfg: Config{
				Brokers:       []string{"localhost:9092"},
				SASLMechanism: "gssapi-kerberos-unsupported",
			},
			expectErr:   true,
			errContains: "unsupported SASL mechanism for Kafka",
		},
		{
			name: "Writer receives batching, compression and dial settings",
			cfg: Config{
				Brokers:           []string{"localhost:9092"},
				BatchTimeout:      10 * time.Millisecond,
				Compression:       compress.Snappy,
				ConnectionTimeout: 3 * time.Second,
				IdleTimeout:       30 * time.Second,
			},
			expectErr: false,
			assertDetails: func(t *testing.T, k *Publisher) {
				assert.Equal(t, 10*time.Millisecond, k.writer.BatchTimeout)
				assert.Equal(t, compress.Snappy, k.writer.Compression)
				assert.Equal(t, 3*time.Second, k.transport.DialTimeout)
				assert.Equal(t, 30*time.Second, k.transport.IdleTimeout)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, err := New(tt.cfg)

			if tt.expectErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, k)
			} else {
				require.NoError(t, err)
				require.NotNil(t, k)
				if tt.assertDetails != nil {
					tt.assertDetails(t, k)
				}
			}
		})
	}
}

func TestNew_ClientID(t *testing.T) {
	k, err := New(Config{Brokers: []string{"localhost:9092"}})
	require.NoError(t, err)
	assert.Equal(t, DefaultClientID, k.transport.ClientID)
	assert.Equal(t, DefaultClientID, k.dialer.ClientID)

	k, err = New(Config{Brokers: []string{"localhost:9092"}, ClientID: "billing-relay"})
	require.NoError(t, err)
	assert.Equal(t, "billing-relay", k.transport.ClientID)
	assert.Equal(t, "billing-relay", k.dialer.ClientID)
}
