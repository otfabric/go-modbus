// SPDX-License-Identifier: MIT

package session

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/logging"
	"github.com/otfabric/go-modbus/internal/protocol"
)

// AttemptObserver receives callbacks for individual retry attempts and re-dials.
// Implementations must be non-blocking.
type AttemptObserver interface {
	OnAttempt(unitID uint8, fc byte, attempt int, duration time.Duration, err error)
	OnRetryDial(attempt int, duration time.Duration, err error)
}

// Interrupter is implemented by transports whose pending I/O can be made to fail
// at once without closing the link. The engine uses it to honour the
// cancellation of a request's context while the request is in flight.
type Interrupter interface {
	Interrupt()
}

// Config configures a session Engine.
type Config struct {
	Dial     func() (Transport[*adu.Request, *adu.Response], error)
	UsePool  bool
	MinConns int
	MaxConns int
	Retry    RetryPolicy
	Logger   logging.Logger
	Attempts AttemptObserver

	// Broken reports whether err, returned by a transport, leaves that connection
	// unusable for further requests (half a frame consumed, stream out of sync,
	// link down). Such a connection is closed and replaced by a new one before
	// the next request, with or without a RetryPolicy. Nil means every error
	// breaks the connection.
	//
	// It applies to the single connection. A pooled connection is discarded
	// after any error, as it always was.
	Broken func(err error) bool

	// Suspect, if set, is called with every response that was received without
	// error. If it reports true the response is still returned to the caller,
	// but the connection it came from is not used again: the response shows that
	// requests and responses are no longer aligned on that connection.
	Suspect func(req *adu.Request, res *adu.Response) bool
}

// Engine owns the execute/retry/pool layer above transports.
// Execute is safe for concurrent use from multiple goroutines.
// In single-transport mode only one request is in flight at a time; other
// callers wait for their turn. In pool mode (MaxConns > 1), each concurrent
// caller acquires its own transport from the pool.
//
// An engine that has been opened stays open until Close: when its single
// connection has been dropped (see Config.Broken) the next request dials a new
// one. A failed dial fails that request, not the engine.
type Engine struct {
	cfg    Config
	logger *logging.PrefixedLogger

	mu     sync.Mutex
	closed bool
	isOpen bool
	// done is closed by Close: it wakes callers waiting for their turn or
	// sleeping between retry attempts.
	done chan struct{}
	// turn serializes single-transport Execute calls. It is a channel rather
	// than a mutex so that waiting can be abandoned on Close and on context
	// cancellation.
	turn chan struct{}
	pool *Pool[*adu.Request, *adu.Response]
	// tr is the single connection, nil while there is none.
	tr Transport[*adu.Request, *adu.Response]
}

// NewEngine creates a session engine from the given config.
func NewEngine(cfg Config) *Engine {
	l := cfg.Logger
	if l == nil {
		l = logging.NopLogger()
	}
	return &Engine{
		cfg:    cfg,
		logger: logging.NewPrefixedLogger("session", l),
		done:   make(chan struct{}),
		turn:   make(chan struct{}, 1),
	}
}

// Open dials transport connections (or creates a pool).
func (e *Engine) Open() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return protocol.ErrClientNotOpen
	}
	if e.isOpen {
		return nil
	}
	if e.cfg.UsePool && e.cfg.MaxConns > 1 {
		p, err := NewPool[*adu.Request, *adu.Response](e.cfg.MinConns, e.cfg.MaxConns, e.cfg.Dial)
		if err != nil {
			return err
		}
		e.pool = p
	} else {
		t, err := e.cfg.Dial()
		if err != nil {
			return err
		}
		e.tr = t
	}
	e.isOpen = true
	return nil
}

// Execute sends a request and returns the response, applying the configured
// RetryPolicy. It is safe for concurrent use.
//
// A context that is already done fails the call with its error before anything
// is sent. Cancelling the context while the request is in flight makes the call
// return promptly with an error matching context.Canceled.
func (e *Engine) Execute(ctx context.Context, req *adu.Request) (*adu.Response, error) {
	e.mu.Lock()
	if e.closed || !e.isOpen {
		e.mu.Unlock()
		return nil, protocol.ErrClientNotOpen
	}
	pool := e.pool
	policy := e.cfg.Retry
	e.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if pool == nil {
		select {
		case e.turn <- struct{}{}:
			defer func() { <-e.turn }()
		case <-e.done:
			return nil, protocol.ErrClientNotOpen
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	obs := e.cfg.Attempts

	var lastErr error
	for attempt := 0; ; attempt++ {
		attemptStart := time.Now()
		res, err := e.executeOnce(ctx, req, pool, attempt)
		var redial *redialError
		if errors.As(err, &redial) {
			// The single connection could not be re-established. That ends this
			// request (the failed attempts before it, if any, are reported with
			// it), but not the engine: the next request dials again.
			if e.isClosed() {
				return nil, errors.Join(lastErr, redial.err, protocol.ErrClientNotOpen)
			}
			return nil, errors.Join(lastErr, redial.err)
		}
		lastErr = err
		if obs != nil {
			obs.OnAttempt(req.UnitID, req.FunctionCode, attempt, time.Since(attemptStart), err)
		}
		if err == nil {
			return res, nil
		}
		// Close tears the connections down under the running requests: whatever
		// the transport reported, the reason is that the client is closed.
		if e.isClosed() {
			if errors.Is(err, protocol.ErrClientNotOpen) {
				return nil, err
			}
			return nil, errors.Join(err, protocol.ErrClientNotOpen)
		}
		// Likewise for a cancelled context: the interrupted I/O reports a timeout.
		// An expired deadline is left as it is (a request timeout).
		if cerr := ctx.Err(); cerr != nil && errors.Is(cerr, context.Canceled) {
			return nil, cerr
		}

		var retry bool
		var delay time.Duration
		if policy != nil {
			retry, delay = policy.ShouldRetry(attempt, err)
		}
		if !retry {
			return nil, err
		}

		e.logger.Debugf("retrying unit=0x%02x fc=0x%02x (attempt %d, delay %v): %v",
			req.UnitID, req.FunctionCode, attempt+1, delay, err)

		if pool == nil {
			// A retry always runs on a fresh connection, whether or not the error
			// was one that breaks the connection.
			e.dropTransport(nil)
		}

		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-e.done:
				timer.Stop()
				return nil, errors.Join(err, protocol.ErrClientNotOpen)
			case <-timer.C:
			}
		}
	}
}

// redialError marks the failure to re-establish the single connection.
type redialError struct{ err error }

func (r *redialError) Error() string { return r.err.Error() }
func (r *redialError) Unwrap() error { return r.err }

func (e *Engine) isClosed() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closed
}

// transport returns the single connection, dialling a new one if there is none.
// Only the goroutine holding the turn calls it, so at most one dial runs at a
// time; the lock is not held while dialling, so that Close does not have to wait.
func (e *Engine) transport(attempt int) (Transport[*adu.Request, *adu.Response], error) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, protocol.ErrClientNotOpen
	}
	if e.tr != nil {
		tr := e.tr
		e.mu.Unlock()
		return tr, nil
	}
	e.mu.Unlock()

	dialStart := time.Now()
	t, err := e.cfg.Dial()
	if obs := e.cfg.Attempts; obs != nil {
		obs.OnRetryDial(attempt, time.Since(dialStart), err)
	}
	if err != nil {
		e.logger.Errorf("reconnect failed (attempt %d): %v", attempt, err)
		return nil, &redialError{err: mapTimeout(err)}
	}
	if t == nil {
		// A dial function that reports success without a transport.
		return nil, protocol.ErrClientNotOpen
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		// Close ran while we were dialling: nobody else would close this one.
		_ = t.Close()
		return nil, protocol.ErrClientNotOpen
	}
	e.tr = t
	return t, nil
}

// dropTransport closes the single connection and forgets it; the next request
// dials a new one. If tr is not nil, the connection is dropped only if it still
// is tr.
func (e *Engine) dropTransport(tr Transport[*adu.Request, *adu.Response]) {
	e.mu.Lock()
	cur := e.tr
	if cur == nil || (tr != nil && cur != tr) {
		e.mu.Unlock()
		return
	}
	e.tr = nil
	e.mu.Unlock()
	_ = cur.Close()
}

// executeOnce runs one attempt of req on a connection of the pool, or on the
// single connection.
func (e *Engine) executeOnce(ctx context.Context, req *adu.Request, pool *Pool[*adu.Request, *adu.Response], attempt int) (*adu.Response, error) {
	if pool != nil {
		tr, err := pool.acquire(ctx)
		if err != nil {
			return nil, mapTimeout(err)
		}
		res, err := e.exchange(ctx, tr, req)
		if err != nil || e.suspect(req, res) {
			pool.discard(tr)
		} else {
			pool.release(tr)
		}
		return res, mapTimeout(err)
	}

	tr, err := e.transport(attempt)
	if err != nil {
		return nil, err
	}
	res, err := e.exchange(ctx, tr, req)
	switch {
	case err != nil:
		if e.cfg.Broken == nil || e.cfg.Broken(err) {
			e.dropTransport(tr)
		}
	case e.suspect(req, res):
		e.dropTransport(tr)
	}
	return res, mapTimeout(err)
}

func (e *Engine) suspect(req *adu.Request, res *adu.Response) bool {
	return e.cfg.Suspect != nil && res != nil && e.cfg.Suspect(req, res)
}

// exchange runs req on tr. If ctx is done while the exchange is in flight, the
// transport's pending I/O is interrupted so that the call returns at once.
func (e *Engine) exchange(ctx context.Context, tr Transport[*adu.Request, *adu.Response], req *adu.Request) (*adu.Response, error) {
	in, ok := tr.(Interrupter)
	if !ok || ctx.Done() == nil {
		return tr.ExecuteRequest(ctx, req)
	}
	interrupted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		in.Interrupt()
		close(interrupted)
	})
	res, err := tr.ExecuteRequest(ctx, req)
	if !stop() {
		// The interrupt is running or has run: let it finish before the transport
		// is used again, so that it cannot hit the next request.
		<-interrupted
	}
	return res, err
}

// mapTimeout turns I/O timeouts into ErrRequestTimedOut.
func mapTimeout(err error) error {
	if err != nil && os.IsTimeout(err) {
		return protocol.ErrRequestTimedOut
	}
	return err
}

// Close shuts down the engine and all transport connections, including the ones
// requests are running on: those requests, and the callers waiting for their
// turn or between two retry attempts, return promptly with ErrClientNotOpen.
func (e *Engine) Close() error {
	e.mu.Lock()
	if !e.closed {
		e.closed = true
		close(e.done)
	}
	e.isOpen = false
	pool, tr := e.pool, e.tr
	e.pool, e.tr = nil, nil
	e.mu.Unlock()

	var errs []error
	if pool != nil {
		if err := pool.CloseAll(); err != nil {
			errs = append(errs, err)
		}
	}
	if tr != nil {
		if err := tr.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
