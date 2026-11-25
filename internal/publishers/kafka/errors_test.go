package kafka

import (
	"context"
	"fmt"
	"testing"

	"github.com/segmentio/kafka-go"
	"github.com/stretchr/testify/assert"
)

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
