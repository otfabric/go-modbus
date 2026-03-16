package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/otfabric/modbus/internal/protocol"
)

func TestNoRetry(t *testing.T) {
	policy := NoRetry()
	retry, delay := policy.ShouldRetry(0, protocol.ErrIllegalFunction)
	if retry {
		t.Error("NoRetry should never retry")
	}
	if delay != 0 {
		t.Errorf("delay should be 0, got %v", delay)
	}
}

func TestExponentialBackoff(t *testing.T) {
	policy := ExponentialBackoff(10*time.Millisecond, time.Second, 2)
	// First attempt: should retry on transient error
	retry, delay := policy.ShouldRetry(0, io.EOF)
	if !retry {
		t.Error("expected retry on first failure")
	}
	if delay <= 0 || delay > time.Second {
		t.Errorf("delay %v should be in (0, 1s]", delay)
	}
	// Second attempt: should retry (attempt 1 < MaxAttempts 2)
	retry, _ = policy.ShouldRetry(1, io.EOF)
	if !retry {
		t.Error("expected retry on second failure")
	}
	// Third attempt: should not retry (attempt >= MaxAttempts)
	retry, _ = policy.ShouldRetry(2, io.EOF)
	if retry {
		t.Error("should not retry after MaxAttempts")
	}
}

func TestExponentialBackoff_ZeroBaseUsesDefault(t *testing.T) {
	policy := ExponentialBackoff(0, time.Second, 1)
	retry, delay := policy.ShouldRetry(0, io.EOF)
	if !retry {
		t.Error("expected retry")
	}
	if delay <= 0 {
		t.Error("delay should be positive (default base)")
	}
}

func TestNewExponentialBackoff(t *testing.T) {
	policy := NewExponentialBackoff(ExponentialBackoffConfig{
		BaseDelay:      5 * time.Millisecond,
		MaxDelay:       100 * time.Millisecond,
		MaxAttempts:    1,
		RetryOnTimeout: true,
	})
	retry, _ := policy.ShouldRetry(0, protocol.ErrRequestTimedOut)
	if !retry {
		t.Error("with RetryOnTimeout, timeout should be retried")
	}
	retry, _ = policy.ShouldRetry(1, protocol.ErrRequestTimedOut)
	if retry {
		t.Error("should not retry after MaxAttempts=1")
	}
}

func TestNewExponentialBackoff_NoRetryOnTimeout(t *testing.T) {
	policy := NewExponentialBackoff(ExponentialBackoffConfig{
		BaseDelay:   time.Millisecond,
		MaxDelay:    time.Second,
		MaxAttempts: 2,
	})
	retry, _ := policy.ShouldRetry(0, protocol.ErrRequestTimedOut)
	if retry {
		t.Error("without RetryOnTimeout, timeout should not be retried")
	}
	retry, _ = policy.ShouldRetry(0, io.EOF)
	if !retry {
		t.Error("transient errors should be retried")
	}
	retry, _ = policy.ShouldRetry(0, errors.New("unknown"))
	if retry {
		t.Error("unknown errors should not be retried")
	}
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		retryTimeout bool
		want         bool
	}{
		{"nil error", nil, false, false},
		{"nil error with retryTimeout", nil, true, false},
		{"context.Canceled", context.Canceled, false, false},
		{"context.DeadlineExceeded", context.DeadlineExceeded, false, false},
		{"ErrClientNotOpen", protocol.ErrClientNotOpen, false, false},
		{"ErrConfigurationError", protocol.ErrConfigurationError, false, false},
		{"ErrProtocolError", protocol.ErrProtocolError, false, false},
		{"ErrBadCRC", protocol.ErrBadCRC, false, false},
		{"ErrShortFrame", protocol.ErrShortFrame, false, false},
		{"ErrBadTransactionID", protocol.ErrBadTransactionID, false, false},
		{"ErrBadUnitID", protocol.ErrBadUnitID, false, false},
		{"ErrUnknownProtocolID", protocol.ErrUnknownProtocolID, false, false},
		{"ErrInvalidMBAPLength", protocol.ErrInvalidMBAPLength, false, false},
		{"ErrUnexpectedParameters", protocol.ErrUnexpectedParameters, false, false},
		{"ExceptionError", protocol.MapExceptionCodeToError(protocol.FCReadHoldingRegisters, protocol.ExIllegalFunction), false, false},
		{"ErrRequestTimedOut retryTimeout=false", protocol.ErrRequestTimedOut, false, false},
		{"ErrRequestTimedOut retryTimeout=true", protocol.ErrRequestTimedOut, true, true},
		{"io.EOF", io.EOF, false, true},
		{"io.ErrUnexpectedEOF", io.ErrUnexpectedEOF, false, true},
		{"net.ErrClosed", net.ErrClosed, false, true},
		{"custom net.Error", &fakeNetError{msg: "connection reset"}, false, true},
		{"random error", errors.New("something"), false, false},
		{"wrapped io.EOF", fmt.Errorf("wrapped: %w", io.EOF), false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsRetryable(tt.err, tt.retryTimeout)
			if got != tt.want {
				t.Errorf("IsRetryable(%v, %v) = %v, want %v", tt.err, tt.retryTimeout, got, tt.want)
			}
		})
	}
}

// fakeNetError implements net.Error for testing.
type fakeNetError struct{ msg string }

func (e *fakeNetError) Error() string   { return e.msg }
func (e *fakeNetError) Timeout() bool   { return false }
func (e *fakeNetError) Temporary() bool { return true }

func TestExponentialBackoff_DelayCappedAtMaxDelay(t *testing.T) {
	// BaseDelay larger than MaxDelay: computed delay exceeds max, should cap.
	policy := ExponentialBackoff(2*time.Second, 500*time.Millisecond, 3)
	_, delay := policy.ShouldRetry(0, io.EOF)
	if delay != 500*time.Millisecond {
		t.Errorf("delay should be capped at 500ms, got %v", delay)
	}
	// High attempt: 2^10 * 100ms = 102.4s, should cap at maxDelay.
	policy2 := ExponentialBackoff(100*time.Millisecond, 2*time.Second, 20)
	_, delay2 := policy2.ShouldRetry(10, io.EOF)
	if delay2 != 2*time.Second {
		t.Errorf("delay at attempt 10 should be capped at 2s, got %v", delay2)
	}
}

func TestExponentialBackoff_ZeroMaxDelayUsesDefault(t *testing.T) {
	policy := NewExponentialBackoff(ExponentialBackoffConfig{
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    0, // should default to 30s
		MaxAttempts: 0, // unlimited so we can test high attempt
	})
	// Use high attempt so computed delay would exceed 30s; should cap at default 30s.
	_, delay := policy.ShouldRetry(15, io.EOF)
	if delay != 30*time.Second {
		t.Errorf("zero MaxDelay should default to 30s cap, got %v", delay)
	}
}
