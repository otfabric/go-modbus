// SPDX-License-Identifier: MIT

package modbus

import (
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
		c.AllowClient = func(remote net.Addr) bool {
			askedMu.Lock()
			asked = append(asked, remote.String())
			askedMu.Unlock()
			return !refuse.Load()
		}
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
		c.AllowClient = func(net.Addr) bool {
			if calls.Add(1) == 1 {
				panic("allow-list unavailable")
			}
			return true
		}
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
