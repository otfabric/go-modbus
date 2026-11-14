// SPDX-License-Identifier: MIT

package session

import (
	"context"
	"errors"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"time"

	"github.com/otfabric/go-modbus/internal/protocol"
)

// RetryPolicy controls whether and how a failed request is retried.
// Each call to ShouldRetry receives the zero-based attempt index (0 = first failure)
// and the error that caused it, and returns whether to retry and how long to wait.
//
// The wait duration is honoured by the caller but capped by the remaining context
// deadline. A nil RetryPolicy is equivalent to NoRetry().
//
// Classification is error-based only; function code and read vs write are not
// considered. A retry after bytes may have been sent can deliver a write at
// least once.
type RetryPolicy interface {
	// ShouldRetry returns (true, waitDuration) to schedule another attempt after
	// waitDuration, or (false, 0) to stop and propagate the error to the caller.
	ShouldRetry(attempt int, err error) (bool, time.Duration)
}

// NoRetry returns a RetryPolicy that never retries; requests fail on the first error.
// This is the default behaviour when no RetryPolicy is configured.
func NoRetry() RetryPolicy { return noRetry{} }

type noRetry struct{}

func (noRetry) ShouldRetry(int, error) (bool, time.Duration) { return false, 0 }

// ExponentialBackoffConfig is the full configuration set for exponential back-off.
type ExponentialBackoffConfig struct {
	// BaseDelay is the wait after the first failure; doubles each subsequent attempt.
	// Defaults to 100 ms when zero.
	BaseDelay time.Duration

	// MaxDelay caps the computed delay. Defaults to 30 s when zero.
	MaxDelay time.Duration

	// MaxAttempts is the maximum number of retries (not counting the original attempt).
	// Zero means unlimited retries — use with care; always pass a context with a deadline.
	MaxAttempts int

	// RetryOnTimeout controls whether ErrRequestTimedOut triggers a retry.
	// Default false: timed-out requests are NOT retried (the deadline has already elapsed).
	RetryOnTimeout bool

	// Jitter spreads the waits, so that clients that fail at the same moment
	// do not all retry at the same moment. Let d be BaseDelay × 2^attempt,
	// capped at MaxDelay. JitterNone (the zero value) waits d exactly, as
	// before Jitter existed; JitterFull waits a duration drawn uniformly from
	// [0, d]; JitterEqual one drawn uniformly from [d − d/2, d]. Either way the
	// wait never exceeds MaxDelay. A value other than these three waits as
	// JitterNone.
	Jitter Jitter
}

// Jitter selects how an exponential back-off spreads its waits (see
// ExponentialBackoffConfig.Jitter).
type Jitter uint8

const (
	// JitterNone waits exactly BaseDelay × 2^attempt, capped at MaxDelay.
	JitterNone Jitter = iota
	// JitterFull waits a duration drawn uniformly from zero to that delay.
	JitterFull
	// JitterEqual waits at least half of that delay and at most all of it.
	JitterEqual
)

// ExponentialBackoff returns an exponential back-off RetryPolicy with common defaults.
// delay grows as base × 2^attempt, capped at maxDelay; retries stop after MaxAttempts.
// Passing maxAttempts = 0 means unlimited retries.
func ExponentialBackoff(base, maxDelay time.Duration, maxAttempts int) RetryPolicy {
	if base <= 0 {
		base = 100 * time.Millisecond
	}
	if maxDelay <= 0 {
		maxDelay = 30 * time.Second
	}

	return &exponentialBackoff{
		cfg: ExponentialBackoffConfig{
			BaseDelay:   base,
			MaxDelay:    maxDelay,
			MaxAttempts: maxAttempts,
		},
		randN: rand.Int64N,
	}
}

// NewExponentialBackoff constructs an exponential back-off RetryPolicy from a
// full ExponentialBackoffConfig, allowing control over RetryOnTimeout and unlimited attempts.
func NewExponentialBackoff(cfg ExponentialBackoffConfig) RetryPolicy {
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = 100 * time.Millisecond
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = 30 * time.Second
	}

	return &exponentialBackoff{cfg: cfg, randN: rand.Int64N}
}

type exponentialBackoff struct {
	cfg ExponentialBackoffConfig
	// randN returns a number drawn uniformly from [0, n), n > 0. It must be
	// safe for concurrent use: ShouldRetry is called from every goroutine
	// that makes requests. Only a policy with Jitter set uses it.
	randN func(n int64) int64
}

// IsRetryable reports whether err represents a transient transport failure that
// is safe to retry. It uses positive classification: only known transient
// errors are retried. Unknown/unclassified errors are NOT retried by default,
// to prevent hiding bugs or creating retry storms in production.
//
// Retried (transient transport failures):
//   - io.EOF / io.ErrUnexpectedEOF (connection dropped)
//   - net.ErrClosed (connection closed between attempts)
//   - net.Error (broken pipe, reset, dial transients)
//   - optionally ErrRequestTimedOut (controlled by retryTimeout flag)
//
// Not retried (non-transient / semantic errors):
//   - context.Canceled / context.DeadlineExceeded
//   - ErrClientNotOpen, ErrConfigurationError
//   - ErrProtocolError, ErrBadCRC, ErrBadLRC, ErrShortFrame
//   - ErrBadTransactionID, ErrBadUnitID, ErrUnknownProtocolID
//   - ErrInvalidMBAPLength, ErrUnexpectedParameters
//   - Modbus exception responses (ExceptionError)
//   - Unknown/unclassified errors (returns false)
func IsRetryable(err error, retryTimeout bool) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, protocol.ErrClientNotOpen) || errors.Is(err, protocol.ErrConfigurationError) {
		return false
	}
	if errors.Is(err, protocol.ErrProtocolError) ||
		errors.Is(err, protocol.ErrBadCRC) ||
		errors.Is(err, protocol.ErrBadLRC) ||
		errors.Is(err, protocol.ErrShortFrame) {
		return false
	}
	if errors.Is(err, protocol.ErrBadTransactionID) ||
		errors.Is(err, protocol.ErrBadUnitID) ||
		errors.Is(err, protocol.ErrUnknownProtocolID) ||
		errors.Is(err, protocol.ErrInvalidMBAPLength) ||
		errors.Is(err, protocol.ErrUnexpectedParameters) {
		return false
	}
	if _, ok := errors.AsType[*protocol.ExceptionError](err); ok {
		return false
	}
	if errors.Is(err, protocol.ErrRequestTimedOut) {
		return retryTimeout
	}

	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

func (e *exponentialBackoff) ShouldRetry(attempt int, err error) (bool, time.Duration) {
	if e.cfg.MaxAttempts > 0 && attempt >= e.cfg.MaxAttempts {
		return false, 0
	}

	if !IsRetryable(err, e.cfg.RetryOnTimeout) {
		return false, 0
	}

	return true, jitterDelay(e.cfg.Jitter, backoffDelay(e.cfg.BaseDelay, e.cfg.MaxDelay, attempt), e.randN)
}

// jitterDelay returns the wait for the delay d under mode, drawing with randN:
// d itself for JitterNone (and for an unknown mode), a duration in [0, d] for
// JitterFull and one in [d − d/2, d] for JitterEqual. It never exceeds d.
func jitterDelay(mode Jitter, d time.Duration, randN func(int64) int64) time.Duration {
	if d <= 0 {
		return d
	}
	switch mode {
	case JitterFull:
		return drawUpTo(d, randN)
	case JitterEqual:
		half := d / 2
		return d - half + drawUpTo(half, randN)
	default:
		return d
	}
}

// drawUpTo returns a duration drawn uniformly from [0, d] with randN; d ≥ 0.
func drawUpTo(d time.Duration, randN func(int64) int64) time.Duration {
	if d <= 0 {
		return 0
	}
	if d == math.MaxInt64 {
		// d+1 does not fit: draw from [0, d), which leaves out one nanosecond.
		return time.Duration(randN(int64(d)))
	}
	return time.Duration(randN(int64(d) + 1))
}

// backoffDelay returns base × 2^attempt, capped at maxDelay. The product is
// compared with the cap as a float64 and converted to a time.Duration only when
// it is below it: after a few dozen attempts it no longer fits one, and the
// result of converting such a value depends on the platform. A product below
// one nanosecond gives maxDelay, as a delay of zero always did.
func backoffDelay(base, maxDelay time.Duration, attempt int) time.Duration {
	d := float64(base) * math.Pow(2, float64(attempt))
	if d >= 1 && d < float64(maxDelay) {
		return time.Duration(d)
	}
	return maxDelay
}
