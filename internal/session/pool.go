// SPDX-License-Identifier: MIT

package session

import (
	"context"
	"errors"
	"sync"

	"github.com/otfabric/go-modbus/internal/protocol"
)

// Pool manages a bounded pool of transport connections. It is generic over
// request/response types so that higher layers can adapt it to their own
// transport interfaces.
//
// A caller takes one of maxConns slots before it gets a connection (an idle one,
// or a freshly dialled one when none is idle) and gives the slot back when the
// connection is released or discarded, or when the dial fails. A caller waiting
// for a slot is therefore woken by any of these, not only by a connection being
// returned in good health. Connections are dialled only by slot holders and only
// when none is idle, so there are never more than maxConns of them.
type Pool[Req any, Res any] struct {
	mu     sync.Mutex
	closed bool
	// done is closed by CloseAll to wake callers waiting for a slot.
	done chan struct{}
	// slots holds one token per caller that may still be given a connection.
	slots chan struct{}
	// idle holds the healthy connections not in use, oldest first.
	idle []Transport[Req, Res]
	// inUse holds the connections handed out by acquire, so that CloseAll can
	// close them and interrupt the requests running on them.
	inUse    map[Transport[Req, Res]]struct{}
	maxConns int
	dial     func() (Transport[Req, Res], error)
}

// errPoolClosed is what the pool returns once CloseAll has run: the client that
// owned it is no longer open.
var errPoolClosed = protocol.ErrClientNotOpen

// NewPool creates a pool and pre-warms minConns connections.
// maxConns must be ≥ 1. minConns is clamped to [0, maxConns].
func NewPool[Req any, Res any](minConns, maxConns int, dial func() (Transport[Req, Res], error)) (*Pool[Req, Res], error) {
	if maxConns <= 0 {
		maxConns = 1
	}
	if minConns < 0 {
		minConns = 0
	}
	if minConns > maxConns {
		minConns = maxConns
	}

	p := &Pool[Req, Res]{
		done:     make(chan struct{}),
		slots:    make(chan struct{}, maxConns),
		inUse:    make(map[Transport[Req, Res]]struct{}),
		maxConns: maxConns,
		dial:     dial,
	}
	for i := 0; i < maxConns; i++ {
		p.slots <- struct{}{}
	}

	// pre-warm MinConns connections
	for i := 0; i < minConns; i++ {
		t, err := dial()
		if err != nil {
			// close what we already opened and propagate the error
			for _, c := range p.idle {
				_ = c.Close()
			}
			return nil, err
		}
		p.idle = append(p.idle, t)
	}

	return p, nil
}

// acquire obtains a transport from the pool.
// If the pool is closed, returns errPoolClosed.
// It waits for a slot (until the pool is closed or ctx is done), then returns
// an idle connection if there is one and dials a new one otherwise.
func (p *Pool[Req, Res]) acquire(ctx context.Context) (Transport[Req, Res], error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil, errPoolClosed
	}

	select {
	case <-p.slots:
	default:
		// pool is at capacity — wait for a slot, shutdown, or ctx cancellation
		select {
		case <-p.slots:
		case <-p.done:
			return nil, errPoolClosed
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.slots <- struct{}{}
		return nil, errPoolClosed
	}
	if len(p.idle) > 0 {
		t := p.idle[0]
		p.idle = p.idle[1:]
		p.inUse[t] = struct{}{}
		p.mu.Unlock()
		return t, nil
	}
	p.mu.Unlock()

	// The slot is held while dialling, so the pool never exceeds maxConns.
	t, err := p.dial()
	if err != nil {
		p.slots <- struct{}{}
		return nil, err
	}

	p.mu.Lock()
	if p.closed {
		// CloseAll ran while we were dialling: nobody else will close this one.
		p.mu.Unlock()
		_ = t.Close()
		p.slots <- struct{}{}
		return nil, errPoolClosed
	}
	p.inUse[t] = struct{}{}
	p.mu.Unlock()
	return t, nil
}

// release returns a healthy transport to the idle pool, or closes it if the pool
// is closed or the transport was not handed out by acquire.
func (p *Pool[Req, Res]) release(t Transport[Req, Res]) {
	// The closed check and the hand-back happen under one lock: a connection can
	// never be queued after CloseAll has drained the idle list.
	p.mu.Lock()
	_, owned := p.inUse[t]
	delete(p.inUse, t)
	keep := owned && !p.closed
	if keep {
		p.idle = append(p.idle, t)
	}
	p.mu.Unlock()
	if !keep {
		_ = t.Close()
	}
	if owned {
		p.slots <- struct{}{}
	}
}

// discard closes an unhealthy transport and frees its slot so that a
// replacement can be dialled, by this caller's next acquire or by a waiter.
func (p *Pool[Req, Res]) discard(t Transport[Req, Res]) {
	p.mu.Lock()
	_, owned := p.inUse[t]
	delete(p.inUse, t)
	p.mu.Unlock()
	_ = t.Close()
	// A transport that was not handed out by acquire holds no slot.
	if owned {
		p.slots <- struct{}{}
	}
}

// total returns the number of connections the pool currently holds.
func (p *Pool[Req, Res]) total() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.idle) + len(p.inUse)
}

// Execute acquires a transport, runs the request, and releases or discards it.
func (p *Pool[Req, Res]) Execute(ctx context.Context, req Req) (Res, error) {
	t, err := p.acquire(ctx)
	if err != nil {
		var zero Res
		return zero, err
	}

	res, err := t.ExecuteRequest(ctx, req)
	if err != nil {
		p.discard(t)
		return res, err
	}

	p.release(t)

	return res, nil
}

// CloseAll marks the pool closed, wakes goroutines blocked in acquire(), and
// closes every connection: the idle ones, and the ones in use, which makes the
// requests running on them fail at once. Their owners still release or discard
// them afterwards; closing a transport twice is harmless.
func (p *Pool[Req, Res]) CloseAll() error {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.done)
	}
	conns := p.idle
	p.idle = nil
	var busy []Transport[Req, Res]
	for t := range p.inUse {
		busy = append(busy, t)
	}
	p.mu.Unlock()

	var errs []error
	for _, t := range conns {
		if err := t.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	for _, t := range busy {
		// Errors of connections that are being torn down under a running request
		// are not interesting to the caller of Close.
		_ = t.Close()
	}

	return errors.Join(errs...)
}
