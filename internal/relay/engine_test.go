package relay

import (
	"errors"
	"testing"
	"time"

	"context"

	"github.com/google/uuid"
	"github.com/open-outbox/relay/internal/telemetry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
)

// Mock Storage
type MockStorage struct {
	mock.Mock
}

func (m *MockStorage) ClaimBatch(
	ctx context.Context,
	relayID string,
	size int,
	buffer []Event,
) ([]Event, error) {
	args := m.Called(ctx, relayID, size, buffer)
	return args.Get(0).([]Event), args.Error(1)
}

func (m *MockStorage) MarkDeliveredBatch(
	ctx context.Context,
	ids []uuid.UUID,
	relayID string,
) error {
	args := m.Called(ctx, ids, relayID)
	return args.Error(0)
}

func (m *MockStorage) MarkFailedBatch(
	ctx context.Context,
	failed []FailedEvent,
	relayID string,
) error {
	args := m.Called(ctx, failed, relayID)
	return args.Error(0)
}

func (m *MockStorage) GetStats(ctx context.Context) (Stats, error) {
	args := m.Called(ctx)
	return args.Get(0).(Stats), args.Error(1)
}

func (m *MockStorage) Prune(
	ctx context.Context,
	opts PruneOptions,
) (PruneResult, error) {
	args := m.Called(ctx, opts)
	return args.Get(0).(PruneResult), args.Error(1)
}

func (m *MockStorage) ReapExpiredLeases(
	ctx context.Context,
	leaseTimeout time.Duration,
	limit int,
) (int64, error) {
	args := m.Called(ctx, leaseTimeout, limit)
	return args.Get(0).(int64), args.Error(1)
}

func (m *MockStorage) Close(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockStorage) Ping(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

// Mock Publisher

type MockPublisher struct {
	mock.Mock
}

func (m *MockPublisher) Publish(ctx context.Context, event Event) error {
	args := m.Called(ctx, event)
	return args.Error(0)
}

func (m *MockPublisher) Close(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func (m *MockPublisher) Ping(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func TestEngine_Process_HappyPath(t *testing.T) {
	// Setup Dependencies
	mockStorage := new(MockStorage)
	mockPublisher := new(MockPublisher)

	// Create some dummy data
	eventID := uuid.New()
	fakeEvent := Event{
		ID:        eventID,
		Type:      "user.created",
		Payload:   []byte(`{"id": 1}`),
		CreatedAt: time.Now().Add(-1 * time.Minute),
	}

	// Define Expectations (The "Contract")
	ctx := mock.Anything
	relayID := "dummy"
	buffer := make([]Event, 2)

	// Expect: Claim 1 event
	mockStorage.On("ClaimBatch", ctx, relayID, 2, buffer).
		Return([]Event{fakeEvent}, nil)

	// Expect: Publish that 1 event
	mockPublisher.On("Publish", ctx, fakeEvent).
		Return(nil)

	// Expect: Mark that 1 event as delivered
	mockStorage.On("MarkDeliveredBatch", ctx, []uuid.UUID{eventID}, relayID).
		Return(nil)

	// Initialize Engine
	// (Using no-op providers for metrics/tracing to keep it simple)
	metrics, err := telemetry.NewMetrics(metricnoop.NewMeterProvider())
	assert.NoError(t, err)

	rp := ExponentialBackoff{
		MaxAttempts: 10,
		BaseDelay:   1 * time.Second,
		MaxDelay:    10 * time.Second,
		Jitter:      0.15,
	}
	params := EngineParams{
		RelayID:            relayID,
		Interval:           1 * time.Second,
		BatchSize:          2,
		LeaseTimeout:       1 * time.Second,
		ReapBatchSize:      5,
		RetryPolicy:        rp,
		EnableBatchPublish: false,
	}

	tm := telemetry.Telemetry{
		Tracer:  tracenoop.NewTracerProvider().Tracer("TestTracer"),
		Meter:   metricnoop.NewMeterProvider().Meter("TestMeter"),
		Logger:  zap.NewNop(),
		Metrics: metrics,
	}

	e, err := NewEngine(
		mockStorage,
		mockPublisher,
		params,
		tm,
	)
	require.NoError(t, err, "Failed to initialize engine")

	// Execution
	_, err = e.process(context.Background())

	// Assertions
	assert.NoError(t, err)
	mockStorage.AssertExpectations(t)
	mockPublisher.AssertExpectations(t)
}

func TestEngine_Process_MixedBatch(t *testing.T) {
	mockStorage := new(MockStorage)
	mockPublisher := new(MockPublisher)

	id1, id2 := uuid.New(), uuid.New()
	event1 := Event{ID: id1, Type: "success.event"}
	event2 := Event{ID: id2, Type: "fail.event"}
	relayID := "Dummy"
	buffer := make([]Event, 2)
	ctx := context.Background()

	// Return BOTH events
	mockStorage.On("ClaimBatch", mock.Anything, relayID, 2, buffer).
		Return([]Event{event1, event2}, nil)

	// Event 1: Publish Success
	mockPublisher.On("Publish", mock.Anything, event1).
		Return(nil)

	// Event 2: Publish Failure
	mockPublisher.On("Publish", mock.Anything, event2).
		Return(errors.New("network error"))

	// Verify BOTH storage updates happen
	// Success side:
	mockStorage.On("MarkDeliveredBatch", mock.Anything, []uuid.UUID{id1}, relayID).
		Return(nil)

	// Failure side: (Notice we check for id2 here)
	mockStorage.On("MarkFailedBatch", mock.Anything, mock.MatchedBy(func(failed []FailedEvent) bool {
		return len(failed) == 1 && failed[0].ID == id2
	}), relayID).
		Return(nil)

	// Initialize & Run
	// Initialize Engine
	// (Using no-op providers for metrics/tracing to keep it simple)
	metrics, err := telemetry.NewMetrics(metricnoop.NewMeterProvider())
	assert.NoError(t, err)
	rp := ExponentialBackoff{
		MaxAttempts: 10,
		BaseDelay:   1 * time.Second,
		MaxDelay:    10 * time.Second,
		Jitter:      0.15,
	}
	params := EngineParams{
		RelayID:            relayID,
		Interval:           1 * time.Second,
		BatchSize:          2,
		LeaseTimeout:       1 * time.Second,
		ReapBatchSize:      2,
		RetryPolicy:        rp,
		EnableBatchPublish: false,
	}

	tm := telemetry.Telemetry{
		Tracer:  tracenoop.NewTracerProvider().Tracer("TestTracer"),
		Meter:   metricnoop.NewMeterProvider().Meter("TestMeter"),
		Logger:  zap.NewNop(),
		Metrics: metrics,
	}

	e, err := NewEngine(
		mockStorage,
		mockPublisher,
		params,
		tm,
	)
	require.NoError(t, err, "Failed to initialize engine")

	_, err = e.process(ctx)

	assert.NoError(t, err)
	mockStorage.AssertExpectations(t)
}
