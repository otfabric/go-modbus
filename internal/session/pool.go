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
	idle []idleConn[Req, Res]
	// inUse holds the connections handed out by acquire, so that CloseAll can
	// close them and interrupt the requests running on them.
	inUse    map[Transport[Req, Res]]*connUse
	maxConns int
	dial     func(ctx context.Context) (Transport[Req, Res], error)
	// observe is told what happens to the pool's connections.
	observe observer
}

// idleConn is an idle connection of the pool.
type idleConn[Req any, Res any] struct {
	t    Transport[Req, Res]
	meta connMeta
}

// connUse is a connection of the pool in use. shut is set by CloseAll, which
// reports the connection closed: its owner's release or discard reports
// nothing more.
type connUse struct {
	meta connMeta
	shut bool
}

// errPoolClosed is what the pool returns once CloseAll has run: the client that
// owned it is no longer open.
var errPoolClosed = protocol.ErrClientNotOpen

// NewPool creates a pool and pre-warms minConns connections, dialled with ctx.
// maxConns must be ≥ 1. minConns is clamped to [0, maxConns].
func NewPool[Req any, Res any](ctx context.Context, minConns, maxConns int, dial func(ctx context.Context) (Transport[Req, Res], error)) (*Pool[Req, Res], error) {
	return newPool(ctx, minConns, maxConns, dial, nil)
}

// newPool is NewPool that reports what happens to the pool's connections to
// observe: the dials (pre-warm ones with OriginOpen, those of acquire with
// OriginOnDemand), the discards and the closes.
func newPool[Req any, Res any](ctx context.Context, minConns, maxConns int, dial func(ctx context.Context) (Transport[Req, Res], error), observe observer) (*Pool[Req, Res], error) {
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
		inUse:    make(map[Transport[Req, Res]]*connUse),
		maxConns: maxConns,
		dial:     dial,
		observe:  observe,
	}
	for i := 0; i < maxConns; i++ {
		p.slots <- struct{}{}
	}

	// pre-warm MinConns connections
	for i := 0; i < minConns; i++ {
		t, meta, err := dialConn(ctx, dial, observe, OriginOpen, true)
		if err == nil && t == nil {
			err = protocol.ErrClientNotOpen
		}
		if err != nil {
			// close what we already opened and propagate the error
			for _, c := range p.idle {
				_ = c.t.Close()
				observe.discarded(c.meta, true, DiscardOpenFailed, err)
			}
			return nil, err
		}
		p.idle = append(p.idle, idleConn[Req, Res]{t: t, meta: meta})
	}

	return p, nil
}

// acquire obtains a transport from the pool.
// If the pool is closed, returns errPoolClosed.
// It waits for a slot (until the pool is closed or ctx is done), then returns
// an idle connection if there is one and dials a new one otherwise.
func (p *Pool[Req, Res]) acquire(ctx context.Context) (Transport[Req, Res], error) {
	t, _, err := p.acquireConn(ctx)
	return t, err
}

// acquireConn is acquire that also returns the connection's identifier.
func (p *Pool[Req, Res]) acquireConn(ctx context.Context) (Transport[Req, Res], uint64, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil, 0, errPoolClosed
	}

	select {
	case <-p.slots:
	default:
		// pool is at capacity — wait for a slot, shutdown, or ctx cancellation
		select {
		case <-p.slots:
		case <-p.done:
			return nil, 0, errPoolClosed
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		p.slots <- struct{}{}
		return nil, 0, errPoolClosed
	}
	if len(p.idle) > 0 {
		c := p.idle[0]
		p.idle = p.idle[1:]
		p.inUse[c.t] = &connUse{meta: c.meta}
		p.mu.Unlock()
		return c.t, c.meta.id, nil
	}
	p.mu.Unlock()

	// The slot is held while dialling, so the pool never exceeds maxConns.
	t, meta, err := dialConn(ctx, p.dial, p.observe, OriginOnDemand, true)
	if err == nil && t == nil {
		// A dial function that reports success without a transport.
		err = protocol.ErrClientNotOpen
	}
	if err != nil {
		p.slots <- struct{}{}
		return nil, 0, mapDialTimeout(err)
	}

	p.mu.Lock()
	if p.closed {
		// CloseAll ran while we were dialling: nobody else will close this one.
		p.mu.Unlock()
		_ = t.Close()
		p.observe.closed(meta, true)
		p.slots <- struct{}{}
		return nil, 0, errPoolClosed
	}
	p.inUse[t] = &connUse{meta: meta}
	p.mu.Unlock()
	return t, meta.id, nil
}

// release returns a healthy transport to the idle pool, or closes it if the pool
// is closed or the transport was not handed out by acquire.
func (p *Pool[Req, Res]) release(t Transport[Req, Res]) {
	// The closed check and the hand-back happen under one lock: a connection can
	// never be queued after CloseAll has drained the idle list.
	p.mu.Lock()
	use, owned := p.inUse[t]
	delete(p.inUse, t)
	keep := owned && !p.closed
	if keep {
		p.idle = append(p.idle, idleConn[Req, Res]{t: t, meta: use.meta})
	}
	p.mu.Unlock()
	if !keep {
		// Not handed out by acquire, or handed out and then shut by CloseAll,
		// which closes the pool and marks every connection in use under one
		// lock, and reports them: there is nothing left to report.
		_ = t.Close()
	}
	if owned {
		p.slots <- struct{}{}
	}
}

// discard closes an unhealthy transport and frees its slot so that a
// replacement can be dialled, by this caller's next acquire or by a waiter.
func (p *Pool[Req, Res]) discard(t Transport[Req, Res]) {
	p.discardFor(t, DiscardTransportError, nil)
}

// discardFor is discard that reports the connection discarded for reason and
// err, unless CloseAll already reported it closed.
func (p *Pool[Req, Res]) discardFor(t Transport[Req, Res], reason DiscardReason, err error) {
	p.mu.Lock()
	use, owned := p.inUse[t]
	delete(p.inUse, t)
	p.mu.Unlock()
	_ = t.Close()
	if owned && !use.shut {
		p.observe.discarded(use.meta, true, reason, err)
	}
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
		p.discardFor(t, DiscardTransportError, err)
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
	closed, err := p.closeAll()
	for _, m := range closed {
		p.observe.closed(m, true)
	}
	return err
}

// closeAll is CloseAll that returns the connections it closed instead of
// reporting them, for a caller that reports them once it has finished closing.
func (p *Pool[Req, Res]) closeAll() ([]connMeta, error) {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.done)
	}
	conns := p.idle
	p.idle = nil
	var busy []Transport[Req, Res]
	var busyMeta []connMeta
	for t, use := range p.inUse {
		if use.shut {
			// A second CloseAll: already closed and reported.
			continue
		}
		use.shut = true
		busy = append(busy, t)
		busyMeta = append(busyMeta, use.meta)
	}
	p.mu.Unlock()

	var errs []error
	closed := make([]connMeta, 0, len(conns)+len(busy))
	for _, c := range conns {
		if err := c.t.Close(); err != nil {
			errs = append(errs, err)
		}
		closed = append(closed, c.meta)
	}
	for i, t := range busy {
		// Errors of connections that are being torn down under a running request
		// are not interesting to the caller of Close.
		_ = t.Close()
		closed = append(closed, busyMeta[i])
	}

	return closed, errors.Join(errs...)
}
