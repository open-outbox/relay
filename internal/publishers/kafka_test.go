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
	"errors"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/open-outbox/relay/internal/relay"
	"github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/compress"
	"github.com/segmentio/kafka-go/protocol"
	metadataAPI "github.com/segmentio/kafka-go/protocol/metadata"
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
				assert.Nil(t, k.transport.SASL)
				assert.NotNil(t, k.transport.Dial, "transport must use a configured dialer")
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
			name: "Valid TLS Enabled without CA uses system trust store",
			cfg: KafkaConfig{
				Brokers:    []string{"localhost:9092"},
				TLSEnabled: true,
				TLSVersion: tls.VersionTLS12,
			},
			expectErr: false,
			assertDetails: func(t *testing.T, k *Kafka) {
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
				assert.Same(t, k.dialer.TLS, k.transport.TLS)
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
				require.NotNil(t, k.transport.SASL, "writer transport must authenticate")
				assert.Equal(t, "PLAIN", k.transport.SASL.Name())
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
				require.NotNil(t, k.transport.SASL, "writer transport must authenticate")
				assert.Equal(t, "SCRAM-SHA-256", k.transport.SASL.Name())
			},
		},
		{
			name: "Valid Auth: SASL mechanism is case and whitespace insensitive",
			cfg: KafkaConfig{
				Brokers:       []string{"localhost:9092"},
				SASLMechanism: " SCRAM-SHA-512 ",
				Username:      "openoutbox-scram",
				Password:      "scram-secret-password",
			},
			expectErr: false,
			assertDetails: func(t *testing.T, k *Kafka) {
				require.NotNil(t, k.transport.SASL)
				assert.Equal(t, "SCRAM-SHA-512", k.transport.SASL.Name())
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
		{
			name: "Writer receives batching, compression and dial settings",
			cfg: KafkaConfig{
				Brokers:           []string{"localhost:9092"},
				BatchTimeout:      10 * time.Millisecond,
				Compression:       compress.Snappy,
				ConnectionTimeout: 3 * time.Second,
				IdleTimeout:       30 * time.Second,
			},
			expectErr: false,
			assertDetails: func(t *testing.T, k *Kafka) {
				assert.Equal(t, 10*time.Millisecond, k.writer.BatchTimeout)
				assert.Equal(t, compress.Snappy, k.writer.Compression)
				assert.Equal(t, 3*time.Second, k.transport.DialTimeout)
				assert.Equal(t, 30*time.Second, k.transport.IdleTimeout)
			},
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
			name:        "Fatal Error: Batch exceeds broker limits",
			err:         kafka.RecordListTooLarge,
			isRetryable: false,
		},
		{
			name:        "Fatal Error: Broker rejected the record",
			err:         kafka.InvalidRecord,
			isRetryable: false,
		},
		{
			name:        "Unknown topic is transient (leader move, auto-creation)",
			err:         kafka.UnknownTopicOrPartition,
			isRetryable: true,
		},
		{
			name:        "Corrupt message is transient (in-transit CRC failure)",
			err:         kafka.InvalidMessage,
			isRetryable: true,
		},
		{
			name:        "Auth failures stay retryable so misconfig does not kill events",
			err:         kafka.SASLAuthenticationFailed,
			isRetryable: true,
		},
		{
			name:        "Client-side oversize message (exceeds BatchBytes) is fatal",
			err:         kafka.MessageTooLargeError{},
			isRetryable: false,
		},
		{
			name:        "Wrapped Kafka errors are still classified",
			err:         fmt.Errorf("write: %w", kafka.MessageSizeTooLarge),
			isRetryable: false,
		},
		{
			name: "Batch errors containing even one fatal error must be marked non-retryable",
			err: kafka.WriteErrors{
				nil,
				kafka.MessageSizeTooLarge, // This is non-retryable
				nil,
			},
			isRetryable: false,
		},
		{
			name: "Batch errors with an unknown topic stay retryable",
			err: kafka.WriteErrors{
				nil,
				kafka.UnknownTopicOrPartition,
			},
			isRetryable: true,
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

// freeClosedAddr returns an address that nothing is listening on.
func freeClosedAddr(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

// fakeKafkaBroker starts a listener that answers Kafka metadata requests with
// the brokers returned by advertise, which receives the fake's own address.
// It speaks just enough of the protocol for Ping.
func fakeKafkaBroker(t *testing.T, advertise func(self string) []string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	self := l.Addr().String()

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				for {
					version, corrID, _, msg, err := protocol.ReadRequest(c)
					if err != nil {
						return
					}
					if _, ok := msg.(*metadataAPI.Request); !ok {
						return
					}

					res := &metadataAPI.Response{}
					for i, addr := range advertise(self) {
						host, port, err := net.SplitHostPort(addr)
						if err != nil {
							return
						}
						p, _ := strconv.Atoi(port)
						res.Brokers = append(res.Brokers, metadataAPI.ResponseBroker{
							NodeID: int32(i + 1), Host: host, Port: int32(p),
						})
					}
					if err := protocol.WriteResponse(c, version, corrID, res); err != nil {
						return
					}
				}
			}()
		}
	}()
	return self
}

// closingListener accepts TCP connections and closes them at once, like a
// port that is open but does not speak Kafka.
func closingListener(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return l.Addr().String()
}

func TestKafka_Ping(t *testing.T) {
	newKafka := func(t *testing.T, brokers ...string) *Kafka {
		t.Helper()
		k, err := NewKafka(KafkaConfig{
			Brokers:           brokers,
			ConnectionTimeout: time.Second,
			ReadTimeout:       time.Second,
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = k.Close(context.Background()) })
		return k
	}
	advertiseSelf := func(self string) []string { return []string{self} }

	t.Run(
		"succeeds when bootstrap answers and an advertised broker is reachable",
		func(t *testing.T) {
			k := newKafka(t, fakeKafkaBroker(t, advertiseSelf))
			assert.NoError(t, k.Ping(context.Background()))
		},
	)

	t.Run("falls back to the next bootstrap broker", func(t *testing.T) {
		k := newKafka(t, freeClosedAddr(t), fakeKafkaBroker(t, advertiseSelf))
		assert.NoError(t, k.Ping(context.Background()))
	})

	t.Run("succeeds when only some advertised brokers are reachable", func(t *testing.T) {
		dead := freeClosedAddr(t)
		k := newKafka(t, fakeKafkaBroker(t, func(self string) []string {
			return []string{dead, self, dead}
		}))
		// Every rotation start must still find the reachable broker.
		for range 3 {
			assert.NoError(t, k.Ping(context.Background()))
		}
	})

	t.Run(
		"fails when advertised brokers are unreachable (bad advertised.listeners)",
		func(t *testing.T) {
			dead := freeClosedAddr(t)
			bootstrap := fakeKafkaBroker(t, func(string) []string { return []string{dead} })
			k := newKafka(t, bootstrap)

			err := k.Ping(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "advertised.listeners")
			assert.Contains(t, err.Error(), bootstrap)
			assert.Contains(t, err.Error(), dead)
		},
	)

	t.Run("fails when the bootstrap port does not speak Kafka", func(t *testing.T) {
		k := newKafka(t, closingListener(t))

		err := k.Ping(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "metadata request failed")
	})

	t.Run("fails and reports every broker when none are reachable", func(t *testing.T) {
		a, b := freeClosedAddr(t), freeClosedAddr(t)
		k := newKafka(t, a, b)

		err := k.Ping(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to reach any kafka broker")
		assert.Contains(t, err.Error(), a)
		assert.Contains(t, err.Error(), b)
	})
}

func TestKafka_Close(t *testing.T) {
	t.Run("is safe on a nil or unbuilt publisher", func(t *testing.T) {
		var k *Kafka
		assert.NoError(t, k.Close(context.Background()))
		assert.NoError(t, (&Kafka{}).Close(context.Background()))
	})

	t.Run("returns promptly when nothing is buffered", func(t *testing.T) {
		k, err := NewKafka(KafkaConfig{Brokers: []string{freeClosedAddr(t)}})
		require.NoError(t, err)
		assert.NoError(t, k.Close(context.Background()))
	})
}

func TestCloseWithContext(t *testing.T) {
	t.Run("returns the close result when it finishes in time", func(t *testing.T) {
		assert.NoError(t, closeWithContext(context.Background(), func() error { return nil }))

		boom := errors.New("boom")
		err := closeWithContext(context.Background(), func() error { return boom })
		assert.ErrorIs(t, err, boom)
	})

	t.Run("gives up when the context expires and lets close finish later", func(t *testing.T) {
		release := make(chan struct{})
		finished := make(chan struct{})

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		err := closeWithContext(ctx, func() error {
			<-release // simulate a flush stuck on an unresponsive broker
			close(finished)
			return nil
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)

		// The background close must still be able to complete (no leak).
		close(release)
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("background close never completed")
		}
	})
}

func TestNewKafka_Balancer(t *testing.T) {
	t.Run("defaults to FNV-1a hash when unset", func(t *testing.T) {
		k, err := NewKafka(KafkaConfig{Brokers: []string{"localhost:9092"}})
		require.NoError(t, err)
		assert.IsType(t, &kafka.Hash{}, k.writer.Balancer)
	})

	t.Run("uses the configured balancer", func(t *testing.T) {
		k, err := NewKafka(KafkaConfig{
			Brokers:  []string{"localhost:9092"},
			Balancer: kafka.Murmur2Balancer{},
		})
		require.NoError(t, err)
		assert.IsType(t, kafka.Murmur2Balancer{}, k.writer.Balancer)
	})
}

func TestKafka_pingDeadline(t *testing.T) {
	// ConnectionTimeout is set to prove it is not added to the read timeout.
	k := &Kafka{cfg: KafkaConfig{ConnectionTimeout: time.Minute, ReadTimeout: 2 * time.Second}}

	t.Run("uses the read timeout without a caller deadline", func(t *testing.T) {
		d := k.pingDeadline(context.Background())
		assert.WithinDuration(t, time.Now().Add(2*time.Second), d, 100*time.Millisecond)
	})

	t.Run("keeps an earlier caller deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		want, _ := ctx.Deadline()
		assert.Equal(t, want, k.pingDeadline(ctx))
	})

	t.Run("ignores a later caller deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()
		d := k.pingDeadline(ctx)
		assert.WithinDuration(t, time.Now().Add(2*time.Second), d, 100*time.Millisecond)
	})

	t.Run("falls back to the default when the read timeout is zero", func(t *testing.T) {
		d := (&Kafka{}).pingDeadline(context.Background())
		assert.WithinDuration(t, time.Now().Add(defaultPingTimeout), d, 100*time.Millisecond)
	})
}

func TestKafka_Publish_EmptyTypeIsPermanent(t *testing.T) {
	// The address is never dialled: the event is rejected before any write.
	k, err := NewKafka(KafkaConfig{Brokers: []string{"127.0.0.1:1"}})
	require.NoError(t, err)

	calls := map[string]func() error{
		"Publish": func() error {
			return k.Publish(context.Background(), relay.Event{Type: ""})
		},
		"PublishBatch": func() error {
			return k.PublishBatch(context.Background(), []relay.Event{{Type: ""}})
		},
	}

	for name, publish := range calls {
		t.Run(name, func(t *testing.T) {
			var pubErr *relay.PublishError
			require.ErrorAs(t, publish(), &pubErr)
			assert.False(t, pubErr.IsRetryable)
			assert.Equal(t, "INVALID_TOPIC", pubErr.Code)
		})
	}
}
