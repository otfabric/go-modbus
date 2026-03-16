package session

import (
	"context"
	"errors"
	"sync"
)

// Pool manages a bounded pool of transport connections. It is generic over
// request/response types so that higher layers can adapt it to their own
// transport interfaces.
type Pool[Req any, Res any] struct {
	mu       sync.Mutex
	closed   bool                                // set by CloseAll; Release() then closes conns instead of requeueing
	done     chan struct{}                       // closed by CloseAll to wake blocked acquire() goroutines
	idle     chan Transport[Req, Res]            // buffered channel — ready connections
	total    int                                 // connections in flight + idle
	maxConns int                                 // hard upper limit on total connections
	dial     func() (Transport[Req, Res], error) // factory for new connections
}

var errPoolClosed = errors.New("connection pool is closed")

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
		idle:     make(chan Transport[Req, Res], maxConns),
		maxConns: maxConns,
		dial:     dial,
	}

	// pre-warm MinConns connections
	var created []Transport[Req, Res]
	for i := 0; i < minConns; i++ {
		t, err := dial()
		if err != nil {
			// close what we already opened and propagate the error
			for _, c := range created {
				_ = c.Close()
			}
			return nil, err
		}
		created = append(created, t)
		p.total++
	}

	// place pre-warmed connections into the idle channel
	for _, t := range created {
		p.idle <- t
	}

	return p, nil
}

// acquire obtains a transport from the pool.
// If the pool is closed, returns errPoolClosed.
// If an idle connection is available it is returned immediately.
// If the pool is below maxConns a new connection is dialled.
// Otherwise the call blocks until one is returned or ctx is cancelled.
func (p *Pool[Req, Res]) acquire(ctx context.Context) (Transport[Req, Res], error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return nil, errPoolClosed
	}
	// fast path — take an idle connection without touching the mutex
	select {
	case t := <-p.idle:
		return t, nil
	default:
	}

	// check whether we can dial a fresh connection
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errPoolClosed
	}
	if p.total < p.maxConns {
		p.total++
		p.mu.Unlock()

		t, err := p.dial()
		if err != nil {
			p.mu.Lock()
			p.total--
			p.mu.Unlock()

			return nil, err
		}

		return t, nil
	}
	p.mu.Unlock()

	// pool is at capacity — wait for an idle connection, shutdown, or ctx cancellation
	select {
	case t := <-p.idle:
		p.mu.Lock()
		closed := p.closed
		p.mu.Unlock()
		if closed {
			_ = t.Close()
			return nil, errPoolClosed
		}
		return t, nil
	case <-p.done:
		return nil, errPoolClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// release returns a healthy transport to the idle pool, or closes it if the pool is closed.
func (p *Pool[Req, Res]) release(t Transport[Req, Res]) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		_ = t.Close()
		p.mu.Lock()
		if p.total > 0 {
			p.total--
		}
		p.mu.Unlock()
		return
	}
	// non-blocking send: if the channel is somehow full (shouldn't happen),
	// close the connection to avoid a goroutine leak.
	select {
	case p.idle <- t:
	default:
		_ = t.Close()
		p.mu.Lock()
		p.total--
		p.mu.Unlock()
	}
}

// discard closes an unhealthy transport and decrements the total count so a
// replacement can be dialled on the next acquire call.
func (p *Pool[Req, Res]) discard(t Transport[Req, Res]) {
	_ = t.Close()

	p.mu.Lock()
	if p.total > 0 {
		p.total--
	}
	p.mu.Unlock()
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

// CloseAll marks the pool closed, wakes goroutines blocked in acquire(),
// drains every idle connection and closes them. In-flight connections are
// closed when they are returned via release(), not requeued.
func (p *Pool[Req, Res]) CloseAll() error {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.done)
	}
	p.mu.Unlock()

	// drain the idle channel
	var idle []Transport[Req, Res]
	for {
		select {
		case t := <-p.idle:
			idle = append(idle, t)
		default:
			goto done
		}
	}
done:
	p.mu.Lock()
	p.total -= len(idle)
	p.mu.Unlock()

	var errs []error
	for _, t := range idle {
		if err := t.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
