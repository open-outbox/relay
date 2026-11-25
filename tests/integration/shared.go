//go:build integration

package integration

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/consul"
	"github.com/testcontainers/testcontainers-go/modules/kafka"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	consulImage   = "hashicorp/consul:1.15"
	kafkaImage    = "confluentinc/confluent-local:7.5.0"
	postgresImage = "postgres:16-alpine"
	redisImage    = "redis:7-alpine"

	// postgresTemplateDB holds the outbox schema. Tests never connect to it,
	// so it can be cloned with CREATE DATABASE ... TEMPLATE.
	postgresTemplateDB = "outbox_db"
)

// Containers shared by all tests in the package. Each starts the first time
// a test asks for it and is stopped in TestMain (main_test.go), so running a
// single test only starts what that test needs.
var (
	sharedPostgres = &shared[*postgresServer]{start: startPostgresServer}
	sharedKafka    = &shared[string]{start: startKafkaBroker}
	sharedConsul   = &shared[string]{start: startConsul}
)

type shared[T any] struct {
	start func(ctx context.Context) (T, testcontainers.Container, error)

	once      sync.Once
	value     T
	container testcontainers.Container
	err       error
}

func (s *shared[T]) get(t *testing.T) T {
	t.Helper()
	s.once.Do(func() {
		s.value, s.container, s.err = s.start(context.Background())
	})
	require.NoError(t, s.err, "failed to start shared container")
	return s.value
}

func (s *shared[T]) stop() {
	if s.container != nil {
		_ = s.container.Terminate(context.Background())
	}
}

type postgresServer struct {
	admin *sql.DB  // connected to the "postgres" database
	base  *url.URL // connection URL without a database name
}

func (p *postgresServer) connString(database string) string {
	u := *p.base
	u.Path = "/" + database
	return u.String()
}

func startPostgresServer(ctx context.Context) (*postgresServer, testcontainers.Container, error) {
	schemaPath, err := filepath.Abs("../../schema/postgres/open-outbox.sql")
	if err != nil {
		return nil, nil, err
	}

	c, err := postgres.Run(ctx,
		postgresImage,
		postgres.WithDatabase(postgresTemplateDB),
		postgres.WithUsername("user"),
		postgres.WithPassword("pass"),
		postgres.WithInitScripts(schemaPath),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(30*time.Second)),
	)
	if err != nil {
		return nil, c, err
	}

	connStr, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return nil, c, err
	}
	base, err := url.Parse(connStr)
	if err != nil {
		return nil, c, err
	}

	server := &postgresServer{base: base}
	server.admin, err = sql.Open("pgx", server.connString("postgres"))
	if err != nil {
		return nil, c, err
	}
	if err := server.admin.PingContext(ctx); err != nil {
		return nil, c, fmt.Errorf("connect to postgres: %w", err)
	}
	return server, c, nil
}

func startKafkaBroker(ctx context.Context) (string, testcontainers.Container, error) {
	c, err := kafka.Run(ctx, kafkaImage)
	if err != nil {
		return "", c, err
	}
	brokers, err := c.Brokers(ctx)
	if err != nil {
		return "", c, err
	}
	return brokers[0], c, nil
}

func startConsul(ctx context.Context) (string, testcontainers.Container, error) {
	c, err := consul.Run(ctx, consulImage)
	if err != nil {
		return "", c, err
	}
	endpoint, err := c.ApiEndpoint(ctx)
	return endpoint, c, err
}
