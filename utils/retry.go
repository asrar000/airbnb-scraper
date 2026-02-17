package utils

import (
	"fmt"
	"time"
)

// RetryConfig holds the parameters for the retry strategy.
type RetryConfig struct {
	MaxAttempts int
	BaseDelay   time.Duration
	Logger      *Logger
}

// Do executes fn with exponential back-off retry logic.
func (r *RetryConfig) Do(operationName string, fn func() error) error {
	var lastErr error
	delay := r.BaseDelay

	for attempt := 1; attempt <= r.MaxAttempts; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}

		if attempt < r.MaxAttempts {
			r.Logger.Warn("[retry] %s failed (attempt %d/%d): %v — retrying in %v",
				operationName, attempt, r.MaxAttempts, lastErr, delay)
			time.Sleep(delay)
			delay *= 2
		}
	}

	return fmt.Errorf("%s failed after %d attempts: %w", operationName, r.MaxAttempts, lastErr)
}
