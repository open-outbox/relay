package relay

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/open-outbox/relay/internal/telemetry"
	"github.com/stretchr/testify/mock"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
)

// MockStorage is a test double that implements the Storage interface.
// It allows for fine-grained control and assertions over database interactions.
type MockStorage struct {
	mock.Mock
}

// ClaimBatch mocks the retrieval and locking of a batch of events.
func (m *MockStorage) ClaimBatch(
	ctx context.Context,
	relayID string,
	size int,
	buffer []Event,
) ([]Event, error) {
	args := m.Called(ctx, relayID, size, buffer)
	return args.Get(0).([]Event), args.Error(1)
}

// MarkDeliveredBatch mocks the finalization of successfully processed events.
func (m *MockStorage) MarkDeliveredBatch(
	ctx context.Context,
	ids []uuid.UUID,
	relayID string,
) error {
	args := m.Called(ctx, ids, relayID)
	return args.Error(0)
}

// MarkFailedBatch mocks the recording of processing failures and retry metadata.
func (m *MockStorage) MarkFailedBatch(
	ctx context.Context,
	failed []FailedEvent,
	relayID string,
) error {
	args := m.Called(ctx, failed, relayID)
	return args.Error(0)
}

// GetStats mocks the retrieval of operational metrics from the storage layer.
func (m *MockStorage) GetStats(ctx context.Context) (Stats, error) {
	args := m.Called(ctx)
	return args.Get(0).(Stats), args.Error(1)
}

// Prune mocks the archival or deletion of old processed events.
func (m *MockStorage) Prune(
	ctx context.Context,
	opts PruneOptions,
) (PruneResult, error) {
	args := m.Called(ctx, opts)
	return args.Get(0).(PruneResult), args.Error(1)
}

// ReapExpiredLeases mocks the recovery of events held by inactive relay instances.
func (m *MockStorage) ReapExpiredLeases(
	ctx context.Context,
	leaseTimeout time.Duration,
	limit int,
) (int64, error) {
	args := m.Called(ctx, leaseTimeout, limit)
	return args.Get(0).(int64), args.Error(1)
}

// Close mocks the graceful shutdown of storage resources.
func (m *MockStorage) Close(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// Ping mocks the connectivity check for the storage backend.
func (m *MockStorage) Ping(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// MockPublisher is a test double that implements the Publisher interface.
// It is used to verify that events are correctly dispatched to messaging systems.
type MockPublisher struct {
	mock.Mock
}

// Connect mocks the connect method of publisher.
func (m *MockPublisher) Connect(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// Publish mocks the dispatching of a single event to the target messaging system.
func (m *MockPublisher) Publish(ctx context.Context, event Event) error {
	args := m.Called(ctx, event)
	return args.Error(0)
}

// Close mocks the graceful shutdown of publisher resources.
func (m *MockPublisher) Close(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// Ping mocks the connectivity check for the messaging backend.
func (m *MockPublisher) Ping(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// CreateNoopTelemetry initializes a Telemetry container with discarded logs,
// no-op metrics, and no-op traces. Useful for isolating logic in unit tests.
func CreateNoopTelemetry() (telemetry.Telemetry, error) {
	metrics, err := telemetry.NewMetrics(metricnoop.NewMeterProvider())
	if err != nil {
		return telemetry.Telemetry{}, err
	}

	return telemetry.Telemetry{
		Tracer:  tracenoop.NewTracerProvider().Tracer("TestTracer"),
		Meter:   metricnoop.NewMeterProvider().Meter("TestMeter"),
		Logger:  zap.NewNop(),
		Metrics: metrics,
	}, nil
}
