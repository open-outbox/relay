package relay

import (
	"math"
	"math/rand"
	"time"
)

const baseJitter = 0.1

type RetryPolicy interface {
	// NextBackoff returns the duration to wait and whether we should retry at all.
	NextBackoff(attempts int) (time.Duration, bool)
}

// ExponentialBackoff is the standard implementation for high-throughput systems.
type ExponentialBackoff struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Jitter      float64
}

const defaultJitter = 0.1

func (p ExponentialBackoff) NextBackoff(attempts int) (time.Duration, bool) {
	if attempts >= p.MaxAttempts {
		return 0, false
	}

	// Calculate Exponential Base
	// 2^(attempts-1) * BaseDelay
	exp := math.Pow(2, float64(attempts-1))
	delay := time.Duration(float64(p.BaseDelay) * exp)

	if delay > p.MaxDelay {
		delay = p.MaxDelay
	}

	// Calculate Jitter Factor
	jitterFactor := p.Jitter
	if jitterFactor <= 0 {
		jitterFactor = defaultJitter
	}

	// Apply Jitter (Compile-safe math)
	jitterMax := int64(float64(delay) * jitterFactor)
	var jitter time.Duration
	if jitterMax > 0 {
		jitter = time.Duration(rand.Int63n(jitterMax))
	}

	return delay + jitter, true
}
