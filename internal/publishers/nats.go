package publishers

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go"
)

type Nats struct {
	conn *nats.Conn
}

// NewNats initializes a new NATS publisher.
func NewNats(url string) (*Nats, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}
	return &Nats{conn: nc}, nil
}

// Publish sends the payload to a NATS subject (topic).
func (n *Nats) Publish(ctx context.Context, topic string, payload []byte) error {
	// TODO: 
	// NATS doesn't natively take a context in the simple Publish call, 
	// but we can use it for timeout logic if needed.
	return n.conn.Publish(topic, payload)
}

// Close ensures the connection is shut down cleanly.
func (n *Nats) Close() {
	n.conn.Close()
}