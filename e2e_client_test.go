// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"
)

// e2eGate lets a test hold requests inside the server handler and observe how
// many are in flight at once.
type e2eGate struct {
	mu       sync.Mutex
	inFlight int
	peak     int
	entered  chan struct{}
	release  chan struct{}
}

func e2eNewGate() *e2eGate {
	return &e2eGate{entered: make(chan struct{}, 1024), release: make(chan struct{})}
}

// hook blocks the calling handler until open is called or its context ends.
func (g *e2eGate) hook(ctx context.Context, _ e2eCall) error {
	g.mu.Lock()
	g.inFlight++
	if g.inFlight > g.peak {
		g.peak = g.inFlight
	}
	g.mu.Unlock()
	g.entered <- struct{}{}
	select {
	case <-g.release:
	case <-ctx.Done():
	}
	g.mu.Lock()
	g.inFlight--
	g.mu.Unlock()
	return nil
}

// waitEntered waits until n handler calls have started.
func (g *e2eGate) waitEntered(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-g.entered:
		case <-time.After(e2eWait):
			t.Fatalf("only %d of %d requests reached the handler", i, n)
		}
	}
}

func (g *e2eGate) open() { close(g.release) }

func (g *e2eGate) peakInFlight() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.peak
}

func TestE2E_Client_OpenCloseLifecycle(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{noOpen: true})
		c := p.client
		ctx := context.Background()

		wantInfo := ClientInfo{
			IsOpen:    false,
			Endpoint:  p.clientConf.URL[len(kind)+3:],
			Transport: TransportKind(kind),
		}
		if got := c.Info(); got != wantInfo {
			t.Errorf("Info before Open = %+v, want %+v", got, wantInfo)
		}
		if got := c.LastObservedTransactionID(); got != 0 {
			t.Errorf("transaction ID before Open = %d", got)
		}

		notOpen := func(when string) {
			t.Helper()
			for _, op := range e2eAllOps() {
				if err := op.call(ctx, c); !errors.Is(err, ErrClientNotOpen) {
					t.Errorf("%s %s: got %v, want ErrClientNotOpen", op.name, when, err)
				}
			}
			if n := dev.callCount(); n != 0 {
				t.Errorf("%s: %d requests reached the server", when, n)
			}
			if p.conns() != 0 {
				t.Errorf("%s: server connections = %d", when, p.conns())
			}
		}
		notOpen("before Open")
		if err := c.Close(); err != nil {
			t.Errorf("Close before Open: %v", err)
		}

		// Open is idempotent: one connection, however often it is called.
		for i := 0; i < 3; i++ {
			if err := c.Open(); err != nil {
				t.Fatalf("Open #%d: %v", i+1, err)
			}
		}
		wantInfo.IsOpen = true
		if got := c.Info(); got != wantInfo {
			t.Errorf("Info after Open = %+v, want %+v", got, wantInfo)
		}
		for i := 1; i <= 3; i++ {
			if err := c.WriteRegister(ctx, e2eUnit, 1, uint16(i)); err != nil {
				t.Fatalf("WriteRegister: %v", err)
			}
			if got := c.LastObservedTransactionID(); got != uint16(i) {
				t.Errorf("transaction ID after request %d = %d", i, got)
			}
		}
		if p.conns() != 1 {
			t.Errorf("server connections after repeated Open = %d, want 1", p.conns())
		}
		firstAddr := dev.takeCalls()[0].ClientAddr

		// Close is idempotent and releases the server-side connection.
		for i := 0; i < 3; i++ {
			if err := c.Close(); err != nil {
				t.Errorf("Close #%d: %v", i+1, err)
			}
		}
		wantInfo.IsOpen = false
		if got := c.Info(); got != wantInfo {
			t.Errorf("Info after Close = %+v, want %+v", got, wantInfo)
		}
		e2eEventually(t, "the server to drop the connection", func() bool { return p.conns() == 0 })
		notOpen("after Close")

		// Reopen: a new connection, transaction IDs start over, state persists.
		if err := c.Open(); err != nil {
			t.Fatalf("reopen: %v", err)
		}
		got, err := c.ReadHoldingRegister(ctx, e2eUnit, 1)
		if err != nil || got != 3 {
			t.Errorf("read after reopen = %d, %v", got, err)
		}
		if id := c.LastObservedTransactionID(); id != 1 {
			t.Errorf("transaction ID after reopen = %d, want 1", id)
		}
		if addr := e2eOneCall(t, "after reopen", dev).ClientAddr; addr == firstAddr {
			t.Errorf("reopened client reused connection %s", addr)
		}
		if p.conns() != 1 {
			t.Errorf("server connections after reopen = %d, want 1", p.conns())
		}
	})
}

// Open against an address nobody listens on fails cleanly, and succeeds once a
// server is there.
func TestE2E_Client_OpenWithoutServer(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		addr := e2eFreeAddr(t)
		c, err := New(e2eClientConfig(t, kind, addr))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })

		start := time.Now()
		if err := c.Open(); err == nil {
			t.Fatal("Open succeeded without a server")
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("refused connection took %v to report", d)
		}
		if c.Info().IsOpen {
			t.Error("client reports open after a failed Open")
		}
		if _, err := c.ReadCoil(context.Background(), e2eUnit, 0); !errors.Is(err, ErrClientNotOpen) {
			t.Errorf("request after a failed Open: %v", err)
		}

		sconf := e2eServerConfig(t, kind, addr)
		server, err := NewServer(&sconf, e2eNewDevice())
		if err != nil {
			t.Fatal(err)
		}
		if err := server.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = server.Stop() })
		if err := c.Open(); err != nil {
			t.Fatalf("Open once the server is up: %v", err)
		}
		if _, err := c.ReadCoil(context.Background(), e2eUnit, 0); err != nil {
			t.Errorf("request once the server is up: %v", err)
		}
	})
}

// The configured Timeout bounds a request against a handler that does not
// answer; the late response does not confuse the next request.
func TestE2E_Client_RequestTimeout(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		const timeout = 40 * time.Millisecond
		dev := e2eNewDevice()
		dev.setHolding(1, 0x1111, 0x2222)
		gate := e2eNewGate()
		dev.setHook(gate.hook)
		p := e2eStart(t, kind, dev, e2eOpts{client: func(c *Config) { c.Timeout = timeout }})
		ctx := context.Background()

		start := time.Now()
		_, err := p.client.ReadHoldingRegister(ctx, e2eUnit, 1)
		elapsed := time.Since(start)
		if !errors.Is(err, ErrRequestTimedOut) {
			t.Fatalf("request against a silent handler: %v, want ErrRequestTimedOut", err)
		}
		if elapsed < timeout*3/4 || elapsed > 2*time.Second {
			t.Errorf("request timed out after %v, configured %v", elapsed, timeout)
		}
		gate.waitEntered(t, 1)

		// The handler answers late. The connection the request timed out on is
		// not used again (the late answer would be on it): the next request runs
		// on a new connection and gets its own answer.
		dev.setHook(nil)
		gate.open()
		long, cancel := context.WithTimeout(ctx, e2eWait)
		defer cancel()
		got, err := p.client.ReadHoldingRegister(long, e2eUnit, 2)
		if err != nil || got != 0x2222 {
			t.Fatalf("request after a timeout = 0x%04X, %v; want 0x2222", got, err)
		}
		if id := p.client.LastObservedTransactionID(); id != 1 {
			t.Errorf("transaction ID = %d, want 1 (first request on a new connection)", id)
		}
		e2eEventually(t, "the timed-out connection to be closed", func() bool { return p.conns() == 1 })
	})
}

// A context deadline replaces the configured Timeout, in both directions.
func TestE2E_Client_ContextDeadline(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		dev.setHolding(1, 0x1111)
		p := e2eStart(t, kind, dev, e2eOpts{client: func(c *Config) { c.Timeout = e2eWait }})
		bg := context.Background()

		// Shorter than Timeout: the request ends at the context deadline.
		gate := e2eNewGate()
		dev.setHook(gate.hook)
		ctx, cancel := context.WithTimeout(bg, 40*time.Millisecond)
		start := time.Now()
		_, err := p.client.ReadHoldingRegister(ctx, e2eUnit, 1)
		elapsed := time.Since(start)
		cancel()
		if !errors.Is(err, ErrRequestTimedOut) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("request past its context deadline: %v", err)
		}
		if elapsed < 30*time.Millisecond || elapsed > e2eWait/2 {
			t.Errorf("request ended after %v, context deadline was 40ms", elapsed)
		}
		gate.waitEntered(t, 1)
		gate.open()

		// An already expired deadline: nothing is sent.
		dev.setHook(nil)
		e2eEventually(t, "the late handler to finish", func() bool {
			_, err := p.client.ReadHoldingRegister(bg, e2eUnit, 1)
			return err == nil
		})
		dev.takeCalls()
		expired, cancel := context.WithDeadline(bg, time.Now().Add(-time.Second))
		_, err = p.client.ReadHoldingRegister(expired, e2eUnit, 1)
		cancel()
		if !errors.Is(err, ErrRequestTimedOut) && !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("request with an expired deadline: %v", err)
		}
		if n := dev.callCount(); n != 0 {
			t.Errorf("request with an expired deadline reached the server (%d)", n)
		}

		// Longer than Timeout: the request may take longer than Timeout.
		short := p.newClient(func(c *Config) { c.Timeout = 20 * time.Millisecond })
		if err := short.Open(); err != nil {
			t.Fatal(err)
		}
		dev.setHook(func(ctx context.Context, _ e2eCall) error {
			select {
			case <-time.After(60 * time.Millisecond):
			case <-ctx.Done():
			}
			return nil
		})
		ctx, cancel = context.WithTimeout(bg, e2eWait)
		got, err := short.ReadHoldingRegister(ctx, e2eUnit, 1)
		cancel()
		if err != nil || got != 0x1111 {
			t.Errorf("request with a context deadline beyond Timeout = 0x%04X, %v", got, err)
		}
		if _, err := short.ReadHoldingRegister(bg, e2eUnit, 1); !errors.Is(err, ErrRequestTimedOut) {
			t.Errorf("the same request without a context deadline: %v, want ErrRequestTimedOut", err)
		}
	})
}

// Cancelling the context of an in-flight request must end the request.
func TestE2E_Client_ContextCancelInFlight(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		gate := e2eNewGate()
		dev.setHook(gate.hook)
		p := e2eStart(t, kind, dev, e2eOpts{client: func(c *Config) { c.Timeout = 150 * time.Millisecond }})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		start := time.Now()
		go func() {
			_, err := p.client.ReadHoldingRegister(ctx, e2eUnit, 1)
			done <- err
		}()
		gate.waitEntered(t, 1)
		cancel()
		err := <-done
		elapsed := time.Since(start)
		gate.open()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelling the context of an in-flight request is ignored: only a context deadline is "+
				"honoured (internal/transport/tcp.go ExecuteRequest); the call returned %v after %v, i.e. at the "+
				"configured Timeout", err, elapsed)
		}
		if elapsed > 100*time.Millisecond {
			t.Errorf("cancelled request returned after %v", elapsed)
		}
	})
}

// A request made with an already cancelled context must not be sent.
func TestE2E_Client_ContextAlreadyCancelled(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		// Probes check the context themselves.
		for _, op := range e2eAllOps() {
			if !op.probe {
				continue
			}
			if err := op.call(ctx, p.client); !errors.Is(err, context.Canceled) || dev.callCount() != 0 {
				t.Errorf("%s with a cancelled context: err=%v, %d requests sent", op.name, err, dev.callCount())
			}
		}

		err := p.client.WriteRegister(ctx, e2eUnit, 5, 0xDEAD)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WriteRegister with an already cancelled context returned %v: the request was sent "+
				"(%d request(s) reached the handler) and the register now holds 0x%04X",
				err, dev.callCount(), dev.holdingAt(5, 1)[0])
		}
		for _, op := range e2eAllOps() {
			if op.probe {
				continue
			}
			if err := op.call(ctx, p.client); !errors.Is(err, context.Canceled) {
				t.Errorf("%s with a cancelled context: %v", op.name, err)
			}
		}
		if n := dev.callCount(); n != 0 {
			t.Errorf("%d requests with a cancelled context reached the server", n)
		}
	})
}

// Close while a request is in flight makes the request fail promptly.
func TestE2E_Client_CloseDuringRequest(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		gate := e2eNewGate()
		dev.setHook(gate.hook)
		p := e2eStart(t, kind, dev, e2eOpts{client: func(c *Config) { c.Timeout = e2eWait }})

		done := make(chan error, 1)
		go func() {
			_, err := p.client.ReadHoldingRegister(context.Background(), e2eUnit, 1)
			done <- err
		}()
		gate.waitEntered(t, 1)
		start := time.Now()
		if err := p.client.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		select {
		case err := <-done:
			if err == nil || errors.Is(err, ErrRequestTimedOut) {
				t.Errorf("in-flight request after Close: %v, want a connection error", err)
			}
		case <-time.After(e2eWait / 2):
			t.Fatal("in-flight request still blocked after Close")
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("request took %v to fail after Close", d)
		}
		gate.open()
		if _, err := p.client.ReadCoil(context.Background(), e2eUnit, 0); !errors.Is(err, ErrClientNotOpen) {
			t.Errorf("request after Close: %v", err)
		}
		// The client can be reopened and used.
		dev.setHook(nil)
		if err := p.client.Open(); err != nil {
			t.Fatal(err)
		}
		if _, err := p.client.ReadCoil(context.Background(), e2eUnit, 0); err != nil {
			t.Errorf("request after reopening: %v", err)
		}
	})
}

// DialTimeout bounds connection establishment (here: a TLS handshake against a
// peer that accepts the TCP connection and then stays silent); Timeout does not.
func TestE2E_Client_DialTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	var held []net.Conn
	var mu sync.Mutex
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			held = append(held, conn)
			mu.Unlock()
		}
	}()
	defer func() {
		mu.Lock()
		for _, c := range held {
			_ = c.Close()
		}
		mu.Unlock()
	}()

	conf := e2eClientConfig(t, "tcp+tls", ln.Addr().String())
	conf.Timeout = time.Minute
	conf.DialTimeout = 60 * time.Millisecond
	c, err := New(conf)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = c.Open()
	elapsed := time.Since(start)
	if err == nil {
		_ = c.Close()
		t.Fatal("Open succeeded against a silent peer")
	}
	var nerr net.Error
	if !errors.As(err, &nerr) || !nerr.Timeout() {
		t.Errorf("Open against a silent peer: %v, want a timeout", err)
	}
	if elapsed < 45*time.Millisecond || elapsed > 3*time.Second {
		t.Errorf("Open gave up after %v, DialTimeout is 60ms", elapsed)
	}
	if c.Info().IsOpen {
		t.Error("client reports open after a failed Open")
	}

	// A plain TCP dial to the same peer connects at once; the request then
	// runs into Timeout, not DialTimeout.
	tconf := Config{URL: "tcp://" + ln.Addr().String(), Timeout: 40 * time.Millisecond, DialTimeout: time.Minute}
	tc, err := New(tconf)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tc.Close() }()
	if err := tc.Open(); err != nil {
		t.Fatalf("plain TCP Open: %v", err)
	}
	start = time.Now()
	_, err = tc.ReadCoil(context.Background(), e2eUnit, 0)
	if !errors.Is(err, ErrRequestTimedOut) {
		t.Errorf("request to a silent peer: %v, want ErrRequestTimedOut", err)
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond || elapsed > 3*time.Second {
		t.Errorf("request gave up after %v, Timeout is 40ms", elapsed)
	}
}

// With MaxConns > 1 concurrent requests really travel over several server
// connections, MinConns connections exist right after Open, and the pool never
// grows beyond MaxConns.
func TestE2E_Client_ConnectionPool(t *testing.T) {
	const maxConns, minConns, callers = 4, 2, 10
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp", dev, e2eOpts{
		noOpen: true,
		client: func(c *Config) {
			c.MaxConns = maxConns
			c.MinConns = minConns
			c.Timeout = e2eWait
		},
	})
	c := p.client
	ctx := context.Background()

	want := ClientInfo{Endpoint: p.hostPort, Transport: TransportTCP, PoolEnabled: true, MaxConns: maxConns}
	if got := c.Info(); got != want {
		t.Errorf("Info = %+v, want %+v", got, want)
	}
	if err := c.Open(); err != nil {
		t.Fatal(err)
	}
	// Pre-warmed connections are visible on the server before any request.
	e2eEventually(t, "MinConns pre-warmed connections", func() bool { return p.conns() == minConns })
	if n := dev.callCount(); n != 0 {
		t.Errorf("pre-warming sent %d requests", n)
	}

	// Sequential requests reuse the pre-warmed connections.
	for i := 0; i < 20; i++ {
		if err := c.WriteRegister(ctx, e2eUnit, uint16(i), uint16(i)); err != nil {
			t.Fatal(err)
		}
	}
	if p.conns() != minConns {
		t.Errorf("sequential requests grew the pool to %d connections", p.conns())
	}
	dev.takeCalls()

	// Concurrent requests: the handler holds them, so maxConns of them must be
	// in flight at once, each on its own server connection.
	gate := e2eNewGate()
	dev.setHook(gate.hook)
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := c.ReadHoldingRegister(ctx, e2eUnit, uint16(i))
			if err == nil && got != uint16(i) {
				err = errors.New("wrong value")
			}
			errs <- err
		}(i)
	}
	gate.waitEntered(t, maxConns)
	e2eEventually(t, "the pool to reach MaxConns", func() bool { return p.conns() == maxConns })
	// No further request may start while all connections are busy.
	select {
	case <-gate.entered:
		t.Fatalf("more than %d requests in flight", maxConns)
	case <-time.After(30 * time.Millisecond):
	}
	if p.conns() != maxConns {
		t.Errorf("server connections = %d, want %d", p.conns(), maxConns)
	}
	gate.open()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("pooled request: %v", err)
		}
	}
	if peak := gate.peakInFlight(); peak != maxConns {
		t.Errorf("peak concurrent requests = %d, want %d", peak, maxConns)
	}
	addrs := map[string]bool{}
	calls := dev.takeCalls()
	for _, call := range calls {
		addrs[call.ClientAddr] = true
	}
	if len(calls) != callers || len(addrs) != maxConns {
		t.Errorf("%d requests over %d connections, want %d over %d", len(calls), len(addrs), callers, maxConns)
	}
	if p.conns() != maxConns {
		t.Errorf("server connections after the burst = %d, want %d", p.conns(), maxConns)
	}

	// A caller waiting for a free connection gives up when its context ends.
	gate = e2eNewGate()
	dev.setHook(gate.hook)
	for i := 0; i < maxConns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.ReadCoil(ctx, e2eUnit, 0)
		}()
	}
	gate.waitEntered(t, maxConns)
	short, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	_, err := c.ReadCoil(short, e2eUnit, 0)
	cancel()
	if !errors.Is(err, ErrRequestTimedOut) && !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("request waiting for a pooled connection: %v, want a timeout", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if _, err := c.ReadCoil(cancelled, e2eUnit, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled request waiting for a pooled connection: %v, want context.Canceled", err)
	}
	gate.open()
	wg.Wait()
	if n := len(dev.takeCalls()); n != maxConns {
		t.Errorf("%d requests reached the server, want %d", n, maxConns)
	}

	// Close closes every pooled connection.
	dev.setHook(nil)
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	e2eEventually(t, "the server to drop all pooled connections", func() bool { return p.conns() == 0 })
	if _, err := c.ReadCoil(ctx, e2eUnit, 0); !errors.Is(err, ErrClientNotOpen) {
		t.Errorf("request after Close: %v", err)
	}
}

// Without a pool (the default, and always on tcp+tls) concurrent callers share
// one connection and their requests are serialised.
func TestE2E_Client_SingleConnectionSerialises(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		for _, maxConns := range []int{0, 1, 4} {
			if kind == "tcp" && maxConns > 1 {
				continue
			}
			dev := e2eNewDevice()
			var mu sync.Mutex
			inFlight, peak := 0, 0
			dev.setHook(func(context.Context, e2eCall) error {
				mu.Lock()
				inFlight++
				if inFlight > peak {
					peak = inFlight
				}
				mu.Unlock()
				time.Sleep(time.Millisecond)
				mu.Lock()
				inFlight--
				mu.Unlock()
				return nil
			})
			p := e2eStart(t, kind, dev, e2eOpts{client: func(c *Config) { c.MaxConns = maxConns; c.MinConns = maxConns }})
			info := p.client.Info()
			if info.PoolEnabled {
				t.Errorf("MaxConns=%d on %s: pool enabled", maxConns, kind)
			}
			if maxConns > 1 && info.MaxConns != 1 {
				t.Errorf("MaxConns=%d on %s reported as %d, want 1 (pooling unsupported)", maxConns, kind, info.MaxConns)
			}

			var wg sync.WaitGroup
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					for j := 0; j < 3; j++ {
						addr := uint16(i*10 + j)
						if err := p.client.WriteRegister(context.Background(), e2eUnit, addr, addr); err != nil {
							t.Errorf("WriteRegister: %v", err)
						}
						if got, err := p.client.ReadHoldingRegister(context.Background(), e2eUnit, addr); err != nil || got != addr {
							t.Errorf("ReadHoldingRegister(%d) = %d, %v", addr, got, err)
						}
					}
				}(i)
			}
			wg.Wait()
			if peak != 1 {
				t.Errorf("MaxConns=%d on %s: %d requests in flight at once, want 1", maxConns, kind, peak)
			}
			if p.conns() != 1 {
				t.Errorf("MaxConns=%d on %s: %d server connections, want 1", maxConns, kind, p.conns())
			}
			if id := p.client.LastObservedTransactionID(); id != 24 {
				t.Errorf("transaction ID = %d after 24 requests", id)
			}
			_ = p.client.Close()
			_ = p.server.Stop()
		}
	})
}

// e2eDropFirst makes the device's server drop the connection (no response) for
// the first n requests it receives.
func e2eDropFirst(dev *e2eDevice, n int) {
	var mu sync.Mutex
	dev.setHook(func(context.Context, e2eCall) error {
		mu.Lock()
		defer mu.Unlock()
		if n > 0 {
			n--
			// A handler returning ErrProtocolError makes the server close the link.
			return ErrProtocolError
		}
		return nil
	})
}

func TestE2E_Client_RetryPolicy(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		cm := &e2eAttemptMetrics{}
		sm := &e2eMetrics{}
		p := e2eStart(t, kind, dev, e2eOpts{
			server: func(c *ServerConfig) { c.Metrics = sm },
			client: func(c *Config) {
				c.RetryPolicy = ExponentialBackoff(time.Millisecond, 4*time.Millisecond, 2)
				c.Metrics = cm
			},
		})
		ctx := context.Background()

		// The server drops the first connection without answering; the
		// request succeeds on the retry, over a new connection.
		e2eDropFirst(dev, 1)
		if err := p.client.WriteRegister(ctx, e2eUnit, 5, 0xBEEF); err != nil {
			t.Fatalf("request with one dropped connection: %v", err)
		}
		calls := dev.takeCalls()
		if len(calls) != 2 || calls[0].ClientAddr == calls[1].ClientAddr {
			t.Fatalf("server saw %+v, want the write twice over two connections", calls)
		}
		for _, call := range calls {
			if call.FC != FCWriteSingleRegister || !reflect.DeepEqual(call.Words, []uint16{0xBEEF}) {
				t.Errorf("server saw %+v", call)
			}
		}
		events := cm.take()
		want := []string{"request:06", "attempt:06", "retrydial:00", "attempt:06", "response:06"}
		if got := e2eKinds(events); !reflect.DeepEqual(got, want) {
			t.Fatalf("client metrics = %v, want %v", events, want)
		}
		if events[1].Attempt != 0 || events[1].Err == nil || events[2].Attempt != 1 || events[2].Err != nil ||
			events[3].Attempt != 1 || events[3].Err != nil {
			t.Errorf("attempt metrics = %v", events)
		}
		e2eEventually(t, "server metrics", func() bool { return sm.len() == 4 })
		sevents := sm.take()
		if got := e2eKinds(sevents); !reflect.DeepEqual(got, []string{"request:06", "error:06", "request:06", "response:06"}) {
			t.Errorf("server metrics = %v", sevents)
		}
		if !errors.Is(sevents[1].Err, ErrProtocolError) {
			t.Errorf("server error metric carries %v", sevents[1].Err)
		}
		e2eEventually(t, "one server connection", func() bool { return p.conns() == 1 })

		// Two retries allowed: three dropped connections exhaust the policy.
		e2eDropFirst(dev, 3)
		err := p.client.WriteRegister(ctx, e2eUnit, 6, 1)
		if err == nil {
			t.Fatal("request succeeded although every attempt was dropped")
		}
		if n := len(dev.takeCalls()); n != 3 {
			t.Errorf("server saw %d attempts, want 3", n)
		}
		want = []string{"request:06", "attempt:06", "retrydial:00", "attempt:06", "retrydial:00", "attempt:06", "error:06"}
		events = cm.take()
		if got := e2eKinds(events); !reflect.DeepEqual(got, want) {
			t.Errorf("client metrics = %v, want %v", events, want)
		}
		if got := dev.holdingAt(6, 1)[0]; got != 0 {
			t.Errorf("register written although every attempt was dropped: %d", got)
		}

		// Exceptions and parameter errors are answers, not failures to retry.
		dev.setHook(func(context.Context, e2eCall) error { return ErrServerDeviceBusy })
		// The previous request left the client without a working connection;
		// this one first reconnects.
		err = p.client.WriteRegister(ctx, e2eUnit, 6, 1)
		if !errors.Is(err, ErrServerDeviceBusy) {
			e2eEventually(t, "a usable connection", func() bool {
				return errors.Is(p.client.WriteRegister(ctx, e2eUnit, 6, 1), ErrServerDeviceBusy)
			})
		}
		dev.takeCalls()
		cm.take()
		err = p.client.WriteRegister(ctx, e2eUnit, 6, 1)
		e2eWantException(t, "busy device with a retry policy", err, FCWriteSingleRegister, exServerDeviceBusy)
		if n := len(dev.takeCalls()); n != 1 {
			t.Errorf("an exception was retried: server saw %d requests", n)
		}
		if got := e2eKinds(cm.take()); !reflect.DeepEqual(got, []string{"request:06", "attempt:06", "error:06"}) {
			t.Errorf("client metrics for an exception = %v", got)
		}
	})
}

// Timeouts are only retried when the policy says so.
func TestE2E_Client_RetryOnTimeout(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		dev.setHolding(1, 0x0707)
		cm := &e2eAttemptMetrics{}
		p := e2eStart(t, kind, dev, e2eOpts{client: func(c *Config) {
			c.Timeout = 40 * time.Millisecond
			c.RetryPolicy = ExponentialBackoff(time.Millisecond, 4*time.Millisecond, 3)
			c.Metrics = cm
		}})
		ctx := context.Background()

		// blockFirst holds the first n requests until released.
		blockFirst := func(n int) chan struct{} {
			release := make(chan struct{})
			var mu sync.Mutex
			dev.setHook(func(ctx context.Context, _ e2eCall) error {
				mu.Lock()
				block := n > 0
				n--
				mu.Unlock()
				if block {
					select {
					case <-release:
					case <-ctx.Done():
					}
				}
				return nil
			})
			return release
		}

		release := blockFirst(1)
		if _, err := p.client.ReadHoldingRegister(ctx, e2eUnit, 1); !errors.Is(err, ErrRequestTimedOut) {
			t.Errorf("default policy: %v, want ErrRequestTimedOut without a retry", err)
		}
		close(release)
		if got := e2eKinds(cm.take()); !reflect.DeepEqual(got, []string{"request:03", "attempt:03", "timeout:03"}) {
			t.Errorf("client metrics = %v", got)
		}
		if n := len(dev.takeCalls()); n != 1 {
			t.Errorf("server saw %d requests, want 1", n)
		}

		retrying := p.newClient(func(c *Config) {
			c.RetryPolicy = NewExponentialBackoff(ExponentialBackoffConfig{
				BaseDelay: time.Millisecond, MaxDelay: 4 * time.Millisecond, MaxAttempts: 8, RetryOnTimeout: true,
			})
		})
		if err := retrying.Open(); err != nil {
			t.Fatal(err)
		}
		release = blockFirst(1)
		got, err := retrying.ReadHoldingRegister(ctx, e2eUnit, 1)
		close(release)
		if err != nil || got != 0x0707 {
			t.Errorf("RetryOnTimeout: 0x%04X, %v", got, err)
		}
		// request, one or more (timed-out attempt, reconnect), the successful
		// attempt, response. A loaded machine may need more than one retry.
		events := cm.take()
		kinds := e2eKinds(events)
		n := len(kinds)
		ok := n >= 5 && n%2 == 1 && kinds[0] == "request:03" && kinds[n-2] == "attempt:03" && kinds[n-1] == "response:03" &&
			events[n-2].Err == nil
		for i := 1; ok && i < n-2; i += 2 {
			ok = kinds[i] == "attempt:03" && errors.Is(events[i].Err, ErrRequestTimedOut) && events[i].Attempt == (i-1)/2 &&
				kinds[i+1] == "retrydial:00" && events[i+1].Err == nil && events[i+1].Attempt == (i+1)/2
		}
		if !ok {
			t.Errorf("client metrics = %v, want request, (timed-out attempt, retrydial)+, attempt, response", events)
		}
		if got := len(dev.takeCalls()); got != (n-1)/2 {
			t.Errorf("server saw %d requests, want %d", got, (n-1)/2)
		}
	})
}

// A pooled client replaces only the failed connection on a retry.
func TestE2E_Client_RetryWithPool(t *testing.T) {
	dev := e2eNewDevice()
	cm := &e2eAttemptMetrics{}
	p := e2eStart(t, "tcp", dev, e2eOpts{client: func(c *Config) {
		c.MaxConns = 3
		c.MinConns = 3
		c.RetryPolicy = ExponentialBackoff(time.Millisecond, 4*time.Millisecond, 2)
		c.Metrics = cm
	}})
	ctx := context.Background()
	e2eEventually(t, "three pooled connections", func() bool { return p.conns() == 3 })

	e2eDropFirst(dev, 1)
	if err := p.client.WriteRegister(ctx, e2eUnit, 5, 0x0505); err != nil {
		t.Fatalf("pooled request with one dropped connection: %v", err)
	}
	calls := dev.takeCalls()
	if len(calls) != 2 || calls[0].ClientAddr == calls[1].ClientAddr {
		t.Errorf("server saw %+v, want two attempts over two connections", calls)
	}
	if got := e2eKinds(cm.take()); !reflect.DeepEqual(got, []string{"request:06", "attempt:06", "attempt:06", "response:06"}) {
		t.Errorf("client metrics = %v", got)
	}
	e2eEventually(t, "the dropped connection to be gone", func() bool { return p.conns() == 2 })
	// The pool grows back on demand.
	for i := 0; i < 5; i++ {
		if err := p.client.WriteRegister(ctx, e2eUnit, 5, uint16(i)); err != nil {
			t.Fatal(err)
		}
	}
	if p.conns() < 2 || p.conns() > 3 {
		t.Errorf("server connections = %d", p.conns())
	}
}

// After a server restart every pooled connection is dead. Each failed attempt
// replaces one of them, so the pool recovers by itself within as many attempts
// as it held connections, with or without a retry policy.
func TestE2E_Client_PoolAfterServerRestart(t *testing.T) {
	const conns = 3
	for _, retries := range []int{0, conns} {
		addr := e2eFreeAddr(t)
		dev := e2eNewDevice()
		sconf := e2eServerConfig(t, "tcp", addr)
		server, err := NewServer(&sconf, dev)
		if err != nil {
			t.Fatal(err)
		}
		if err := server.Start(); err != nil {
			t.Fatal(err)
		}
		conf := e2eClientConfig(t, "tcp", addr)
		conf.MaxConns, conf.MinConns = conns, conns
		if retries > 0 {
			conf.RetryPolicy = ExponentialBackoff(time.Millisecond, 4*time.Millisecond, retries)
		}
		c, err := New(conf)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Open(); err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		e2eEventually(t, "the pool to be pre-warmed", func() bool { return e2eServerConns(server) == conns })
		if err := c.WriteRegister(ctx, e2eUnit, 1, 0x0101); err != nil {
			t.Fatal(err)
		}

		if err := server.Stop(); err != nil {
			t.Fatal(err)
		}
		server2, err := NewServer(&sconf, dev)
		if err != nil {
			t.Fatal(err)
		}
		if err := server2.Start(); err != nil {
			t.Fatal(err)
		}

		failures := 0
		for i := 0; i <= conns; i++ {
			got, err := c.ReadHoldingRegister(ctx, e2eUnit, 1)
			if err == nil {
				if got != 0x0101 {
					t.Errorf("retries=%d: read 0x%04X after the restart", retries, got)
				}
				break
			}
			failures++
		}
		// Without a retry policy up to one request per dead connection fails.
		if failures > conns || (retries > 0 && failures != 0) {
			t.Errorf("retries=%d: %d requests failed after the restart", retries, failures)
		}
		// From here on the pool is healthy again.
		for i := 0; i < 2*conns; i++ {
			if _, err := c.ReadHoldingRegister(ctx, e2eUnit, 1); err != nil {
				t.Errorf("retries=%d: request %d on the recovered pool: %v", retries, i, err)
			}
		}
		_ = c.Close()
		_ = server2.Stop()
	}
}

// After a retry whose reconnect failed, the client must be able to reach the
// server again once it is back.
func TestE2E_Client_RetryRecoversAfterFailedRedial(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		addr := e2eFreeAddr(t)
		dev := e2eNewDevice()
		sconf := e2eServerConfig(t, kind, addr)
		server, err := NewServer(&sconf, dev)
		if err != nil {
			t.Fatal(err)
		}
		if err := server.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = server.Stop() })

		cm := &e2eAttemptMetrics{}
		conf := e2eClientConfig(t, kind, addr)
		conf.RetryPolicy = ExponentialBackoff(time.Millisecond, 4*time.Millisecond, 2)
		conf.Metrics = cm
		c, err := New(conf)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		if err := c.Open(); err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if err := c.WriteRegister(ctx, e2eUnit, 1, 0x0101); err != nil {
			t.Fatal(err)
		}
		cm.take()

		// Server down: the request fails after the reconnect is refused.
		if err := server.Stop(); err != nil {
			t.Fatal(err)
		}
		if _, err := c.ReadHoldingRegister(ctx, e2eUnit, 1); err == nil {
			t.Fatal("request succeeded against a stopped server")
		}
		events := cm.take()
		if got := e2eKinds(events); !reflect.DeepEqual(got, []string{"request:03", "attempt:03", "retrydial:00", "error:03"}) {
			t.Errorf("client metrics = %v", events)
		} else if events[2].Err == nil {
			t.Error("the refused reconnect was reported as successful")
		}

		// Server back (a new instance on the same address): the client, which
		// was never closed, must work again.
		server2, err := NewServer(&sconf, dev)
		if err != nil {
			t.Fatal(err)
		}
		if err := server2.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = server2.Stop() })
		got, err := c.ReadHoldingRegister(ctx, e2eUnit, 1)
		if errors.Is(err, ErrClientNotOpen) {
			info := c.Info()
			openErr := c.Open()
			_, again := c.ReadHoldingRegister(ctx, e2eUnit, 1)
			t.Fatalf("after a retry whose reconnect failed, the session engine stays without a transport "+
				"(internal/session/engine.go Execute): every later request returns ErrClientNotOpen although "+
				"Info().IsOpen is %v, and Open() is a no-op (returned %v, next request: %v); only Close+Open recovers",
				info.IsOpen, openErr, again)
		}
		if err != nil || got != 0x0101 {
			t.Errorf("request once the server is back: 0x%04X, %v", got, err)
		}
	})
}

// e2eTLSListener returns a TLS listener using the suite's server certificate.
func e2eTLSListener(t *testing.T, mut func(*tls.Config)) net.Listener {
	t.Helper()
	pki := e2ePKI(t)
	conf := &tls.Config{
		Certificates: []tls.Certificate{pki.serverCert},
		ClientCAs:    pki.ca.pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS10,
	}
	if mut != nil {
		mut(conf)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", conf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}
