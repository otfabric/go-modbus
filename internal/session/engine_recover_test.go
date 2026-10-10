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

// blockingTransport blocks in ExecuteRequest until it is interrupted, closed or
// released, like a transport waiting for a response.
type blockingTransport struct {
	entered     chan struct{}
	release     chan struct{}
	interrupted chan struct{}
	closedCh    chan struct{}
	once        sync.Once
	closeOnce   sync.Once
}

func newBlockingTransport() *blockingTransport {
	return &blockingTransport{
		entered:     make(chan struct{}, 16),
		release:     make(chan struct{}),
		interrupted: make(chan struct{}),
		closedCh:    make(chan struct{}),
	}
}

func (b *blockingTransport) ExecuteRequest(_ context.Context, req *adu.Request) (*adu.Response, error) {
	b.entered <- struct{}{}
	select {
	case <-b.release:
		return &adu.Response{UnitID: req.UnitID, FunctionCode: req.FunctionCode}, nil
	case <-b.interrupted:
		return nil, os.ErrDeadlineExceeded
	case <-b.closedCh:
		return nil, io.ErrClosedPipe
	}
}

func (b *blockingTransport) Interrupt() { b.once.Do(func() { close(b.interrupted) }) }

func (b *blockingTransport) Close() error {
	b.closeOnce.Do(func() { close(b.closedCh) })
	return nil
}

func (b *blockingTransport) isClosed() bool {
	select {
	case <-b.closedCh:
		return true
	default:
		return false
	}
}

func okResponse(_ context.Context, req *adu.Request) (*adu.Response, error) {
	return &adu.Response{UnitID: req.UnitID, FunctionCode: req.FunctionCode}, nil
}

// Without a RetryPolicy, an error that breaks the connection fails the request;
// the next request dials a new connection.
func TestEngine_RedialsLazilyWithoutRetryPolicy(t *testing.T) {
	obs := &recordingObserver{}
	var dials, closes atomic.Int32
	e := NewEngine(Config{
		Attempts: obs,
		Dial: func() (aduTransport, error) {
			n := dials.Add(1)
			return &mockTransport{
				execFn: func(ctx context.Context, req *adu.Request) (*adu.Response, error) {
					if n == 1 {
						return nil, os.ErrDeadlineExceeded
					}
					return okResponse(ctx, req)
				},
				closeFn: func() error { closes.Add(1); return nil },
			}, nil
		},
	})
	if err := e.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = e.Close() }()

	if _, err := e.Execute(context.Background(), &adu.Request{}); !errors.Is(err, protocol.ErrRequestTimedOut) {
		t.Fatalf("first request: %v, want ErrRequestTimedOut", err)
	}
	if closes.Load() != 1 {
		t.Fatalf("the connection the request timed out on was not closed")
	}
	if _, err := e.Execute(context.Background(), &adu.Request{}); err != nil {
		t.Fatalf("second request: %v", err)
	}
	if dials.Load() != 2 {
		t.Fatalf("dials = %d, want 2", dials.Load())
	}
	if len(obs.dials) != 1 || obs.dials[0].attempt != 0 || obs.dials[0].err != nil {
		t.Fatalf("unexpected OnRetryDial records %+v", obs.dials)
	}
}

// Config.Broken decides which errors cost the single connection; Config.Suspect
// can condemn a connection on a response that was received without error.
func TestEngine_BrokenAndSuspect(t *testing.T) {
	linkDown := errors.New("link down")
	var dials atomic.Int32
	// next is what the transport answers with: an error or a response.
	type answer struct {
		res *adu.Response
		err error
	}
	var next atomic.Pointer[answer]
	e := NewEngine(Config{
		Broken:  func(err error) bool { return errors.Is(err, linkDown) },
		Suspect: func(_ *adu.Request, res *adu.Response) bool { return res.UnitID == 0xEE },
		Dial: func() (aduTransport, error) {
			dials.Add(1)
			return &mockTransport{execFn: func(context.Context, *adu.Request) (*adu.Response, error) {
				a := next.Load()
				return a.res, a.err
			}}, nil
		},
	})
	if err := e.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = e.Close() }()
	ctx := context.Background()

	// A timeout does not break this connection.
	next.Store(&answer{err: os.ErrDeadlineExceeded})
	if _, err := e.Execute(ctx, &adu.Request{}); !errors.Is(err, protocol.ErrRequestTimedOut) {
		t.Fatalf("timeout: %v", err)
	}
	next.Store(&answer{res: &adu.Response{UnitID: 1}})
	if _, err := e.Execute(ctx, &adu.Request{}); err != nil || dials.Load() != 1 {
		t.Fatalf("after a timeout: err=%v dials=%d, want the same connection", err, dials.Load())
	}
	// A link error does.
	next.Store(&answer{err: linkDown})
	if _, err := e.Execute(ctx, &adu.Request{}); !errors.Is(err, linkDown) {
		t.Fatalf("link error: %v", err)
	}
	// A suspect response is returned, and costs the connection too.
	next.Store(&answer{res: &adu.Response{UnitID: 0xEE}})
	res, err := e.Execute(ctx, &adu.Request{})
	if err != nil || res.UnitID != 0xEE || dials.Load() != 2 {
		t.Fatalf("suspect response: res=%+v err=%v dials=%d", res, err, dials.Load())
	}
	next.Store(&answer{res: &adu.Response{UnitID: 1}})
	if _, err := e.Execute(ctx, &adu.Request{}); err != nil || dials.Load() != 3 {
		t.Fatalf("after a suspect response: err=%v dials=%d, want a third connection", err, dials.Load())
	}
}

func TestEngine_ContextDoneBeforeCall(t *testing.T) {
	var calls atomic.Int32
	for _, pooled := range []bool{false, true} {
		e := NewEngine(Config{
			UsePool:  pooled,
			MaxConns: 2,
			Dial: func() (aduTransport, error) {
				return &mockTransport{execFn: func(ctx context.Context, req *adu.Request) (*adu.Response, error) {
					calls.Add(1)
					return okResponse(ctx, req)
				}}, nil
			},
		})
		if err := e.Open(); err != nil {
			t.Fatalf("Open: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := e.Execute(ctx, &adu.Request{}); !errors.Is(err, context.Canceled) {
			t.Errorf("pooled=%v: cancelled context: %v", pooled, err)
		}
		expired, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		if _, err := e.Execute(expired, &adu.Request{}); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("pooled=%v: expired context: %v", pooled, err)
		}
		cancel2()
		_ = e.Close()
	}
	if calls.Load() != 0 {
		t.Fatalf("%d request(s) were sent with a context that was already done", calls.Load())
	}
}

// Cancelling the context of a request in flight interrupts the transport and
// returns context.Canceled; an expiring deadline stays a request timeout.
func TestEngine_ContextEndsInFlight(t *testing.T) {
	for _, pooled := range []bool{false, true} {
		var mu sync.Mutex
		var made []*blockingTransport
		e := NewEngine(Config{
			UsePool:  pooled,
			MaxConns: 2,
			Dial: func() (aduTransport, error) {
				bt := newBlockingTransport()
				mu.Lock()
				made = append(made, bt)
				mu.Unlock()
				return bt, nil
			},
		})
		if err := e.Open(); err != nil {
			t.Fatalf("Open: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := e.Execute(ctx, &adu.Request{})
			done <- err
		}()
		waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(made) == 1 })
		<-made[0].entered
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("pooled=%v: cancelled in flight: %v, want context.Canceled", pooled, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("pooled=%v: cancelled request did not return", pooled)
		}
		if !made[0].isClosed() {
			t.Errorf("pooled=%v: the interrupted connection was not closed", pooled)
		}

		short, cancelShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
		_, err := e.Execute(short, &adu.Request{})
		cancelShort()
		if !errors.Is(err, protocol.ErrRequestTimedOut) {
			t.Errorf("pooled=%v: deadline in flight: %v, want ErrRequestTimedOut", pooled, err)
		}
		_ = e.Close()
	}
}

// Close interrupts the request in flight, the callers waiting for their turn and
// a request sleeping between two attempts.
func TestEngine_CloseInterrupts(t *testing.T) {
	t.Run("in flight and queued", func(t *testing.T) {
		for _, pooled := range []bool{false, true} {
			bt := newBlockingTransport()
			e := NewEngine(Config{
				UsePool:  pooled,
				MaxConns: 2,
				MinConns: 1,
				Dial:     func() (aduTransport, error) { return bt, nil },
			})
			if err := e.Open(); err != nil {
				t.Fatalf("Open: %v", err)
			}
			const callers = 4
			done := make(chan error, callers)
			for i := 0; i < callers; i++ {
				go func() {
					_, err := e.Execute(context.Background(), &adu.Request{})
					done <- err
				}()
			}
			<-bt.entered
			time.Sleep(10 * time.Millisecond)
			if err := e.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			for i := 0; i < callers; i++ {
				select {
				case err := <-done:
					if !errors.Is(err, protocol.ErrClientNotOpen) {
						t.Errorf("pooled=%v: request interrupted by Close: %v, want ErrClientNotOpen", pooled, err)
					}
				case <-time.After(3 * time.Second):
					t.Fatalf("pooled=%v: a request did not return after Close", pooled)
				}
			}
		}
	})
	t.Run("retry back-off", func(t *testing.T) {
		e := NewEngine(Config{
			Retry: fixedRetry{max: 5, delay: time.Minute},
			Dial: func() (aduTransport, error) {
				return &mockTransport{execFn: func(context.Context, *adu.Request) (*adu.Response, error) {
					return nil, io.EOF
				}}, nil
			},
		})
		if err := e.Open(); err != nil {
			t.Fatalf("Open: %v", err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := e.Execute(context.Background(), &adu.Request{})
			done <- err
		}()
		time.Sleep(20 * time.Millisecond)
		_ = e.Close()
		select {
		case err := <-done:
			if !errors.Is(err, protocol.ErrClientNotOpen) || !errors.Is(err, io.EOF) {
				t.Errorf("request interrupted in its back-off: %v, want io.EOF joined with ErrClientNotOpen", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("Close did not end the retry back-off")
		}
		if err := e.Open(); !errors.Is(err, protocol.ErrClientNotOpen) {
			t.Errorf("Open on a closed engine: %v, want ErrClientNotOpen", err)
		}
	})
	t.Run("while dialling", func(t *testing.T) {
		var dials atomic.Int32
		dialling := make(chan struct{})
		proceed := make(chan struct{})
		late := newBlockingTransport()
		e := NewEngine(Config{
			Dial: func() (aduTransport, error) {
				if dials.Add(1) == 1 {
					return &mockTransport{execFn: func(context.Context, *adu.Request) (*adu.Response, error) {
						return nil, io.EOF
					}}, nil
				}
				close(dialling)
				<-proceed
				return late, nil
			},
		})
		if err := e.Open(); err != nil {
			t.Fatalf("Open: %v", err)
		}
		if _, err := e.Execute(context.Background(), &adu.Request{}); !errors.Is(err, io.EOF) {
			t.Fatalf("first request: %v", err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := e.Execute(context.Background(), &adu.Request{})
			done <- err
		}()
		<-dialling
		_ = e.Close()
		close(proceed)
		if err := <-done; !errors.Is(err, protocol.ErrClientNotOpen) {
			t.Errorf("request that was dialling during Close: %v, want ErrClientNotOpen", err)
		}
		if !late.isClosed() {
			t.Error("the connection dialled during Close was not closed")
		}
	})
}

// A caller waiting for its turn on the single connection gives up when its
// context ends.
func TestEngine_WaitingForTurnHonoursContext(t *testing.T) {
	bt := newBlockingTransport()
	e := NewEngine(Config{Dial: func() (aduTransport, error) { return bt, nil }})
	if err := e.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = e.Close() }()
	first := make(chan error, 1)
	go func() {
		_, err := e.Execute(context.Background(), &adu.Request{})
		first <- err
	}()
	<-bt.entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := e.Execute(ctx, &adu.Request{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting caller: %v, want context.DeadlineExceeded", err)
	}
	close(bt.release)
	if err := <-first; err != nil {
		t.Fatalf("request in flight: %v", err)
	}
}

// A caller waiting for a pooled connection is woken when a connection in use is
// discarded, and when another caller's dial fails.
func TestPool_WaitersWokenOnDiscardAndDialFailure(t *testing.T) {
	var dials atomic.Int32
	dialErr := errors.New("refused")
	gate := make(chan struct{})
	p, err := NewPool(0, 1, func() (aduTransport, error) {
		switch dials.Add(1) {
		case 1:
			return &fakeTransport{}, nil
		case 2:
			<-gate
			return nil, dialErr
		default:
			return &fakeTransport{}, nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.CloseAll() }()

	first, err := p.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			tr, err := p.acquire(context.Background())
			if err == nil {
				p.release(tr)
			}
			results <- err
		}()
	}
	time.Sleep(10 * time.Millisecond)
	// Discarding the only connection lets one waiter dial (and fail); its failure
	// lets the other one dial.
	p.discard(first)
	close(gate)
	var failed, ok int
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if errors.Is(err, dialErr) {
				failed++
			} else if err == nil {
				ok++
			}
		case <-time.After(3 * time.Second):
			t.Fatal("a waiter was not woken")
		}
	}
	if failed != 1 || ok != 1 {
		t.Fatalf("waiters: %d dial failure(s), %d success(es); want 1 and 1", failed, ok)
	}
	if p.total() != 1 {
		t.Fatalf("total = %d, want 1", p.total())
	}
}

// CloseAll closes connections in use, and a connection whose dial completes
// after CloseAll.
func TestPool_CloseAllClosesBusyAndLateConnections(t *testing.T) {
	var dials atomic.Int32
	dialling := make(chan struct{})
	proceed := make(chan struct{})
	busy, late := &fakeTransport{}, &fakeTransport{}
	p, err := NewPool(0, 2, func() (aduTransport, error) {
		if dials.Add(1) == 1 {
			return busy, nil
		}
		close(dialling)
		<-proceed
		return late, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	lateErr := make(chan error, 1)
	go func() {
		_, err := p.acquire(context.Background())
		lateErr <- err
	}()
	<-dialling
	if err := p.CloseAll(); err != nil {
		t.Fatal(err)
	}
	close(proceed)
	if err := <-lateErr; !errors.Is(err, protocol.ErrClientNotOpen) {
		t.Fatalf("acquire that was dialling during CloseAll: %v, want ErrClientNotOpen", err)
	}
	busy.mu.Lock()
	busyClosed := busy.closed
	busy.mu.Unlock()
	late.mu.Lock()
	lateClosed := late.closed
	late.mu.Unlock()
	if !busyClosed || !lateClosed {
		t.Fatalf("busy closed=%v, late closed=%v; want both closed", busyClosed, lateClosed)
	}
	// Releasing a connection twice, or one the pool never handed out, must not
	// corrupt the slot count.
	p.release(busy)
	p.release(busy)
	p.discard(&fakeTransport{})
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}
