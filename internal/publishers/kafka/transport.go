package kafka

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"

	"github.com/open-outbox/relay/internal/utils"
	"github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/sasl"
	"github.com/segmentio/kafka-go/sasl/plain"
	"github.com/segmentio/kafka-go/sasl/scram"
)

func (k *Publisher) getSASLMechanism() (sasl.Mechanism, error) {
	switch k.cfg.SASLMechanism {
	case "plain":
		return plain.Mechanism{
			Username: k.cfg.Username,
			Password: k.cfg.Password,
		}, nil

	case "scram-sha-256":
		mechanism, err := scram.Mechanism(scram.SHA256, k.cfg.Username, k.cfg.Password)
		if err != nil {
			return nil, fmt.Errorf("failed to create SCRAM-SHA-256 mechanism: %w", err)
		}
		return mechanism, nil

	case "scram-sha-512":
		mechanism, err := scram.Mechanism(scram.SHA512, k.cfg.Username, k.cfg.Password)
		if err != nil {
			return nil, fmt.Errorf("failed to create SCRAM-SHA-512 mechanism: %w", err)
		}
		return mechanism, nil

	case "oauthbearer":
		return k.newOAuthBearer()

	default:
		return nil, fmt.Errorf("unsupported SASL mechanism for Kafka: %s", k.cfg.SASLMechanism)
	}
}

func (k *Publisher) buildTransport() (*kafka.Dialer, *kafka.Transport, error) {

	ca, err := utils.LoadBytes(k.cfg.TLSCA)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load TLS CA: %w", err)
	}
	cert, err := utils.LoadBytes(k.cfg.TLSCert)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load TLS Cert: %w", err)
	}
	key, err := utils.LoadBytes(k.cfg.TLSKey)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load TLS Key: %w", err)
	}

	// Supplying any TLS material, or asking to skip verification, implies TLS.
	// TLSEnabled covers brokers with publicly trusted certificates, where none
	// of those are needed: RootCAs stays nil and the system trust store is used.
	tlsEnabled := k.cfg.TLSEnabled || ca != nil || cert != nil || key != nil || k.cfg.Insecure

	var tlsConfig *tls.Config
	if tlsEnabled {
		tlsConfig = &tls.Config{
			ServerName:         k.cfg.ServerName,
			InsecureSkipVerify: k.cfg.Insecure,
			MinVersion:         k.cfg.TLSVersion,
		}

		if ca != nil {
			cp := x509.NewCertPool()
			if !cp.AppendCertsFromPEM(ca) {
				return nil, nil, fmt.Errorf(
					"failed to append CA certificate to pool: invalid or malformed X509 PEM data",
				)
			}
			tlsConfig.RootCAs = cp
		}

		if cert != nil || key != nil {
			if cert == nil || key == nil {
				return nil, nil, fmt.Errorf(
					"incomplete mTLS configuration: both TLSCert and TLSKey must be provided together",
				)
			}

			reloader, err := newCertReloader(k.cfg.TLSCert, k.cfg.TLSKey)
			if err != nil {
				return nil, nil, err
			}
			if reloader != nil {
				tlsConfig.GetClientCertificate = reloader.GetClientCertificate
			} else {
				pair, err := tls.X509KeyPair(cert, key)
				if err != nil {
					return nil, nil, fmt.Errorf("failed to parse mTLS keypair: %w", err)
				}
				tlsConfig.Certificates = []tls.Certificate{pair}
			}
		}
	}

	dialer := &kafka.Dialer{
		ClientID:  k.clientID(),
		Timeout:   k.cfg.ConnectionTimeout,
		DualStack: true,
		TLS:       tlsConfig,
		KeepAlive: k.cfg.KeepAlive,
	}

	if k.cfg.SASLMechanism != "" {
		mechanism, err := k.getSASLMechanism()
		if err != nil {
			return nil, nil, err
		}
		dialer.SASLMechanism = mechanism
	}

	// The Transport is what the Writer uses to produce messages, so it needs
	// its own dial, TLS and SASL settings; it does not inherit them from the
	// Dialer above (which is only used by Ping).
	netDialer := &net.Dialer{
		Timeout:   k.cfg.ConnectionTimeout,
		KeepAlive: k.cfg.KeepAlive,
	}

	transport := &kafka.Transport{
		ClientID:       k.clientID(),
		Dial:           netDialer.DialContext,
		DialTimeout:    k.cfg.ConnectionTimeout,
		TLS:            tlsConfig,
		SASL:           dialer.SASLMechanism,
		IdleTimeout:    k.cfg.IdleTimeout,
		MetadataTopics: k.cfg.MetadataTopics,
		MetadataTTL:    k.cfg.MetadataTTL,
	}

	return dialer, transport, nil
}

func (k *Publisher) clientID() string {
	if k.cfg.ClientID == "" {
		return DefaultClientID
	}
	return k.cfg.ClientID
}
