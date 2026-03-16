package modbus

import (
	"time"

	intsession "github.com/otfabric/modbus/internal/session"
)

// RetryPolicy is the public alias for the internal/session.RetryPolicy.
// It is accepted by Config and implemented by NoRetry and
// the ExponentialBackoff helpers.
type RetryPolicy = intsession.RetryPolicy

// ExponentialBackoffConfig is the public alias for the internal/session.ExponentialBackoffConfig.
type ExponentialBackoffConfig = intsession.ExponentialBackoffConfig

// NoRetry returns a RetryPolicy that never retries; requests fail on the first error.
// This is the default behaviour when Config.RetryPolicy is nil.
func NoRetry() RetryPolicy { return intsession.NoRetry() }

// ExponentialBackoff returns an exponential back-off RetryPolicy with common defaults.
// delay grows as base × 2^attempt, capped at maxDelay; retries stop after maxAttempts.
// Passing maxAttempts = 0 means unlimited retries.
func ExponentialBackoff(base, maxDelay time.Duration, maxAttempts int) RetryPolicy {
	return intsession.ExponentialBackoff(base, maxDelay, maxAttempts)
}

// NewExponentialBackoff constructs an exponential back-off RetryPolicy from a
// full ExponentialBackoffConfig, allowing control over RetryOnTimeout and unlimited attempts.
func NewExponentialBackoff(cfg ExponentialBackoffConfig) RetryPolicy {
	return intsession.NewExponentialBackoff(cfg)
}
