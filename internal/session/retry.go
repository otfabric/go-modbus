// SPDX-License-Identifier: MIT

package session

import (
	"context"
	"errors"
	"io"
	"math"
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
}

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

	return &exponentialBackoff{cfg: cfg}
}

type exponentialBackoff struct {
	cfg ExponentialBackoffConfig
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
//   - ErrProtocolError, ErrBadCRC, ErrShortFrame
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
	var excErr *protocol.ExceptionError
	if errors.As(err, &excErr) {
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

	delay := time.Duration(float64(e.cfg.BaseDelay) * math.Pow(2, float64(attempt)))
	if delay > e.cfg.MaxDelay || delay <= 0 {
		delay = e.cfg.MaxDelay
	}

	return true, delay
}
