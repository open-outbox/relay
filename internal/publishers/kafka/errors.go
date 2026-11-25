package kafka

import (
	"errors"

	"github.com/open-outbox/relay/internal/utils"
	"github.com/segmentio/kafka-go"
)

func isKafkaErrorRetryable(err error) bool {
	if err == nil {
		return true
	}

	if utils.IsContextErr(err) {
		return true
	}

	var writeErrs kafka.WriteErrors
	if errors.As(err, &writeErrs) {
		for _, e := range writeErrs {
			if e != nil {
				if !isIndividualKafkaErrorRetryable(e) {
					return false
				}
			}
		}
		return true
	}

	return isIndividualKafkaErrorRetryable(err)
}

func isIndividualKafkaErrorRetryable(err error) bool {
	var kErr kafka.Error
	if !errors.As(err, &kErr) {
		return true
	}

	// Only errors caused by the message itself are permanent: retrying the
	// same event can never succeed. Everything else (leader elections,
	// metadata propagation, auth or ACL misconfiguration, broker outages)
	// can resolve without the event changing, so it stays retryable.
	//
	// Note that UnknownTopicOrPartition and InvalidMessage (CORRUPT_MESSAGE)
	// are deliberately absent: Kafka marks both as retriable, since they
	// occur transiently during leader moves, topic auto-creation and
	// in-transit corruption.
	switch kErr {
	case
		kafka.InvalidMessageSize,
		kafka.MessageSizeTooLarge,
		kafka.RecordListTooLarge,
		kafka.InvalidRecord,
		kafka.InvalidTopic:
		return false
	default:
		return true
	}
}
