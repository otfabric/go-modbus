// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// Contract tests of the server's callbacks and of its accept loop: what a
// ServerMetrics hook that does not return normally costs, what Stop and
// Shutdown do when a callback calls them (row L10 of the lifecycle matrix in
// TESTING.md), that only the first Stop waits (row L2), and how the accept loop
// paces itself after errors.

// mustReturn runs fn and fails the test if it has not returned after e2eWait.
func mustReturn(t *testing.T, what string, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(e2eWait):
		t.Fatalf("%s did not return within %v", what, e2eWait)
		return nil
	}
}

// startCallbackServer starts a tcp server for h and returns it with one raw
// connection to it.
func startCallbackServer(t *testing.T, h RequestHandler, mut func(*ServerConfig)) (*Server, net.Conn) {
	t.Helper()
	conf := &ServerConfig{URL: "tcp://127.0.0.1:0", Timeout: 30 * time.Second}
	if mut != nil {
		mut(conf)
	}
	ms, err := NewServer(conf, h)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := ms.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn, err := net.Dial("tcp", ms.tcpListener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return ms, conn
}

// sendReadRequest writes one FC03 request for one register.
func sendReadRequest(t *testing.T, conn net.Conn) {
	t.Helper()
	if _, err := conn.Write(rawRequestFrame(1, 1, byte(FCReadHoldingRegisters), u16(0, 1))); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// wantClosedUnanswered waits until the server has closed conn and checks that
// it sent nothing first.
func wantClosedUnanswered(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(e2eWait))
	n, err := conn.Read(make([]byte, 16))
	if n != 0 || err == nil || os.IsTimeout(err) {
		t.Fatalf("read %d bytes, error %v; want the connection closed by the server without a response", n, err)
	}
}

// hookMetrics is a ServerMetrics whose methods call the functions set on it.
type hookMetrics struct {
	onRequest  func(uint8, FunctionCode)
	onResponse func(uint8, FunctionCode)
	onError    func(uint8, FunctionCode, error)

	requests, responses, errs atomic.Int32
}

func (m *hookMetrics) OnRequest(unitID uint8, fc FunctionCode) {
	m.requests.Add(1)
	if m.onRequest != nil {
		m.onRequest(unitID, fc)
	}
}

func (m *hookMetrics) OnResponse(unitID uint8, fc FunctionCode, _ time.Duration) {
	m.responses.Add(1)
	if m.onResponse != nil {
		m.onResponse(unitID, fc)
	}
}

func (m *hookMetrics) OnError(unitID uint8, fc FunctionCode, _ time.Duration, err error) {
	m.errs.Add(1)
	if m.onError != nil {
		m.onError(unitID, fc, err)
	}
}

// A ServerMetrics hook that does not return normally is treated like a handler
// that does not: it is logged with its stack, and the request is answered as if
// the hook had returned. The connection and the server keep serving, and the
// other hooks are still called.
func TestServerMetrics_HookThatDoesNotReturnIsContained(t *testing.T) {
	type counts struct{ requests, responses, errs int32 }
	cases := []struct {
		hook string
		// arm makes one hook stop short; the returned function disarms it.
		arm func(m *hookMetrics, on *atomic.Bool)
		// call makes one request and checks what the client got.
		call func(t *testing.T, c *Client, dev *e2eDevice)
		want counts
	}{
		{
			hook: "OnRequest",
			arm: func(m *hookMetrics, on *atomic.Bool) {
				m.onRequest = func(_ uint8, fc FunctionCode) {
					if on.Load() {
						// A table sized for the function codes the application expects.
						var perFC [3]int
						perFC[fc]++
					}
				}
			},
			call: func(t *testing.T, c *Client, dev *e2eDevice) {
				dev.setHolding(7, 0xBEEF)
				// FC03 is past the end of that table.
				if v, err := c.ReadHoldingRegister(context.Background(), e2eUnit, 7); err != nil || v != 0xBEEF {
					t.Errorf("ReadHoldingRegister: %#04x, %v; want 0xBEEF", v, err)
				}
			},
			want: counts{requests: 1, responses: 1},
		},
		{
			hook: "OnResponse",
			arm: func(m *hookMetrics, on *atomic.Bool) {
				m.onResponse = func(uint8, FunctionCode) {
					if on.Load() {
						panic("metrics hook")
					}
				}
			},
			call: func(t *testing.T, c *Client, dev *e2eDevice) {
				dev.setHolding(8, 0xCAFE)
				if v, err := c.ReadHoldingRegister(context.Background(), e2eUnit, 8); err != nil || v != 0xCAFE {
					t.Errorf("ReadHoldingRegister: %#04x, %v; want 0xCAFE", v, err)
				}
			},
			want: counts{requests: 1, responses: 1},
		},
		{
			hook: "OnError",
			arm: func(m *hookMetrics, on *atomic.Bool) {
				m.onError = func(uint8, FunctionCode, error) {
					if on.Load() {
						panic("metrics hook")
					}
				}
			},
			call: func(t *testing.T, c *Client, _ *e2eDevice) {
				// A unit the device does not serve: the handler's error is the
				// client's exception, whatever OnError does with it.
				_, err := c.ReadHoldingRegister(context.Background(), e2eUnit+1, 0)
				if _, ok := errors.AsType[*ExceptionError](err); !ok {
					t.Errorf("ReadHoldingRegister on an unknown unit: %v, want an exception response", err)
				}
			},
			want: counts{requests: 1, errs: 1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.hook, func(t *testing.T) {
			dev := e2eNewDevice()
			log := &e2eLogger{}
			m := &hookMetrics{}
			var on atomic.Bool
			tc.arm(m, &on)
			p := e2eStart(t, "tcp", dev, e2eOpts{server: func(c *ServerConfig) { c.Logger, c.Metrics = log, m }})

			on.Store(true)
			tc.call(t, p.client, dev)
			on.Store(false)

			if got := (counts{m.requests.Load(), m.responses.Load(), m.errs.Load()}); got != tc.want {
				t.Errorf("hooks called %+v, want %+v", got, tc.want)
			}
			if !log.has("E ", "ServerMetrics."+tc.hook, "goroutine ") {
				t.Errorf("no error-level log line naming ServerMetrics.%s with a stack:\n%s", tc.hook, log.dump())
			}

			// The same connection serves the next request.
			dev.setHolding(9, 0x1234)
			if v, err := p.client.ReadHoldingRegister(context.Background(), e2eUnit, 9); err != nil || v != 0x1234 {
				t.Errorf("request after the hook: %#04x, %v", v, err)
			}
			if got := p.client.LastObservedTransactionID(); got != 2 {
				t.Errorf("transaction ID %d after two requests: the connection was replaced", got)
			}
			if p.conns() != 1 {
				t.Errorf("server connections = %d, want 1", p.conns())
			}
		})
	}
}

// reportMetrics names its hooks as the interface does.
func TestMetricsHook_String(t *testing.T) {
	for hook, want := range map[metricsHook]string{
		metricsOnRequest: "OnRequest", metricsOnResponse: "OnResponse", metricsOnError: "OnError",
	} {
		if got := hook.String(); got != want {
			t.Errorf("metricsHook(%d) = %q, want %q", hook, got, want)
		}
	}
}

// L10, server. A handler method runs on a goroutine that Shutdown waits for.
// With the handler's own context, which the shutdown cancels, Shutdown returns
// context.Canceled once the listener and the connections are closed.
func TestServerL10_HandlerCallsShutdownWithItsContext(t *testing.T) {
	returned := make(chan error, 1)
	h := &blockingHandler{}
	ms, conn := startCallbackServer(t, h, nil)
	h.fn = func(ctx context.Context) { returned <- ms.Shutdown(ctx) }

	sendReadRequest(t, conn)
	select {
	case err := <-returned:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Shutdown with the handler's context returned %v, want context.Canceled", err)
		}
	case <-time.After(e2eWait):
		t.Fatalf("Shutdown with the handler's context did not return within %v", e2eWait)
	}
	// The request being handled is not answered: the shutdown closed its connection.
	wantClosedUnanswered(t, conn)
	// The server is stopped: no new connection is accepted, and a later Stop has
	// nothing to do.
	if c, err := net.DialTimeout("tcp", ms.tcpListener.Addr().String(), time.Second); err == nil {
		_ = c.Close()
		t.Error("the listener still accepts connections after Shutdown")
	}
	if err := mustReturn(t, "Stop after the handler's Shutdown", ms.Stop); err != nil {
		t.Errorf("Stop after the handler's Shutdown: %v", err)
	}
}

// L10, server. With any other context, a Shutdown called from a handler method
// or a ServerMetrics hook returns only when that context ends, with its error:
// it waits for the goroutine it was called on. Stop is Shutdown with a context
// that never ends.
func TestServerL10_CallbackCallsShutdownWithAnotherContext(t *testing.T) {
	for _, site := range []string{"handler", "metrics hook"} {
		t.Run(site, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			returned := make(chan error, 1)
			var ms *Server
			var once sync.Once
			shutdown := func() { once.Do(func() { returned <- ms.Shutdown(ctx) }) }

			h := &blockingHandler{fn: func(context.Context) {}}
			m := &hookMetrics{}
			if site == "handler" {
				h.fn = func(context.Context) { shutdown() }
			} else {
				m.onRequest = func(uint8, FunctionCode) { shutdown() }
			}
			var conn net.Conn
			ms, conn = startCallbackServer(t, h, func(c *ServerConfig) { c.Metrics = m })

			sendReadRequest(t, conn)
			// The shutdown has closed the connection: from here it only waits.
			wantClosedUnanswered(t, conn)
			select {
			case err := <-returned:
				t.Fatalf("Shutdown returned %v before its context ended", err)
			default:
			}
			cancel()
			select {
			case err := <-returned:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Shutdown returned %v, want its context's error", err)
				}
			case <-time.After(e2eWait):
				t.Fatalf("Shutdown did not return within %v of its context ending", e2eWait)
			}
		})
	}
}

// L10, server. A Stop started from a handler on another goroutine completes
// once the handler has returned, and not before.
func TestServerL10_HandlerStartsStopOnAnotherGoroutine(t *testing.T) {
	stopped := make(chan error, 1)
	release := make(chan struct{})
	h := &blockingHandler{}
	ms, conn := startCallbackServer(t, h, nil)
	var once sync.Once
	h.fn = func(context.Context) {
		once.Do(func() { go func() { stopped <- ms.Stop() }() })
		<-release
	}

	sendReadRequest(t, conn)
	// Stop has closed the connection and waits for the handler.
	wantClosedUnanswered(t, conn)
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned %v while the handler was still running", err)
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
}

// L2, server. Only the first Stop or Shutdown of a cycle waits for the handler
// goroutines: one called while it is still waiting finds the server stopped and
// returns nil at once.
func TestServerL2_OnlyTheFirstStopWaits(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	h := &blockingHandler{fn: func(context.Context) {
		once.Do(func() { close(entered) })
		<-release
	}}
	ms, conn := startCallbackServer(t, h, nil)

	sendReadRequest(t, conn)
	<-entered
	first := make(chan error, 1)
	go func() { first <- ms.Stop() }()
	// The first Stop has marked the server stopped and closed the connection.
	wantClosedUnanswered(t, conn)

	if err := mustReturn(t, "a second Stop", ms.Stop); err != nil {
		t.Errorf("second Stop: %v", err)
	}
	if err := mustReturn(t, "a Shutdown after Stop", func() error { return ms.Shutdown(context.Background()) }); err != nil {
		t.Errorf("Shutdown after Stop: %v", err)
	}
	select {
	case err := <-first:
		t.Fatalf("the first Stop returned %v while the handler was still running", err)
	default:
	}

	close(release)
	select {
	case err := <-first:
		if err != nil {
			t.Fatalf("first Stop: %v", err)
		}
	case <-time.After(e2eWait):
		t.Fatalf("the first Stop did not return within %v of the handler returning", e2eWait)
	}
}

// scriptedListener answers Accept from a script: an error, or a connection for
// a nil entry. Past the end of the script, and once closed, it reports
// net.ErrClosed. It counts the calls.
type scriptedListener struct {
	mu     sync.Mutex
	script []error
	// repeat, if not nil, is the error of every call past the script, up to
	// limit calls in all.
	repeat error
	limit  int
	calls  int
	closed bool
	peers  []net.Conn
}

func (l *scriptedListener) Accept() (net.Conn, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.closed {
		return nil, net.ErrClosed
	}
	if len(l.script) > 0 {
		err := l.script[0]
		l.script = l.script[1:]
		if err != nil {
			return nil, err
		}
		c, peer := net.Pipe()
		l.peers = append(l.peers, peer)
		return c, nil
	}
	if l.repeat != nil && l.calls <= l.limit {
		return nil, l.repeat
	}
	return nil, net.ErrClosed
}

func (l *scriptedListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	for _, p := range l.peers {
		_ = p.Close()
	}
	return nil
}

func (l *scriptedListener) Addr() net.Addr { return &net.TCPAddr{} }

func (l *scriptedListener) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// acceptLoop runs the accept loop of a server on ln, as Start does.
func acceptLoop(t *testing.T, ln net.Listener, logger Logger) (stop func(), done <-chan struct{}) {
	t.Helper()
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Logger: logger}, &fnHandler{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	run := &serverRun{listener: ln, wg: &sync.WaitGroup{}}
	run.ctx, run.cancel = context.WithCancel(context.Background())
	run.wg.Add(1)
	ended := make(chan struct{})
	go func() {
		ms.acceptTCPClients(run)
		close(ended)
	}()
	// As Shutdown: the stop context first, then the listener.
	return func() { run.cancel(); _ = ln.Close() }, ended
}

// The accept loop waits after an Accept that failed: 5 ms, then twice as long
// after each failure that follows, up to one second. Time is the fake clock of
// testing/synctest, so the schedule is exact.
func TestServerAccept_BacksOffAfterErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// A listener that fails for as long as it is asked: without a wait
		// between the calls the limit is reached at once.
		ln := &scriptedListener{repeat: errors.New("accept tcp: too many open files"), limit: 10000}
		log := &e2eLogger{}
		stop, done := acceptLoop(t, ln, log)

		start := time.Now()
		schedule := []struct {
			at    time.Duration
			calls int
		}{
			{0, 1},
			{4 * time.Millisecond, 1},
			{5 * time.Millisecond, 2},
			{14 * time.Millisecond, 2},
			{15 * time.Millisecond, 3},
			{35 * time.Millisecond, 4},
			{75 * time.Millisecond, 5},
			{155 * time.Millisecond, 6},
			{315 * time.Millisecond, 7},
			{635 * time.Millisecond, 8},
			{1274 * time.Millisecond, 8},
			{1275 * time.Millisecond, 9}, // 640 ms after the eighth
			{2274 * time.Millisecond, 9},
			{2275 * time.Millisecond, 10}, // one second after the ninth: the cap
			{3275 * time.Millisecond, 11},
			{60275 * time.Millisecond, 68},
		}
		for _, step := range schedule {
			time.Sleep(time.Until(start.Add(step.at)))
			synctest.Wait()
			if got := ln.count(); got != step.calls {
				t.Fatalf("%v after the first failure: Accept called %d times, want %d", step.at, got, step.calls)
			}
		}
		if !log.has("W ", "failed to accept", "next attempt in 5ms") || !log.has("W ", "next attempt in 1s") {
			t.Errorf("the waits are not in the log:\n%.600s", log.dump())
		}

		// Stopping ends the wait: the loop does not sit out its second.
		stopped := time.Now()
		stop()
		<-done
		if d := time.Since(stopped); d != 0 {
			t.Fatalf("the accept loop ended %v after the server stopped, want at once", d)
		}
		if got := ln.count(); got != 68 {
			t.Fatalf("Accept called %d times, want no call after the stop", got)
		}
	})
}

// An Accept that succeeds starts the sequence over.
func TestServerAccept_BackoffStartsOverAfterSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fail := errors.New("accept tcp: connection aborted")
		// Two failures, one connection, then failures again.
		ln := &scriptedListener{script: []error{fail, fail, nil}, repeat: fail, limit: 10000}
		stop, done := acceptLoop(t, ln, nil)

		start := time.Now()
		for _, step := range []struct {
			at    time.Duration
			calls int
		}{
			{0, 1},
			{5 * time.Millisecond, 2},
			// The third call returns a connection and the fourth fails right after it.
			{15 * time.Millisecond, 4},
			{19 * time.Millisecond, 4},
			{20 * time.Millisecond, 5}, // 5 ms after the fourth, not 20
			{30 * time.Millisecond, 6},
		} {
			time.Sleep(time.Until(start.Add(step.at)))
			synctest.Wait()
			if got := ln.count(); got != step.calls {
				t.Fatalf("%v after the first failure: Accept called %d times, want %d", step.at, got, step.calls)
			}
		}
		stop()
		<-done
	})
}

func TestNextAcceptBackoff(t *testing.T) {
	cases := []struct{ prev, want time.Duration }{
		{-time.Second, 5 * time.Millisecond},
		{0, 5 * time.Millisecond},
		{5 * time.Millisecond, 10 * time.Millisecond},
		{320 * time.Millisecond, 640 * time.Millisecond},
		{499 * time.Millisecond, 998 * time.Millisecond},
		{500 * time.Millisecond, time.Second},
		{640 * time.Millisecond, time.Second},
		{time.Second, time.Second},
		{time.Hour, time.Second},
	}
	for _, tc := range cases {
		if got := nextAcceptBackoff(tc.prev); got != tc.want {
			t.Errorf("nextAcceptBackoff(%v) = %v, want %v", tc.prev, got, tc.want)
		}
	}
}

// On the real clock: stopping the server ends a loop whose listener keeps
// failing, whether it is waiting or in Accept at that moment.
func TestServerAccept_StopsWhileBackingOff(t *testing.T) {
	ln := &scriptedListener{repeat: io.ErrUnexpectedEOF, limit: 1 << 30}
	stop, done := acceptLoop(t, ln, nil)
	stop()
	select {
	case <-done:
	case <-time.After(e2eWait):
		t.Fatal("the accept loop did not end when the server stopped")
	}
}
