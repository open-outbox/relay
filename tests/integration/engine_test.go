//go build: integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestEngine_GracefulShutdown(t *testing.T) {
	// Setup mocks
	store := new(relay.MockStorage)
	pub := new(relay.MockPublisher)

	store.On("ClaimBatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]relay.Event{}, nil)
	store.On("Close", mock.Anything).Return(nil)
	store.On("ReapExpiredLeases", mock.Anything, mock.Anything, mock.Anything).
		Return(int64(0), nil)
	store.On("GetStats", mock.Anything).
		Return(relay.Stats{}, nil)
	pub.On("Close", mock.Anything).Return(nil)

	// Initialize Engine
	params := relay.EngineParams{
		Interval:     10 * time.Millisecond,
		BatchSize:    10,
		LeaseTimeout: 1 * time.Minute,
	}

	tel, err := relay.CreateNoopTelemetry()
	assert.NoError(t, err)

	engine, err := relay.NewEngine(store, pub, params, tel)
	assert.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	// Start the engine in a goroutine
	done := make(chan error, 1)
	go func() {
		done <- engine.Start(ctx)
	}()

	// Let it run for a few ticks
	time.Sleep(50 * time.Millisecond)

	// Trigger Shutdown
	cancel()

	// Assertions
	select {
	case err := <-done:
		// Engine should return context.Canceled
		assert.ErrorIs(
			t,
			err,
			context.Canceled,
			"Engine should return context.Canceled on shutdown",
		)
	case <-time.After(2 * time.Second):
		t.Fatal("Engine failed to shut down within timeout")
	}

	err = engine.Stop(context.Background())
	assert.NoError(t, err)

	store.AssertExpectations(t)
	pub.AssertExpectations(t)
}

func TestEngine_NonRetryableError_MovesToDead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := new(relay.MockStorage)
	pub := new(relay.MockPublisher)

	eventID := uuid.New()
	event := relay.Event{
		ID:      eventID,
		Type:    "test.event",
		Payload: []byte(`{}`),
	}

	// --- Mocks ---
	store.On("ClaimBatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]relay.Event{event}, nil).Once()

	// We must return empty for subsequent calls to prevent the worker from spinning
	store.On("ClaimBatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]relay.Event{}, nil).Maybe()

	pub.On("Publish", mock.Anything, event).
		Return(&relay.PublishError{Err: fmt.Errorf("poison pill"), IsRetryable: false}).
		Once()

	store.On("MarkFailedBatch", mock.Anything, mock.MatchedBy(func(f []relay.FailedEvent) bool {
		return len(f) == 1 && f[0].ID == eventID && f[0].NewStatus == relay.StatusDead
	}), mock.Anything).Return(nil).Once()

	// Silence background noise
	store.On("ReapExpiredLeases", mock.Anything, mock.Anything, mock.Anything).
		Return(int64(0), nil).
		Maybe()
	store.On("GetStats", mock.Anything).Return(relay.Stats{}, nil).Maybe()

	// --- Init ---
	params := relay.EngineParams{
		Interval:     10 * time.Millisecond,
		BatchSize:    10,
		LeaseTimeout: 1 * time.Minute,
		RetryPolicy:  &relay.ExponentialBackoff{MaxAttempts: 3},
	}

	tel, _ := relay.CreateNoopTelemetry()
	engine, err := relay.NewEngine(store, pub, params, tel)
	require.NoError(t, err)

	// --- Execution ---
	go func() {
		_ = engine.Start(ctx)
	}()

	// Give it a realistic window to fire the loops and process the event.
	// 50ms is plenty for a 10ms interval.
	time.Sleep(50 * time.Millisecond)

	// Now that we've waited, AssertExpectations should pass immediately.
	assert.True(t, store.AssertExpectations(t), "Storage expectations not met")
	assert.True(t, pub.AssertExpectations(t), "Publisher expectations not met")

	cancel()
}

func TestEngine_BacklogDrain_LoopsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := new(relay.MockStorage)
	pub := new(relay.MockPublisher)

	// Create a full batch (10 events)
	fullBatch := make([]relay.Event, 10)
	for i := 0; i < 10; i++ {
		fullBatch[i] = relay.Event{ID: uuid.New(), Type: "test.event"}
	}

	// First call: Return a FULL batch (10/10)
	store.On("ClaimBatch", mock.Anything, mock.Anything, 10, mock.Anything).
		Return(fullBatch, nil).Once()

	// Second call: Return empty (simulating the drain is finished)
	// If the engine is working correctly, this second call should happen
	// almost instantly after the first, without waiting for the Interval.
	store.On("ClaimBatch", mock.Anything, mock.Anything, 10, mock.Anything).
		Return(fullBatch, nil).Once()

	// Return an empty batch so that the engine will sleep for Interval duration
	store.On("ClaimBatch", mock.Anything, mock.Anything, 10, mock.Anything).
		Return([]relay.Event{}, nil).Once()

	// Handle the publishing of the 10 events
	pub.On("Publish", mock.Anything, mock.Anything).Return(nil).Times(20)
	store.On("MarkDeliveredBatch", mock.Anything, mock.Anything, mock.Anything).Return(nil)

	// Silence background noise
	store.On("ReapExpiredLeases", mock.Anything, mock.Anything, mock.Anything).
		Return(int64(0), nil).
		Maybe()
	store.On("GetStats", mock.Anything).Return(relay.Stats{}, nil).Maybe()

	params := relay.EngineParams{
		Interval:     1 * time.Second,
		BatchSize:    10,
		LeaseTimeout: 1 * time.Minute,
	}

	tel, _ := relay.CreateNoopTelemetry()
	engine, err := relay.NewEngine(store, pub, params, tel)
	require.NoError(t, err)

	go func() {
		_ = engine.Start(ctx)
	}()

	// We sleep for a very short time (way less than the Interval).
	// If the engine waits for the ticker, it will only have called ClaimBatch once.
	// If it drains, it will have called it twice and met the expectations.
	time.Sleep(50 * time.Millisecond)

	assert.True(t, store.AssertExpectations(t), "Engine did not drain the backlog immediately")
	cancel()
}
