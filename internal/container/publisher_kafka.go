package container

import (
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/open-outbox/relay/internal/config"
	kafkapub "github.com/open-outbox/relay/internal/publishers/kafka"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/segmentio/kafka-go"
	"github.com/segmentio/kafka-go/compress"
)

var kafkaCompressions = map[string]kafka.Compression{
	"gzip":   compress.Gzip,
	"snappy": compress.Snappy,
	"lz4":    compress.Lz4,
	"zstd":   compress.Zstd,
	"none":   compress.None,
}

// Both the names and the numeric values Kafka uses are accepted.
var kafkaRequiredAcks = map[string]kafka.RequiredAcks{
	"all":  kafka.RequireAll,
	"one":  kafka.RequireOne,
	"none": kafka.RequireNone,
	"-1":   kafka.RequireAll,
	"1":    kafka.RequireOne,
	"0":    kafka.RequireNone,
}

// The balancers are only read, so sharing one instance is safe.
var kafkaPartitioners = map[string]kafka.Balancer{
	"":        &kafka.Hash{},           // fnv1a (Sarama-compatible)
	"fnv1a":   &kafka.Hash{},           // Sarama-compatible
	"murmur2": kafka.Murmur2Balancer{}, // Java client, Streams, Connect
	"crc32":   kafka.CRC32Balancer{},   // librdkafka consistent_random
}

var kafkaSASLMechanisms = map[string]string{
	"":              "",
	"plain":         "plain",
	"scram-sha-256": "scram-sha-256",
	"scram-sha-512": "scram-sha-512",
	"oauthbearer":   "oauthbearer",
}

var kafkaTLSVersions = map[string]uint16{
	"":    tls.VersionTLS12,
	"1.0": tls.VersionTLS10,
	"1.1": tls.VersionTLS11,
	"1.2": tls.VersionTLS12,
	"1.3": tls.VersionTLS13,
}

func buildKafkaPublisher(cfg config.Config) (relay.Publisher, error) {
	brokers := parseKafkaBrokers(cfg.PublisherURL)
	if len(brokers) == 0 {
		return nil, fmt.Errorf(
			"failed to create publisher: no broker addresses provided for kafka publisher",
		)
	}

	compression, err := lookupOption("Kafka compression", cfg.KafkaCompression, kafkaCompressions)
	if err != nil {
		return nil, err
	}
	acks, err := lookupOption("Kafka required acks", cfg.KafkaRequiredAcks, kafkaRequiredAcks)
	if err != nil {
		return nil, err
	}
	balancer, err := lookupOption("Kafka partitioner", cfg.KafkaPartitioner, kafkaPartitioners)
	if err != nil {
		return nil, err
	}
	mechanism, err := lookupOption(
		"Kafka SASL mechanism", cfg.KafkaSASLMechanism, kafkaSASLMechanisms,
	)
	if err != nil {
		return nil, err
	}
	tlsVersion, err := lookupOption("Kafka TLS version", cfg.KafkaTLSVersion, kafkaTLSVersions)
	if err != nil {
		return nil, err
	}

	extensions, err := parseKafkaOAuthExtensions(cfg.KafkaOAuthExtensions)
	if err != nil {
		return nil, err
	}

	return kafkapub.New(kafkapub.Config{
		Brokers:           brokers,
		ClientID:          strings.TrimSpace(cfg.KafkaClientID),
		MaxAttempts:       cfg.KafkaMaxAttempts,
		WriteTimeout:      cfg.KafkaWriteTimeout,
		ReadTimeout:       cfg.KafkaReadTimeout,
		ConnectionTimeout: cfg.KafkaConnectionTimeout,
		BatchSize:         cfg.KafkaBatchSize,
		BatchBytes:        cfg.KafkaBatchBytes,
		BatchTimeout:      cfg.KafkaBatchTimeout,
		Async:             cfg.KafkaAsync,
		Compression:       compression,
		RequiredAcks:      acks,
		Balancer:          balancer,
		TLSEnabled:        cfg.KafkaTLSEnabled,
		TLSCA:             cfg.KafkaTLSCA,
		TLSCert:           cfg.KafkaTLSCert,
		TLSKey:            cfg.KafkaTLSKey,
		TLSVersion:        tlsVersion,
		ServerName:        cfg.KafkaServerName,
		Insecure:          cfg.KafkaInsecure,
		SASLMechanism:     mechanism,
		Username:          cfg.KafkaUsername,
		Password:          cfg.KafkaPassword,
		OAuthTokenURL:     strings.TrimSpace(cfg.KafkaOAuthTokenURL),
		OAuthClientID:     cfg.KafkaOAuthClientID,
		OAuthClientSecret: cfg.KafkaOAuthClientSecret,
		OAuthScopes:       splitAndTrim(cfg.KafkaOAuthScopes),
		OAuthExtensions:   extensions,
		IdleTimeout:       cfg.KafkaIdleTimeout,
		KeepAlive:         cfg.KafkaKeepAlive,
	})
}

func parseKafkaBrokers(url string) []string {
	var brokers []string
	for _, entry := range strings.Split(url, ",") {
		entry = strings.TrimPrefix(strings.TrimSpace(entry), "kafka://")
		if entry != "" {
			brokers = append(brokers, entry)
		}
	}
	return brokers
}

func parseKafkaOAuthExtensions(list string) (map[string]string, error) {
	items := splitAndTrim(list)
	if len(items) == 0 {
		return nil, nil
	}

	extensions := make(map[string]string, len(items))
	for _, item := range items {
		key, value, ok := strings.Cut(item, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf(
				"invalid KAFKA_OAUTH_EXTENSIONS entry %q: expected key=value",
				item,
			)
		}
		extensions[key] = strings.TrimSpace(value)
	}
	return extensions, nil
}
