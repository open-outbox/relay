package utils

import (
	"context"
	"errors"
)

// IsContextErr explicitly checks if the failure was caused by
// a timeout or a manual cancellation of the operation.
func IsContextErr(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled)
}
