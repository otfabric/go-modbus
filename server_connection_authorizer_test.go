// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// connAuthMetrics records, in order, the reports of a tcp+tls connection that
// ServerConfig.ConnectionAuthorizer sits between: the handshake, the
// rejection (both interfaces) and the lifecycle.
type connAuthMetrics struct {
	countingServerMetrics
	mu        sync.Mutex
	events    []string
	hs        []TLSHandshakeInfo
	conn      []ServerConnectionEvent
	rejected  []rejectedConn
	onReject  func()
	onConnEvt func(ServerConnectionEvent)
}

func (m *connAuthMetrics) OnTimeout(uint8, FunctionCode, time.Duration) {}

func (m *connAuthMetrics) add(ev string) {
	m.mu.Lock()
	m.events = append(m.events, ev)
	m.mu.Unlock()
}

func (m *connAuthMetrics) OnTLSHandshake(info TLSHandshakeInfo) {
	m.mu.Lock()
	m.hs = append(m.hs, info)
	m.events = append(m.events, "handshake")
	m.mu.Unlock()
}

func (m *connAuthMetrics) OnConnectionRejected(addr string, reason RejectReason) {
	m.mu.Lock()
	m.rejected = append(m.rejected, rejectedConn{addr, reason})
	m.events = append(m.events, "rejected "+reason.String())
	fn := m.onReject
	m.mu.Unlock()
	if fn != nil {
		fn()
	}
}

func (m *connAuthMetrics) OnConnectionOpened(string, string) { m.add("opened") }
func (m *connAuthMetrics) OnConnectionClosed(string)         { m.add("closed") }

func (m *connAuthMetrics) OnServerConnection(ev ServerConnectionEvent) {
	m.mu.Lock()
	m.conn = append(m.conn, ev)
	m.events = append(m.events, "conn "+ev.Kind.String())
	fn := m.onConnEvt
	m.mu.Unlock()
	if fn != nil {
		fn(ev)
	}
}

func (m *connAuthMetrics) snapshot() ([]string, []TLSHandshakeInfo, []ServerConnectionEvent, []rejectedConn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.events...), append([]TLSHandshakeInfo(nil), m.hs...),
		append([]ServerConnectionEvent(nil), m.conn...), append([]rejectedConn(nil), m.rejected...)
}

// connAuthRecorder is a ConnectionAuthorizer that records what it is asked
// (as an event of m, when set) and decides with decide.
type connAuthRecorder struct {
	m      *connAuthMetrics
	mu     sync.Mutex
	asked  []TLSHandshakeInfo
	decide func(TLSHandshakeInfo) error
}

func (a *connAuthRecorder) AuthorizeConnection(_ context.Context, info TLSHandshakeInfo) error {
	a.mu.Lock()
	a.asked = append(a.asked, info)
	a.mu.Unlock()
	if a.m != nil {
		a.m.add("authorize")
	}
	if a.decide == nil {
		return nil
	}
	return a.decide(info)
}

func (a *connAuthRecorder) take() []TLSHandshakeInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]TLSHandshakeInfo(nil), a.asked...)
}

// An allowed connection: the authorizer runs after the handshake is reported
// and before the connection is opened, with the verified facts of the
// handshake, and the connection is served.
func TestConnectionAuthorizer_Allows(t *testing.T) {
	pki := e2ePKI(t)
	m := &connAuthMetrics{}
	a := &connAuthRecorder{m: m}
	dev := e2eNewDevice()
	dev.setHolding(1, 0x0101)
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{server: func(c *ServerConfig) {
		c.Metrics = m
		c.ConnectionAuthorizer = a
	}})
	if v, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1); err != nil || v != 0x0101 {
		t.Fatalf("read: %#04x, %v", v, err)
	}
	events, hs, _, _ := m.snapshot()
	want := []string{"handshake", "authorize", "opened", "conn opened"}
	if len(events) < len(want) || strings.Join(events[:len(want)], ",") != strings.Join(want, ",") {
		t.Fatalf("events %v, want %v first", events, want)
	}
	asked := a.take()
	if len(asked) != 1 {
		t.Fatalf("asked %d times, want 1", len(asked))
	}
	info := asked[0]
	if info.ConnectionID == 0 || info.ConnectionID != hs[0].ConnectionID {
		t.Errorf("ConnectionID %v, want the handshake's %v", info.ConnectionID, hs[0].ConnectionID)
	}
	if info.Err != nil || !info.Verified || info.Role != "operator" || info.RoleErr != nil {
		t.Errorf("info %+v: want verified, role operator", info)
	}
	if info.RemoteAddr == "" || info.LocalAddr != p.hostPort || info.Version == 0 || info.Duration <= 0 {
		t.Errorf("info %+v: want the addresses and the negotiated parameters", info)
	}
	if len(info.PeerCertificates) != 1 || info.PeerCertificates[0].Subject.CommonName != "e2e client" {
		t.Errorf("peer certificates %v, want the client's", info.PeerCertificates)
	}
	if len(info.VerifiedChains) != 1 || len(info.VerifiedChains[0]) != 2 ||
		info.VerifiedChains[0][0].Subject.CommonName != "e2e client" ||
		!info.VerifiedChains[0][1].Equal(pki.ca.cert) {
		t.Errorf("verified chains %v, want client -> CA", info.VerifiedChains)
	}
}

// A refused connection is closed before it is served and reported as
// RejectNotAuthorized, with an error that matches ErrNotAuthorized and wraps
// the authorizer's; it is never opened or closed, and frees its slot.
func TestConnectionAuthorizer_Refuses(t *testing.T) {
	errDenied := errors.New("issuer not pinned")
	m := &connAuthMetrics{}
	a := &connAuthRecorder{m: m, decide: func(TLSHandshakeInfo) error { return errDenied }}
	log := &e2eLogger{}
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{noOpen: true, server: func(c *ServerConfig) {
		c.Metrics = m
		c.ConnectionAuthorizer = a
		c.Logger = log
	}, client: func(c *Config) { c.Timeout = time.Second }})
	if err := p.client.Open(); err == nil {
		if _, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1); err == nil {
			t.Fatal("a refused connection was served")
		}
	}
	e2eEventually(t, "the rejection", func() bool { _, _, c, _ := m.snapshot(); return len(c) == 1 })
	e2eEventually(t, "the slot to be freed", func() bool { return p.conns() == 0 })
	events, hs, conn, rejected := m.snapshot()
	want := []string{"handshake", "authorize", "rejected not authorized", "conn rejected"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Errorf("events %v, want %v", events, want)
	}
	if len(rejected) != 1 || rejected[0].reason != RejectNotAuthorized || rejected[0].addr != hs[0].RemoteAddr {
		t.Errorf("rejected %v, want %s as not authorized", rejected, hs[0].RemoteAddr)
	}
	ev := conn[0]
	if ev.Kind != ServerConnectionRejected || ev.RejectReason != RejectNotAuthorized || ev.ConnectionID != hs[0].ConnectionID {
		t.Errorf("event %+v, want rejected, not authorized, the handshake's id", ev)
	}
	if !errors.Is(ev.Err, ErrNotAuthorized) || !errors.Is(ev.Err, errDenied) {
		t.Errorf("Err %v: want it to match ErrNotAuthorized and the authorizer's error", ev.Err)
	}
	if errors.Is(ev.Err, ErrIllegalFunction) {
		t.Errorf("Err %v matches an exception error", ev.Err)
	}
	if ev.Err.Error() != "modbus: connection not authorized: issuer not pinned" {
		t.Errorf("Err %q", ev.Err)
	}
	if ev.Role != "operator" || ev.RemoteAddr == "" || ev.LocalAddr != p.hostPort {
		t.Errorf("event %+v: want the role and the addresses", ev)
	}
	if dev.callCount() != 0 {
		t.Error("a request of a refused connection reached the handler")
	}
	if !log.has("W ", "rejecting ", "connection not authorized: issuer not pinned") {
		t.Error("the rejection was not logged")
	}
}

// An authorizer that does not return normally refuses the connection: it
// fails closed, and is logged with its stack.
func TestConnectionAuthorizer_PanicRefuses(t *testing.T) {
	m := &connAuthMetrics{}
	log := &e2eLogger{}
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{noOpen: true, server: func(c *ServerConfig) {
		c.Metrics = m
		c.Logger = log
		c.ConnectionAuthorizer = ConnectionAuthorizerFunc(func(context.Context, TLSHandshakeInfo) error {
			panic("authorizer boom")
		})
	}, client: func(c *Config) { c.Timeout = time.Second }})
	if err := p.client.Open(); err == nil {
		if _, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1); err == nil {
			t.Fatal("a connection was served past a failed authorizer")
		}
	}
	e2eEventually(t, "the rejection", func() bool { _, _, c, _ := m.snapshot(); return len(c) == 1 })
	_, _, conn, _ := m.snapshot()
	if conn[0].RejectReason != RejectNotAuthorized || !errors.Is(conn[0].Err, ErrNotAuthorized) ||
		!errors.Is(conn[0].Err, errConnectionAuthorizerFailed) {
		t.Errorf("event %+v: want not authorized, the authorizer failed", conn[0])
	}
	if dev.callCount() != 0 {
		t.Error("a request reached the handler past a failed authorizer")
	}
	e2eEventually(t, "the log line", func() bool {
		return log.has("E ", "ConnectionAuthorizer.AuthorizeConnection for ", "did not return normally: authorizer boom", "goroutine ")
	})
}

// Stop called from the authorizer returns; the connection is closed without
// being served, and nothing more is reported for it.
func TestConnectionAuthorizer_StopFromAuthorizer(t *testing.T) {
	for _, allow := range []bool{true, false} {
		name := "refuses"
		if allow {
			name = "allows"
		}
		t.Run(name, func(t *testing.T) {
			stopped := make(chan error, 1)
			var ms *Server
			m := &connAuthMetrics{}
			dev := e2eNewDevice()
			p := e2eStart(t, "tcp+tls", dev, e2eOpts{noOpen: true, server: func(c *ServerConfig) {
				c.Metrics = m
				c.ConnectionAuthorizer = ConnectionAuthorizerFunc(func(context.Context, TLSHandshakeInfo) error {
					stopped <- ms.Stop()
					if allow {
						return nil
					}
					return errors.New("no")
				})
			}, client: func(c *Config) { c.Timeout = time.Second }})
			ms = p.server
			if err := p.client.Open(); err == nil {
				if _, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1); err == nil {
					t.Error("the connection was served after Stop")
				}
			}
			select {
			case err := <-stopped:
				if err != nil {
					t.Fatalf("Stop: %v", err)
				}
			case <-time.After(e2eWait):
				t.Fatal("Stop from the authorizer did not return")
			}
			time.Sleep(50 * time.Millisecond)
			events, _, _, _ := m.snapshot()
			if strings.Join(events, ",") != "handshake" {
				t.Errorf("events %v, want only the handshake", events)
			}
			if dev.callCount() != 0 {
				t.Error("a request reached the handler after Stop")
			}
		})
	}
}

// Shutdown called from OnConnectionRejected for RejectNotAuthorized, on the
// connection's goroutine, returns; ServerConnectionMetrics is then not told.
func TestConnectionAuthorizer_ShutdownFromRejected(t *testing.T) {
	stopped := make(chan error, 1)
	var ms *Server
	m := &connAuthMetrics{onReject: func() { stopped <- ms.Shutdown(context.Background()) }}
	p := e2eStart(t, "tcp+tls", e2eNewDevice(), e2eOpts{noOpen: true, server: func(c *ServerConfig) {
		c.Metrics = m
		c.ConnectionAuthorizer = ConnectionAuthorizerFunc(func(context.Context, TLSHandshakeInfo) error {
			return errors.New("no")
		})
	}, client: func(c *Config) { c.Timeout = time.Second }})
	ms = p.server
	_ = p.client.Open()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(e2eWait):
		t.Fatal("Shutdown from OnConnectionRejected did not return")
	}
	time.Sleep(50 * time.Millisecond)
	if _, _, conn, _ := m.snapshot(); len(conn) != 0 {
		t.Errorf("ServerConnectionMetrics told %v after the Shutdown", conn)
	}
}

// Shutdown called from OnServerConnection for the rejection returns.
func TestConnectionAuthorizer_ShutdownFromServerConnection(t *testing.T) {
	stopped := make(chan error, 1)
	var ms *Server
	m := &connAuthMetrics{onConnEvt: func(ServerConnectionEvent) { stopped <- ms.Shutdown(context.Background()) }}
	p := e2eStart(t, "tcp+tls", e2eNewDevice(), e2eOpts{noOpen: true, server: func(c *ServerConfig) {
		c.Metrics = m
		c.ConnectionAuthorizer = ConnectionAuthorizerFunc(func(context.Context, TLSHandshakeInfo) error {
			return errors.New("no")
		})
	}, client: func(c *Config) { c.Timeout = time.Second }})
	ms = p.server
	_ = p.client.Open()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(e2eWait):
		t.Fatal("Shutdown from OnServerConnection did not return")
	}
}

// The info is the authorizer's: changing it changes neither what
// TLSHandshakeMetrics was told nor the next connection's.
func TestConnectionAuthorizer_InfoIsACopy(t *testing.T) {
	m := &connAuthMetrics{}
	var seenMu sync.Mutex
	var seen []string
	a := &connAuthRecorder{decide: func(info TLSHandshakeInfo) error {
		seenMu.Lock()
		seen = append(seen, info.PeerCertificates[0].Subject.CommonName)
		seenMu.Unlock()
		for _, c := range info.PeerCertificates {
			c.Subject.CommonName = "tampered"
		}
		for _, chain := range info.VerifiedChains {
			for _, c := range chain {
				c.Subject.CommonName = "tampered"
			}
		}
		return nil
	}}
	p := e2eStart(t, "tcp+tls", e2eNewDevice(), e2eOpts{server: func(c *ServerConfig) {
		c.Metrics = m
		c.ConnectionAuthorizer = a
	}})
	if _, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1); err != nil {
		t.Fatalf("read: %v", err)
	}
	c2 := p.newClient(nil)
	if err := c2.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := c2.ReadHoldingRegister(t.Context(), e2eUnit, 1); err != nil {
		t.Fatalf("read of the second client: %v", err)
	}
	_, hs, _, _ := m.snapshot()
	if hs[0].PeerCertificates[0].Subject.CommonName != "e2e client" ||
		hs[0].VerifiedChains[0][0].Subject.CommonName != "e2e client" {
		t.Error("the authorizer changed what TLSHandshakeMetrics was told")
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	if len(seen) != 2 || seen[1] != "e2e client" {
		t.Errorf("the authorizer saw %v: its change reached the next connection", seen)
	}
}

// Connections that are allowed and refused at the same time: every one is
// either opened or rejected, once. Run with -race.
func TestConnectionAuthorizer_Concurrent(t *testing.T) {
	m := &connAuthMetrics{}
	var n atomic.Int32
	p := e2eStart(t, "tcp+tls", e2eNewDevice(), e2eOpts{noOpen: true, server: func(c *ServerConfig) {
		c.Metrics = m
		c.MaxClients = 32
		c.ConnectionAuthorizer = ConnectionAuthorizerFunc(func(context.Context, TLSHandshakeInfo) error {
			if n.Add(1)%2 == 0 {
				return errors.New("every other one")
			}
			return nil
		})
	}, client: func(c *Config) { c.Timeout = time.Second }})
	const clients = 16
	var wg sync.WaitGroup
	for range clients {
		c := p.newClient(nil)
		wg.Go(func() {
			if err := c.Open(); err == nil {
				_, _ = c.ReadHoldingRegister(context.Background(), e2eUnit, 1)
				_ = c.Close()
			}
		})
	}
	wg.Wait()
	e2eEventually(t, "every connection to end", func() bool {
		_, _, conn, _ := m.snapshot()
		var rejected, closed int
		for _, ev := range conn {
			switch ev.Kind {
			case ServerConnectionRejected:
				rejected++
			case ServerConnectionClosed:
				closed++
			case ServerConnectionOpened:
			}
		}
		return rejected+closed == clients
	})
	_, _, conn, _ := m.snapshot()
	seen := map[ConnectionID]ServerConnectionEventKind{}
	for _, ev := range conn {
		prev, ok := seen[ev.ConnectionID]
		switch {
		case ev.Kind == ServerConnectionOpened && ok,
			ev.Kind == ServerConnectionRejected && ok,
			ev.Kind == ServerConnectionClosed && prev != ServerConnectionOpened:
			t.Errorf("connection %v: %v after %v", ev.ConnectionID, ev.Kind, prev)
		}
		seen[ev.ConnectionID] = ev.Kind
	}
}

// ConnectionAuthorizer needs tcp+tls: on tcp it is a configuration error.
func TestConnectionAuthorizer_RequiresTLS(t *testing.T) {
	_, err := NewServer(&ServerConfig{
		URL: "tcp://127.0.0.1:0",
		ConnectionAuthorizer: ConnectionAuthorizerFunc(func(context.Context, TLSHandshakeInfo) error {
			return nil
		}),
	}, &fnHandler{})
	if !errors.Is(err, ErrConfigurationError) || !strings.Contains(err.Error(), "ConnectionAuthorizer") {
		t.Errorf("NewServer: %v, want a configuration error naming ConnectionAuthorizer", err)
	}
}
