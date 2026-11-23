// SPDX-License-Identifier: MIT

package modbus

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// outcomeRecorder records the error of every OnAttempt and OnError.
type outcomeRecorder struct {
	mu          sync.Mutex
	attemptErrs []error
	callErrs    []error
}

func (r *outcomeRecorder) OnRequest(uint8, FunctionCode)                 {}
func (r *outcomeRecorder) OnResponse(uint8, FunctionCode, time.Duration) {}
func (r *outcomeRecorder) OnTimeout(uint8, FunctionCode, time.Duration)  {}
func (r *outcomeRecorder) OnError(_ uint8, _ FunctionCode, _ time.Duration, err error) {
	r.mu.Lock()
	r.callErrs = append(r.callErrs, err)
	r.mu.Unlock()
}
func (r *outcomeRecorder) OnAttempt(_ uint8, _ FunctionCode, _ int, _ time.Duration, err error) {
	r.mu.Lock()
	r.attemptErrs = append(r.attemptErrs, err)
	r.mu.Unlock()
}
func (r *outcomeRecorder) OnRetryDial(int, time.Duration, error) {}

func (r *outcomeRecorder) check(t *testing.T, want error) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.attemptErrs) != 1 || r.attemptErrs[0] != nil {
		t.Errorf("OnAttempt errors %v, want one nil: the response frame was received", r.attemptErrs)
	}
	if len(r.callErrs) != 1 || !errors.Is(r.callErrs[0], want) {
		t.Errorf("OnError errors %v, want one matching %v", r.callErrs, want)
	}
}

// A request the device answers with an exception is a completed attempt
// (OnAttempt with err nil) and a failed call (OnError with the exception):
// AttemptMetrics measures the link, ClientMetrics the outcome.
func TestAttemptMetrics_ExceptionIsCompletedAttempt(t *testing.T) {
	h := &fnHandler{holding: func(*HoldingRegistersRequest) ([]uint16, error) { return nil, ErrIllegalDataAddress }}
	srv, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Timeout: time.Minute}, h)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Stop() }()
	r := &outcomeRecorder{}
	c, err := New(Config{URL: "tcp://" + srv.Addr().String(), Timeout: 2 * time.Second, Metrics: r,
		RetryPolicy: ExponentialBackoff(time.Millisecond, time.Millisecond, 3)})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Open(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_, err = c.ReadHoldingRegister(t.Context(), 1, 0)
	var exc *ExceptionError
	if !errors.As(err, &exc) {
		t.Fatalf("read: %v, want an *ExceptionError", err)
	}
	r.check(t, ErrIllegalDataAddress)
}

// So is a response the calling method rejects: here one for another unit.
func TestAttemptMetrics_RejectedResponseIsCompletedAttempt(t *testing.T) {
	r := &outcomeRecorder{}
	c, srv := scriptedMBAPClient(t, func(conf *Config) { conf.Metrics = r })
	go func() {
		hdr := make([]byte, 12)
		if _, err := io.ReadFull(srv, hdr); err != nil {
			return
		}
		txn := uint16(hdr[0])<<8 | uint16(hdr[1])
		_, _ = srv.Write(rawRequestFrame(txn, 2, 3, []byte{2, 0, 7}))
	}()
	if _, err := c.ReadHoldingRegister(t.Context(), 1, 0); !errors.Is(err, ErrBadUnitID) {
		t.Fatalf("read: %v, want ErrBadUnitID", err)
	}
	r.check(t, ErrBadUnitID)
}
