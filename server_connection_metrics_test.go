// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"net"
	"sync"
	"testing"
)

// ConnectionMetrics: the connections the server closes to keep within
// MaxClients are reported, with their address and the reason, once each.

// dialRaw opens a TCP connection to the server that sends nothing.
func dialRaw(t *testing.T, ms *Server) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", ms.tcpListener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// wantRejected waits until m has recorded len(want) rejections and checks them.
func wantRejected(t *testing.T, m *rejectMetrics, want ...rejectedConn) {
	t.Helper()
	e2eEventually(t, "the rejections to be reported", func() bool { return len(m.snapshot()) >= len(want) })
	got := m.snapshot()
	if len(got) != len(want) {
		t.Fatalf("rejections %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rejection %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// On tcp, every connection past MaxClients is closed at once and reported as
// RejectMaxClients, with its remote address.
func TestServerConnectionMetrics_MaxClients(t *testing.T) {
	m := &rejectMetrics{}
	ms, _ := startCallbackServer(t, &blockingHandler{fn: func(context.Context) {}}, func(c *ServerConfig) {
		c.MaxClients = 2
		c.Metrics = m
	})
	t.Cleanup(func() { _ = ms.Stop() })
	dialRaw(t, ms)
	e2eEventually(t, "two connections", func() bool { return e2eServerConns(ms) == 2 })

	third := dialRaw(t, ms)
	wantClosedUnanswered(t, third)
	fourth := dialRaw(t, ms)
	wantClosedUnanswered(t, fourth)
	wantRejected(t, m,
		rejectedConn{third.LocalAddr().String(), RejectMaxClients},
		rejectedConn{fourth.LocalAddr().String(), RejectMaxClients})
	if n := e2eServerConns(ms); n != 2 {
		t.Errorf("server connections = %d, want the 2 admitted", n)
	}
}

// On tcp+tls, a connection whose handshake has not completed is closed to make
// room for a newer one and reported as RejectTLSHandshakeEvicted; once every
// slot holds a connection that completed its handshake, a new one is refused
// and reported as RejectMaxClients.
func TestServerConnectionMetrics_TLSHandshakeEvicted(t *testing.T) {
	m := &rejectMetrics{}
	dev := e2eNewDevice()
	dev.setHolding(1, 0x0101)
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{noOpen: true, server: func(c *ServerConfig) {
		c.MaxClients = 1
		c.Metrics = m
	}})
	pending := dialRaw(t, p.server)
	e2eEventually(t, "the pending connection", func() bool { return p.conns() == 1 })

	// The client takes the slot of the connection that sends nothing.
	if err := p.client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if v, err := p.client.ReadHoldingRegister(context.Background(), e2eUnit, 1); err != nil || v != 0x0101 {
		t.Fatalf("request of the client that took the slot: %#04x, %v", v, err)
	}
	wantClosedUnanswered(t, pending)
	wantRejected(t, m, rejectedConn{pending.LocalAddr().String(), RejectTLSHandshakeEvicted})

	// The slot now holds a completed handshake: a new connection is refused.
	refused := dialRaw(t, p.server)
	wantClosedUnanswered(t, refused)
	wantRejected(t, m,
		rejectedConn{pending.LocalAddr().String(), RejectTLSHandshakeEvicted},
		rejectedConn{refused.LocalAddr().String(), RejectMaxClients})
}

// An OnConnectionRejected that does not return normally is logged at error
// level with its stack, and the accept loop goes on: the next connection is
// served once a slot is free.
func TestServerConnectionMetrics_HookThatDoesNotReturn(t *testing.T) {
	log := &e2eLogger{}
	m := &rejectMetrics{onRejected: func(string, RejectReason) { panic("rejection hook") }}
	dev := e2eNewDevice()
	dev.setHolding(1, 0x0101)
	p := e2eStart(t, "tcp", dev, e2eOpts{server: func(c *ServerConfig) {
		c.MaxClients = 1
		c.Metrics = m
		c.Logger = log
	}})
	e2eEventually(t, "the client's connection", func() bool { return p.conns() == 1 })
	refused := dialRaw(t, p.server)
	wantClosedUnanswered(t, refused)
	wantRejected(t, m, rejectedConn{refused.LocalAddr().String(), RejectMaxClients})
	e2eEventually(t, "the error-level log line", func() bool {
		return log.has("E ", "ConnectionMetrics.OnConnectionRejected for "+refused.LocalAddr().String()+" did not return normally: rejection hook", "goroutine ")
	})

	// The accept loop is still there: a slot freed is taken by a new client.
	if err := p.client.Close(); err != nil {
		t.Fatal(err)
	}
	e2eEventually(t, "the slot to be freed", func() bool { return p.conns() == 0 })
	c := p.newClient(nil)
	if err := c.Open(); err != nil {
		t.Fatalf("Open after the hook: %v", err)
	}
	if v, err := c.ReadHoldingRegister(context.Background(), e2eUnit, 1); err != nil || v != 0x0101 {
		t.Errorf("request after the hook: %#04x, %v", v, err)
	}
}

// A connection accepted while the server is stopping is closed but not
// reported: it was not refused for MaxClients.
func TestServerAccept_ConnectionDuringStopIsNotReported(t *testing.T) {
	m := &rejectMetrics{}
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Metrics: m, Logger: NopLogger()}, &fnHandler{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	// One connection, then net.ErrClosed. The server is not started, as when
	// Shutdown has run between Accept and the admission.
	ln := &scriptedListener{script: []error{nil}}
	run := &serverRun{listener: ln, wg: &sync.WaitGroup{}}
	run.ctx, run.cancel = context.WithCancel(context.Background())
	defer run.cancel()
	run.wg.Add(1)
	ms.acceptTCPClients(run)
	run.wg.Wait()

	if got := m.snapshot(); len(got) != 0 {
		t.Errorf("rejections %v, want none for a connection accepted while stopping", got)
	}
	ln.mu.Lock()
	peer := ln.peers[0]
	ln.mu.Unlock()
	if _, err := peer.Read(make([]byte, 1)); err == nil {
		t.Error("the connection accepted while stopping was not closed")
	}
}

func TestRejectReason_String(t *testing.T) {
	for r, want := range map[RejectReason]string{
		RejectMaxClients:          "max clients",
		RejectTLSHandshakeEvicted: "tls handshake evicted",
		0:                         "RejectReason(0)",
		200:                       "RejectReason(200)",
	} {
		if got := r.String(); got != want {
			t.Errorf("RejectReason(%d).String() = %q, want %q", uint8(r), got, want)
		}
	}
}
