package utils

import (
	"context"
	"errors"

	"go.uber.org/zap"
)

// LogIfError writes a log entry at the Error level only if the provided error
// is non-nil and does not represent a normal context cancellation.
//
// This is typically used to handle background process errors where
// context.Canceled is an expected signal during shutdown rather than a failure.
func LogIfError(logger *zap.Logger, err error, msg string, fields ...zap.Field) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	logger.Error(msg, fields...)
}
