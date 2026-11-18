// SPDX-License-Identifier: MIT

package modbus

import (
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// lifecycleMetrics records the connections the server refuses, opens and
// closes.
type lifecycleMetrics struct {
	rejectMetrics
	evMu   sync.Mutex
	events []string
}

func (m *lifecycleMetrics) OnConnectionOpened(addr, role string) {
	m.evMu.Lock()
	m.events = append(m.events, "opened "+addr+" role="+role)
	m.evMu.Unlock()
}

func (m *lifecycleMetrics) OnConnectionClosed(addr string) {
	m.evMu.Lock()
	m.events = append(m.events, "closed "+addr)
	m.evMu.Unlock()
}

func (m *lifecycleMetrics) eventsSnapshot() []string {
	m.evMu.Lock()
	defer m.evMu.Unlock()
	return append([]string(nil), m.events...)
}

func holdingOne() *fnHandler {
	return &fnHandler{holding: func(*HoldingRegistersRequest) ([]uint16, error) { return []uint16{0x0102}, nil }}
}

// AllowClient refuses a connection as soon as it is accepted: it is closed
// without an answer, reported as RejectNotAllowed with its address, and takes
// no MaxClients slot; a connection it allows is served.
func TestServerAllowClient_RefusesBeforeReading(t *testing.T) {
	m := &lifecycleMetrics{}
	var refuse atomic.Bool
	refuse.Store(true)
	var askedMu sync.Mutex
	var asked []string
	ms, first := startCallbackServer(t, holdingOne(), func(c *ServerConfig) {
		c.MaxClients = 1
		c.Metrics = m
		c.AllowClient = ClientFilterFunc(func(remote net.Addr) bool {
			askedMu.Lock()
			asked = append(asked, remote.String())
			askedMu.Unlock()
			return !refuse.Load()
		})
	})
	t.Cleanup(func() { _ = ms.Stop() })
	sendReadRequest(t, first)
	wantClosedUnanswered(t, first)
	wantRejected(t, &m.rejectMetrics, rejectedConn{first.LocalAddr().String(), RejectNotAllowed})
	askedMu.Lock()
	if len(asked) != 1 || asked[0] != first.LocalAddr().String() {
		t.Errorf("AllowClient asked about %v, want [%s]", asked, first.LocalAddr())
	}
	askedMu.Unlock()
	if got := m.eventsSnapshot(); len(got) != 0 {
		t.Errorf("a refused connection was reported %v", got)
	}

	refuse.Store(false)
	second := dialRaw(t, ms)
	sendReadRequest(t, second)
	_ = second.SetReadDeadline(time.Now().Add(e2eWait))
	resp := make([]byte, 11)
	if _, err := io.ReadFull(second, resp); err != nil {
		t.Fatalf("the allowed connection, in the slot the refused one did not take: %v", err)
	}
	if resp[7] != byte(FCReadHoldingRegisters) || resp[9] != 0x01 || resp[10] != 0x02 {
		t.Errorf("response % x", resp)
	}
}

// An AllowClient that panics refuses the connection; the server keeps
// accepting.
func TestServerAllowClient_PanicRefuses(t *testing.T) {
	m := &lifecycleMetrics{}
	var calls atomic.Int32
	ms, first := startCallbackServer(t, holdingOne(), func(c *ServerConfig) {
		c.Metrics = m
		c.Logger = NopLogger()
		c.AllowClient = ClientFilterFunc(func(net.Addr) bool {
			if calls.Add(1) == 1 {
				panic("allow-list unavailable")
			}
			return true
		})
	})
	t.Cleanup(func() { _ = ms.Stop() })
	wantClosedUnanswered(t, first)
	wantRejected(t, &m.rejectMetrics, rejectedConn{first.LocalAddr().String(), RejectNotAllowed})
	second := dialRaw(t, ms)
	e2eEventually(t, "the next connection to be served", func() bool { return e2eServerConns(ms) == 1 })
	_ = second
}

// ConnectionLifecycleMetrics sees a served connection open, with its address
// and role, and close once when the client leaves.
func TestServerConnectionLifecycle_OpenedThenClosed(t *testing.T) {
	m := &lifecycleMetrics{}
	ms, conn := startCallbackServer(t, holdingOne(), func(c *ServerConfig) { c.Metrics = m })
	t.Cleanup(func() { _ = ms.Stop() })
	addr := conn.LocalAddr().String()
	sendReadRequest(t, conn)
	_ = conn.SetReadDeadline(time.Now().Add(e2eWait))
	if _, err := io.ReadFull(conn, make([]byte, 11)); err != nil {
		t.Fatalf("response: %v", err)
	}
	if got := m.eventsSnapshot(); len(got) != 1 || got[0] != "opened "+addr+" role=" {
		t.Fatalf("events while connected %v, want [opened %s role=]", got, addr)
	}
	_ = conn.Close()
	e2eEventually(t, "the close to be reported", func() bool { return len(m.eventsSnapshot()) == 2 })
	if got := m.eventsSnapshot(); got[1] != "closed "+addr {
		t.Errorf("events %v, want the close of %s", got, addr)
	}
	if got := m.snapshot(); len(got) != 0 {
		t.Errorf("rejections %v for a served connection", got)
	}
}

// On tcp+tls a connection is reported opened once its TLS handshake has
// completed, not before: one that never handshakes is never opened.
func TestServerConnectionLifecycle_TLSOpensAfterHandshake(t *testing.T) {
	m := &lifecycleMetrics{}
	dev := e2eNewDevice()
	dev.setHolding(1, 0x0101)
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{noOpen: true, server: func(c *ServerConfig) { c.Metrics = m }})
	silent := dialRaw(t, p.server)
	e2eEventually(t, "the silent connection", func() bool { return p.conns() == 1 })
	if got := m.eventsSnapshot(); len(got) != 0 {
		t.Fatalf("a connection that has not handshaken was reported %v", got)
	}
	_ = silent.Close()
	if err := p.client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := m.eventsSnapshot(); len(got) != 1 || len(got[0]) < 7 || got[0][:7] != "opened " {
		t.Fatalf("events %v, want one opened", got)
	}
	if err := p.client.Close(); err != nil {
		t.Fatal(err)
	}
	e2eEventually(t, "the close to be reported", func() bool { return len(m.eventsSnapshot()) == 2 })
}

// Addr is nil before Start and then the address the server listens on, with
// the port the system chose for port 0.
func TestServerAddr(t *testing.T) {
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Logger: NopLogger()}, holdingOne())
	if err != nil {
		t.Fatal(err)
	}
	if a := ms.Addr(); a != nil {
		t.Fatalf("Addr before Start = %v, want nil", a)
	}
	if err := ms.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ms.Stop() })
	a, ok := ms.Addr().(*net.TCPAddr)
	if !ok || a.Port == 0 || !a.IP.IsLoopback() {
		t.Fatalf("Addr = %v, want 127.0.0.1 with the chosen port", ms.Addr())
	}
	c, err := net.Dial("tcp", a.String())
	if err != nil {
		t.Fatalf("dial Addr: %v", err)
	}
	_ = c.Close()
	if err := ms.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := ms.Addr(); got == nil || got.String() != a.String() {
		t.Errorf("Addr after Stop = %v, want the last address %v", got, a)
	}
}

// On tcp+tls AllowClient is asked before the handshake: a refused connection
// is closed without one, reported as RejectNotAllowed and never opened.
func TestServerAllowClient_TLSRefusesBeforeHandshake(t *testing.T) {
	m := &lifecycleMetrics{}
	p := e2eStart(t, "tcp+tls", e2eNewDevice(), e2eOpts{noOpen: true, server: func(c *ServerConfig) {
		c.Metrics = m
		c.AllowClient = ClientFilterFunc(func(net.Addr) bool { return false })
	}})
	raw := dialRaw(t, p.server)
	wantRejected(t, &m.rejectMetrics, rejectedConn{raw.LocalAddr().String(), RejectNotAllowed})
	_ = raw.SetReadDeadline(time.Now().Add(e2eWait))
	if n, err := raw.Read(make([]byte, 1)); err == nil {
		t.Errorf("the refused connection sent %d bytes (a handshake answer?)", n)
	}
	if err := p.client.Open(); err == nil {
		_, err = p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1)
		if err == nil {
			t.Error("a refused client was served")
		}
	}
	if got := m.eventsSnapshot(); len(got) != 0 {
		t.Errorf("a refused connection was reported %v", got)
	}
}

// A Stop called from OnConnectionOpened stops the server: the connection is
// not served and, with no callback after the Shutdown, not reported closed.
func TestServerConnectionLifecycle_StopFromOpened(t *testing.T) {
	stopped := make(chan error, 1)
	var ms *Server
	m := &stopOnOpen{stop: func() { stopped <- ms.Stop() }}
	var err error
	ms, err = NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Metrics: m, Logger: NopLogger()}, holdingOne())
	if err != nil {
		t.Fatal(err)
	}
	if err := ms.Start(); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", ms.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop from OnConnectionOpened: %v", err)
		}
	case <-time.After(e2eWait):
		t.Fatal("Stop from OnConnectionOpened did not return")
	}
	wantClosedUnanswered(t, conn)
	if n := m.closed.Load(); n != 0 {
		t.Errorf("OnConnectionClosed ran %d times after the Shutdown", n)
	}
}

type stopOnOpen struct {
	hookMetrics
	stop   func()
	closed atomic.Int32
}

func (m *stopOnOpen) OnConnectionOpened(string, string) { m.stop() }
func (m *stopOnOpen) OnConnectionClosed(string)         { m.closed.Add(1) }

// panicLifecycle panics in OnConnectionOpened and OnConnectionClosed, and
// counts the calls.
type panicLifecycle struct {
	hookMetrics
	opened, closed atomic.Int32
}

func (m *panicLifecycle) OnConnectionOpened(string, string) {
	m.opened.Add(1)
	panic("status page unavailable")
}

func (m *panicLifecycle) OnConnectionClosed(string) {
	m.closed.Add(1)
	panic("status page unavailable")
}

// Lifecycle callbacks that panic are logged; the connection is served and the
// server goes on as if they had returned.
func TestServerConnectionLifecycle_CallbacksPanic(t *testing.T) {
	m := &panicLifecycle{}
	ms, conn := startCallbackServer(t, holdingOne(), func(c *ServerConfig) {
		c.Metrics = m
		c.Logger = NopLogger()
	})
	t.Cleanup(func() { _ = ms.Stop() })
	for i, c := range []net.Conn{conn, dialRaw(t, ms)} {
		sendReadRequest(t, c)
		_ = c.SetReadDeadline(time.Now().Add(e2eWait))
		if _, err := io.ReadFull(c, make([]byte, 11)); err != nil {
			t.Fatalf("connection %d after a panicking OnConnectionOpened: %v", i, err)
		}
		_ = c.Close()
	}
	e2eEventually(t, "both closes to be reported", func() bool { return m.closed.Load() == 2 })
	if n := m.opened.Load(); n != 2 {
		t.Errorf("OnConnectionOpened ran %d times, want 2", n)
	}
}

// A Stop called from AllowClient stops the server and returns, whatever
// AllowClient answers: the connection is closed unanswered, the accept loop
// ends, and no callback runs after the Shutdown (no rejection, no log entry).
func TestServerAllowClient_StopFromAllowClient(t *testing.T) {
	for _, allow := range []bool{true, false} {
		t.Run(fmt.Sprintf("allow=%v", allow), func(t *testing.T) {
			stopped := make(chan error, 1)
			m := &lifecycleMetrics{}
			logs := &countLogger{}
			var ms *Server
			var err error
			ms, err = NewServer(&ServerConfig{
				URL:     "tcp://127.0.0.1:0",
				Metrics: m,
				Logger:  logs,
				AllowClient: ClientFilterFunc(func(net.Addr) bool {
					stopped <- ms.Stop()
					logs.mark()
					return allow
				}),
			}, holdingOne())
			if err != nil {
				t.Fatal(err)
			}
			if err := ms.Start(); err != nil {
				t.Fatal(err)
			}
			conn, err := net.Dial("tcp", ms.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			select {
			case err := <-stopped:
				if err != nil {
					t.Fatalf("Stop from AllowClient: %v", err)
				}
			case <-time.After(e2eWait):
				t.Fatal("Stop from AllowClient did not return")
			}
			wantClosedUnanswered(t, conn)
			if c, err := net.DialTimeout("tcp", ms.Addr().String(), time.Second); err == nil {
				_ = c.Close()
				t.Error("the server still accepts after a Stop from AllowClient")
			}
			if got := m.eventsSnapshot(); len(got) != 0 {
				t.Errorf("events %v for a connection the stopped server did not serve", got)
			}
			if got := m.snapshot(); len(got) != 0 {
				t.Errorf("rejections %v reported after the Shutdown", got)
			}
			if n := logs.afterMark(); n != 0 {
				t.Errorf("%d log entries after the Shutdown", n)
			}
		})
	}
}

// A Stop called from OnConnectionRejected for a connection AllowClient refused
// returns, and the accept loop ends there: nothing is accepted or logged after
// it.
func TestServerAllowClient_StopFromRejected(t *testing.T) {
	stopped := make(chan error, 1)
	m := &lifecycleMetrics{}
	logs := &countLogger{}
	var ms *Server
	m.onRejected = func(_ string, reason RejectReason) {
		if reason != RejectNotAllowed {
			t.Errorf("reason %v, want %v", reason, RejectNotAllowed)
		}
		stopped <- ms.Stop()
		logs.mark()
	}
	var err error
	ms, err = NewServer(&ServerConfig{
		URL:         "tcp://127.0.0.1:0",
		Metrics:     m,
		Logger:      logs,
		AllowClient: ClientFilterFunc(func(net.Addr) bool { return false }),
	}, holdingOne())
	if err != nil {
		t.Fatal(err)
	}
	if err := ms.Start(); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", ms.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop from OnConnectionRejected: %v", err)
		}
	case <-time.After(e2eWait):
		t.Fatal("Stop from OnConnectionRejected did not return")
	}
	wantClosedUnanswered(t, conn)
	if c, err := net.DialTimeout("tcp", ms.Addr().String(), time.Second); err == nil {
		_ = c.Close()
		t.Error("the server still accepts after a Stop from OnConnectionRejected")
	}
	if got := m.snapshot(); len(got) != 1 {
		t.Errorf("rejections %v, want the one refused connection", got)
	}
	if n := logs.afterMark(); n != 0 {
		t.Errorf("%d log entries after the Shutdown", n)
	}
}

// countLogger counts the entries logged after mark.
type countLogger struct {
	marked atomic.Bool
	after  atomic.Int32
}

func (l *countLogger) mark()          { l.marked.Store(true) }
func (l *countLogger) afterMark() int { return int(l.after.Load()) }
func (l *countLogger) entry() {
	if l.marked.Load() {
		l.after.Add(1)
	}
}
func (l *countLogger) Debugf(string, ...any) { l.entry() }
func (l *countLogger) Infof(string, ...any)  { l.entry() }
func (l *countLogger) Warnf(string, ...any)  { l.entry() }
func (l *countLogger) Errorf(string, ...any) { l.entry() }

// Under concurrent connections, every connection reported opened is reported
// closed exactly once, after it was opened, whether its client leaves or the
// server stops; a connection refused for MaxClients is never reported opened.
func TestServerConnectionLifecycle_PairsUnderLoad(t *testing.T) {
	m := &lifecycleMetrics{}
	const maxClients = 8
	ms, first := startCallbackServer(t, holdingOne(), func(c *ServerConfig) {
		c.Metrics = m
		c.MaxClients = maxClients
		c.Logger = NopLogger()
	})
	_ = first.Close()

	// Clients that come and go, more of them at once than MaxClients allows.
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.Dial("tcp", ms.Addr().String())
			if err != nil {
				return
			}
			defer func() { _ = c.Close() }()
			if _, err := c.Write(rawRequestFrame(1, 1, byte(FCReadHoldingRegisters), u16(0, 1))); err != nil {
				return
			}
			_ = c.SetReadDeadline(time.Now().Add(e2eWait))
			_, _ = io.ReadFull(c, make([]byte, 11))
		}()
	}
	wg.Wait()
	// Clients still connected when the server stops.
	for i := 0; i < 4; i++ {
		c := dialRaw(t, ms)
		sendReadRequest(t, c)
		_ = c.SetReadDeadline(time.Now().Add(e2eWait))
		if _, err := io.ReadFull(c, make([]byte, 11)); err != nil {
			t.Fatalf("connection %d: %v", i, err)
		}
	}
	if err := ms.Stop(); err != nil {
		t.Fatal(err)
	}

	open := map[string]bool{}
	seen := map[string]int{}
	for _, ev := range m.eventsSnapshot() {
		switch {
		case len(ev) > 7 && ev[:7] == "opened ":
			addr := ev[7:]
			addr = addr[:len(addr)-len(" role=")]
			if open[addr] || seen[addr] > 0 {
				t.Errorf("%s opened twice", addr)
			}
			open[addr] = true
			seen[addr]++
		case len(ev) > 7 && ev[:7] == "closed ":
			addr := ev[7:]
			if !open[addr] {
				t.Errorf("%s closed without being open", addr)
			}
			open[addr] = false
		}
	}
	for addr, still := range open {
		if still {
			t.Errorf("%s opened and never closed", addr)
		}
	}
	// One connection per served client: the first, the clients that got a
	// slot, and the four connected at Stop.
	if len(seen) < 1+4 {
		t.Errorf("%d connections reported opened", len(seen))
	}
	for _, r := range m.snapshot() {
		if seen[r.addr] > 0 {
			t.Errorf("%s was reported both %v and opened", r.addr, r.reason)
		}
	}
}

// ServerConfig stays comparable with ==, as it was up to v1.17.0: a field of a
// function type would make this fail to compile (gorelease: incompatible).
var _ = ServerConfig{} == ServerConfig{}

// A ClientFilter that is not a ClientFilterFunc is asked the same way.
type denyAll struct{}

func (denyAll) AllowClient(net.Addr) bool { return false }

func TestServerAllowClient_Interface(t *testing.T) {
	m := &lifecycleMetrics{}
	ms, conn := startCallbackServer(t, holdingOne(), func(c *ServerConfig) {
		c.Metrics = m
		c.AllowClient = denyAll{}
	})
	t.Cleanup(func() { _ = ms.Stop() })
	sendReadRequest(t, conn)
	wantClosedUnanswered(t, conn)
	wantRejected(t, &m.rejectMetrics, rejectedConn{conn.LocalAddr().String(), RejectNotAllowed})
}
