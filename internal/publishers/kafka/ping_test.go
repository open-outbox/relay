package kafka

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/segmentio/kafka-go/protocol"
	metadataAPI "github.com/segmentio/kafka-go/protocol/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	newKafka := func(t *testing.T, brokers ...string) *Publisher {
		t.Helper()
		k, err := New(Config{
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

func TestKafka_pingDeadline(t *testing.T) {
	// ConnectionTimeout is set to prove it is not added to the read timeout.
	k := &Publisher{cfg: Config{ConnectionTimeout: time.Minute, ReadTimeout: 2 * time.Second}}

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
		d := (&Publisher{}).pingDeadline(context.Background())
		assert.WithinDuration(t, time.Now().Add(defaultPingTimeout), d, 100*time.Millisecond)
	})
}
