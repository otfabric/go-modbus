// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/secevent"
)

// evCollector is a secevent.Sink that keeps what it receives.
type evCollector struct {
	mu sync.Mutex
	ev []secevent.Event
}

func (c *evCollector) OnEvent(e secevent.Event) {
	c.mu.Lock()
	c.ev = append(c.ev, e)
	c.mu.Unlock()
}

func (c *evCollector) snapshot() []secevent.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]secevent.Event(nil), c.ev...)
}

func (c *evCollector) kinds() []secevent.Kind {
	var out []secevent.Kind
	for _, e := range c.snapshot() {
		out = append(out, e.Kind)
	}
	return out
}

// wait returns the first event of kind, once there is one.
func (c *evCollector) wait(t *testing.T, kind secevent.Kind) secevent.Event {
	t.Helper()
	var got secevent.Event
	e2eEventually(t, string(kind), func() bool {
		for _, e := range c.snapshot() {
			if e.Kind == kind {
				got = e
				return true
			}
		}
		return false
	})
	return got
}

func (c *evCollector) count(kind secevent.Kind) int {
	n := 0
	for _, e := range c.snapshot() {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// eventsOn is a ServerConfig modifier that reports to c without merging, so
// that every occurrence is an event of its own.
func eventsOn(c *evCollector) func(*ServerConfig) {
	return func(conf *ServerConfig) {
		conf.Security = &SecurityPolicy{Policy: secevent.Policy{Sink: c, AggregateWindow: -1}}
	}
}

func chain(muts ...func(*ServerConfig)) func(*ServerConfig) {
	return func(c *ServerConfig) {
		for _, m := range muts {
			m(c)
		}
	}
}

// A served connection is reported accepted and closed, with its peer, a
// connection id that ties the events together, and the reason it ended.
func TestServerEvents_AcceptedServedClosed(t *testing.T) {
	c := &evCollector{}
	ms, conn := startCallbackServer(t, holdingOne(), eventsOn(c))
	t.Cleanup(func() { _ = ms.Stop() })
	sendReadRequest(t, conn)
	_ = conn.SetReadDeadline(time.Now().Add(e2eWait))
	if _, err := io.ReadFull(conn, make([]byte, 11)); err != nil {
		t.Fatalf("response: %v", err)
	}
	_ = conn.Close()
	closed := c.wait(t, secevent.KindConnClosed)
	acc := c.wait(t, secevent.KindConnAccepted)

	if acc.ConnID == 0 || acc.ConnID != closed.ConnID {
		t.Errorf("connection ids %d and %d should be the same, non-zero", acc.ConnID, closed.ConnID)
	}
	if acc.Protocol != "modbus" || acc.Transport != "tcp" || acc.Role != "server" || acc.Severity != secevent.SeverityInfo {
		t.Errorf("accepted: %+v", acc)
	}
	remote := addrPortOf(conn.LocalAddr())
	if acc.Peer.RemoteAddr != remote || acc.Peer.Network != "tcp" || acc.Peer.ConnectedAt.IsZero() ||
		acc.Peer.LocalAddr != addrPortOf(conn.RemoteAddr()) || acc.Local != conn.RemoteAddr().String() {
		t.Errorf("accepted peer %+v, want remote %v", acc.Peer, remote)
	}
	if closed.Action != secevent.ActionObserved || closed.Detail == "" {
		t.Errorf("closed: %+v", closed)
	}
	if c.count(secevent.KindConnNoRequest) != 0 {
		t.Errorf("a connection that sent a request was reported as having none: %v", c.kinds())
	}
	st := ms.SecurityStats()
	if st.Events[secevent.KindConnAccepted] != 1 || st.Events[secevent.KindConnClosed] != 1 {
		t.Errorf("stats: %+v", st)
	}
}

// A connection that ends without a request is reported as such, once, and
// then closed.
func TestServerEvents_NoRequest(t *testing.T) {
	c := &evCollector{}
	ms, conn := startCallbackServer(t, holdingOne(), eventsOn(c))
	t.Cleanup(func() { _ = ms.Stop() })
	_ = conn.Close()
	e := c.wait(t, secevent.KindConnNoRequest)
	c.wait(t, secevent.KindConnClosed)
	if e.Severity != secevent.SeverityNotice || e.ConnID == 0 || e.Technique == "" {
		t.Errorf("no_request: %+v", e)
	}
	if got := c.kinds(); len(got) != 3 || got[1] != secevent.KindConnNoRequest || got[2] != secevent.KindConnClosed {
		t.Errorf("events %v, want accepted, no_request, closed", got)
	}
}

// AllowClient refusing a connection is reported after its accept, with the
// same connection id, and nothing is reported as served.
func TestServerEvents_Refused(t *testing.T) {
	c := &evCollector{}
	ms, conn := startCallbackServer(t, holdingOne(), chain(eventsOn(c), func(conf *ServerConfig) {
		conf.AllowClient = ClientFilterFunc(func(net.Addr) bool { return false })
	}))
	t.Cleanup(func() { _ = ms.Stop() })
	wantClosedUnanswered(t, conn)
	e := c.wait(t, secevent.KindConnUnexpected)
	acc := c.wait(t, secevent.KindConnAccepted)
	if e.Action != secevent.ActionRejected || e.ConnID != acc.ConnID || e.Severity != secevent.SeverityWarning {
		t.Errorf("unexpected: %+v (accepted %+v)", e, acc)
	}
	if c.count(secevent.KindConnClosed) != 0 {
		t.Errorf("a refused connection was reported closed: %v", c.kinds())
	}
}

// A connection refused for want of a MaxClients slot is reported.
func TestServerEvents_RejectedLimit(t *testing.T) {
	c := &evCollector{}
	ms, first := startCallbackServer(t, holdingOne(), chain(eventsOn(c), func(conf *ServerConfig) { conf.MaxClients = 1 }))
	t.Cleanup(func() { _ = ms.Stop() })
	sendReadRequest(t, first)
	_ = first.SetReadDeadline(time.Now().Add(e2eWait))
	if _, err := io.ReadFull(first, make([]byte, 11)); err != nil {
		t.Fatal(err)
	}
	second := dialRaw(t, ms)
	wantClosedUnanswered(t, second)
	e := c.wait(t, secevent.KindConnRejectedLimit)
	if e.Action != secevent.ActionRejected || e.Detail == "" || e.Peer.RemoteAddr != addrPortOf(second.LocalAddr()) {
		t.Errorf("rejected_limit: %+v", e)
	}
}

// The idle timeout is reported as a connection that never sent, or as an idle
// one that had.
func TestServerEvents_IdleTimeout(t *testing.T) {
	c := &evCollector{}
	ms, silent := startCallbackServer(t, holdingOne(), chain(eventsOn(c), func(conf *ServerConfig) { conf.Timeout = 150 * time.Millisecond }))
	t.Cleanup(func() { _ = ms.Stop() })
	_ = silent
	e := c.wait(t, secevent.KindConnNoRequest)
	if e.Err == nil || !errors.Is(e.Err, os.ErrDeadlineExceeded) {
		t.Errorf("no_request after idling: %+v", e)
	}

	talker := dialRaw(t, ms)
	sendReadRequest(t, talker)
	if e.Action != secevent.ActionDisconnected {
		t.Errorf("no_request after idling: action %v, want disconnected", e.Action)
	}
	idle := c.wait(t, secevent.KindConnIdleTimeout)
	if idle.Action != secevent.ActionDisconnected || idle.Severity != secevent.SeverityInfo {
		t.Errorf("idle_timeout: %+v", idle)
	}
}

// The close reasons that say something about the peer become events of their
// own, before conn.closed.
func TestServerEvents_ProtocolViolations(t *testing.T) {
	c := &evCollector{}
	ms, bad := startCallbackServer(t, holdingOne(), eventsOn(c))
	t.Cleanup(func() { _ = ms.Stop() })
	// protocol identifier 1: the MBAP header cannot be framed
	if _, err := bad.Write([]byte{0, 1, 0, 1, 0, 6, 1, 3, 0, 0, 0, 1}); err != nil {
		t.Fatal(err)
	}
	e := c.wait(t, secevent.KindProtoBadMBAP)
	if e.Action != secevent.ActionDisconnected || !errors.Is(e.Err, ErrUnknownProtocolID) {
		t.Errorf("bad_mbap: %+v", e)
	}

	// FC03 with a PDU of two bytes instead of four: a protocol error
	short := dialRaw(t, ms)
	if _, err := short.Write(rawRequestFrame(1, 1, byte(FCReadHoldingRegisters), []byte{0, 1})); err != nil {
		t.Fatal(err)
	}
	p := c.wait(t, secevent.KindProtoBadPDU)
	if p.Action != secevent.ActionDisconnected || !errors.Is(p.Err, ErrProtocolError) {
		t.Errorf("bad_pdu: %+v", p)
	}
	if c.count(secevent.KindConnClosed) < 2 {
		e2eEventually(t, "both closes", func() bool { return c.count(secevent.KindConnClosed) == 2 })
	}
}

// ctxHandler lets a test see the context a handler receives.
type ctxHandler struct {
	fnHandler
	onRead func(ctx context.Context, req *HoldingRegistersRequest)
}

func (h *ctxHandler) HandleHoldingRegisters(ctx context.Context, req *HoldingRegistersRequest) ([]uint16, error) {
	h.onRead(ctx, req)
	return []uint16{1}, nil
}

// A handler reaches the peer through its context and can report events of its
// own, which carry the connection's identity.
func TestServerEvents_HandlerContext(t *testing.T) {
	c := &evCollector{}
	type seen struct {
		peer     secevent.PeerInfo
		hasPeer  bool
		reported bool
	}
	got := make(chan seen, 1)
	h := &ctxHandler{onRead: func(ctx context.Context, req *HoldingRegistersRequest) {
		var s seen
		s.peer, s.hasPeer = secevent.PeerFromContext(ctx)
		s.reported = secevent.Report(ctx, secevent.Event{
			Kind: "app.write_denied", Severity: secevent.SeverityWarning, Detail: "register is read-only",
			Fields: secevent.FieldUnitID | secevent.FieldAddress, UnitID: req.UnitID, Address: req.Addr,
		})
		got <- s
	}}
	ms, conn := startCallbackServer(t, h, eventsOn(c))
	t.Cleanup(func() { _ = ms.Stop() })
	sendReadRequest(t, conn)
	s := <-got
	if !s.hasPeer || !s.reported || s.peer.RemoteAddr != addrPortOf(conn.LocalAddr()) {
		t.Fatalf("handler saw %+v", s)
	}
	e := c.wait(t, "app.write_denied")
	acc := c.wait(t, secevent.KindConnAccepted)
	if e.ConnID != acc.ConnID || e.Peer.RemoteAddr != s.peer.RemoteAddr || e.Transport != "tcp" || !e.Has(secevent.FieldUnitID|secevent.FieldAddress) {
		t.Errorf("handler event %+v, accepted %+v", e, acc)
	}
}

// Without a security policy the context carries nothing and Report does
// nothing.
func TestServerEvents_NoPolicyNoReporter(t *testing.T) {
	got := make(chan bool, 1)
	h := &ctxHandler{onRead: func(ctx context.Context, _ *HoldingRegistersRequest) {
		_, ok := secevent.PeerFromContext(ctx)
		got <- ok || secevent.Report(ctx, secevent.Event{Kind: "app.x"})
	}}
	ms, conn := startCallbackServer(t, h, nil)
	t.Cleanup(func() { _ = ms.Stop() })
	sendReadRequest(t, conn)
	if <-got {
		t.Error("a server without a security policy gave the handler a reporter")
	}
	if st := ms.SecurityStats(); st.Delivered != 0 || len(st.Events) != 0 {
		t.Errorf("stats %+v", st)
	}
}

// On tcp+tls the peer carries the certificate and the Modbus role.
func TestServerEvents_TLSPeer(t *testing.T) {
	c := &evCollector{}
	dev := e2eNewDevice()
	dev.setHolding(1, 0x0101)
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{server: eventsOn(c)})
	if _, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1); err != nil {
		t.Fatal(err)
	}
	acc := c.wait(t, secevent.KindConnAccepted)
	if acc.Transport != "tcp+tls" {
		t.Errorf("transport %q", acc.Transport)
	}
	if err := p.client.Close(); err != nil {
		t.Fatal(err)
	}
	closed := c.wait(t, secevent.KindConnClosed)
	tp := closed.Peer.TLS
	if tp == nil || !tp.Verified || tp.Subject == "" || tp.Issuer == "" || tp.FingerprintSHA256 == "" ||
		tp.SerialHex == "" || tp.NotAfter.IsZero() || tp.Version == 0 {
		t.Fatalf("TLS peer of a served connection: %+v", tp)
	}
	if acc.Peer.TLS != nil {
		t.Errorf("the accept happens before the handshake, but carries %+v", acc.Peer.TLS)
	}
	if closed.ConnID != acc.ConnID {
		t.Error("connection ids differ")
	}
}

// Stop called from the sink does not wait for the sink; the server stops and
// the events it queued are still delivered.
func TestServerEvents_StopFromSink(t *testing.T) {
	var ms *Server
	stopped := make(chan error, 1)
	var once sync.Once
	sink := secevent.SinkFunc(func(e secevent.Event) {
		if e.Kind == secevent.KindConnAccepted {
			once.Do(func() { stopped <- ms.Stop() })
		}
	})
	var err error
	ms, err = NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Logger: NopLogger(),
		Security: &SecurityPolicy{Policy: secevent.Policy{Sink: sink}}}, holdingOne())
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
	defer func() { _ = conn.Close() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Errorf("Stop from the sink: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stop called from the sink did not return")
	}
}

// Stop returns after the events the server queued have been delivered.
func TestServerEvents_StopFlushes(t *testing.T) {
	c := &evCollector{}
	ms, conn := startCallbackServer(t, holdingOne(), eventsOn(c))
	sendReadRequest(t, conn)
	_ = conn.SetReadDeadline(time.Now().Add(e2eWait))
	if _, err := io.ReadFull(conn, make([]byte, 11)); err != nil {
		t.Fatal(err)
	}
	if err := ms.Stop(); err != nil {
		t.Fatal(err)
	}
	// the connection was closed by the stop; its end is delivered by the time
	// Stop returns
	if c.count(secevent.KindConnClosed) != 1 || c.count(secevent.KindConnAccepted) != 1 {
		t.Errorf("after Stop: %v", c.kinds())
	}
	if ms.SecurityStats().Delivered != 2 {
		t.Errorf("Delivered = %d, want 2", ms.SecurityStats().Delivered)
	}
}

// A dispatcher the application shares is used and not closed by the server,
// and keeps serving a second server.
func TestServerEvents_SharedDispatcher(t *testing.T) {
	c := &evCollector{}
	d, err := secevent.NewDispatcher(secevent.Config{Sink: c, AggregateWindow: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close(context.Background()) }()
	shared := func(conf *ServerConfig) { conf.Security = &SecurityPolicy{Policy: secevent.Policy{Dispatcher: d}} }

	ms1, c1 := startCallbackServer(t, holdingOne(), shared)
	_ = c1.Close()
	c.wait(t, secevent.KindConnClosed)
	if err := ms1.Stop(); err != nil {
		t.Fatal(err)
	}
	d.Emit(secevent.Event{Kind: "app.still_open"})
	c.wait(t, "app.still_open")

	_, c2 := startCallbackServer(t, holdingOne(), shared)
	_ = c2.Close()
	e2eEventually(t, "the second server's events", func() bool { return c.count(secevent.KindConnAccepted) == 2 })
}

func TestServerEvents_StatsBeforeStart(t *testing.T) {
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Logger: NopLogger(),
		Security: &SecurityPolicy{Policy: secevent.Policy{Sink: &evCollector{}}}}, holdingOne())
	if err != nil {
		t.Fatal(err)
	}
	if st := ms.SecurityStats(); st.Delivered != 0 || st.Events != nil {
		t.Errorf("stats before Start: %+v", st)
	}
}

func TestServerEvents_InvalidPolicy(t *testing.T) {
	sink := &evCollector{}
	d, err := secevent.NewDispatcher(secevent.Config{Sink: sink})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close(context.Background()) }()
	for name, p := range map[string]secevent.Policy{
		"both":  {Sink: sink, Dispatcher: d},
		"queue": {Sink: sink, QueueSize: -1},
	} {
		_, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Security: &SecurityPolicy{Policy: p}}, holdingOne())
		var ce *ConfigurationError
		if !errors.As(err, &ce) || ce.Field != "Security" {
			t.Errorf("%s: err = %v, want a ConfigurationError on Security", name, err)
		}
	}
}

// NewServer copies the policy: changing it afterwards changes nothing.
func TestServerEvents_PolicyIsCopied(t *testing.T) {
	c := &evCollector{}
	pol := &SecurityPolicy{Policy: secevent.Policy{Sink: c, AggregateWindow: -1}}
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Logger: NopLogger(), Security: pol}, holdingOne())
	if err != nil {
		t.Fatal(err)
	}
	pol.Sink = nil
	if err := ms.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ms.Stop() }()
	conn, err := net.Dial("tcp", ms.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	c.wait(t, secevent.KindConnAccepted)
}

// A ServerConfig stays comparable with a security policy in it.
func TestServerConfigWithSecurityIsComparable(t *testing.T) {
	a := ServerConfig{URL: "x", Security: &SecurityPolicy{}}
	b := a
	if a != b {
		t.Error("copies of a ServerConfig differ")
	}
}

// An Accept that fails is reported, and the loop goes on.
func TestServerEvents_AcceptError(t *testing.T) {
	c := &evCollector{}
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Logger: NopLogger(),
		Security: &SecurityPolicy{Policy: secevent.Policy{Sink: c, AggregateWindow: -1}}}, holdingOne())
	if err != nil {
		t.Fatal(err)
	}
	ln := &scriptedListener{script: []error{errors.New("too many open files")}}
	run := &serverRun{listener: ln, wg: &sync.WaitGroup{}}
	run.ctx, run.cancel = context.WithCancel(context.Background())
	defer run.cancel()
	if err := ms.startEvents(run); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = run.events.Close(context.Background()) }()
	run.wg.Add(1)
	go ms.acceptTCPClients(run)
	e := c.wait(t, secevent.KindConnAcceptError)
	if e.Severity != secevent.SeverityCritical || e.Err == nil || e.Transport != "tcp" || e.Category != secevent.CategoryResource {
		t.Errorf("accept_error: %+v", e)
	}
	ln.mu.Lock()
	ln.closed = true
	ln.mu.Unlock()
	run.cancel()
	run.wg.Wait()
}

// emitClosed maps each close reason onto the events a defender cares about.
func TestServerEvents_CloseMapping(t *testing.T) {
	deadline := os.ErrDeadlineExceeded
	for _, tc := range []struct {
		name     string
		reason   CloseReason
		requests int64
		want     []secevent.Kind
	}{
		{"client leaves without a request", CloseClientClosed, 0, []secevent.Kind{secevent.KindConnNoRequest, secevent.KindConnClosed}},
		{"idle without a request", CloseIdleTimeout, 0, []secevent.Kind{secevent.KindConnNoRequest, secevent.KindConnClosed}},
		{"client leaves", CloseClientClosed, 3, []secevent.Kind{secevent.KindConnClosed}},
		{"idle", CloseIdleTimeout, 3, []secevent.Kind{secevent.KindConnIdleTimeout, secevent.KindConnClosed}},
		{"malformed", CloseMalformedFrame, 0, []secevent.Kind{secevent.KindProtoBadMBAP, secevent.KindConnClosed}},
		{"protocol error", CloseProtocolError, 2, []secevent.Kind{secevent.KindProtoBadPDU, secevent.KindConnClosed}},
		{"write failed", CloseWriteFailed, 2, []secevent.Kind{secevent.KindDoSWriteStall, secevent.KindConnClosed}},
		{"read failed", CloseReadFailed, 2, []secevent.Kind{secevent.KindConnClosed}},
		{"reset without a request", CloseReadFailed, 0, []secevent.Kind{secevent.KindConnNoRequest, secevent.KindConnClosed}},
		{"server stopped", CloseServerStopped, 0, []secevent.Kind{secevent.KindConnClosed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &evCollector{}
			ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Logger: NopLogger(),
				Security: &SecurityPolicy{Policy: secevent.Policy{Sink: c, AggregateWindow: -1}}}, holdingOne())
			if err != nil {
				t.Fatal(err)
			}
			run := &serverRun{}
			if err := ms.startEvents(run); err != nil {
				t.Fatal(err)
			}
			a, b := net.Pipe()
			defer func() { _ = a.Close(); _ = b.Close() }()
			ce, _ := ms.newConnEvents(run, context.Background(), 5, nil, a, "", nil)
			ce.requests.Store(tc.requests)
			ms.emitClosed(run, ce, tc.reason, deadline)
			if err := run.events.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := c.kinds()
			if len(got) != len(tc.want) {
				t.Fatalf("events %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("events %v, want %v", got, tc.want)
				}
			}
			// a connection without events reports nothing
			ms.emitClosed(run, nil, tc.reason, nil)
		})
	}
}

// The refusal events have the reasons the accept loop reports to metrics, and
// ignore a reason they do not know.
func TestServerEvents_Refusals(t *testing.T) {
	c := &evCollector{}
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Logger: NopLogger(),
		Security: &SecurityPolicy{Policy: secevent.Policy{Sink: c, AggregateWindow: -1}}}, holdingOne())
	if err != nil {
		t.Fatal(err)
	}
	run := &serverRun{}
	if err := ms.startEvents(run); err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	defer func() { _ = a.Close(); _ = b.Close() }()
	for _, r := range []RejectReason{RejectNotAllowed, RejectMaxClients, RejectTLSHandshakeEvicted, 0} {
		ms.emitRefused(run, 4, a, r)
	}
	ms.emitAccepted(run, 4, a)
	_ = run.events.Close(context.Background())
	got := c.kinds()
	want := []secevent.Kind{secevent.KindConnUnexpected, secevent.KindConnRejectedLimit, secevent.KindTLSHandshakeEvicted, secevent.KindConnAccepted}
	if len(got) != len(want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d is %s, want %s", i, got[i], want[i])
		}
	}
	// a peer that is not a TCP address has no address in the event
	if e := c.snapshot()[0]; e.Peer.RemoteAddr.IsValid() {
		t.Errorf("pipe peer: %+v", e.Peer)
	}
}

func TestAddrPortOf(t *testing.T) {
	if got := addrPortOf(nil); got.IsValid() {
		t.Errorf("nil: %v", got)
	}
	if got := addrPortOf(&net.TCPAddr{IP: net.ParseIP("::ffff:10.0.0.1"), Port: 5}); got != netip.MustParseAddrPort("10.0.0.1:5") {
		t.Errorf("mapped IPv4: %v", got)
	}
	if got := addrPortOf(fakeAddr("192.0.2.9:80")); got != netip.MustParseAddrPort("192.0.2.9:80") {
		t.Errorf("other net.Addr: %v", got)
	}
	if got := addrPortOf(fakeAddr("pipe")); got.IsValid() {
		t.Errorf("unparsable: %v", got)
	}
}

type fakeAddr string

func (a fakeAddr) Network() string { return "fake" }
func (a fakeAddr) String() string  { return string(a) }

// A scan that resets the connection without a request is reported as one that
// sent no request, like one that closes it.
func TestServerEvents_ResetIsNoRequest(t *testing.T) {
	c := &evCollector{}
	ms, conn := startCallbackServer(t, holdingOne(), eventsOn(c))
	t.Cleanup(func() { _ = ms.Stop() })
	c.wait(t, secevent.KindConnAccepted)
	if err := conn.(*net.TCPConn).SetLinger(0); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	e := c.wait(t, secevent.KindConnNoRequest)
	c.wait(t, secevent.KindConnClosed)
	if e.Action != secevent.ActionObserved {
		t.Errorf("no_request after a reset: %+v", e)
	}
}

// The eviction of a pending TLS handshake is reported with the connection id
// its conn.accepted announced, and a connection whose handshake fails is
// reported closed.
func TestServerEvents_TLSEvictionAndHandshakeFailure(t *testing.T) {
	c := &evCollector{}
	dev := e2eNewDevice()
	dev.setHolding(1, 0x0101)
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{noOpen: true, server: chain(eventsOn(c), func(conf *ServerConfig) {
		conf.MaxClients = 1
	})})
	pending := dialRaw(t, p.server)
	acc := c.wait(t, secevent.KindConnAccepted)
	if err := p.client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := p.client.ReadHoldingRegister(context.Background(), e2eUnit, 1); err != nil {
		t.Fatal(err)
	}
	wantClosedUnanswered(t, pending)
	ev := c.wait(t, secevent.KindTLSHandshakeEvicted)
	if ev.ConnID == 0 || ev.ConnID != acc.ConnID || ev.Peer.RemoteAddr != addrPortOf(pending.LocalAddr()) {
		t.Errorf("eviction %+v, accepted %+v", ev, acc)
	}
	for _, e := range c.snapshot() {
		if e.Kind == secevent.KindConnClosed && e.ConnID == acc.ConnID {
			t.Errorf("the evicted connection was also reported closed: %+v", e)
		}
	}
	if err := p.client.Close(); err != nil {
		t.Fatal(err)
	}
	e2eEventually(t, "the slot to be freed", func() bool { return p.conns() == 0 })

	// not TLS: the handshake fails
	bad := dialRaw(t, p.server)
	if _, err := bad.Write([]byte("GET / HTTP/1.0\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	wantClosedUnanswered(t, bad)
	local := addrPortOf(bad.LocalAddr())
	var closed secevent.Event
	e2eEventually(t, "conn.closed of the failed handshake", func() bool {
		for _, e := range c.snapshot() {
			if e.Kind == secevent.KindConnClosed && e.Peer.RemoteAddr == local {
				closed = e
				return true
			}
		}
		return false
	})
	if closed.Action != secevent.ActionDisconnected || closed.Detail != "the TLS handshake failed" || closed.Err == nil || closed.ConnID == 0 {
		t.Errorf("failed handshake: %+v", closed)
	}
}

// A connection accepted while the server is stopping is reported closed after
// its conn.accepted, and not refused.
func TestServerEvents_ConnectionDuringStop(t *testing.T) {
	c := &evCollector{}
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Logger: NopLogger(),
		Security: &SecurityPolicy{Policy: secevent.Policy{Sink: c, AggregateWindow: -1}}}, &fnHandler{})
	if err != nil {
		t.Fatal(err)
	}
	ln := &scriptedListener{script: []error{nil}}
	run := &serverRun{listener: ln, wg: &sync.WaitGroup{}}
	run.ctx, run.cancel = context.WithCancel(context.Background())
	defer run.cancel()
	if err := ms.startEvents(run); err != nil {
		t.Fatal(err)
	}
	run.wg.Add(1)
	ms.acceptTCPClients(run)
	run.wg.Wait()
	if err := run.events.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	ev := c.snapshot()
	if len(ev) != 2 || ev[0].Kind != secevent.KindConnAccepted || ev[1].Kind != secevent.KindConnClosed ||
		ev[0].ConnID != ev[1].ConnID || ev[1].Action != secevent.ActionDisconnected {
		t.Fatalf("events %+v", ev)
	}
}

// A sink that does not return does not hold Stop for ever: Stop gives up on
// the queued events after eventsFlushTimeout and logs it.
func TestServerEvents_StuckSinkDoesNotHoldStop(t *testing.T) {
	old := eventsFlushTimeout
	eventsFlushTimeout = 100 * time.Millisecond
	t.Cleanup(func() { eventsFlushTimeout = old })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	sink := secevent.SinkFunc(func(secevent.Event) { <-release })
	log := &e2eLogger{}
	ms, conn := startCallbackServer(t, holdingOne(), func(conf *ServerConfig) {
		conf.Logger = log
		conf.Security = &SecurityPolicy{Policy: secevent.Policy{Sink: sink, AggregateWindow: -1}}
	})
	_ = conn.Close()
	stopped := make(chan error, 1)
	go func() { stopped <- ms.Stop() }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not return with a sink that does not return")
	}
	if !log.has("did not take the queued events") {
		t.Error("the flush timeout was not logged")
	}
}

// Only a response write that ran out of time is a write stall; a reset or a
// broken pipe is reported as the close alone.
func TestServerEvents_WriteFailureIsAStallOnlyOnTimeout(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want []secevent.Kind
	}{
		{os.ErrDeadlineExceeded, []secevent.Kind{secevent.KindDoSWriteStall, secevent.KindConnClosed}},
		{&net.OpError{Op: "write", Err: os.NewSyscallError("write", syscall.ECONNRESET)}, []secevent.Kind{secevent.KindConnClosed}},
		{io.ErrClosedPipe, []secevent.Kind{secevent.KindConnClosed}},
	} {
		c := &evCollector{}
		ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Logger: NopLogger(),
			Security: &SecurityPolicy{Policy: secevent.Policy{Sink: c, AggregateWindow: -1}}}, holdingOne())
		if err != nil {
			t.Fatal(err)
		}
		run := &serverRun{}
		if err := ms.startEvents(run); err != nil {
			t.Fatal(err)
		}
		a, b := net.Pipe()
		ce, _ := ms.newConnEvents(run, context.Background(), 5, nil, a, "", nil)
		ce.requests.Store(2)
		ms.emitClosed(run, ce, CloseWriteFailed, tc.err)
		ms.emitClosed(run, ce, CloseWriteFailed, tc.err) // reported once
		_ = run.events.Close(context.Background())
		_, _ = a.Close(), b.Close()
		if got := c.kinds(); !slices.Equal(got, tc.want) {
			t.Errorf("%v: events %v, want %v", tc.err, got, tc.want)
		}
	}
}

// wantAcceptedThenClosed checks that the connection from local was reported
// accepted, then closed once, with the same id.
func wantAcceptedThenClosed(t *testing.T, c *evCollector, local net.Addr) secevent.Event {
	t.Helper()
	peer := addrPortOf(local)
	var acc, closed []secevent.Event
	for _, e := range c.snapshot() {
		if e.Peer.RemoteAddr != peer {
			continue
		}
		switch e.Kind {
		case secevent.KindConnAccepted:
			acc = append(acc, e)
		case secevent.KindConnClosed, secevent.KindConnUnexpected:
			closed = append(closed, e)
		default: // the other kinds say nothing about the pairing
		}
	}
	if len(acc) != 1 || len(closed) != 1 || acc[0].ConnID != closed[0].ConnID || acc[0].ConnID == 0 {
		t.Fatalf("accepted %+v, ended %+v; want one of each with the same id\nall: %v", acc, closed, c.kinds())
	}
	return closed[0]
}

// A Stop called from AllowClient still reports the end of the connection it
// was deciding on, whatever AllowClient answers.
func TestServerEvents_StopFromAllowClient(t *testing.T) {
	for _, allow := range []bool{true, false} {
		c := &evCollector{}
		stopped := make(chan error, 1)
		var ms *Server
		var err error
		ms, err = NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Logger: NopLogger(),
			Security: &SecurityPolicy{Policy: secevent.Policy{Sink: c, AggregateWindow: -1}},
			AllowClient: ClientFilterFunc(func(net.Addr) bool {
				stopped <- ms.Stop()
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
		if err := <-stopped; err != nil {
			t.Fatal(err)
		}
		closed := wantAcceptedThenClosed(t, c, conn.LocalAddr())
		if closed.Kind != secevent.KindConnClosed || closed.Detail != "closed unserved: the server is stopping" {
			t.Errorf("allow=%v: %+v", allow, closed)
		}
		_ = conn.Close()
	}
}

// A Stop called from a handler still reports the end of the handler's
// connection.
func TestServerEvents_StopFromHandler(t *testing.T) {
	c := &evCollector{}
	returned := make(chan error, 1)
	h := &blockingHandler{}
	ms, conn := startCallbackServer(t, h, eventsOn(c))
	h.fn = func(context.Context) { returned <- ms.Stop() }
	sendReadRequest(t, conn)
	if err := <-returned; err != nil {
		t.Fatal(err)
	}
	closed := wantAcceptedThenClosed(t, c, conn.LocalAddr())
	if closed.Kind != secevent.KindConnClosed || closed.Action != secevent.ActionObserved {
		t.Errorf("closed: %+v", closed)
	}
}

// stopLogger calls stop on its first warning that contains match.
type stopLogger struct {
	match string
	once  sync.Once
	stop  func()
}

func (l *stopLogger) Debugf(string, ...any) {}
func (l *stopLogger) Infof(string, ...any)  {}
func (l *stopLogger) Errorf(string, ...any) {}
func (l *stopLogger) Warnf(format string, args ...any) {
	if strings.Contains(fmt.Sprintf(format, args...), l.match) {
		l.once.Do(l.stop)
	}
}

// A Stop called from the Logger while the TLS handshake failure is logged
// still reports the end of that connection, once.
func TestServerEvents_StopWhileLoggingHandshakeFailure(t *testing.T) {
	c := &evCollector{}
	stopped := make(chan error, 1)
	log := &stopLogger{match: "TLS handshake with"}
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{noOpen: true, server: chain(eventsOn(c), func(conf *ServerConfig) {
		conf.Logger = log
	})})
	log.stop = func() { stopped <- p.server.Stop() }
	bad := dialRaw(t, p.server)
	if _, err := bad.Write([]byte("GET / HTTP/1.0\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(e2eWait):
		t.Fatal("Stop from the Logger did not return")
	}
	closed := wantAcceptedThenClosed(t, c, bad.LocalAddr())
	if closed.Kind != secevent.KindConnClosed || closed.Action != secevent.ActionDisconnected {
		t.Errorf("closed: %+v", closed)
	}
}

// A connection over MaxClientsPerHost is reported as rejected for the limit.
func TestServerEvents_PerHostLimit(t *testing.T) {
	c := &evCollector{}
	ms, first := startCallbackServer(t, holdingOne(), chain(eventsOn(c), func(conf *ServerConfig) { conf.MaxClientsPerHost = 1 }))
	t.Cleanup(func() { _ = ms.Stop() })
	sendReadRequest(t, first)
	_ = first.SetReadDeadline(time.Now().Add(e2eWait))
	if _, err := io.ReadFull(first, make([]byte, 11)); err != nil {
		t.Fatal(err)
	}
	second := dialRaw(t, ms)
	wantClosedUnanswered(t, second)
	e := c.wait(t, secevent.KindConnRejectedLimit)
	if e.Action != secevent.ActionRejected || !strings.Contains(e.Detail, "per host") || e.Peer.RemoteAddr != addrPortOf(second.LocalAddr()) {
		t.Errorf("rejected_limit: %+v", e)
	}
}

// A connection that sends nothing within FirstRequestTimeout is reported as
// one that sent no request, closed by the server.
func TestServerEvents_FirstRequestTimeout(t *testing.T) {
	c := &evCollector{}
	ms, conn := startCallbackServer(t, holdingOne(), chain(eventsOn(c), func(conf *ServerConfig) { conf.FirstRequestTimeout = 100 * time.Millisecond }))
	t.Cleanup(func() { _ = ms.Stop() })
	_ = conn
	e := c.wait(t, secevent.KindConnNoRequest)
	if e.Action != secevent.ActionDisconnected || !strings.Contains(e.Detail, "first-request") {
		t.Errorf("no_request: %+v", e)
	}
	c.wait(t, secevent.KindConnClosed)
}

// The events of a connection carry its ConnectionID, the one the other
// reports about it carry.
func TestServerEvents_ConnIDIsTheConnectionID(t *testing.T) {
	c := &evCollector{}
	ids := make(chan ConnectionID, 1)
	ms, conn := startCallbackServer(t, holdingOne(), chain(eventsOn(c), func(conf *ServerConfig) {
		conf.Authorizer = RequestAuthorizerFunc(func(_ context.Context, ar *AuthorizationRequest) error {
			select {
			case ids <- ar.ConnectionID:
			default:
			}
			return nil
		})
	}))
	t.Cleanup(func() { _ = ms.Stop() })
	sendReadRequest(t, conn)
	_ = conn.SetReadDeadline(time.Now().Add(e2eWait))
	if _, err := io.ReadFull(conn, make([]byte, 11)); err != nil {
		t.Fatal(err)
	}
	id := <-ids
	acc := c.wait(t, secevent.KindConnAccepted)
	if id == 0 || acc.ConnID != uint64(id) {
		t.Errorf("event id %d, ConnectionID %d", acc.ConnID, id)
	}
}
