// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/session"
)

// connRecorder records the connection identity reports of a client or a
// server: the detailed lifecycle and the payloads that carry a ConnectionID.
type connRecorder struct {
	countingServerMetrics
	mu      sync.Mutex
	server  []ServerConnectionEvent
	client  []ClientConnectionEvent
	hs      []TLSHandshakeInfo
	onEvent func(ServerConnectionEvent)
}

func (r *connRecorder) OnTimeout(uint8, FunctionCode, time.Duration) {}

func (r *connRecorder) OnServerConnection(ev ServerConnectionEvent) {
	r.mu.Lock()
	r.server = append(r.server, ev)
	fn := r.onEvent
	r.mu.Unlock()
	if fn != nil {
		fn(ev)
	}
}

func (r *connRecorder) OnClientConnection(ev ClientConnectionEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.client = append(r.client, ev)
}

func (r *connRecorder) OnTLSHandshake(info TLSHandshakeInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hs = append(r.hs, info)
}

func (r *connRecorder) snapshot() ([]ServerConnectionEvent, []ClientConnectionEvent, []TLSHandshakeInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ServerConnectionEvent(nil), r.server...),
		append([]ClientConnectionEvent(nil), r.client...),
		append([]TLSHandshakeInfo(nil), r.hs...)
}

// One tcp+tls connection carries the same ConnectionID in every report of
// its server (TLS handshake, opened, authorization, PDU tap, closed) and of
// its client (dial, TLS handshake, PDU tap, closed); the two ends have
// different identifiers.
func TestConnectionID_CorrelatesReports(t *testing.T) {
	sm, cm := &connRecorder{}, &connRecorder{}
	var mu sync.Mutex
	var authIDs, stapIDs, ctapIDs []ConnectionID
	p := e2eStart(t, "tcp+tls", e2eNewDevice(), e2eOpts{
		server: func(c *ServerConfig) {
			c.Metrics = sm
			c.Authorizer = RequestAuthorizerFunc(func(_ context.Context, ar *AuthorizationRequest) error {
				mu.Lock()
				defer mu.Unlock()
				authIDs = append(authIDs, ar.ConnectionID)
				return nil
			})
			c.PDUTap = ServerPDUTapFunc(func(_ context.Context, ex ServerPDUExchange) {
				mu.Lock()
				defer mu.Unlock()
				stapIDs = append(stapIDs, ex.ConnectionID)
			})
		},
		client: func(c *Config) {
			c.Metrics = cm
			c.PDUTap = PDUTapFunc(func(_ context.Context, ex PDUExchange) {
				mu.Lock()
				defer mu.Unlock()
				ctapIDs = append(ctapIDs, ex.ConnectionID)
			})
		},
	})
	for range 2 {
		if _, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1); err != nil {
			t.Fatalf("read: %v", err)
		}
	}
	if err := p.client.Close(); err != nil {
		t.Fatal(err)
	}
	e2eEventually(t, "the server's close", func() bool { evs, _, _ := sm.snapshot(); return len(evs) == 2 })

	sevs, _, shs := sm.snapshot()
	_, cevs, chs := cm.snapshot()
	if len(shs) != 1 || len(chs) != 1 {
		t.Fatalf("handshakes: server %d, client %d; want 1 each", len(shs), len(chs))
	}
	sid := shs[0].ConnectionID
	if sid == 0 {
		t.Fatal("server handshake without a ConnectionID")
	}
	opened, closed := sevs[0], sevs[1]
	if opened.Kind != ServerConnectionOpened || opened.ConnectionID != sid || opened.Role != "operator" ||
		opened.LocalAddr != p.hostPort || opened.RemoteAddr == "" {
		t.Errorf("opened %+v, want connection %d with its role and addresses", opened, sid)
	}
	if closed.Kind != ServerConnectionClosed || closed.ConnectionID != sid || closed.CloseReason != CloseClientClosed ||
		closed.Err == nil || closed.Age <= 0 || closed.RemoteAddr != opened.RemoteAddr {
		t.Errorf("closed %+v, want connection %d closed by the client", closed, sid)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, ids := range [][]ConnectionID{authIDs, stapIDs} {
		if len(ids) != 2 || ids[0] != sid || ids[1] != sid {
			t.Errorf("server ids %v, want [%d %d]", ids, sid, sid)
		}
	}

	cid := chs[0].ConnectionID
	if cid == 0 || cid == sid {
		t.Fatalf("client ConnectionID %d: want one of its own (server %d)", cid, sid)
	}
	if len(ctapIDs) != 2 || ctapIDs[0] != cid || ctapIDs[1] != cid {
		t.Errorf("client tap ids %v, want [%d %d]", ctapIDs, cid, cid)
	}
	if len(cevs) != 2 {
		t.Fatalf("client events %+v, want a dial and a close", cevs)
	}
	d, c := cevs[0], cevs[1]
	if d.Kind != ClientConnectionDialed || d.ConnectionID != cid || d.Origin != ConnectionOriginOpen || d.Err != nil ||
		d.Duration <= 0 || d.Pooled || !strings.HasSuffix(d.Target, ":"+portOf(p.hostPort)) {
		t.Errorf("dial %+v, want connection %d dialled by Open", d, cid)
	}
	if c.Kind != ClientConnectionClosed || c.ConnectionID != cid || c.Age <= 0 || c.Err != nil {
		t.Errorf("close %+v, want connection %d closed", c, cid)
	}
}

func portOf(hostPort string) string {
	_, port, _ := net.SplitHostPort(hostPort)
	return port
}

// Rejected connections are reported with an identifier of their own; one
// evicted during its TLS handshake has the identifier of that handshake.
func TestServerConnectionMetrics_Rejected(t *testing.T) {
	m := &connRecorder{}
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{noOpen: true, server: func(c *ServerConfig) {
		c.MaxClients = 1
		c.Metrics = m
	}})
	pending := dialRaw(t, p.server)
	e2eEventually(t, "the pending connection", func() bool { return p.conns() == 1 })
	if err := p.client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1); err != nil {
		t.Fatalf("read: %v", err)
	}
	refused := dialRaw(t, p.server)
	wantClosedUnanswered(t, refused)
	e2eEventually(t, "the reports", func() bool {
		evs, _, hs := m.snapshot()
		return len(evs) >= 3 && len(hs) >= 2
	})
	evs, _, hs := m.snapshot()
	var evicted, maxed, opened *ServerConnectionEvent
	for i := range evs {
		switch {
		case evs[i].Kind == ServerConnectionRejected && evs[i].RejectReason == RejectTLSHandshakeEvicted:
			evicted = &evs[i]
		case evs[i].Kind == ServerConnectionRejected && evs[i].RejectReason == RejectMaxClients:
			maxed = &evs[i]
		case evs[i].Kind == ServerConnectionOpened:
			opened = &evs[i]
		}
	}
	if evicted == nil || maxed == nil || opened == nil {
		t.Fatalf("events %+v: want an eviction, a refusal and an open", evs)
	}
	if evicted.RemoteAddr != pending.LocalAddr().String() || maxed.RemoteAddr != refused.LocalAddr().String() {
		t.Errorf("rejections %+v, %+v: want the pending and the refused connection", evicted, maxed)
	}
	var failedHS ConnectionID
	for _, h := range hs {
		if h.Err != nil && h.RemoteAddr == pending.LocalAddr().String() {
			failedHS = h.ConnectionID
		}
	}
	if failedHS == 0 || evicted.ConnectionID != failedHS {
		t.Errorf("evicted id %d, failed handshake id %d: want the same", evicted.ConnectionID, failedHS)
	}
	ids := map[ConnectionID]bool{evicted.ConnectionID: true, maxed.ConnectionID: true, opened.ConnectionID: true}
	if len(ids) != 3 || ids[0] {
		t.Errorf("ids %d, %d, %d: want three distinct, non-zero", evicted.ConnectionID, maxed.ConnectionID, opened.ConnectionID)
	}
	if maxed.LocalAddr != p.hostPort {
		t.Errorf("refused LocalAddr %q, want %q", maxed.LocalAddr, p.hostPort)
	}
}

// AllowClient refusals are reported too.
func TestServerConnectionMetrics_NotAllowed(t *testing.T) {
	m := &connRecorder{}
	p := e2eStart(t, "tcp", e2eNewDevice(), e2eOpts{noOpen: true, server: func(c *ServerConfig) {
		c.Metrics = m
		c.AllowClient = ClientFilterFunc(func(net.Addr) bool { return false })
	}})
	c := dialRaw(t, p.server)
	wantClosedUnanswered(t, c)
	e2eEventually(t, "the refusal", func() bool { evs, _, _ := m.snapshot(); return len(evs) == 1 })
	evs, _, _ := m.snapshot()
	if evs[0].Kind != ServerConnectionRejected || evs[0].RejectReason != RejectNotAllowed || evs[0].ConnectionID == 0 {
		t.Errorf("event %+v, want a not-allowed rejection with an id", evs[0])
	}
}

// An OnServerConnection or OnClientConnection that does not return normally
// is logged, and the server and the client go on.
func TestConnectionMetrics_PanicIsContained(t *testing.T) {
	slog, clog := &e2eLogger{}, &e2eLogger{}
	sm := &connRecorder{onEvent: func(ServerConnectionEvent) { panic("server connection hook") }}
	cm := &panicClientConn{}
	p := e2eStart(t, "tcp", e2eNewDevice(), e2eOpts{
		server: func(c *ServerConfig) { c.Metrics = sm; c.Logger = slog },
		client: func(c *Config) { c.Metrics = cm; c.Logger = clog },
	})
	if _, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1); err != nil {
		t.Fatalf("read after the panics: %v", err)
	}
	e2eEventually(t, "the server log line", func() bool {
		return slog.has("E ", "ServerConnectionMetrics.OnServerConnection (opened) for", "did not return normally: server connection hook")
	})
	if !clog.has("E ", "ClientConnectionMetrics.OnClientConnection did not return normally: client connection hook") {
		t.Error("the client's panic was not logged")
	}
}

type panicClientConn struct{ countingServerMetricsClient }

func (*panicClientConn) OnClientConnection(ClientConnectionEvent) { panic("client connection hook") }

// Stop called from OnServerConnection (opened) returns, and nothing more is
// reported for the connection.
func TestServerConnectionMetrics_StopFromCallback(t *testing.T) {
	stopped := make(chan error, 1)
	var ms *Server
	m := &connRecorder{}
	m.onEvent = func(ev ServerConnectionEvent) {
		if ev.Kind == ServerConnectionOpened {
			stopped <- ms.Stop()
		}
	}
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp", dev, e2eOpts{noOpen: true, server: func(c *ServerConfig) { c.Metrics = m }})
	ms = p.server
	c := p.newClient(func(conf *Config) { conf.Timeout = time.Second })
	if err := c.Open(); err == nil {
		_, _ = c.ReadHoldingRegister(t.Context(), e2eUnit, 1)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(e2eWait):
		t.Fatal("Stop from OnServerConnection did not return")
	}
	e2eEventually(t, "the connection to be dropped", func() bool { return p.conns() == 0 })
	time.Sleep(20 * time.Millisecond)
	if evs, _, _ := m.snapshot(); len(evs) != 1 {
		t.Errorf("events %+v, want the open alone", evs)
	}
	if dev.callCount() != 0 {
		t.Error("the connection was served after Stop")
	}
}

// A pooled client reports its pre-warmed and on-demand dials, and closes
// them all on Close; a failed dial is reported with its error.
func TestClientConnectionMetrics_Pool(t *testing.T) {
	m := &connRecorder{}
	gate := make(chan struct{})
	h := &blockingHandler{fn: func(context.Context) { <-gate }}
	p := e2eStart(t, "tcp", h, e2eOpts{noOpen: true})
	ms, addr := p.server, p.hostPort
	c := p.newClient(func(conf *Config) { conf.MinConns, conf.MaxConns, conf.Metrics = 1, 3, m })
	if err := c.Open(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() { _, _ = c.ReadHoldingRegister(context.Background(), 1, 0) })
	}
	e2eEventually(t, "three connections", func() bool { return e2eServerConns(ms) == 3 })
	close(gate)
	wg.Wait()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	_, evs, _ := m.snapshot()
	var open, demand, closed int
	ids := map[ConnectionID]bool{}
	for _, ev := range evs {
		if !ev.Pooled || ev.ConnectionID == 0 || ev.Target != addr {
			t.Errorf("event %+v, want pooled, with an id, to %s", ev, addr)
		}
		switch {
		case ev.Kind == ClientConnectionDialed && ev.Origin == ConnectionOriginOpen:
			open++
			ids[ev.ConnectionID] = true
		case ev.Kind == ClientConnectionDialed && ev.Origin == ConnectionOriginOnDemand:
			demand++
			ids[ev.ConnectionID] = true
		case ev.Kind == ClientConnectionClosed:
			closed++
			if !ids[ev.ConnectionID] {
				t.Errorf("closed %d was not dialled", ev.ConnectionID)
			}
		default:
			t.Errorf("unexpected event %+v", ev)
		}
	}
	if open != 1 || demand != 2 || closed != 3 || len(ids) != 3 {
		t.Errorf("events %+v: want 1 pre-warmed, 2 on-demand dials, 3 closes", evs)
	}

	// A dial that fails.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	_ = ln.Close()
	fm := &connRecorder{}
	fc, err := New(Config{URL: "tcp://" + dead, Timeout: time.Second, Metrics: fm, Logger: NopLogger()})
	if err != nil {
		t.Fatal(err)
	}
	openErr := fc.Open()
	if openErr == nil {
		t.Fatal("Open to a closed port succeeded")
	}
	_, fevs, _ := fm.snapshot()
	if len(fevs) != 1 || fevs[0].Kind != ClientConnectionDialed || fevs[0].Err == nil || fevs[0].ConnectionID == 0 ||
		!errors.Is(openErr, fevs[0].Err) {
		t.Errorf("events %+v, want the failed dial with Open's error (%v)", fevs, openErr)
	}
}

func TestConnectionEnumStrings(t *testing.T) {
	for _, tc := range []struct {
		got, want string
	}{
		{ConnectionID(42).String(), "42"},
		{ConnectionID(0).String(), "0"},
		{ServerConnectionRejected.String(), "rejected"},
		{ServerConnectionOpened.String(), "opened"},
		{ServerConnectionClosed.String(), "closed"},
		{ServerConnectionEventKind(9).String(), "ServerConnectionEventKind(9)"},
		{ClientConnectionDialed.String(), "dialed"},
		{ClientConnectionDiscarded.String(), "discarded"},
		{ClientConnectionClosed.String(), "closed"},
		{ClientConnectionEventKind(9).String(), "ClientConnectionEventKind(9)"},
		{ConnectionOriginOpen.String(), "open"},
		{ConnectionOriginOnDemand.String(), "on demand"},
		{ConnectionOriginRedial.String(), "redial"},
		{ConnectionOrigin(9).String(), "ConnectionOrigin(9)"},
		{DiscardTransportError.String(), "transport error"},
		{DiscardSuspectResponse.String(), "suspect response"},
		{DiscardRetry.String(), "retry"},
		{DiscardOpenFailed.String(), "open failed"},
		{DiscardReason(9).String(), "DiscardReason(9)"},
		// The public values are the session's, converted.
		{ConnectionOrigin(session.OriginOpen).String(), "open"},
		{ConnectionOrigin(session.OriginOnDemand).String(), "on demand"},
		{ConnectionOrigin(session.OriginRedial).String(), "redial"},
		{DiscardReason(session.DiscardTransportError).String(), "transport error"},
		{DiscardReason(session.DiscardSuspectResponse).String(), "suspect response"},
		{DiscardReason(session.DiscardRetry).String(), "retry"},
		{DiscardReason(session.DiscardOpenFailed).String(), "open failed"},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
}
