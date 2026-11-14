// SPDX-License-Identifier: MIT

package modbus

import (
	"time"

	intsession "github.com/otfabric/go-modbus/internal/session"
)

// RetryPolicy is the public alias for the internal/session.RetryPolicy.
// It is accepted by Config and implemented by NoRetry and
// the ExponentialBackoff helpers.
//
// Built-in policies classify by error only (not function code). Retries apply
// equally to reads and writes; after a request may have been written to the
// wire, a retry can deliver a write at least once. Prefer NoRetry for
// non-idempotent writes, or use application-level idempotency.
type RetryPolicy = intsession.RetryPolicy

// ExponentialBackoffConfig is the public alias for the internal/session.ExponentialBackoffConfig.
type ExponentialBackoffConfig = intsession.ExponentialBackoffConfig

// Jitter selects how a policy of NewExponentialBackoff spreads its waits, so
// that clients that fail at the same moment do not retry in step (see
// ExponentialBackoffConfig.Jitter). The zero value, JitterNone, keeps the
// exact schedule.
type Jitter = intsession.Jitter

const (
	// JitterNone waits exactly BaseDelay × 2^attempt, capped at MaxDelay.
	JitterNone = intsession.JitterNone
	// JitterFull waits a duration drawn uniformly from zero to that delay.
	JitterFull = intsession.JitterFull
	// JitterEqual waits at least half of that delay and at most all of it.
	JitterEqual = intsession.JitterEqual
)

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
// full ExponentialBackoffConfig, allowing control over RetryOnTimeout, Jitter
// and unlimited attempts. With Jitter set it draws each wait from a source of
// its own that is safe for concurrent use.
func NewExponentialBackoff(cfg ExponentialBackoffConfig) RetryPolicy {
	return intsession.NewExponentialBackoff(cfg)
}
