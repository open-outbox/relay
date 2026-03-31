package publishers

import (
	"context"
	"errors"
)

// isContextError explicitly checks if the failure was caused by
// a timeout or a manual cancellation of the operation.
func isContextError(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled)
}
