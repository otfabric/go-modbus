// SPDX-License-Identifier: MIT

package session

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

type fakeTransport struct {
	mu       sync.Mutex
	closed   bool
	execErr  error
	execRes  *adu.Response
	closeErr error
}

func (f *fakeTransport) ExecuteRequest(_ context.Context, _ *adu.Request) (*adu.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.execRes, f.execErr
}

func (f *fakeTransport) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return f.closeErr
}

func newFakeDial() func() (Transport[*adu.Request, *adu.Response], error) {
	return func() (Transport[*adu.Request, *adu.Response], error) {
		return &fakeTransport{execRes: &adu.Response{}}, nil
	}
}

func TestNewPool_MinMaxClamping(t *testing.T) {
	p, err := NewPool(5, 2, newFakeDial())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.CloseAll() }()
	if p.maxConns != 2 {
		t.Errorf("maxConns = %d, want 2", p.maxConns)
	}
}

func TestNewPool_PreWarm(t *testing.T) {
	calls := 0
	dial := func() (Transport[*adu.Request, *adu.Response], error) {
		calls++
		return &fakeTransport{execRes: &adu.Response{}}, nil
	}
	p, err := NewPool(3, 5, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.CloseAll() }()
	if calls != 3 {
		t.Errorf("dial called %d times, want 3", calls)
	}
	if p.total != 3 {
		t.Errorf("total = %d, want 3", p.total)
	}
}

func TestNewPool_PreWarmFailure(t *testing.T) {
	count := 0
	dial := func() (Transport[*adu.Request, *adu.Response], error) {
		count++
		if count == 2 {
			return nil, errors.New("dial failed")
		}
		return &fakeTransport{execRes: &adu.Response{}}, nil
	}
	_, err := NewPool(3, 5, dial)
	if err == nil {
		t.Fatal("expected error on pre-warm failure")
	}
}

func TestPool_Execute(t *testing.T) {
	p, err := NewPool(1, 2, newFakeDial())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.CloseAll() }()

	ctx := context.Background()
	req := &adu.Request{UnitID: 1, FunctionCode: 0x03}
	res, err := p.Execute(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if res == nil {
		t.Fatal("expected non-nil response")
	}
}

func TestPool_ExecuteDiscardsOnError(t *testing.T) {
	execErr := errors.New("transport error")
	dial := func() (Transport[*adu.Request, *adu.Response], error) {
		return &fakeTransport{execErr: execErr}, nil
	}
	p, err := NewPool(0, 2, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.CloseAll() }()

	_, err = p.Execute(context.Background(), &adu.Request{})
	if !errors.Is(err, execErr) {
		t.Errorf("got %v, want %v", err, execErr)
	}
}

func TestPool_CloseAllWakesAcquire(t *testing.T) {
	dial := func() (Transport[*adu.Request, *adu.Response], error) {
		return &fakeTransport{execRes: &adu.Response{}}, nil
	}
	p, err := NewPool(1, 1, dial)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	_, err = p.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := p.acquire(ctx)
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	_ = p.CloseAll()

	select {
	case err := <-done:
		if err != errPoolClosed {
			t.Errorf("acquire() returned %v, want errPoolClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acquire() was not woken by CloseAll()")
	}
}

func TestPool_AcquireAfterClose(t *testing.T) {
	p, err := NewPool(0, 2, newFakeDial())
	if err != nil {
		t.Fatal(err)
	}
	_ = p.CloseAll()
	_, err = p.acquire(context.Background())
	if err != errPoolClosed {
		t.Errorf("got %v, want errPoolClosed", err)
	}
}

func TestPool_AcquireContextCancellation(t *testing.T) {
	dial := func() (Transport[*adu.Request, *adu.Response], error) {
		return &fakeTransport{execRes: &adu.Response{}}, nil
	}
	p, err := NewPool(1, 1, dial)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.CloseAll() }()

	_, err = p.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = p.acquire(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v, want context.DeadlineExceeded", err)
	}
}

func TestPool_CloseAllIdempotent(t *testing.T) {
	p, err := NewPool(1, 2, newFakeDial())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CloseAll(); err != nil {
		t.Fatal(err)
	}
	if err := p.CloseAll(); err != nil {
		t.Fatalf("second CloseAll should not fail: %v", err)
	}
}

func TestPool_ReleaseAfterClose(t *testing.T) {
	p, err := NewPool(1, 2, newFakeDial())
	if err != nil {
		t.Fatal(err)
	}
	tr, err := p.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = p.CloseAll()
	p.release(tr)
	ft := tr.(*fakeTransport)
	ft.mu.Lock()
	closed := ft.closed
	ft.mu.Unlock()
	if !closed {
		t.Error("transport should be closed after release on a closed pool")
	}
}
