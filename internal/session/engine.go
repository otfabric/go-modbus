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

// Config configures a session Engine.
type Config struct {
	Dial     func() (Transport[*adu.Request, *adu.Response], error)
	UsePool  bool
	MinConns int
	MaxConns int
	Retry    RetryPolicy
	Logger   logging.Logger
	Attempts AttemptObserver
}

// Engine owns the execute/retry/pool layer above transports.
// Execute is safe for concurrent use from multiple goroutines.
// In single-transport mode, execMu serializes all Execute calls so that only
// one request is in flight at a time. In pool mode (MaxConns > 1), each
// concurrent caller acquires its own transport from the pool.
type Engine struct {
	cfg    Config
	logger *logging.PrefixedLogger

	mu     sync.Mutex
	execMu sync.Mutex // serializes single-transport Execute calls
	closed bool
	isOpen bool
	pool   *Pool[*adu.Request, *adu.Response]
	tr     Transport[*adu.Request, *adu.Response]
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
	}
}

// Open dials transport connections (or creates a pool).
func (e *Engine) Open() error {
	e.mu.Lock()
	defer e.mu.Unlock()
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
// RetryPolicy. For pool mode the call is safe for concurrent use.
func (e *Engine) Execute(ctx context.Context, req *adu.Request) (*adu.Response, error) {
	e.mu.Lock()
	if e.closed || !e.isOpen {
		e.mu.Unlock()
		return nil, protocol.ErrClientNotOpen
	}
	usePool := e.pool != nil
	policy := e.cfg.Retry
	e.mu.Unlock()

	if !usePool {
		e.execMu.Lock()
		defer e.execMu.Unlock()
	}

	obs := e.cfg.Attempts

	for attempt := 0; ; attempt++ {
		attemptStart := time.Now()
		res, err := e.executeOnce(ctx, req, usePool)
		if obs != nil {
			obs.OnAttempt(req.UnitID, req.FunctionCode, attempt, time.Since(attemptStart), err)
		}
		if err == nil {
			return res, nil
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

		if !usePool {
			e.mu.Lock()
			if e.tr != nil {
				_ = e.tr.Close()
				e.tr = nil
				e.isOpen = false
			}
			e.mu.Unlock()
		}

		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}

		if !usePool {
			e.mu.Lock()
			if !e.isOpen {
				dialStart := time.Now()
				t, dialErr := e.cfg.Dial()
				if obs != nil {
					obs.OnRetryDial(attempt+1, time.Since(dialStart), dialErr)
				}
				if dialErr != nil {
					e.mu.Unlock()
					e.logger.Errorf("reconnect failed unit=0x%02x fc=0x%02x (attempt %d): %v",
						req.UnitID, req.FunctionCode, attempt+1, dialErr)
					return nil, errors.Join(err, dialErr)
				}
				e.tr = t
				e.isOpen = true
			}
			e.mu.Unlock()
		}
	}
}

func (e *Engine) executeOnce(ctx context.Context, req *adu.Request, usePool bool) (*adu.Response, error) {
	var res *adu.Response
	var err error

	if usePool {
		e.mu.Lock()
		p := e.pool
		e.mu.Unlock()
		if p == nil {
			return nil, protocol.ErrClientNotOpen
		}
		res, err = p.Execute(ctx, req)
	} else {
		e.mu.Lock()
		tr := e.tr
		e.mu.Unlock()
		if tr == nil {
			return nil, protocol.ErrClientNotOpen
		}
		res, err = tr.ExecuteRequest(ctx, req)
	}

	if err != nil {
		if os.IsTimeout(err) {
			return nil, protocol.ErrRequestTimedOut
		}
		return nil, err
	}
	return res, nil
}

// Close shuts down the engine and all transport connections.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	e.isOpen = false
	var errs []error
	if e.pool != nil {
		if err := e.pool.CloseAll(); err != nil {
			errs = append(errs, err)
		}
		e.pool = nil
	}
	if e.tr != nil {
		if err := e.tr.Close(); err != nil {
			errs = append(errs, err)
		}
		e.tr = nil
	}
	return errors.Join(errs...)
}
