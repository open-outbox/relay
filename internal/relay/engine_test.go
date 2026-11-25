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

// newConnectedTestEngine builds an engine around the mocks and marks the
// publisher healthy, as Start does after a successful Connect.
func newConnectedTestEngine(t *testing.T, s *MockStorage, p *MockPublisher, batchSize int) *Engine {
	t.Helper()
	tm, err := CreateNoopTelemetry()
	require.NoError(t, err)

	e, err := NewEngine(s, p, EngineParams{
		RelayID:       "test-relay",
		Interval:      time.Second,
		BatchSize:     batchSize,
		LeaseTimeout:  time.Minute,
		ReapBatchSize: batchSize,
		RetryPolicy: ExponentialBackoff{
			MaxAttempts: 10,
			BaseDelay:   time.Second,
			MaxDelay:    10 * time.Second,
		},
	}, tm)
	require.NoError(t, err)

	p.On("Connect", mock.Anything).Return(nil).Once()
	require.NoError(t, e.connectToPublisher(context.Background()))
	return e
}

func TestEngine_Process_PausesMidBatch(t *testing.T) {
	t.Run("records handled events and leaves the rest for the reaper", func(t *testing.T) {
		s, p := new(MockStorage), new(MockPublisher)
		e := newConnectedTestEngine(t, s, p, 3)

		ev1 := Event{ID: uuid.New(), Type: "t"}
		ev2 := Event{ID: uuid.New(), Type: "t"}
		ev3 := Event{ID: uuid.New(), Type: "t"}
		s.On("ClaimBatch", mock.Anything, 3, mock.Anything).Return([]Event{ev1, ev2, ev3}, nil)

		p.On("Publish", mock.Anything, ev1).Return(nil).Once()
		// ev2 fails while the health monitor's Ping notices the outage.
		p.On("Publish", mock.Anything, ev2).
			Run(func(mock.Arguments) { e.isHealthy.Store(false) }).
			Return(errors.New("broker unreachable")).Once()

		s.On("MarkDeliveredBatch", mock.Anything, []uuid.UUID{ev1.ID}).Return(int64(1), nil).Once()
		s.On("MarkFailedBatch", mock.Anything, mock.MatchedBy(func(f []FailedEvent) bool {
			return len(f) == 1 && f[0].ID == ev2.ID && f[0].NewStatus == EventStatusPending
		})).Return(int64(1), nil).Once()

		n, err := e.process(context.Background())

		require.ErrorIs(t, err, ErrPublisherPaused)
		assert.Equal(t, 2, n, "only the handled events are counted")
		p.AssertNotCalled(t, "Publish", mock.Anything, ev3)
		s.AssertExpectations(t)
		p.AssertExpectations(t)
	})

	t.Run("does not write failures when none occurred", func(t *testing.T) {
		s, p := new(MockStorage), new(MockPublisher)
		e := newConnectedTestEngine(t, s, p, 2)

		ev1 := Event{ID: uuid.New(), Type: "t"}
		ev2 := Event{ID: uuid.New(), Type: "t"}
		s.On("ClaimBatch", mock.Anything, 2, mock.Anything).Return([]Event{ev1, ev2}, nil)

		// The outage is detected right after ev1 was delivered.
		p.On("Publish", mock.Anything, ev1).
			Run(func(mock.Arguments) { e.isHealthy.Store(false) }).
			Return(nil).Once()
		s.On("MarkDeliveredBatch", mock.Anything, []uuid.UUID{ev1.ID}).Return(int64(1), nil).Once()

		_, err := e.process(context.Background())

		require.ErrorIs(t, err, ErrPublisherPaused)
		p.AssertNotCalled(t, "Publish", mock.Anything, ev2)
		s.AssertNotCalled(t, "MarkFailedBatch", mock.Anything, mock.Anything)
		s.AssertExpectations(t)
	})

	t.Run("publishes nothing if the outage is detected after claiming", func(t *testing.T) {
		s, p := new(MockStorage), new(MockPublisher)
		e := newConnectedTestEngine(t, s, p, 2)

		ev1 := Event{ID: uuid.New(), Type: "t"}
		s.On("ClaimBatch", mock.Anything, 2, mock.Anything).
			Run(func(mock.Arguments) { e.isHealthy.Store(false) }).
			Return([]Event{ev1}, nil)

		n, err := e.process(context.Background())

		require.ErrorIs(t, err, ErrPublisherPaused)
		assert.Equal(t, 0, n)
		p.AssertNotCalled(t, "Publish", mock.Anything, mock.Anything)
		s.AssertNotCalled(t, "MarkDeliveredBatch", mock.Anything, mock.Anything)
		s.AssertNotCalled(t, "MarkFailedBatch", mock.Anything, mock.Anything)
	})
}

func TestEngine_Process_ShutdownMidBatch(t *testing.T) {
	// liveCtx matches a context that is still usable, proving the records are
	// written with a detached context rather than the cancelled one.
	liveCtx := mock.MatchedBy(func(ctx context.Context) bool { return ctx.Err() == nil })

	t.Run("records delivered events when cancelled between publishes", func(t *testing.T) {
		s, p := new(MockStorage), new(MockPublisher)
		e := newConnectedTestEngine(t, s, p, 2)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ev1 := Event{ID: uuid.New(), Type: "t"}
		ev2 := Event{ID: uuid.New(), Type: "t"}
		s.On("ClaimBatch", mock.Anything, 2, mock.Anything).Return([]Event{ev1, ev2}, nil)

		// Shutdown arrives right after ev1 was delivered.
		p.On("Publish", mock.Anything, ev1).
			Run(func(mock.Arguments) { cancel() }).
			Return(nil).
			Once()
		s.On("MarkDeliveredBatch", liveCtx, []uuid.UUID{ev1.ID}).Return(int64(1), nil).Once()

		n, err := e.process(ctx)

		require.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 1, n)
		p.AssertNotCalled(t, "Publish", mock.Anything, ev2)
		s.AssertNotCalled(t, "MarkFailedBatch", mock.Anything, mock.Anything)
		s.AssertExpectations(t)
	})

	t.Run("records nothing for the event whose publish was interrupted", func(t *testing.T) {
		s, p := new(MockStorage), new(MockPublisher)
		e := newConnectedTestEngine(t, s, p, 3)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ev1 := Event{ID: uuid.New(), Type: "t"}
		ev2 := Event{ID: uuid.New(), Type: "t"}
		ev3 := Event{ID: uuid.New(), Type: "t"}
		s.On("ClaimBatch", mock.Anything, 3, mock.Anything).Return([]Event{ev1, ev2, ev3}, nil)

		p.On("Publish", mock.Anything, ev1).Return(nil).Once()
		// ev2's publish is cut off by shutdown: its outcome is unknown, so it
		// must be left for the reaper rather than marked failed.
		p.On("Publish", mock.Anything, ev2).
			Run(func(mock.Arguments) { cancel() }).
			Return(context.Canceled).Once()
		s.On("MarkDeliveredBatch", liveCtx, []uuid.UUID{ev1.ID}).Return(int64(1), nil).Once()

		_, err := e.process(ctx)

		require.ErrorIs(t, err, context.Canceled)
		p.AssertNotCalled(t, "Publish", mock.Anything, ev3)
		s.AssertNotCalled(t, "MarkFailedBatch", mock.Anything, mock.Anything)
		s.AssertExpectations(t)
	})
}

func TestNewEngine_ShutdownTimeout(t *testing.T) {
	tm, err := CreateNoopTelemetry()
	require.NoError(t, err)

	build := func(timeout time.Duration) *Engine {
		e, err := NewEngine(new(MockStorage), new(MockPublisher), EngineParams{
			BatchSize:       1,
			ShutdownTimeout: timeout,
		}, tm)
		require.NoError(t, err)
		return e
	}

	assert.Equal(t, defaultShutdownTimeout, build(0).ShutdownTimeout(), "zero uses the default")
	assert.Equal(t, 25*time.Second, build(25*time.Second).ShutdownTimeout())
}
