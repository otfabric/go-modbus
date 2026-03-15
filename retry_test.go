package modbus

import (
	"errors"
	"testing"
	"time"
)

func TestNoRetry(t *testing.T) {
	policy := NoRetry()
	retry, delay := policy.ShouldRetry(0, ErrIllegalFunction)
	if retry {
		t.Error("NoRetry should never retry")
	}
	if delay != 0 {
		t.Errorf("delay should be 0, got %v", delay)
	}
}

func TestExponentialBackoff(t *testing.T) {
	policy := ExponentialBackoff(10*time.Millisecond, time.Second, 2)
	// First attempt: should retry
	retry, delay := policy.ShouldRetry(0, ErrIllegalDataAddress)
	if !retry {
		t.Error("expected retry on first failure")
	}
	if delay <= 0 || delay > time.Second {
		t.Errorf("delay %v should be in (0, 1s]", delay)
	}
	// Second attempt: should retry (attempt 1 < MaxAttempts 2)
	retry, _ = policy.ShouldRetry(1, ErrIllegalDataAddress)
	if !retry {
		t.Error("expected retry on second failure")
	}
	// Third attempt: should not retry (attempt >= MaxAttempts)
	retry, _ = policy.ShouldRetry(2, ErrIllegalDataAddress)
	if retry {
		t.Error("should not retry after MaxAttempts")
	}
}

func TestExponentialBackoff_ZeroBaseUsesDefault(t *testing.T) {
	policy := ExponentialBackoff(0, time.Second, 1)
	retry, delay := policy.ShouldRetry(0, ErrIllegalFunction)
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
	retry, _ := policy.ShouldRetry(0, ErrRequestTimedOut)
	if !retry {
		t.Error("with RetryOnTimeout, timeout should be retried")
	}
	retry, _ = policy.ShouldRetry(1, ErrRequestTimedOut)
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
	retry, _ := policy.ShouldRetry(0, ErrRequestTimedOut)
	if retry {
		t.Error("without RetryOnTimeout, timeout should not be retried")
	}
	retry, _ = policy.ShouldRetry(0, errors.New("other"))
	if !retry {
		t.Error("other errors should be retried")
	}
}
