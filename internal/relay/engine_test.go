package relay

import (
	"errors"
	"testing"
	"time"

	"context"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

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
	mockStorage.On("ClaimBatch", ctx, 2, buffer).
		Return([]Event{fakeEvent}, nil)

	// Expect: Publish that 1 event
	mockPublisher.On("Publish", ctx, fakeEvent).
		Return(nil)

	// Expect: Mark that 1 event as delivered
	mockStorage.On("MarkDeliveredBatch", ctx, []uuid.UUID{eventID}).
		Return(int64(0), nil)

	mockPublisher.On("Connect", mock.Anything).Return(nil)

	// Initialize Engine

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

	tm, err := CreateNoopTelemetry()
	assert.NoError(t, err)

	e, err := NewEngine(
		mockStorage,
		mockPublisher,
		params,
		tm,
	)
	require.NoError(t, err, "Failed to initialize engine")

	err = e.connectToPublisher(context.Background())
	require.NoError(t, err)

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
	mockStorage.On("ClaimBatch", mock.Anything, 2, buffer).
		Return([]Event{event1, event2}, nil)

	// Event 1: Publish Success
	mockPublisher.On("Publish", mock.Anything, event1).
		Return(nil)

	// Event 2: Publish Failure
	mockPublisher.On("Publish", mock.Anything, event2).
		Return(errors.New("network error"))

	// Verify BOTH storage updates happen
	// Success side:
	mockStorage.On("MarkDeliveredBatch", mock.Anything, []uuid.UUID{id1}).
		Return(int64(0), nil)

	mockPublisher.On("Connect", mock.Anything).Return(nil)

	// Failure side: (Notice we check for id2 here)
	mockStorage.On("MarkFailedBatch", mock.Anything, mock.MatchedBy(func(failed []FailedEvent) bool {
		return len(failed) == 1 && failed[0].ID == id2
	})).
		Return(int64(1), nil)

	// Initialize Engine

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

	tm, err := CreateNoopTelemetry()
	assert.NoError(t, err)

	e, err := NewEngine(
		mockStorage,
		mockPublisher,
		params,
		tm,
	)
	require.NoError(t, err, "Failed to initialize engine")

	err = e.connectToPublisher(context.Background())
	require.NoError(t, err)

	_, err = e.process(ctx)

	assert.NoError(t, err)
	mockStorage.AssertExpectations(t)
}
