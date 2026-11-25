//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync"
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

	// Signal the first claim so we only cancel once the loop is past its
	// random startup jitter (otherwise ClaimBatch may never be called).
	claimed := make(chan struct{})
	var claimedOnce sync.Once
	store.On("ClaimBatch", mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return([]relay.Event{}, nil).
		Run(func(mock.Arguments) { claimedOnce.Do(func() { close(claimed) }) })
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
		EnableStats:  true,
		EnableReaper: true,
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

	// Let it run until the processing loop has actually started
	select {
	case <-claimed:
	case <-time.After(2 * time.Second):
		t.Fatal("processing loop never claimed a batch")
	}

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

	// Closed once the engine has marked the event as failed. The processing
	// loop sleeps a random startup jitter (up to 50ms) before its first claim,
	// so a fixed sleep here is racy on slow CI runners.
	markedFailed := make(chan struct{})

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
	}), mock.Anything).Return(int64(0), nil).Once().
		Run(func(mock.Arguments) { close(markedFailed) })

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

	select {
	case <-markedFailed:
	case <-time.After(2 * time.Second):
		t.Fatal("engine did not mark the poison event as failed in time")
	}

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
	store.On("ClaimBatch", mock.Anything, 10, mock.Anything).
		Return(fullBatch, nil).Once()

	// Second call: Return empty (simulating the drain is finished)
	// If the engine is working correctly, this second call should happen
	// almost instantly after the first, without waiting for the Interval.
	store.On("ClaimBatch", mock.Anything, 10, mock.Anything).
		Return(fullBatch, nil).Once()

	// Return an empty batch so that the engine will sleep for Interval duration
	drained := make(chan struct{})
	store.On("ClaimBatch", mock.Anything, 10, mock.Anything).
		Return([]relay.Event{}, nil).Once().
		Run(func(mock.Arguments) { close(drained) })

	// Handle the publishing of the 10 events
	pub.On("Publish", mock.Anything, mock.Anything).Return(nil).Times(20)
	pub.On("Connect", mock.Anything).Return(nil)
	store.On("MarkDeliveredBatch", mock.Anything, mock.Anything).Return(int64(0), nil)

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

	// Wait well under the 1s Interval (but far beyond the startup jitter).
	// If the engine waited for the ticker between full batches, the third
	// ClaimBatch would not happen within this window.
	select {
	case <-drained:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Engine did not drain the backlog immediately")
	}

	assert.True(t, store.AssertExpectations(t), "Engine did not drain the backlog immediately")
	cancel()
}

func TestEngine_Reaper_LockTheftPrevention(t *testing.T) {
	db, pgConnStr := setupTestPostgres(t)
	kafkaBrokers := setupKafka(t, "test.event")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	oldWorkerID := "slow-worker-99"
	newWorkerID := "fast-worker-01" // This is what our Engine will use
	eventID := uuid.New()

	// 1. CONFIG
	t.Setenv("RELAY_ID", newWorkerID)
	t.Setenv("LEASE_TIMEOUT", "5s")
	t.Setenv("POLL_INTERVAL", "20ms")
	t.Setenv("STORAGE_URL", pgConnStr)
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_METRICS_EXPORTER", "none")

	// Point Kafka to a "Slow" address
	// We use a real IP that won't respond (like 192.0.2.1) or just a closed port
	// This ensures the WriteMessages call hangs for exactly the timeout duration.
	t.Setenv("PUBLISHER_TYPE", "kafka")
	t.Setenv("PUBLISHER_URL", kafkaBrokers)
	t.Setenv("KAFKA_WRITE_TIMEOUT", "10s") // THE TRAP: This gives us a 10s window

	// 2. SEED: An event already "DELIVERING" by the OLD zombie worker
	// It's already expired (locked 5 mins ago). Its payload exceeds the
	// broker's message size limit, so publishing fails permanently.
	_, err := db.Exec(`
        INSERT INTO openoutbox_events (
            event_id, event_type, payload, status, locked_by, locked_at, available_at
        ) VALUES ($1, 'test.event', $2, 'DELIVERING', $3, $4, $5)`,
		eventID, oversizedKafkaPayload(t), oldWorkerID,
		time.Now().Add(-5*time.Minute), time.Now().Add(-10*time.Minute))
	require.NoError(t, err)

	di, _ := container.BuildContainer(ctx)

	// 3. START ENGINE
	// Sequence of events that will happen automatically:
	// A) Reaper runs: Sees event, sets status='DELIVERING', locked_by=oldWorkerID,
	// and reaps it
	// B) Worker runs: Sees event is PENDING and available, claims it.
	//    Sets status='DELIVERING', locked_by='fast-worker-01'
	// C) The message is too large, so the fast worker moves the event to DEAD
	di.Invoke(func(engine *relay.Engine) {
		go engine.Start(ctx)
	})

	// STEP A: The Engine reaps and then CLAIMS the event.
	// Because the message is too large, the worker fails immediately and moves
	// the event to 'DEAD' because MessageSizeTooLarge is non-retryable.
	assert.Eventually(t, func() bool {
		var lockedBy *string
		var status string
		_ = db.QueryRow("SELECT status, locked_by FROM openoutbox_events WHERE event_id = $1", eventID).
			Scan(&status, &lockedBy)

		// We caught it! The new worker has the lock and hasn't failed yet.
		return status == "DEAD" && lockedBy == nil
	}, 7*time.Second, 50*time.Millisecond)

	// THE ZOMBIE ATTACK
	// Now, the OLD worker (slow-worker-99) finally finishes its "slow" publish.
	// It tries to mark the event as DELIVERED using its own ID.
	di.Invoke(func(store relay.Storage) {
		// We simulate the old worker's final DB call
		ra, err := store.MarkDeliveredBatch(ctx, []uuid.UUID{eventID})
		require.NoError(t, err)
		assert.Equal(t, int64(0), ra)
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
