//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-outbox/relay/internal/container"
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
	pub.On("Connect", mock.Anything).Return(nil)

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

	pub.On("Connect", mock.Anything).Return(nil)

	store.On("MarkFailedBatch", mock.Anything, mock.MatchedBy(func(f []relay.FailedEvent) bool {
		return len(f) == 1 && f[0].ID == eventID && f[0].NewStatus == relay.EventStatusDead
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
	pub.On("Connect", mock.Anything).Return(nil)
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

func TestEngine_Reaper_LockTheftPrevention(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, pgConnStr := setupTestPostgres(t)
	kafkaBrokers := setupKafka(t, "test.event")

	oldWorkerID := "slow-worker-99"
	newWorkerID := "fast-worker-01" // This is what our Engine will use
	eventID := uuid.New()

	// 1. CONFIG
	t.Setenv("RELAY_ID", newWorkerID)
	t.Setenv("LEASE_TIMEOUT", "5s")
	t.Setenv("POLL_INTERVAL", "100ms")
	t.Setenv("STORAGE_URL", pgConnStr)

	// Point Kafka to a "Slow" address
	// We use a real IP that won't respond (like 192.0.2.1) or just a closed port
	// This ensures the WriteMessages call hangs for exactly the timeout duration.
	t.Setenv("PUBLISHER_TYPE", "kafka")
	t.Setenv("PUBLISHER_URL", kafkaBrokers)
	t.Setenv("KAFKA_WRITE_TIMEOUT", "10s") // THE TRAP: This gives us a 10s window

	// 2. SEED: An event already "DELIVERING" by the OLD zombie worker
	// It's already expired (locked 5 mins ago)
	_, err := db.Exec(`
        INSERT INTO openoutbox_events (
            event_id, event_type, payload, status, locked_by, locked_at, available_at
        ) VALUES ($1, 'test.event.nonexistence', '{}', 'DELIVERING', $2, $3, $4)`,
		eventID, oldWorkerID, time.Now().Add(-5*time.Minute), time.Now().Add(-10*time.Minute))
	require.NoError(t, err)

	di, _ := container.BuildContainer(ctx)

	// 3. START ENGINE
	// Sequence of events that will happen automatically:
	// A) Reaper runs: Sees event, sets status='DELIVERING', locked_by=oldWorkerID,
	// and reaps it
	// B) Worker runs: Sees event is PENDING and available, claims it.
	//    Sets status='DELIVERING', locked_by='fast-worker-01'
	// C) The topic doesn't exists, so the fast worker will move the event back to PENDING
	di.Invoke(func(engine *relay.Engine) {
		go engine.Start(ctx)
	})

	// STEP A: The Engine reaps and then CLAIMS the event.
	// Because the topic doesn't exist, the worker fails immediately (or after retries)
	// and moves the event to 'DEAD' because it's a non-retryable metadata error..
	assert.Eventually(t, func() bool {
		var lockedBy *string
		var status string
		_ = db.QueryRow("SELECT status, locked_by FROM openoutbox_events WHERE event_id = $1", eventID).
			Scan(&status, &lockedBy)

		// We caught it! The new worker has the lock and hasn't failed yet.
		return status == "DEAD" && lockedBy == nil
	}, 5*time.Second, 100*time.Millisecond)

	// THE ZOMBIE ATTACK
	// Now, the OLD worker (slow-worker-99) finally finishes its "slow" publish.
	// It tries to mark the event as DELIVERED using its own ID.
	di.Invoke(func(store relay.Storage) {
		// We simulate the old worker's final DB call
		err := store.MarkDeliveredBatch(ctx, []uuid.UUID{eventID}, oldWorkerID)
		require.NoError(t, err)
	})

	// FINAL INTEGRITY CHECK
	var status string
	var currentLock *string
	err = db.QueryRow("SELECT status, locked_by FROM openoutbox_events WHERE event_id = $1", eventID).
		Scan(&status, &currentLock)
	require.NoError(t, err)

	// Even though the old worker called MarkDelivered, the status should
	// NOT be DELIVERED because the 'WHERE locked_by = oldWorkerID' failed.
	// It will become `DEAD' because the fast worker will claim it and fail
	// to deliver it
	assert.NotEqual(t, "DELIVERED", status, "Event should not be in DELIVERED")
	assert.Equal(t, "DEAD", status, "Event should be in DEAD")

	// // The most important part: The old worker couldn't have cleared the new worker's lock
	assert.Nil(t, currentLock, "The lock must be NULL after the event is killed")
}

func TestEngine_EmptyStorage_RespectsPollInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := new(relay.MockStorage)
	pub := new(relay.MockPublisher)

	// 1. USE AN ATOMIC COUNTER
	var callCount int32
	store.On("ClaimBatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]relay.Event{}, nil).
		Run(func(args mock.Arguments) {
			atomic.AddInt32(&callCount, 1)
		})

	// Setup other mocks as before...
	store.On("ReapExpiredLeases", mock.Anything, mock.Anything, mock.Anything).
		Return(int64(0), nil).
		Maybe()
	store.On("GetStats", mock.Anything).Return(relay.Stats{}, nil).Maybe()

	pub.On("Connect", mock.Anything).Return(nil)

	pollInterval := 500 * time.Millisecond
	testDuration := 2 * time.Second

	params := relay.EngineParams{
		RelayID:      "test-quiet-worker",
		Interval:     pollInterval,
		BatchSize:    10,
		LeaseTimeout: 5 * time.Minute,
	}

	tel, _ := relay.CreateNoopTelemetry()
	engine, err := relay.NewEngine(store, pub, params, tel)
	require.NoError(t, err)

	// 2. RUN
	go func() {
		_ = engine.Start(ctx)
	}()

	time.Sleep(testDuration)
	cancel()

	// WAIT A MOMENT
	// Give the goroutines a tiny window to actually exit before we read the counter
	time.Sleep(50 * time.Millisecond)

	// ASSERTION
	finalCalls := atomic.LoadInt32(&callCount)

	// Expected: 2000ms / 500ms = 4 calls.
	// (Plus potentially 1 for the immediate start)
	assert.GreaterOrEqual(t, int(finalCalls), 4, "Engine is polling too slowly")
	assert.LessOrEqual(t, int(finalCalls), 6, "Engine is busy-spinning (polling too fast)")
}
