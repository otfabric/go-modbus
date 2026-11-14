// SPDX-License-Identifier: MIT

package session

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/protocol"
)

// openWait bounds how long a test waits for a call that should return; it
// detects a call that does not, it does not order events.
const openWait = 10 * time.Second

// gatedDial is a Dial that reports each call on dialling and returns a
// transport once proceed is closed; closes counts the Close calls of the
// transports it returned.
type gatedDial struct {
	dialling chan struct{}
	proceed  chan struct{}
	closes   atomic.Int32
}

func newGatedDial() *gatedDial {
	return &gatedDial{dialling: make(chan struct{}, 4), proceed: make(chan struct{})}
}

func (g *gatedDial) dial(context.Context) (Transport[*adu.Request, *adu.Response], error) {
	g.dialling <- struct{}{}
	<-g.proceed
	return &mockTransport{closeFn: func() error { g.closes.Add(1); return nil }}, nil
}

func gatedEngine(g *gatedDial, pool bool) *Engine {
	cfg := Config{Dial: g.dial}
	if pool {
		cfg.UsePool, cfg.MinConns, cfg.MaxConns = true, 1, 2
	}
	return NewEngine(cfg)
}

func asyncErr(fn func() error) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- fn() }()
	return ch
}

func await(t *testing.T, what string, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(openWait):
		t.Fatalf("%s did not return within %v", what, openWait)
		return nil
	}
}

func engineOpen(e *Engine) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.isOpen
}

func modes(t *testing.T, fn func(t *testing.T, pool bool)) {
	for _, pool := range []bool{false, true} {
		name := "single"
		if pool {
			name = "pool"
		}
		t.Run(name, func(t *testing.T) { fn(t, pool) })
	}
}

// Open does not hold the engine's lock while it dials: Close returns while a
// dial is in progress, and the Open that dialled then closes what it dialled
// and returns ErrClientNotOpen. On v1.15.3 Close waited for the dial.
func TestEngine_CloseDuringOpen(t *testing.T) {
	modes(t, func(t *testing.T, pool bool) {
		g := newGatedDial()
		e := gatedEngine(g, pool)
		opened := asyncErr(func() error { return e.Open(context.Background()) })
		<-g.dialling
		if err := await(t, "Close during Open", asyncErr(e.Close)); err != nil {
			t.Fatalf("Close: %v", err)
		}
		close(g.proceed)
		if err := await(t, "Open", opened); !errors.Is(err, protocol.ErrClientNotOpen) {
			t.Fatalf("Open after Close: %v, want ErrClientNotOpen", err)
		}
		if n := g.closes.Load(); n != 1 {
			t.Errorf("%d transports closed, want the one Open dialled", n)
		}
		if engineOpen(e) {
			t.Error("the engine is open")
		}
	})
}

// Two Opens at once both return nil and leave one connection: the one that
// finishes second closes what it dialled.
func TestEngine_ConcurrentOpen(t *testing.T) {
	modes(t, func(t *testing.T, pool bool) {
		g := newGatedDial()
		e := gatedEngine(g, pool)
		first := asyncErr(func() error { return e.Open(context.Background()) })
		second := asyncErr(func() error { return e.Open(context.Background()) })
		for range 2 {
			select {
			case <-g.dialling:
			case <-time.After(openWait):
				t.Fatalf("the two Opens do not dial at the same time")
			}
		}
		close(g.proceed)
		for _, ch := range []<-chan error{first, second} {
			if err := await(t, "Open", ch); err != nil {
				t.Fatalf("Open: %v", err)
			}
		}
		if n := g.closes.Load(); n != 1 {
			t.Errorf("%d transports closed, want 1", n)
		}
		if !engineOpen(e) {
			t.Error("the engine is not open")
		}
		if err := e.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		if n := g.closes.Load(); n != 2 {
			t.Errorf("%d transports closed after Close, want 2", n)
		}
	})
}
