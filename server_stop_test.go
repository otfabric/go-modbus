// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/goid"
)

// Row L10 of the lifecycle matrix in TESTING.md, server: Stop and Shutdown
// called from the application's code that the server runs (a handler method, a
// ServerMetrics or ConnectionMetrics method, the Logger) return, after waiting
// for every goroutine of the server but the caller's own connection (or accept
// loop); a Stop on any other goroutine still waits for them all.

// shutdownWaits reports whether the Shutdown called on goroutine caller is
// waiting for the server's goroutines: the goroutine it starts for that is
// blocked in WaitGroup.Wait. It reads the stacks of all goroutines, as
// testenv-style checks do, so that a test knows Shutdown is waiting instead of
// guessing it from a Shutdown that has not returned yet.
func shutdownWaits(caller uint64) bool {
	created := fmt.Sprintf("created by github.com/otfabric/go-modbus.(*Server).Shutdown in goroutine %d\n", caller)
	for g := range strings.SplitSeq(chaosAllStacks(), "\n\n") {
		if strings.Contains(g, "sync.(*WaitGroup).Wait(") && strings.Contains(g, created) {
			return true
		}
	}
	return false
}

// waitUntilShutdownWaits returns once the Shutdown called on the goroutine
// whose id caller holds (0 until it is known) waits for the server's
// goroutines.
func waitUntilShutdownWaits(t *testing.T, caller *atomic.Uint64) {
	t.Helper()
	e2eEventually(t, "Shutdown to wait for the server's goroutines", func() bool {
		id := caller.Load()
		return id != 0 && shutdownWaits(id)
	})
}

// stopCall is one way of stopping the server from a callback.
type stopCall struct {
	name string
	stop func(ms *Server) error
}

// stopCalls are Stop, and Shutdown with contexts that are not done when it is
// called: Shutdown returns once the other goroutines have ended, as Stop does.
func stopCalls() []stopCall {
	return []stopCall{
		{"Stop", (*Server).Stop},
		{"Shutdown(Background)", func(ms *Server) error { return ms.Shutdown(context.Background()) }},
		{"Shutdown(WithTimeout)", func(ms *Server) error {
			ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
			defer cancel()
			return ms.Shutdown(ctx)
		}},
	}
}

// L10, server. Stop or Shutdown called from a handler method or a ServerMetrics
// hook returns. When it does, the listener and the connection are closed and
// the request is not answered. The handler goes on and returns (the handler of
// the request is called, with its context cancelled, when the hook was
// OnRequest), the request's outcome is reported, no further request of the
// connection is dispatched, and the connection's goroutines end.
func TestServerL10_CallbackStopsTheServer(t *testing.T) {
	for _, call := range stopCalls() {
		for _, site := range []string{"handler", "OnRequest"} {
			t.Run(call.name+"/"+site, func(t *testing.T) {
				var ms *Server
				returned := make(chan error, 1)
				var once sync.Once
				stop := func() { once.Do(func() { returned <- call.stop(ms) }) }

				var handled atomic.Int32
				var ctxDoneInHandler atomic.Bool
				var handlerReturned atomic.Bool
				h := &blockingHandler{fn: func(ctx context.Context) {
					handled.Add(1)
					if site == "handler" {
						stop()
						// The call has returned on this goroutine: the handler goes on.
						handlerReturned.Store(true)
					}
					ctxDoneInHandler.Store(ctx.Err() != nil)
				}}
				m := &hookMetrics{}
				if site == "OnRequest" {
					m.onRequest = func(uint8, FunctionCode) { stop() }
				}
				var conn net.Conn
				ms, conn = startCallbackServer(t, h, func(c *ServerConfig) { c.Metrics = m })
				addr := ms.tcpListener.Addr().String()

				// Two requests in one write: only the first is dispatched.
				frames := append(rawRequestFrame(1, 1, byte(FCReadHoldingRegisters), u16(0, 1)),
					rawRequestFrame(2, 1, byte(FCReadHoldingRegisters), u16(0, 1))...)
				if _, err := conn.Write(frames); err != nil {
					t.Fatalf("write: %v", err)
				}
				select {
				case err := <-returned:
					if err != nil {
						t.Fatalf("%s from the %s: %v", call.name, site, err)
					}
				case <-time.After(e2eWait):
					t.Fatalf("%s called from the %s did not return within %v", call.name, site, e2eWait)
				}
				// Returned: the listener is closed.
				if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
					_ = c.Close()
					t.Error("the listener still accepts connections")
				}
				wantClosedUnanswered(t, conn)
				// The caller's connection winds down once the callback returns.
				e2eEventually(t, "the connection to end", func() bool { return e2eServerConns(ms) == 0 })
				e2eEventually(t, "the outcome of the request", func() bool { return m.responses.Load()+m.errs.Load() == 1 })

				if got := handled.Load(); got != 1 {
					t.Errorf("handler called %d times, want 1: no request is dispatched after the stop", got)
				}
				if site == "handler" && !handlerReturned.Load() {
					t.Error("the handler did not go on after the call returned")
				}
				if !ctxDoneInHandler.Load() {
					t.Error("the handler's context was not cancelled by the stop")
				}
				if got := m.requests.Load(); got != 1 {
					t.Errorf("OnRequest called %d times, want 1", got)
				}
				if err := mustReturn(t, "a Stop after the callback's", ms.Stop); err != nil {
					t.Errorf("a Stop after the callback's: %v", err)
				}
			})
		}
	}
}

// L10, server. A Stop or Shutdown called from a handler, or from a hook, waits
// for the handlers of the other connections: it returns once the one in
// progress has returned (Stop, Shutdown with a context that does not end), or
// when its context ends, with that context's error.
func TestServerL10_CallbackStopWaitsForOtherConnections(t *testing.T) {
	for _, site := range []string{"handler", "OnResponse"} {
		for _, until := range []string{"handler returns", "context ends"} {
			t.Run(site+"/"+until, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var ms *Server
				var caller atomic.Uint64
				returned := make(chan error, 1)
				stop := func() {
					caller.Store(goid.ID())
					if until == "context ends" {
						returned <- ms.Shutdown(ctx)
					} else {
						returned <- ms.Stop()
					}
				}

				entered := make(chan struct{})
				release := make(chan struct{})
				var otherCtxDone atomic.Bool
				// Unit 2 is the other connection's: its handler blocks until
				// released. Unit 1 stops the server.
				h := &unitHandler{fn: func(ctx context.Context, unit uint8) {
					if unit == 2 {
						close(entered)
						<-release
						otherCtxDone.Store(ctx.Err() != nil)
						return
					}
					if site == "handler" {
						stop()
					}
				}}
				m := &hookMetrics{}
				if site == "OnResponse" {
					m.onResponse = func(unit uint8, _ FunctionCode) {
						if unit == 1 {
							stop()
						}
					}
				}
				var conn net.Conn
				ms, conn = startCallbackServer(t, h, func(c *ServerConfig) { c.Metrics = m })
				other, err := net.Dial("tcp", ms.tcpListener.Addr().String())
				if err != nil {
					t.Fatalf("dial: %v", err)
				}
				defer func() { _ = other.Close() }()
				if _, err := other.Write(rawRequestFrame(1, 2, byte(FCReadHoldingRegisters), u16(0, 1))); err != nil {
					t.Fatalf("write: %v", err)
				}
				<-entered

				sendReadRequest(t, conn)
				waitUntilShutdownWaits(t, &caller)
				// Shutdown is in WaitGroup.Wait, for the other handler: it cannot
				// have returned.
				select {
				case err := <-returned:
					t.Fatalf("returned %v while the other connection's handler was running", err)
				default:
				}

				if until == "context ends" {
					cancel()
					select {
					case err := <-returned:
						if !errors.Is(err, context.Canceled) {
							t.Errorf("Shutdown returned %v, want its context's error", err)
						}
					case <-time.After(e2eWait):
						t.Fatalf("Shutdown did not return within %v of its context ending", e2eWait)
					}
					close(release)
					return
				}
				close(release)
				select {
				case err := <-returned:
					if err != nil {
						t.Errorf("Stop: %v", err)
					}
				case <-time.After(e2eWait):
					t.Fatalf("Stop did not return within %v of the other handler returning", e2eWait)
				}
				if !otherCtxDone.Load() {
					t.Error("the other connection's context was not cancelled by the stop")
				}
			})
		}
	}
}

// L10, server. A Stop on a goroutine the server does not run, whether a
// handler started it (go srv.Stop()) or not, waits for the handler in
// progress, and returns once it has returned.
func TestServerL10_StopOnAnotherGoroutineWaitsForTheHandler(t *testing.T) {
	for _, from := range []string{"started by the handler", "the test"} {
		t.Run(from, func(t *testing.T) {
			var ms *Server
			var caller atomic.Uint64
			stopped := make(chan error, 1)
			stop := func() {
				caller.Store(goid.ID())
				stopped <- ms.Stop()
			}
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			h := &blockingHandler{fn: func(context.Context) {
				once.Do(func() {
					if from == "started by the handler" {
						go stop()
					}
					close(entered)
				})
				<-release
			}}
			var conn net.Conn
			ms, conn = startCallbackServer(t, h, nil)

			sendReadRequest(t, conn)
			<-entered
			if from == "the test" {
				go stop()
			}
			waitUntilShutdownWaits(t, &caller)
			wantClosedUnanswered(t, conn)
			select {
			case err := <-stopped:
				t.Fatalf("Stop returned %v while the handler was running", err)
			default:
			}
			close(release)
			select {
			case err := <-stopped:
				if err != nil {
					t.Fatalf("Stop: %v", err)
				}
			case <-time.After(e2eWait):
				t.Fatalf("Stop did not return within %v of the handler returning", e2eWait)
			}
		})
	}
}

// L10, server. ConnectionMetrics.OnConnectionRejected runs on the accept loop.
// A Stop called there waits for the connections, not for the accept loop, and
// returns; the accept loop then ends.
func TestServerL10_ConnectionMetricsStopsTheServer(t *testing.T) {
	var ms *Server
	returned := make(chan error, 1)
	m := &rejectMetrics{}
	m.onRejected = func(string, RejectReason) { returned <- ms.Stop() }
	var conn net.Conn
	ms, conn = startCallbackServer(t, &blockingHandler{fn: func(context.Context) {}}, func(c *ServerConfig) {
		c.MaxClients = 1
		c.Metrics = m
	})
	e2eEventually(t, "the first connection to be admitted", func() bool { return e2eServerConns(ms) == 1 })
	refused, err := net.Dial("tcp", ms.tcpListener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = refused.Close() }()

	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("Stop from OnConnectionRejected: %v", err)
		}
	case <-time.After(e2eWait):
		t.Fatalf("Stop called from OnConnectionRejected did not return within %v", e2eWait)
	}
	// It waited for the admitted connection's goroutine.
	if n := e2eServerConns(ms); n != 0 {
		t.Errorf("%d connections still served after Stop returned", n)
	}
	wantClosedUnanswered(t, conn)
	wantClosedUnanswered(t, refused)
}

// L10, server. The Logger is called on the goroutine that reads a connection's
// requests too. A Stop called there returns, and the request whose arrival it
// logged is not dispatched.
func TestServerL10_LoggerStopsTheServer(t *testing.T) {
	var ms *Server
	returned := make(chan error, 1)
	var once sync.Once
	log := &hookLogger{debugf: func(line string) {
		if strings.Contains(line, "RX:") {
			once.Do(func() { returned <- ms.Stop() })
		}
	}}
	var handled atomic.Int32
	var conn net.Conn
	ms, conn = startCallbackServer(t, &blockingHandler{fn: func(context.Context) { handled.Add(1) }},
		func(c *ServerConfig) { c.Logger = log })

	sendReadRequest(t, conn)
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("Stop from the Logger: %v", err)
		}
	case <-time.After(e2eWait):
		t.Fatalf("Stop called from the Logger did not return within %v", e2eWait)
	}
	wantClosedUnanswered(t, conn)
	e2eEventually(t, "the connection to end", func() bool { return e2eServerConns(ms) == 0 })
	if n := handled.Load(); n != 0 {
		t.Errorf("handler called %d times after the Logger stopped the server", n)
	}
}

// unitHandler is a RequestHandler whose HandleHoldingRegisters calls fn with
// the unit id of the request.
type unitHandler struct {
	noSerialFCHandler
	fn func(ctx context.Context, unit uint8)
}

func (h *unitHandler) HandleHoldingRegisters(ctx context.Context, req *HoldingRegistersRequest) ([]uint16, error) {
	h.fn(ctx, req.UnitID)
	return make([]uint16, req.Quantity), nil
}

// rejectMetrics is a ServerMetrics and ConnectionMetrics that records the
// connections reported as rejected and calls onRejected for each.
type rejectMetrics struct {
	hookMetrics
	mu         sync.Mutex
	rejected   []rejectedConn
	onRejected func(addr string, reason RejectReason)
}

type rejectedConn struct {
	addr   string
	reason RejectReason
}

func (m *rejectMetrics) OnConnectionRejected(addr string, reason RejectReason) {
	m.mu.Lock()
	m.rejected = append(m.rejected, rejectedConn{addr, reason})
	m.mu.Unlock()
	if m.onRejected != nil {
		m.onRejected(addr, reason)
	}
}

func (m *rejectMetrics) snapshot() []rejectedConn {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]rejectedConn(nil), m.rejected...)
}

// hookLogger is a Logger that records every line and passes the formatted
// debug lines to debugf.
type hookLogger struct {
	e2eLogger
	debugf func(line string)
}

func (l *hookLogger) Debugf(format string, args ...any) {
	l.e2eLogger.Debugf(format, args...)
	if l.debugf != nil {
		l.debugf(fmt.Sprintf(format, args...))
	}
}
