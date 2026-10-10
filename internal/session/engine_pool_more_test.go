// SPDX-License-Identifier: MIT

package session

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/protocol"
)

type aduTransport = Transport[*adu.Request, *adu.Response]

// recordingObserver records every AttemptObserver callback.
type recordingObserver struct {
	mu       sync.Mutex
	attempts []attemptRecord
	dials    []dialRecord
}

type attemptRecord struct {
	unitID  uint8
	fc      byte
	attempt int
	err     error
}

type dialRecord struct {
	attempt int
	err     error
}

func (o *recordingObserver) OnAttempt(unitID uint8, fc byte, attempt int, _ time.Duration, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.attempts = append(o.attempts, attemptRecord{unitID, fc, attempt, err})
}

func (o *recordingObserver) OnRetryDial(attempt int, _ time.Duration, err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.dials = append(o.dials, dialRecord{attempt, err})
}

// fixedRetry retries every error up to max times with a constant delay.
type fixedRetry struct {
	max   int
	delay time.Duration
}

func (f fixedRetry) ShouldRetry(attempt int, _ error) (bool, time.Duration) {
	return attempt < f.max, f.delay
}

func TestEngine_PoolMode_OpenExecuteClose(t *testing.T) {
	var dials atomic.Int32
	var made []*fakeTransport
	var mu sync.Mutex
	e := NewEngine(Config{
		UsePool:  true,
		MinConns: 2,
		MaxConns: 3,
		Dial: func() (aduTransport, error) {
			dials.Add(1)
			ft := &fakeTransport{execRes: &adu.Response{UnitID: 9, FunctionCode: 3}}
			mu.Lock()
			made = append(made, ft)
			mu.Unlock()
			return ft, nil
		},
	})
	if err := e.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := dials.Load(); got != 2 {
		t.Fatalf("pre-warmed dials = %d, want 2", got)
	}
	res, err := e.Execute(context.Background(), &adu.Request{UnitID: 9, FunctionCode: 3})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.UnitID != 9 || res.FunctionCode != 3 {
		t.Fatalf("unexpected response %+v", res)
	}
	if got := dials.Load(); got != 2 {
		t.Fatalf("Execute dialled a new connection although idle ones existed (dials=%d)", got)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for i, ft := range made {
		ft.mu.Lock()
		closed := ft.closed
		ft.mu.Unlock()
		if !closed {
			t.Errorf("pooled transport %d not closed by Engine.Close", i)
		}
	}
	if _, err := e.Execute(context.Background(), &adu.Request{}); !errors.Is(err, protocol.ErrClientNotOpen) {
		t.Fatalf("Execute after Close: want ErrClientNotOpen, got %v", err)
	}
}

func TestEngine_PoolMode_OpenPreWarmFailure(t *testing.T) {
	dialErr := errors.New("dial refused")
	e := NewEngine(Config{
		UsePool:  true,
		MinConns: 1,
		MaxConns: 2,
		Dial:     func() (aduTransport, error) { return nil, dialErr },
	})
	if err := e.Open(); !errors.Is(err, dialErr) {
		t.Fatalf("Open: want dial error, got %v", err)
	}
	if _, err := e.Execute(context.Background(), &adu.Request{}); !errors.Is(err, protocol.ErrClientNotOpen) {
		t.Fatalf("Execute on failed engine: want ErrClientNotOpen, got %v", err)
	}
}

func TestEngine_PoolMode_CloseReportsTransportCloseError(t *testing.T) {
	closeErr := errors.New("close failed")
	e := NewEngine(Config{
		UsePool:  true,
		MinConns: 1,
		MaxConns: 2,
		Dial: func() (aduTransport, error) {
			return &fakeTransport{execRes: &adu.Response{}, closeErr: closeErr}, nil
		},
	})
	if err := e.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := e.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close: want close error, got %v", err)
	}
}

// Closing the engine while a pooled request is in flight makes the retry of
// that request fail with ErrClientNotOpen instead of using a stale pool.
func TestEngine_PoolMode_ClosedDuringRetry(t *testing.T) {
	var e *Engine
	e = NewEngine(Config{
		UsePool:  true,
		MaxConns: 2,
		Retry:    fixedRetry{max: 3},
		Dial: func() (aduTransport, error) {
			return &mockTransport{execFn: func(context.Context, *adu.Request) (*adu.Response, error) {
				_ = e.Close()
				return nil, io.EOF
			}}, nil
		},
	})
	if err := e.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := e.Execute(context.Background(), &adu.Request{}); !errors.Is(err, protocol.ErrClientNotOpen) {
		t.Fatalf("want ErrClientNotOpen, got %v", err)
	}
}

func TestEngine_SingleTransport_CloseReportsCloseError(t *testing.T) {
	closeErr := errors.New("close failed")
	e := NewEngine(Config{Dial: func() (aduTransport, error) {
		return &mockTransport{closeFn: func() error { return closeErr }}, nil
	}})
	if err := e.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := e.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close: want close error, got %v", err)
	}
}

func TestEngine_Execute_NilTransportFromDial(t *testing.T) {
	e := NewEngine(Config{Dial: func() (aduTransport, error) { return nil, nil }})
	if err := e.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := e.Execute(context.Background(), &adu.Request{}); !errors.Is(err, protocol.ErrClientNotOpen) {
		t.Fatalf("want ErrClientNotOpen, got %v", err)
	}
}

func TestEngine_Execute_TimeoutMappedToRequestTimedOut(t *testing.T) {
	obs := &recordingObserver{}
	e := NewEngine(Config{
		Attempts: obs,
		Dial: func() (aduTransport, error) {
			return &mockTransport{execFn: func(context.Context, *adu.Request) (*adu.Response, error) {
				return nil, os.ErrDeadlineExceeded
			}}, nil
		},
	})
	if err := e.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = e.Close() }()
	_, err := e.Execute(context.Background(), &adu.Request{UnitID: 7, FunctionCode: 4})
	if !errors.Is(err, protocol.ErrRequestTimedOut) {
		t.Fatalf("want ErrRequestTimedOut, got %v", err)
	}
	if len(obs.attempts) != 1 {
		t.Fatalf("OnAttempt calls = %d, want 1", len(obs.attempts))
	}
	a := obs.attempts[0]
	if a.unitID != 7 || a.fc != 4 || a.attempt != 0 || !errors.Is(a.err, protocol.ErrRequestTimedOut) {
		t.Fatalf("unexpected attempt record %+v", a)
	}
	if len(obs.dials) != 0 {
		t.Fatalf("OnRetryDial called without a retry: %+v", obs.dials)
	}
}

func TestEngine_Execute_RetryObserverAndRedial(t *testing.T) {
	obs := &recordingObserver{}
	var dials, closes atomic.Int32
	e := NewEngine(Config{
		Attempts: obs,
		Retry:    fixedRetry{max: 5, delay: time.Millisecond},
		Dial: func() (aduTransport, error) {
			n := dials.Add(1)
			return &mockTransport{
				execFn: func(_ context.Context, req *adu.Request) (*adu.Response, error) {
					if n < 3 {
						return nil, io.EOF
					}
					return &adu.Response{UnitID: req.UnitID, FunctionCode: req.FunctionCode, Payload: []byte{byte(n)}}, nil
				},
				closeFn: func() error { closes.Add(1); return nil },
			}, nil
		},
	})
	if err := e.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = e.Close() }()

	res, err := e.Execute(context.Background(), &adu.Request{UnitID: 2, FunctionCode: 6})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Payload) != 1 || res.Payload[0] != 3 {
		t.Fatalf("response did not come from the third connection: %+v", res)
	}
	if got := dials.Load(); got != 3 {
		t.Fatalf("dials = %d, want 3 (open + 2 reconnects)", got)
	}
	if got := closes.Load(); got != 2 {
		t.Fatalf("failed transports closed = %d, want 2", got)
	}
	if len(obs.attempts) != 3 {
		t.Fatalf("OnAttempt calls = %d, want 3", len(obs.attempts))
	}
	for i, a := range obs.attempts {
		wantErr := i < 2
		if a.attempt != i || a.unitID != 2 || a.fc != 6 || (a.err != nil) != wantErr {
			t.Errorf("attempt record %d = %+v", i, a)
		}
	}
	if len(obs.dials) != 2 || obs.dials[0].attempt != 1 || obs.dials[1].attempt != 2 ||
		obs.dials[0].err != nil || obs.dials[1].err != nil {
		t.Fatalf("unexpected OnRetryDial records %+v", obs.dials)
	}
}

func TestEngine_Execute_RedialFailureJoinsErrors(t *testing.T) {
	obs := &recordingObserver{}
	dialErr := errors.New("reconnect refused")
	var dials atomic.Int32
	e := NewEngine(Config{
		Attempts: obs,
		Retry:    fixedRetry{max: 5},
		Dial: func() (aduTransport, error) {
			if dials.Add(1) > 1 {
				return nil, dialErr
			}
			return &mockTransport{execFn: func(context.Context, *adu.Request) (*adu.Response, error) {
				return nil, io.ErrUnexpectedEOF
			}}, nil
		},
	})
	if err := e.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	_, err := e.Execute(context.Background(), &adu.Request{})
	if !errors.Is(err, io.ErrUnexpectedEOF) || !errors.Is(err, dialErr) {
		t.Fatalf("want request error joined with dial error, got %v", err)
	}
	if len(obs.dials) != 1 || obs.dials[0].attempt != 1 || !errors.Is(obs.dials[0].err, dialErr) {
		t.Fatalf("unexpected OnRetryDial records %+v", obs.dials)
	}
	// The failed transport was dropped, but the engine stays open: the next request
	// dials again, and fails with the dial error alone.
	_, err = e.Execute(context.Background(), &adu.Request{})
	if !errors.Is(err, dialErr) || errors.Is(err, protocol.ErrClientNotOpen) {
		t.Fatalf("Execute after failed reconnect: want the dial error, got %v", err)
	}
	if got := dials.Load(); got != 3 {
		t.Fatalf("dials = %d, want 3 (open, failed reconnect, failed lazy reconnect)", got)
	}
}

func TestEngine_Execute_ContextCancelledDuringBackoff(t *testing.T) {
	var dials atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e := NewEngine(Config{
		Retry: fixedRetry{max: 5, delay: time.Minute},
		Dial: func() (aduTransport, error) {
			dials.Add(1)
			return &mockTransport{execFn: func(context.Context, *adu.Request) (*adu.Response, error) {
				cancel()
				return nil, io.EOF
			}}, nil
		},
	})
	if err := e.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = e.Close() }()
	start := time.Now()
	_, err := e.Execute(ctx, &adu.Request{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("Execute waited for the full backoff delay")
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("engine reconnected after cancellation (dials=%d)", got)
	}
}

func TestNewPool_ClampsNonPositiveLimits(t *testing.T) {
	var dials int
	p, err := NewPool(-3, 0, func() (aduTransport, error) {
		dials++
		return &fakeTransport{execRes: &adu.Response{}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.CloseAll() }()
	if p.maxConns != 1 {
		t.Errorf("maxConns = %d, want 1", p.maxConns)
	}
	if dials != 0 || p.total() != 0 {
		t.Errorf("negative minConns pre-warmed connections: dials=%d total=%d", dials, p.total())
	}
}

func TestPool_Execute_DialErrorFreesSlot(t *testing.T) {
	dialErr := errors.New("dial refused")
	fail := true
	p, err := NewPool(0, 1, func() (aduTransport, error) {
		if fail {
			return nil, dialErr
		}
		return &fakeTransport{execRes: &adu.Response{UnitID: 5}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.CloseAll() }()

	res, err := p.Execute(context.Background(), &adu.Request{})
	if !errors.Is(err, dialErr) {
		t.Fatalf("want dial error, got %v", err)
	}
	if res != nil {
		t.Fatalf("want nil response on acquire failure, got %+v", res)
	}
	if p.total() != 0 {
		t.Fatalf("failed dial leaked a slot: total=%d", p.total())
	}
	// With maxConns=1 a leaked slot would block this call forever.
	fail = false
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err = p.Execute(ctx, &adu.Request{})
	if err != nil {
		t.Fatalf("Execute after dial recovery: %v", err)
	}
	if res.UnitID != 5 {
		t.Fatalf("unexpected response %+v", res)
	}
}

// A caller blocked on an exhausted pool is handed the connection as soon as
// the in-flight request releases it.
func TestPool_ExhaustedWaiterGetsReleasedConnection(t *testing.T) {
	var dials atomic.Int32
	entered := make(chan struct{}, 2)
	unblock := make(chan struct{})
	p, err := NewPool(0, 1, func() (aduTransport, error) {
		dials.Add(1)
		return &mockTransport{execFn: func(context.Context, *adu.Request) (*adu.Response, error) {
			entered <- struct{}{}
			<-unblock
			return &adu.Response{}, nil
		}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.CloseAll() }()

	errs := make(chan error, 2)
	go func() {
		_, err := p.Execute(context.Background(), &adu.Request{})
		errs <- err
	}()
	<-entered // first request holds the only connection

	// While exhausted, a waiter with an expiring context gives up.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	_, err = p.Execute(ctx, &adu.Request{})
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("exhausted pool: want context.DeadlineExceeded, got %v", err)
	}

	go func() {
		_, err := p.Execute(context.Background(), &adu.Request{})
		errs <- err
	}()
	// Give the waiter time to block on the exhausted pool; the assertions
	// below hold either way, this only makes the hand-over path the likely one.
	time.Sleep(20 * time.Millisecond)
	close(unblock)
	for i := 0; i < 2; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("Execute %d: %v", i, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("waiter was never handed the released connection")
		}
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("dials = %d, want 1 (connection must be reused)", got)
	}
}

func TestPool_ReleaseWithFullIdleQueueClosesConnection(t *testing.T) {
	p, err := NewPool(1, 1, newFakeDial())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.CloseAll() }()

	extra := &fakeTransport{}
	p.release(extra)

	if !extra.closed {
		t.Error("surplus transport was not closed")
	}
	if p.total() != 1 || len(p.idle) != 1 {
		t.Errorf("total=%d idle=%d, want 1/1", p.total(), len(p.idle))
	}
}

func TestPool_CloseAllJoinsCloseErrors(t *testing.T) {
	errA, errB := errors.New("close a"), errors.New("close b")
	closeErrs := []error{errA, nil, errB}
	i := 0
	p, err := NewPool(3, 3, func() (aduTransport, error) {
		ft := &fakeTransport{closeErr: closeErrs[i]}
		i++
		return ft, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = p.CloseAll()
	if !errors.Is(err, errA) || !errors.Is(err, errB) {
		t.Fatalf("CloseAll: want both close errors joined, got %v", err)
	}
	if p.total() != 0 {
		t.Fatalf("total = %d after CloseAll, want 0", p.total())
	}
}

func TestExponentialBackoff_NonPositiveMaxDelayDefaults(t *testing.T) {
	for _, maxDelay := range []time.Duration{0, -time.Second} {
		policy := ExponentialBackoff(100*time.Millisecond, maxDelay, 0)
		retry, delay := policy.ShouldRetry(20, io.EOF)
		if !retry || delay != 30*time.Second {
			t.Errorf("maxDelay=%v: got (%v, %v), want (true, 30s)", maxDelay, retry, delay)
		}
	}
}

func TestNewExponentialBackoff_NonPositiveBaseDelayDefaults(t *testing.T) {
	for _, base := range []time.Duration{0, -time.Second} {
		policy := NewExponentialBackoff(ExponentialBackoffConfig{BaseDelay: base, MaxDelay: time.Hour})
		retry, delay := policy.ShouldRetry(0, io.EOF)
		if !retry || delay != 100*time.Millisecond {
			t.Errorf("base=%v attempt 0: got (%v, %v), want (true, 100ms)", base, retry, delay)
		}
		if _, delay := policy.ShouldRetry(2, io.EOF); delay != 400*time.Millisecond {
			t.Errorf("base=%v attempt 2: delay %v, want 400ms", base, delay)
		}
	}
}

// Close during a retry must be final: the engine may not re-dial, and the request must
// fail with ErrClientNotOpen instead of running on a connection nobody will close.
func TestEngine_SingleTransport_ClosedDuringRetryStaysClosed(t *testing.T) {
	for name, delay := range map[string]time.Duration{"no backoff": 0, "with backoff": 5 * time.Millisecond} {
		t.Run(name, func(t *testing.T) {
			var dials, closes atomic.Int32
			var e *Engine
			e = NewEngine(Config{
				Retry: fixedRetry{max: 5, delay: delay},
				Dial: func() (aduTransport, error) {
					dials.Add(1)
					return &mockTransport{
						execFn: func(context.Context, *adu.Request) (*adu.Response, error) {
							// The client is closed while this request is failing.
							_ = e.Close()
							return nil, io.EOF
						},
						closeFn: func() error { closes.Add(1); return nil },
					}, nil
				},
			})
			if err := e.Open(); err != nil {
				t.Fatalf("Open: %v", err)
			}

			_, err := e.Execute(context.Background(), &adu.Request{})
			if !errors.Is(err, protocol.ErrClientNotOpen) || !errors.Is(err, io.EOF) {
				t.Fatalf("want ErrClientNotOpen joined with the request error, got %v", err)
			}
			if got := dials.Load(); got != 1 {
				t.Fatalf("engine re-dialled after Close (dials=%d)", got)
			}
			if got := closes.Load(); got != 1 {
				t.Fatalf("transport closed %d times, want 1", got)
			}
			if _, err := e.Execute(context.Background(), &adu.Request{}); !errors.Is(err, protocol.ErrClientNotOpen) {
				t.Fatalf("Execute after Close: want ErrClientNotOpen, got %v", err)
			}
		})
	}
}
