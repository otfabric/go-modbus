// SPDX-License-Identifier: MIT

package session

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/protocol"
)

// timeoutDialError is a dial error that reports a timeout, as a *net.OpError
// for an i/o timeout does.
type timeoutDialError struct{}

func (timeoutDialError) Error() string { return "dial tcp: i/o timeout" }
func (timeoutDialError) Timeout() bool { return true }

func TestMapDialTimeout(t *testing.T) {
	if mapDialTimeout(nil) != nil {
		t.Fatal("nil error mapped to non-nil")
	}

	plain := errors.New("refused")
	if got := mapDialTimeout(plain); got != plain {
		t.Fatalf("non-timeout error changed: %v", got)
	}

	got := mapDialTimeout(timeoutDialError{})
	if !errors.Is(got, protocol.ErrRequestTimedOut) {
		t.Fatalf("timeout not mapped to ErrRequestTimedOut: %v", got)
	}
	var te timeoutDialError
	if !errors.As(got, &te) {
		t.Fatalf("dial error not reachable after mapping: %v", got)
	}

	// Already a request timeout: left as it is.
	if got := mapDialTimeout(protocol.ErrRequestTimedOut); got != protocol.ErrRequestTimedOut {
		t.Fatalf("ErrRequestTimedOut wrapped again: %v", got)
	}
	if !os.IsTimeout(timeoutDialError{}) {
		t.Fatal("test error does not report a timeout")
	}
}

// The pool's pre-warm and on-demand dials get the context they are given, and
// a dial that returns no transport fails without taking a connection slot.
func TestPool_DialContextAndNilTransport(t *testing.T) {
	type ctxKey struct{}
	openCtx := context.WithValue(context.Background(), ctxKey{}, "open")

	var seen []any
	nilNext := false
	dial := func(ctx context.Context) (aduTransport, error) {
		seen = append(seen, ctx.Value(ctxKey{}))
		if nilNext {
			return nil, nil
		}
		return &fakeTransport{execRes: &adu.Response{}}, nil
	}

	p, err := NewPool(openCtx, 1, 1, dial)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer func() { _ = p.CloseAll() }()
	if len(seen) != 1 || seen[0] != "open" {
		t.Fatalf("pre-warm dial saw %v, want the Open context", seen)
	}

	// Take the pre-warmed connection and discard it: the next acquire dials.
	tr, err := p.acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	p.discard(tr)

	nilNext = true
	reqCtx := context.WithValue(context.Background(), ctxKey{}, "request")
	if _, err := p.acquire(reqCtx); !errors.Is(err, protocol.ErrClientNotOpen) {
		t.Fatalf("acquire with a nil transport: want ErrClientNotOpen, got %v", err)
	}
	if last := seen[len(seen)-1]; last != "request" {
		t.Fatalf("on-demand dial saw %v, want the request context", last)
	}
	// The slot was given back: a healthy dial succeeds at once.
	nilNext = false
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tr, err = p.acquire(ctx)
	if err != nil {
		t.Fatalf("acquire after a nil transport: %v", err)
	}
	p.release(tr)
}

// A pre-warm dial that returns no transport fails NewPool and closes the
// connections already opened.
func TestNewPool_PreWarmNilTransport(t *testing.T) {
	var opened []*fakeTransport
	dial := func(context.Context) (aduTransport, error) {
		if len(opened) == 1 {
			return nil, nil
		}
		f := &fakeTransport{execRes: &adu.Response{}}
		opened = append(opened, f)
		return f, nil
	}
	if _, err := NewPool(context.Background(), 2, 2, dial); !errors.Is(err, protocol.ErrClientNotOpen) {
		t.Fatalf("NewPool: want ErrClientNotOpen, got %v", err)
	}
	if !opened[0].closed {
		t.Fatal("pre-warmed connection not closed after a failed pre-warm")
	}
}

// A pooled dial that times out reports ErrRequestTimedOut and keeps the dial
// error reachable.
func TestPool_DialTimeoutMapped(t *testing.T) {
	e := NewEngine(Config{
		UsePool:  true,
		MaxConns: 2,
		Dial:     func(context.Context) (aduTransport, error) { return nil, timeoutDialError{} },
	})
	if err := e.Open(context.Background()); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = e.Close() }()
	_, err := e.Execute(context.Background(), &adu.Request{UnitID: 1, FunctionCode: 3})
	var te timeoutDialError
	if !errors.Is(err, protocol.ErrRequestTimedOut) || !errors.As(err, &te) {
		t.Fatalf("want ErrRequestTimedOut wrapping the dial error, got %v", err)
	}
}

// A re-dial of the single connection runs under the request's context, and a
// timed-out re-dial reports ErrRequestTimedOut wrapping the dial error.
func TestEngine_RedialUsesRequestContext(t *testing.T) {
	type ctxKey struct{}
	var seen []any
	first := true
	e := NewEngine(Config{Dial: func(ctx context.Context) (aduTransport, error) {
		seen = append(seen, ctx.Value(ctxKey{}))
		if first {
			first = false
			return &fakeTransport{execErr: errors.New("broken")}, nil
		}
		return nil, timeoutDialError{}
	}})
	if err := e.Open(context.WithValue(context.Background(), ctxKey{}, "open")); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = e.Close() }()
	req := &adu.Request{UnitID: 1, FunctionCode: 3}
	// The first request fails and drops the connection (Broken is nil).
	if _, err := e.Execute(context.Background(), req); err == nil {
		t.Fatal("first request succeeded")
	}
	_, err := e.Execute(context.WithValue(context.Background(), ctxKey{}, "request"), req)
	var te timeoutDialError
	if !errors.Is(err, protocol.ErrRequestTimedOut) || !errors.As(err, &te) {
		t.Fatalf("want ErrRequestTimedOut wrapping the dial error, got %v", err)
	}
	if len(seen) != 2 || seen[0] != "open" || seen[1] != "request" {
		t.Fatalf("dials saw contexts %v, want [open request]", seen)
	}
}

// An engine whose pool cannot pre-warm fails Open with the dial error and
// stays closed.
func TestEngine_OpenPoolPreWarmFails(t *testing.T) {
	dialErr := errors.New("refused")
	e := NewEngine(Config{
		UsePool:  true,
		MinConns: 1,
		MaxConns: 2,
		Dial:     func(context.Context) (aduTransport, error) { return nil, dialErr },
	})
	if err := e.Open(context.Background()); !errors.Is(err, dialErr) {
		t.Fatalf("Open: want the dial error, got %v", err)
	}
	if _, err := e.Execute(context.Background(), &adu.Request{UnitID: 1, FunctionCode: 3}); !errors.Is(err, protocol.ErrClientNotOpen) {
		t.Fatalf("Execute after a failed Open: want ErrClientNotOpen, got %v", err)
	}
}
