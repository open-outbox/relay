package relay

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRetryPolicy_NextBackoff(t *testing.T) {
	policy := ExponentialBackoff{
		MaxAttempts: 10,
		BaseDelay:   1 * time.Second,
		MaxDelay:    10 * time.Second,
		Jitter:      0.15,
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

		// Calculation: MaxDelay (10s) + (Jitter 0.15 * MaxDelay) = 11.5s max possible.
		// We use 11.6s to provide a safe buffer for floating point precision.
		assert.LessOrEqual(t, delay, 11*time.Second+600*time.Millisecond)
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
		assert.NotEqual(
			t,
			d1.Nanoseconds(),
			d2.Nanoseconds(),
			"Jitter should provide different results",
		)
	})
}
