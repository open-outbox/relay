package kafka

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/segmentio/kafka-go"
)

// defaultPingTimeout is used when the read timeout is zero.
const defaultPingTimeout = 10 * time.Second

func (k *Publisher) fetchBrokers(ctx context.Context, addr string) ([]kafka.Broker, error) {
	// Not k.transport: it answers metadata requests from its cache.
	conn, err := k.dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(k.pingDeadline(ctx)); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}

	brokers, err := conn.Brokers()
	if err != nil {
		return nil, fmt.Errorf("metadata request failed: %w", err)
	}
	if len(brokers) == 0 {
		return nil, errors.New("metadata response listed no brokers")
	}
	return brokers, nil
}

func (k *Publisher) pingAdvertised(
	ctx context.Context,
	bootstrap string,
	brokers []kafka.Broker,
) error {
	start := int(k.pingCursor.Add(1)) % len(brokers)
	errs := make([]error, 0, len(brokers))

	for i := range brokers {
		b := brokers[(start+i)%len(brokers)]
		addr := net.JoinHostPort(b.Host, strconv.Itoa(b.Port))

		conn, err := k.dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			return nil
		}

		errs = append(errs, fmt.Errorf("broker %d at %s: %w", b.ID, addr, err))
		if ctx.Err() != nil {
			break
		}
	}

	return fmt.Errorf(
		"bootstrap broker %s answered, but none of the brokers it advertises is reachable "+
			"(check the brokers' advertised.listeners): %w",
		bootstrap, errors.Join(errs...),
	)
}

func (k *Publisher) pingDeadline(ctx context.Context) time.Time {
	timeout := k.cfg.ReadTimeout
	if timeout <= 0 {
		timeout = defaultPingTimeout
	}
	deadline := time.Now().Add(timeout)

	// kafka.Conn ignores ctx, so the caller's deadline only applies through
	// this one; keep whichever comes first.
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		return d
	}
	return deadline
}
