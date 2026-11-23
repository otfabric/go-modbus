// SPDX-License-Identifier: MIT

package session

import (
	"context"
	"errors"
	"fmt"
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

// Resyncer is implemented by transports that can realign a link they keep: Resync
// makes the next request first discard what is pending on the link. The engine
// calls it, when Config.ResyncSuspect is set, for a response that Config.Suspect
// flags, instead of closing the connection.
type Resyncer interface {
	Resync()
}

// Config configures a session Engine.
type Config struct {
	// Dial opens a new connection. ctx bounds the dial: it is the request's
	// context for a re-dial, and the one passed to Open for the first dial.
	Dial     func(ctx context.Context) (Transport[*adu.Request, *adu.Response], error)
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

	// ResyncSuspect keeps the single connection after a response that Suspect
	// flags, when its transport implements Resyncer: Resync is called instead,
	// so that the next request first discards what is pending on the link. It
	// is meant for links that are not re-opened lightly (a serial port).
	ResyncSuspect bool

	// Connections, if set, is told what happens to every connection, single
	// or pooled: each dial (Dial's ctx carries the connection's identifier,
	// see internal/connid), each discard after a failure, and each close at
	// shutdown. It is called outside the engine's and the pool's locks, on
	// the goroutine of the request, Open or Close that caused the event, and
	// must not block.
	Connections func(ConnEvent)
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
	// tr is the single connection, nil while there is none; trMeta is what
	// the engine keeps about it.
	tr     Transport[*adu.Request, *adu.Response]
	trMeta connMeta
}

// observe returns Config.Connections as an observer.
func (e *Engine) observe() observer { return observer(e.cfg.Connections) }

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

// Open dials transport connections (or creates a pool). ctx bounds the dials.
// The lock is not held while dialling: a dial runs the application's code (a
// dialer, the connections it returns, a TLS key), which must not run under it.
// When Close, or another Open, completed meanwhile, what this Open dialled is
// closed again.
func (e *Engine) Open(ctx context.Context) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return protocol.ErrClientNotOpen
	}
	if e.isOpen {
		e.mu.Unlock()
		return nil
	}
	e.mu.Unlock()

	var pool *Pool[*adu.Request, *adu.Response]
	var tr Transport[*adu.Request, *adu.Response]
	var meta connMeta
	if e.cfg.UsePool && e.cfg.MaxConns > 1 {
		p, err := newPool[*adu.Request, *adu.Response](ctx, e.cfg.MinConns, e.cfg.MaxConns, e.cfg.Dial, e.observe())
		if err != nil {
			return err
		}
		pool = p
	} else {
		t, m, err := dialConn(ctx, e.cfg.Dial, e.observe(), OriginOpen, false)
		if err != nil {
			return err
		}
		tr, meta = t, m
	}

	e.mu.Lock()
	if e.closed || e.isOpen {
		closed := e.closed
		e.mu.Unlock()
		if pool != nil {
			_ = pool.CloseAll()
		}
		if tr != nil {
			_ = tr.Close()
			e.observe().closed(meta, false)
		}
		if closed {
			return protocol.ErrClientNotOpen
		}
		return nil
	}
	e.pool, e.tr, e.trMeta = pool, tr, meta
	e.isOpen = true
	e.mu.Unlock()
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
		if redial, ok := errors.AsType[*redialError](err); ok {
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
			// Closing a connection takes a moment, and a TLS one tells the peer first:
			// a response can still arrive once Close has begun.
			if e.isClosed() {
				return nil, protocol.ErrClientNotOpen
			}
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
			e.dropTransport(nil, DiscardRetry, err)
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
// It also returns the connection's identifier.
func (e *Engine) transport(ctx context.Context, attempt int) (Transport[*adu.Request, *adu.Response], uint64, error) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil, 0, protocol.ErrClientNotOpen
	}
	if e.tr != nil {
		tr, id := e.tr, e.trMeta.id
		e.mu.Unlock()
		return tr, id, nil
	}
	e.mu.Unlock()

	dialStart := time.Now()
	t, meta, err := dialConn(ctx, e.cfg.Dial, e.observe(), OriginRedial, false)
	if obs := e.cfg.Attempts; obs != nil {
		obs.OnRetryDial(attempt, time.Since(dialStart), err)
	}
	if err != nil {
		e.logger.Errorf("reconnect failed (attempt %d): %v", attempt, err)
		return nil, 0, &redialError{err: mapDialTimeout(err)}
	}
	if t == nil {
		// A dial function that reports success without a transport.
		return nil, 0, protocol.ErrClientNotOpen
	}

	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		// Close ran while we were dialling: nobody else would close this one.
		// (Without the lock: closing it may run the application's code.)
		_ = t.Close()
		e.observe().closed(meta, false)
		return nil, 0, protocol.ErrClientNotOpen
	}
	e.tr, e.trMeta = t, meta
	e.mu.Unlock()
	return t, meta.id, nil
}

// dropTransport closes the single connection and forgets it; the next request
// dials a new one. If tr is not nil, the connection is dropped only if it still
// is tr. A connection it drops is reported discarded for reason and err.
func (e *Engine) dropTransport(tr Transport[*adu.Request, *adu.Response], reason DiscardReason, err error) {
	e.mu.Lock()
	cur, meta := e.tr, e.trMeta
	if cur == nil || (tr != nil && cur != tr) {
		e.mu.Unlock()
		return
	}
	e.tr, e.trMeta = nil, connMeta{}
	e.mu.Unlock()
	_ = cur.Close()
	e.observe().discarded(meta, false, reason, err)
}

// executeOnce runs one attempt of req on a connection of the pool, or on the
// single connection.
func (e *Engine) executeOnce(ctx context.Context, req *adu.Request, pool *Pool[*adu.Request, *adu.Response], attempt int) (*adu.Response, error) {
	if pool != nil {
		tr, id, err := pool.acquireConn(ctx)
		if err != nil {
			return nil, mapTimeout(err)
		}
		res, err := e.exchange(ctx, tr, req)
		setConnID(res, id)
		switch {
		case err != nil:
			pool.discardFor(tr, DiscardTransportError, err)
		case e.suspect(req, res):
			pool.discardFor(tr, DiscardSuspectResponse, nil)
		default:
			pool.release(tr)
		}
		return res, mapTimeout(err)
	}

	tr, id, err := e.transport(ctx, attempt)
	if err != nil {
		return nil, err
	}
	res, err := e.exchange(ctx, tr, req)
	setConnID(res, id)
	switch {
	case err != nil:
		if e.cfg.Broken == nil || e.cfg.Broken(err) {
			e.dropTransport(tr, DiscardTransportError, err)
		}
	case e.suspect(req, res):
		if r, ok := tr.(Resyncer); ok && e.cfg.ResyncSuspect {
			r.Resync()
		} else {
			e.dropTransport(tr, DiscardSuspectResponse, nil)
		}
	}
	return res, mapTimeout(err)
}

// setConnID records on res, when there is one, the connection it came from.
func setConnID(res *adu.Response, id uint64) {
	if res != nil {
		res.ConnectionID = id
	}
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
// mapDialTimeout is mapTimeout for the error of a dial: a dial that timed out
// also matches ErrRequestTimedOut, as it did before dials took a context, and
// the dial error stays reachable with errors.Is and errors.As.
func mapDialTimeout(err error) error {
	if err != nil && os.IsTimeout(err) && !errors.Is(err, protocol.ErrRequestTimedOut) {
		return fmt.Errorf("%w: %w", protocol.ErrRequestTimedOut, err)
	}
	return err
}

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
	report, err := e.CloseDeferred()
	report()
	return err
}

// CloseDeferred is Close that does not report the connections it closes to
// Config.Connections: it returns report, which does, for a caller that must
// finish closing before it runs the application's code (a Close called from
// that code would otherwise wait for itself). report is never nil.
func (e *Engine) CloseDeferred() (report func(), err error) {
	e.mu.Lock()
	if !e.closed {
		e.closed = true
		close(e.done)
	}
	e.isOpen = false
	pool, tr, meta := e.pool, e.tr, e.trMeta
	e.pool, e.tr, e.trMeta = nil, nil, connMeta{}
	e.mu.Unlock()

	var errs []error
	var closed []connMeta
	if pool != nil {
		c, err := pool.closeAll()
		if err != nil {
			errs = append(errs, err)
		}
		closed = c
	}
	pooled := len(closed)
	if tr != nil {
		if err := tr.Close(); err != nil {
			errs = append(errs, err)
		}
		closed = append(closed, meta)
	}
	o := e.observe()
	return func() {
		for i, m := range closed {
			o.closed(m, i < pooled)
		}
	}, errors.Join(errs...)
}
