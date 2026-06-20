package publishers

import (
	"context"
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

	"github.com/segmentio/kafka-go"
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

func TestKafka_NewKafka_TransportConfigurations(t *testing.T) {
	mockCACert, mockClientKey := generateValidMockCert(t)
	mockClientCert := mockCACert

	tests := []struct {
		name          string
		cfg           KafkaConfig
		expectErr     bool
		errContains   string
		assertDetails func(t *testing.T, k *Kafka)
	}{
		{
			name: "Error: No Brokers Provided",
			cfg: KafkaConfig{
				Brokers: []string{},
			},
			expectErr:   true,
			errContains: "no broker addresses provided",
		},
		{
			name: "Valid Plaintext Default Configuration",
			cfg: KafkaConfig{
				Brokers: []string{"localhost:9092"},
			},
			expectErr: false,
			assertDetails: func(t *testing.T, k *Kafka) {
				assert.Nil(t, k.dialer.TLS)
				assert.Nil(t, k.transport.TLS)
				assert.Nil(t, k.dialer.SASLMechanism)
			},
		},
		{
			name: "Valid Standard One-Way TLS (Insecure Skip Verify Option)",
			cfg: KafkaConfig{
				Brokers:  []string{"localhost:9092"},
				Insecure: true,
			},
			expectErr: false,
			assertDetails: func(t *testing.T, k *Kafka) {
				require.NotNil(t, k.dialer.TLS)
				assert.True(t, k.dialer.TLS.InsecureSkipVerify)
				assert.Nil(t, k.dialer.TLS.RootCAs)
			},
		},
		{
			name: "Valid One-Way TLS with Explicit CA Inline/PEM Data",
			cfg: KafkaConfig{
				Brokers:    []string{"localhost:9092"},
				TLSCA:      mockCACert,
				ServerName: "kafka.production.internal",
				TLSVersion: tls.VersionTLS13,
			},
			expectErr: false,
			assertDetails: func(t *testing.T, k *Kafka) {
				require.NotNil(t, k.dialer.TLS)
				assert.NotNil(t, k.dialer.TLS.RootCAs)
				assert.Equal(t, "kafka.production.internal", k.dialer.TLS.ServerName)
				assert.Equal(t, uint16(tls.VersionTLS13), k.dialer.TLS.MinVersion)
				assert.Empty(t, k.dialer.TLS.Certificates) // No client certs yet
			},
		},
		{
			name: "Valid Strict mTLS (CA + Cert + Key via Inline Base64 Data)",
			cfg: KafkaConfig{
				Brokers: []string{"localhost:9092"},
				TLSCA:   "base64://" + base64.StdEncoding.EncodeToString([]byte(mockCACert)),
				TLSCert: "base64://" + base64.StdEncoding.EncodeToString([]byte(mockClientCert)),
				TLSKey:  "base64://" + base64.StdEncoding.EncodeToString([]byte(mockClientKey)),
			},
			expectErr: false,
			assertDetails: func(t *testing.T, k *Kafka) {
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
			cfg: KafkaConfig{
				Brokers: []string{"localhost:9092"},
				TLSCert: mockClientCert,
			},
			expectErr:   true,
			errContains: "incomplete mTLS configuration: both TLSCert and TLSKey must be provided together",
		},
		{
			name: "Error TLS: Invalid Corrupted CA input layout",
			cfg: KafkaConfig{
				Brokers: []string{"localhost:9092"},
				TLSCA:   "-----BEGIN CERTIFICATE-----\ninvalid-truncated-junk-data",
			},
			expectErr:   true,
			errContains: "malformed PEM data",
		},
		{
			name: "Valid Auth: SASL Plain authentication layout configuration",
			cfg: KafkaConfig{
				Brokers:       []string{"localhost:9092"},
				SASLMechanism: "plain",
				Username:      "openoutbox-admin",
				Password:      "secure-password-string",
			},
			expectErr: false,
			assertDetails: func(t *testing.T, k *Kafka) {
				require.NotNil(t, k.dialer.SASLMechanism)
				assert.Equal(t, "PLAIN", k.dialer.SASLMechanism.Name())
			},
		},
		{
			name: "Valid Auth: SASL SCRAM-SHA-256 layout configuration",
			cfg: KafkaConfig{
				Brokers:       []string{"localhost:9092"},
				SASLMechanism: "scram-sha-256",
				Username:      "openoutbox-scram",
				Password:      "scram-secret-password",
			},
			expectErr: false,
			assertDetails: func(t *testing.T, k *Kafka) {
				require.NotNil(t, k.dialer.SASLMechanism)
				assert.Equal(t, "SCRAM-SHA-256", k.dialer.SASLMechanism.Name())
			},
		},
		{
			name: "Error Auth: Unsupported SASL Configuration Engine",
			cfg: KafkaConfig{
				Brokers:       []string{"localhost:9092"},
				SASLMechanism: "gssapi-kerberos-unsupported",
			},
			expectErr:   true,
			errContains: "unsupported SASL mechanism for Kafka",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			k, err := NewKafka(tt.cfg)

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

func TestKafka_isKafkaErrorRetryable(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		isRetryable bool
	}{
		{
			name:        "Nil error returns true (Safe Fallback status)",
			err:         nil,
			isRetryable: true,
		},
		{
			name:        "Standard network timeouts are retryable",
			err:         context.DeadlineExceeded,
			isRetryable: true,
		},
		{
			name:        "Fatal Error: Topic is completely invalid",
			err:         kafka.InvalidTopic,
			isRetryable: false,
		},
		{
			name:        "Fatal Error: Payload message size limits breached",
			err:         kafka.MessageSizeTooLarge,
			isRetryable: false,
		},
		{
			name: "Batch errors containing even one fatal error must be marked non-retryable",
			err: kafka.WriteErrors{
				nil,
				kafka.UnknownTopicOrPartition, // This is non-retryable
				nil,
			},
			isRetryable: false,
		},
		{
			name: "Batch errors containing only network hiccups stay retryable",
			err: kafka.WriteErrors{
				kafka.LeaderNotAvailable,
				kafka.BrokerNotAvailable,
			},
			isRetryable: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := isKafkaErrorRetryable(tt.err)
			assert.Equal(t, tt.isRetryable, res)
		})
	}
}
