//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/open-outbox/relay/internal/relay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type seedOptions struct {
	ID          uuid.UUID
	Status      relay.Status
	CreatedAt   time.Time
	AvailableAt time.Time
	DeliveredAt *time.Time
	UpdatedAt   *time.Time
	LockedAt    *time.Time
	LockedBy    string
	Attempts    int
}

func ptr[T any](v T) *T {
	return &v
}

type EventSeeder func(seedOptions)

func runStorageContractTest(
	t *testing.T,
	store relay.Storage,
	seed EventSeeder,
	truncate func(),
) {
	ctx := context.Background()
	runTest := func(name string, fn func(t *testing.T)) {
		t.Run(name, func(t *testing.T) {
			truncate()
			fn(t)
		})
	}

	runTest("Claiming/RespectBatchSizeAndOrdering", func(t *testing.T) {
		now := time.Now().Truncate(time.Millisecond)

		// Seed 4 events with explicit creation times
		// We want them to be picked up in this order: id1 -> id2 -> id3 -> id4
		id1, id2 := uuid.New(), uuid.New()
		id3, id4 := uuid.New(), uuid.New()

		// id1: Oldest
		seed(seedOptions{
			ID:          id1,
			Status:      relay.StatusPending,
			CreatedAt:   now.Add(-10 * time.Minute),
			AvailableAt: now.Add(-10 * time.Minute)})
		// id2: Newer than id1
		seed(seedOptions{
			ID:          id2,
			Status:      relay.StatusPending,
			CreatedAt:   now.Add(-5 * time.Minute),
			AvailableAt: now.Add(-5 * time.Minute),
		})
		// id3: Same created_at as id4, but id3 available_at is earlier
		seed(seedOptions{
			ID:          id3,
			Status:      relay.StatusPending,
			CreatedAt:   now.Add(-2 * time.Minute),
			AvailableAt: now.Add(-1 * time.Minute),
		})
		seed(seedOptions{
			ID:          id4,
			Status:      relay.StatusPending,
			CreatedAt:   now.Add(-2 * time.Minute),
			AvailableAt: now.Add(-5 * time.Minute),
		})

		// Claim first batch of 2
		buf1 := make([]relay.Event, 5)
		batch1, err := store.ClaimBatch(ctx, "worker-1", 2, buf1)

		require.NoError(t, err)
		assert.Len(t, batch1, 2)
		assert.Equal(t, id1, batch1[0].ID, "Oldest event should be first")
		assert.Equal(t, id2, batch1[1].ID, "Second oldest should be second")

		// Claim second batch of 2
		buf2 := make([]relay.Event, 5)
		batch2, err := store.ClaimBatch(ctx, "worker-1", 2, buf2)

		require.NoError(t, err)
		assert.Len(t, batch2, 2)
		assert.Equal(t, id4, batch2[0].ID, "Should order by available_at if created_at is equal")
		assert.Equal(t, id3, batch2[1].ID)

		// 4. Verify buffer population for the last batch
		assert.Equal(t, batch2[0].ID, buf2[0].ID)
		assert.Equal(t, batch2[1].ID, buf2[1].ID)
	})

	runTest("Claiming/StateTransitionAndExclusion", func(t *testing.T) {
		now := time.Now().Truncate(time.Millisecond)
		eventID := uuid.New()
		seed(seedOptions{
			ID:          eventID,
			Status:      relay.StatusPending,
			CreatedAt:   now.Add(-10 * time.Minute),
			AvailableAt: now.Add(-10 * time.Minute),
		})

		// Claim the event
		buf := make([]relay.Event, 1)
		claimed, _ := store.ClaimBatch(ctx, "worker-1", 1, buf)
		require.Len(t, claimed, 1)

		// Verify State Transition via immediate re-claim attempt
		// If it transitioned to DELIVERING, the pending count should drop
		stats, _ := store.GetStats(ctx)
		assert.Equal(t, int64(0), stats.PendingCount, "Claimed event should no longer be PENDING")

		// Verify Worker Isolation (The Exclusion rule)
		buf2 := make([]relay.Event, 1)
		claimedByOther, err := store.ClaimBatch(ctx, "worker-2", 1, buf2)
		require.NoError(t, err)
		assert.Empty(t, claimedByOther, "Worker 2 should not see events locked by Worker 1")
	})

	runTest("Claiming/RespectsBackoff", func(t *testing.T) {
		now := time.Now().Truncate(time.Millisecond)
		eventID := uuid.New()
		// Available in future
		seed(seedOptions{
			ID:          eventID,
			Status:      relay.StatusPending,
			CreatedAt:   now.Add(-10 * time.Minute),
			AvailableAt: time.Now().Add(1 * time.Minute),
		})

		buf := make([]relay.Event, 1)
		claimed, _ := store.ClaimBatch(ctx, "worker-1", 1, buf)
		assert.Empty(t, claimed, "Event should be invisible until AvailableAt is reached")
	})

	runTest("Lifecycle/MarkDeliveredBatch", func(t *testing.T) {
		now := time.Now().Truncate(time.Millisecond)
		eventID := uuid.New()
		seed(seedOptions{
			ID:          eventID,
			Status:      relay.StatusPending,
			CreatedAt:   now.Add(-1 * time.Minute),
			AvailableAt: now.Add(-1 * time.Minute),
		})

		buf := make([]relay.Event, 1)
		claimed, _ := store.ClaimBatch(ctx, "worker-1", 1, buf)

		err := store.MarkDeliveredBatch(ctx, []uuid.UUID{claimed[0].ID}, "worker-1")
		assert.NoError(t, err)

		// Verify it's effectively removed from the pending pool
		buf2 := make([]relay.Event, 1)
		reclaimed, _ := store.ClaimBatch(ctx, "worker-1", 1, buf2)
		assert.Empty(t, reclaimed)
	})

	runTest("Lifecycle/MarkFailedBatch/Retryable", func(t *testing.T) {
		now := time.Now().Truncate(time.Millisecond)
		eventID := uuid.New()
		seed(seedOptions{
			ID:          eventID,
			Status:      relay.StatusPending,
			CreatedAt:   now.Add(-1 * time.Minute),
			AvailableAt: now.Add(-1 * time.Minute),
			Attempts:    0,
		})

		buf := make([]relay.Event, 1)
		claimed, _ := store.ClaimBatch(ctx, "worker-1", 1, buf)

		// Simulate a retryable failure with a 50ms backoff
		failTime := time.Now().Add(50 * time.Millisecond)
		failures := []relay.FailedEvent{
			{
				ID:          claimed[0].ID,
				NewStatus:   relay.StatusPending, // Assuming you have this constant
				AvailableAt: failTime,
				Attempts:    1,
				LastError:   "connection timeout",
			},
		}

		err := store.MarkFailedBatch(ctx, failures, "worker-1")
		assert.NoError(t, err)

		// Should not be claimable immediately
		buf2 := make([]relay.Event, 1)
		reclaimed, _ := store.ClaimBatch(ctx, "worker-1", 1, buf2)
		assert.Empty(t, reclaimed)

		// After backoff, it should be claimable again
		time.Sleep(150 * time.Millisecond)

		// Reclaim and verify metadata update
		reclaimed, err = store.ClaimBatch(ctx, "worker-1", 1, buf2)
		assert.NoError(t, err)
		assert.Len(t, reclaimed, 1)
		assert.Equal(
			t,
			1,
			reclaimed[0].Attempts,
			"Storage should have persisted the incremented attempts",
		)
	})

	runTest("Lifecycle/MarkFailedBatch/Quarantine", func(t *testing.T) {
		now := time.Now().Truncate(time.Millisecond)
		eventID := uuid.New()
		seed(seedOptions{
			ID:          eventID,
			Status:      relay.StatusPending,
			CreatedAt:   now.Add(-1 * time.Minute),
			AvailableAt: now.Add(-1 * time.Minute),
			Attempts:    0,
		})

		buf := make([]relay.Event, 1)
		claimed, _ := store.ClaimBatch(ctx, "worker-1", 1, buf)

		// Simulate moving to DEAD after too many retries
		failures := []relay.FailedEvent{
			{
				ID:          claimed[0].ID,
				NewStatus:   relay.StatusDead, // Assuming status for quarantined events
				AvailableAt: time.Time{},      // Doesn't matter for Dead events
				Attempts:    5,
				LastError:   "max retries exceeded",
			},
		}

		err := store.MarkFailedBatch(ctx, failures, "worker-1")
		assert.NoError(t, err)

		// Verify it's never claimable again
		buf2 := make([]relay.Event, 1)
		reclaimed, _ := store.ClaimBatch(ctx, "worker-1", 1, buf2)
		assert.Empty(t, reclaimed, "Dead events should never be claimed")
	})

	runTest("Reaper/RecoversStuckEvents", func(t *testing.T) {
		eventID := uuid.New()
		// Seed an event locked by a "crashed" worker 20 minutes ago
		seed(seedOptions{
			ID:       eventID,
			Status:   relay.StatusDelivering,
			LockedAt: ptr(time.Now().Add(-20 * time.Minute)),
			LockedBy: "crashed-worker",
		})

		// Run reaper with a 5-minute timeout threshold
		count, err := store.ReapExpiredLeases(ctx, 5*time.Minute, 100)
		require.NoError(t, err)
		assert.Equal(t, int64(1), count)

		// Event should now be PENDING and available for a new worker
		buf := make([]relay.Event, 1)
		reclaimed, _ := store.ClaimBatch(ctx, "new-worker", 1, buf)
		assert.Len(t, reclaimed, 1)
		assert.Equal(t, eventID, reclaimed[0].ID)
	})

	runTest("Stats/Accuracy", func(t *testing.T) {
		now := time.Now().Truncate(time.Millisecond)

		// Seed events with varying ages and statuses
		idOldest := uuid.New() // 10 mins old
		idNewer := uuid.New()  // 1 min old
		idLocked := uuid.New() // 20 mins old, but DELIVERING

		// Oldest PENDING
		seed(seedOptions{
			ID:        idOldest,
			Status:    relay.StatusPending,
			CreatedAt: now.Add(-10 * time.Minute),
		})
		// Newer PENDING
		seed(seedOptions{
			ID:        idNewer,
			Status:    relay.StatusPending,
			CreatedAt: now.Add(-1 * time.Minute),
		})
		// Manually seed a locked event (should be ignored by PendingCount and Age)
		seed(seedOptions{
			ID:     idLocked,
			Status: relay.StatusDelivering,
		})

		// Fetch Stats
		stats, err := store.GetStats(ctx)
		require.NoError(t, err)

		// Assertions
		// Only the 2 PENDING events should be counted
		assert.Equal(
			t,
			int64(2),
			stats.PendingCount,
			"PendingCount should only include PENDING events",
		)

		// Age should be based on idOldest (approx 600s), not idLocked (1200s) or idNewer (60s)
		// We use a small range to allow for test execution time
		assert.GreaterOrEqual(
			t,
			stats.OldestAgeSec,
			int64(600),
			"OldestAgeSec should reflect the oldest PENDING message",
		)
		assert.Less(
			t,
			stats.OldestAgeSec,
			int64(610),
			"OldestAgeSec should not include DELIVERING events",
		)
	})

	t.Run("Maintenance/PruningLogic", func(t *testing.T) {
		truncate()
		now := time.Now().Truncate(time.Second)

		// Seed events with different statuses and ages
		idDeliveredOld := uuid.New() // Delivered 2 days ago
		idDeliveredNew := uuid.New() // Delivered 1 hour ago
		idDeadOld := uuid.New()      // Dead 40 days ago
		idDeadOld2 := uuid.New()     // Dead 40 days ago
		idPendingOld := uuid.New()   // Pending 40 days ago (Should NEVER be pruned)

		// Helper to seed specific states (you might need to add a seedPrunable helper)
		// seedRaw(id, status, createdAt, availableAt)
		seed(seedOptions{
			ID:          idDeliveredOld,
			Status:      relay.StatusDelivered,
			DeliveredAt: ptr(now.Add(-48 * time.Hour)),
		})
		seed(seedOptions{
			ID:          idDeliveredNew,
			Status:      relay.StatusDelivered,
			DeliveredAt: ptr(now.Add(-1 * time.Hour)),
		})
		seed(seedOptions{
			ID:        idDeadOld,
			Status:    relay.StatusDead,
			UpdatedAt: ptr(now.Add(-40 * 24 * time.Hour)),
		})
		seed(seedOptions{
			ID:        idDeadOld2,
			Status:    relay.StatusDead,
			UpdatedAt: ptr(now.Add(-40 * 24 * time.Hour)),
		})
		seed(seedOptions{
			ID:        idPendingOld,
			Status:    relay.StatusPending,
			CreatedAt: now.Add(-40 * 24 * time.Hour),
		})

		// TEST DRY RUN (Safety Check)
		// Thresholds: Prune Delivered > 24h, Dead > 7 days
		dryRunStats, err := store.Prune(ctx, relay.PruneOptions{
			DeliveredAge: "24h",
			DeadAge:      "7d",
			DryRun:       true,
		})
		require.NoError(t, err)
		assert.Equal(
			t,
			int64(1),
			dryRunStats.DeliveredDeleted,
			"DryRun should report 1 delivered item to prune",
		)
		assert.Equal(
			t,
			int64(2),
			dryRunStats.DeadDeleted,
			"DryRun should report 2 dead items to prune",
		)

		// Verify nothing was actually deleted
		dryRunStats2, err := store.Prune(ctx, relay.PruneOptions{
			DeliveredAge: "24h",
			DeadAge:      "7d",
			DryRun:       true,
		})
		require.NoError(t, err)
		assert.Equal(
			t,
			int64(1),
			dryRunStats2.DeliveredDeleted,
			"DryRun should report 1 delivered item to prune",
		)
		assert.Equal(
			t,
			int64(2),
			dryRunStats2.DeadDeleted,
			"DryRun should report 2 dead items to prune",
		)

		// // TEST ACTUAL PRUNE
		pruneStats, err := store.Prune(ctx, relay.PruneOptions{
			DeliveredAge: "24h",
			DeadAge:      "7d",
			DryRun:       false,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), pruneStats.DeliveredDeleted)
		assert.Equal(t, int64(2), pruneStats.DeadDeleted)

		// // VERIFY FINAL STATE
		dryRunStats3, err := store.Prune(ctx, relay.PruneOptions{
			DeliveredAge: "24h",
			DeadAge:      "7d",
			DryRun:       true,
		})
		require.NoError(t, err)
		assert.Equal(
			t,
			int64(0),
			dryRunStats3.DeliveredDeleted,
			"DryRun should report 0 delivered item to prune",
		)
		assert.Equal(
			t,
			int64(0),
			dryRunStats3.DeadDeleted,
			"DryRun should report 0 dead items to prune",
		)
	})

	runTest("Claiming/BoundaryConditions", func(t *testing.T) {
		// Test 0 batch size
		buf := make([]relay.Event, 0)
		claimed, err := store.ClaimBatch(ctx, "worker-1", 0, buf)
		assert.NoError(t, err)
		assert.Empty(t, claimed)

		// Test nil buffer
		claimed, err = store.ClaimBatch(ctx, "worker-1", 10, nil)
		// Depending on your implementation, this should either
		// error or return a freshly allocated slice.
		assert.NoError(t, err)
	})

	runTest("Claiming/EmptyStorage", func(t *testing.T) {
		buf := make([]relay.Event, 10)
		claimed, err := store.ClaimBatch(ctx, "worker-1", 10, buf)
		assert.NoError(t, err)
		assert.Empty(t, claimed)
	})

	runTest("Lifecycle/ReapIdempotency", func(t *testing.T) {
		// Reap empty DB
		affected, err := store.ReapExpiredLeases(ctx, time.Minute, 100)
		assert.NoError(t, err)
		assert.Equal(t, int64(0), affected)

		// Seed and reap twice
		seed(seedOptions{
			Status:   relay.StatusDelivering,
			LockedAt: ptr(time.Now().Add(-20 * time.Minute)),
		})

		// First pass should catch it
		a1, _ := store.ReapExpiredLeases(ctx, 5*time.Minute, 100)
		assert.Equal(t, int64(1), a1)

		// Second pass immediately after should do nothing
		a2, _ := store.ReapExpiredLeases(ctx, 5*time.Minute, 100)
		assert.Equal(t, int64(0), a2)
	})
}
