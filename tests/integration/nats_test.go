//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib" // Required for sql.Open with pgx
	dockercontainer "github.com/moby/moby/api/types/container"
	nats_go "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/open-outbox/relay/internal/container"
	natspub "github.com/open-outbox/relay/internal/publishers/nats"
	"github.com/open-outbox/relay/internal/relay"
)

func TestNatsHappyPath(t *testing.T) {
	db, pgConnStr := setupTestPostgres(t)

	nc, natsUrl := setupNats(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	setNatsRelayEnv(t, pgConnStr, natsUrl)

	// Build Application via DI Container
	di, err := container.BuildContainer(ctx)
	require.NoError(t, err)

	// The server is shared, so the stream and subjects are this test's own.
	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats_go.StreamConfig{
		Name:     "RELAY_TESTS_HAPPY",
		Subjects: []string{"relay-tests.happy.>"},
	})
	require.NoError(t, err, "Failed to create JetStream stream")
	t.Cleanup(func() { _ = js.DeleteStream("RELAY_TESTS_HAPPY") })

	topic := "relay-tests.happy.v1"
	sub, err := nc.SubscribeSync(topic)
	require.NoError(t, err)

	eventID := uuid.New()
	payload := []byte(`{"order_id": 123, "status": "paid"}`)

	_, err = db.Exec(`
		INSERT INTO openoutbox_events (event_id, event_type, payload, headers, status)
		VALUES ($1, $2, $3, '{"trace_id":"abc","retry":3,"skip":null}', 'PENDING')`,
		eventID, topic, payload)
	require.NoError(t, err)

	err = di.Invoke(func(pub relay.Publisher) {
		pub.Connect(ctx)
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		assert.NoError(t, pub.Ping(pingCtx), "Publisher Ping method should be covered and pass")
	})
	require.NoError(t, err)

	// Start the Engine through DI
	err = di.Invoke(func(engine *relay.Engine) {
		go func() {
			if err := engine.Start(ctx); err != nil {
				// We expect context canceled error on cleanup
				t.Logf("Engine stopped: %v", err)
			}
		}()
	})
	require.NoError(t, err)

	// Assertions
	// Check NATS received the message
	msg, err := sub.NextMsg(5 * time.Second)
	assert.NoError(t, err, "Should have received message on NATS")
	require.NotNil(t, msg, "Message should not be nil")
	assert.Equal(t, payload, msg.Data)
	assert.Equal(t, "abc", msg.Header.Get("trace_id"))
	assert.Equal(t, "3", msg.Header.Get("retry"), "numbers are sent as their JSON text")
	assert.Empty(t, msg.Header.Values("skip"), "null headers are left out")
	assert.Equal(t, eventID.String(), msg.Header.Get("Nats-Msg-Id"))

	// Check DB updated to 'DELIVERED' (matching your schema status)
	var status string
	// Wait a moment for the DB write to propagate after NATS publish
	assert.Eventually(t, func() bool {
		err := db.QueryRow("SELECT status FROM openoutbox_events WHERE event_id = $1", eventID).
			Scan(&status)
		return err == nil && status == "DELIVERED"
	}, 2*time.Second, 50*time.Millisecond)
}

func TestNats_PublisherErrorHandling(t *testing.T) {
	db, pgConnStr := setupTestPostgres(t)
	_, natsUrl := setupNats(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	setNatsRelayEnv(t, pgConnStr, natsUrl)

	di, _ := container.BuildContainer(ctx)

	// No stream covers this subject, so NATS rejects the publish.
	eventID := uuid.New()
	_, err := db.Exec(`INSERT INTO openoutbox_events (event_id, event_type, payload, status)
                       VALUES ($1, 'relay-tests.nostream.v1', '{}', 'PENDING')`, eventID)
	require.NoError(t, err)

	// Start the Engine through DI
	err = di.Invoke(func(engine *relay.Engine) {
		go func() {
			if err := engine.Start(ctx); err != nil {
				// We expect context canceled error on cleanup
				t.Logf("Engine stopped: %v", err)
			}
		}()
	})
	require.NoError(t, err)

	// ASSERTION: The engine should increment 'attempts' because NATS returned an error
	assert.Eventually(t, func() bool {
		var attempts int
		var status string
		err := db.QueryRow("SELECT attempts, status FROM openoutbox_events WHERE event_id = $1", eventID).
			Scan(&attempts, &status)

		// We want to see attempts > 0 and status still PENDING (or DELIVERING)
		return err == nil && attempts > 0 && status != "DELIVERED"
	}, 5*time.Second, 50*time.Millisecond, "Engine should record failure when NATS reject publish")
}

// TestNats_RecoversAfterRepeatedAuthFailures checks that the publisher
// reconnects once its credentials work again, after the server rejected
// them on consecutive reconnects. By default nats.go closes the connection
// for good the second time it sees the same auth error, and the engine
// never calls Connect again, so the relay stayed paused until restarted.
func TestNats_RecoversAfterRepeatedAuthFailures(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	srv := setupNatsWithPassword(t, "first")
	srv.addStream(t, "first", "RELAY_TESTS_AUTH", "relay-tests.auth.>")

	pub, err := natspub.New(srv.url("first"), 2*time.Second, 2*time.Second)
	require.NoError(t, err)
	require.NoError(t, pub.Connect(ctx))
	t.Cleanup(func() { _ = pub.Close(context.Background()) })

	event := relay.Event{ID: uuid.New(), Type: "relay-tests.auth.v1", Payload: []byte(`{}`)}
	require.NoError(t, pub.Publish(ctx, event))

	// The reload disconnects the relay, and its reconnects are rejected.
	srv.setPassword(t, "second")
	require.Eventually(t, func() bool { return pub.Ping(ctx) != nil },
		10*time.Second, 100*time.Millisecond, "Ping should fail once the password changed")

	// nats.go gives up by default after the second rejection in a row.
	require.Eventually(t, func() bool { return srv.authFailures(t) >= 2 },
		20*time.Second, 200*time.Millisecond, "the server should reject the relay's reconnects")

	srv.setPassword(t, "first")
	require.Eventually(t, func() bool { return pub.Ping(ctx) == nil },
		20*time.Second, 200*time.Millisecond,
		"the publisher should reconnect once its credentials are accepted again")

	event.ID = uuid.New()
	require.NoError(t, pub.Publish(ctx, event))
}

// natsAuthServer is a NATS server of the test's own whose password can be
// changed while it runs, by rewriting its config and reloading it.
type natsAuthServer struct {
	container *tcnats.NATSContainer
	hostPort  string
}

const (
	natsAuthUser       = "relay"
	natsAuthConfigPath = "/etc/nats/relay-test.conf"
)

func natsAuthConfig(password string) string {
	return fmt.Sprintf(`port: 4222
jetstream { store_dir: /tmp/jetstream }
authorization { user: %s, password: %q }
`, natsAuthUser, password)
}

func setupNatsWithPassword(t *testing.T, password string) *natsAuthServer {
	t.Helper()
	ctx := context.Background()

	c, err := tcnats.Run(ctx, natsImage,
		testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader:            strings.NewReader(natsAuthConfig(password)),
			ContainerFilePath: natsAuthConfigPath,
			FileMode:          0o644,
		}),
		testcontainers.WithWaitStrategy(wait.ForLog("Server is ready")),
		testcontainers.WithConfigModifier(func(conf *dockercontainer.Config) {
			conf.Cmd = []string{"-c", natsAuthConfigPath}
		}),
	)
	t.Cleanup(func() {
		if c != nil {
			terminate(c)
		}
	})
	require.NoError(t, err)

	hostPort, err := c.PortEndpoint(ctx, "4222/tcp", "")
	require.NoError(t, err)
	return &natsAuthServer{container: c, hostPort: hostPort}
}

func (s *natsAuthServer) url(password string) string {
	return fmt.Sprintf("nats://%s:%s@%s", natsAuthUser, password, s.hostPort)
}

func (s *natsAuthServer) addStream(t *testing.T, password, name, subjects string) {
	t.Helper()
	nc, err := nats_go.Connect(s.url(password))
	require.NoError(t, err)
	defer nc.Close()

	js, err := nc.JetStream()
	require.NoError(t, err)
	_, err = js.AddStream(&nats_go.StreamConfig{Name: name, Subjects: []string{subjects}})
	require.NoError(t, err)
}

// setPassword rewrites the config and reloads it. On reload the server
// disconnects clients whose credentials it no longer accepts.
func (s *natsAuthServer) setPassword(t *testing.T, password string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.container.CopyToContainer(ctx,
		[]byte(natsAuthConfig(password)), natsAuthConfigPath, 0o644))

	// The image's entrypoint execs nats-server, so it is PID 1.
	code, _, err := s.container.Exec(ctx, []string{"kill", "-HUP", "1"})
	require.NoError(t, err)
	require.Zero(t, code, "reloading the NATS config failed")
}

// authFailures counts the logins the server has rejected so far.
func (s *natsAuthServer) authFailures(t *testing.T) int {
	t.Helper()
	logs, err := s.container.Logs(context.Background())
	require.NoError(t, err)
	defer logs.Close()

	data, err := io.ReadAll(logs)
	require.NoError(t, err)
	return strings.Count(strings.ToLower(string(data)), "authentication error")
}

// TestNats_ConcurrentUseDuringConnect mimics startup: the HTTP server's
// readiness probe calls Ping while the engine is still connecting. Run with
// -race; reading the connection while Connect stored it used to race.
func TestNats_ConcurrentUseDuringConnect(t *testing.T) {
	_, url := setupNats(t)
	ctx := context.Background()

	pub, err := natspub.New(url, time.Second, time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { _ = pub.Close(ctx) })

	event := relay.Event{ID: uuid.New(), Type: "relay-tests.concurrent.v1", Payload: []byte(`{}`)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 500 {
			_ = pub.Ping(ctx)
			_ = pub.Publish(ctx, event) // no stream covers it; only the race matters
		}
	}()

	require.NoError(t, pub.Connect(ctx))
	require.NoError(t, pub.Connect(ctx), "a second Connect is a no-op")
	<-done
	require.NoError(t, pub.Ping(ctx))
}
