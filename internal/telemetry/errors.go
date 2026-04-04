package telemetry

import (
	"context"
	"errors"
)

// IsSilent returns true if the error is a normal part of the
// application lifecycle (like a graceful shutdown).
func IsSilent(err error) bool {
	if err == nil {
		return true
	}

	// Check for context cancellation (SIGTERM, manual cancel)
	if errors.Is(err, context.Canceled) {
		return true
	}

	return false
}
