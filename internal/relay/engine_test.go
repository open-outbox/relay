package relay

import (
	"testing"
	"time"

	"context"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"go.uber.org/zap"
)

type MockStorage struct {
	mock.Mock
}

func (m *MockStorage) ClaimBatch(ctx context.Context, relayId string, size int, lease int) ([]Event, error) {
	args := m.Called(ctx, relayId, size, lease)
	return args.Get(0).([]Event), args.Error(1)
}

func (m *MockStorage) MarkDeliveredBatch(ctx context.Context, ids []uuid.UUID, relayId string) error {
	args := m.Called(ctx, ids, relayId)
	return args.Error(0)
}

func (m *MockStorage) MarkFailedBatch(ctx context.Context, failed []FailedEvent, relayId string) error {
	args := m.Called(ctx, failed, relayId)
	return args.Error(0)
}

type MockPublisher struct {
	mock.Mock
}

func (m *MockPublisher) Publish(ctx context.Context, event Event) (PublishResult, error) {
	args := m.Called(ctx, event)
	return args.Get(0).(PublishResult), args.Error(1)
}

func TestRetryPolicy_NextBackoff(t *testing.T) {
	policy := RetryPolicy{
		MaxAttempts: 10,
		BaseDelay:   1 * time.Second,
		MaxDelay:    10 * time.Second,
	}

	t.Run("Exponential Growth", func(t *testing.T) {
		// Attempt 1: 2^0 * 1s = 1s (+ jitter)
		delay, retry := policy.NextBackoff(1)
		assert.True(t, retry)
		assert.GreaterOrEqual(t, delay, 1*time.Second)
		assert.Less(t, delay, 1200*time.Millisecond, "~1s plus small jitter")

		// Attempt 3: 2^2 * 1s = 4s (+ jitter)
		delay, retry = policy.NextBackoff(3)
		assert.True(t, retry)
		assert.GreaterOrEqual(t, delay, 4*time.Second)
	})

	t.Run("Respects MaxDelay", func(t *testing.T) {
		// Attempt 5: 2^4 * 1s = 16s, but MaxDelay is 10s
		delay, retry := policy.NextBackoff(5)
		assert.True(t, retry, "Should still retry on attempt 5")
		// MaxDelay is 10s. Jitter is 10% of 10s (1s). Max possible is 11s.
		assert.LessOrEqual(t, delay, 11*time.Second)
		assert.GreaterOrEqual(t, delay, 10*time.Second)
	})

	t.Run("Stop After MaxAttempts", func(t *testing.T) {
		_, retry := policy.NextBackoff(11)
		assert.False(t, retry, "Should stop after reaching MaxAttempts")
	})

	t.Run("Jitter Variation", func(t *testing.T) {
		// Run it twice for the same attempt.
		// Statistically, with 10% jitter, they shouldn't be identical.
		d1, _ := policy.NextBackoff(2)
		d2, _ := policy.NextBackoff(2)

		// Note: There's a tiny chance they match, but in a test,
		// this proves the random seed is working.
		assert.NotEqual(t, d1.Nanoseconds(), d2.Nanoseconds(), "Jitter should provide different results")
	})
}

func TestEngine_Process_HappyPath(t *testing.T) {
	// 1. Setup Dependencies
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

	// 2. Define Expectations (The "Contract")
	ctx := mock.Anything
	relayID := "test-relay"

	// Expect: Claim 1 event
	mockStorage.On("ClaimBatch", ctx, relayID, 10, 5).
		Return([]Event{fakeEvent}, nil)

	// Expect: Publish that 1 event
	mockPublisher.On("Publish", ctx, fakeEvent).
		Return(PublishResult{Status: 200}, nil)

	// Expect: Mark that 1 event as delivered
	mockStorage.On("MarkDeliveredBatch", ctx, []uuid.UUID{eventID}, relayID).
		Return(nil)

	// 3. Initialize Engine
	// (Using no-op providers for metrics/tracing to keep it simple)
	metrics, err := NewMetrics(metricnoop.NewMeterProvider())
	assert.NoError(t, err)

	e := NewEngine(
		mockStorage,
		mockPublisher,
		1*time.Second,
		10,
		5,
		zap.NewNop(),
		metrics, // Ensure your Metrics struct handles nil or use a mock
		tracenoop.NewTracerProvider(),
		metricnoop.NewMeterProvider(),
	)
	e.relayId = relayID // Override for the test

	// 4. Execution
	err = e.process(context.Background())

	// 5. Assertions
	assert.NoError(t, err)
	mockStorage.AssertExpectations(t)
	mockPublisher.AssertExpectations(t)
}
