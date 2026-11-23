// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// hardeningMetrics records rejections and close reasons.
type hardeningMetrics struct {
	closeMetrics
	rmu      sync.Mutex
	rejected []RejectReason
}

func (m *hardeningMetrics) OnConnectionRejected(_ string, r RejectReason) {
	m.rmu.Lock()
	m.rejected = append(m.rejected, r)
	m.rmu.Unlock()
}

func (m *hardeningMetrics) rejections() []RejectReason {
	m.rmu.Lock()
	defer m.rmu.Unlock()
	return append([]RejectReason(nil), m.rejected...)
}

func startHardened(t *testing.T, kind string, m *hardeningMetrics, mut func(*ServerConfig)) *e2ePair {
	t.Helper()
	dev := e2eNewDevice()
	dev.setHolding(1, 0x0101)
	return e2eStart(t, kind, dev, e2eOpts{noOpen: true, server: func(c *ServerConfig) {
		c.Metrics = m
		c.Logger = NopLogger()
		if mut != nil {
			mut(c)
		}
	}})
}

// wantRawServed sends a read on conn and checks that it is answered.
func wantRawServed(t *testing.T, conn net.Conn) {
	t.Helper()
	sendReadRequest(t, conn)
	readResponse(t, conn)
}

// A host gets MaxClientsPerHost connections; the next one is refused at
// accept, takes no slot, and a slot freed is taken again.
func TestServerMaxClientsPerHost(t *testing.T) {
	m := &hardeningMetrics{}
	p := startHardened(t, "tcp", m, func(c *ServerConfig) { c.MaxClientsPerHost = 2 })
	a, b := dialRaw(t, p.server), dialRaw(t, p.server)
	wantRawServed(t, a)
	wantRawServed(t, b)
	refused := dialRaw(t, p.server)
	wantClosedUnanswered(t, refused)
	e2eEventually(t, "the rejection", func() bool { return len(m.rejections()) == 1 })
	if got := m.rejections(); got[0] != RejectPerHostLimit {
		t.Errorf("rejections %v, want one per-host limit", got)
	}
	if n := p.conns(); n != 2 {
		t.Errorf("%d connections tracked, want 2: a refused connection takes no slot", n)
	}
	_ = a.Close()
	e2eEventually(t, "the slot to be freed", func() bool { return p.conns() == 1 })
	wantRawServed(t, dialRaw(t, p.server))
	for _, ev := range m.eventsSnapshot() {
		if strings.Contains(ev, refused.LocalAddr().String()) {
			t.Errorf("events %v: the refused connection must not be opened", m.eventsSnapshot())
		}
	}
}

// On tcp+tls the connections still handshaking count, and one beyond the
// limit evicts nobody.
func TestServerMaxClientsPerHost_TLSHandshaking(t *testing.T) {
	m := &hardeningMetrics{}
	p := startHardened(t, "tcp+tls", m, func(c *ServerConfig) {
		c.MaxClientsPerHost = 1
		c.MaxClients = 1
	})
	pending := dialRaw(t, p.server)
	e2eEventually(t, "the pending connection", func() bool { return p.conns() == 1 })
	refused := dialRaw(t, p.server)
	wantClosedUnanswered(t, refused)
	e2eEventually(t, "the rejection", func() bool { return len(m.rejections()) == 1 })
	if got := m.rejections(); got[0] != RejectPerHostLimit {
		t.Errorf("rejections %v, want per-host limit (and no eviction)", got)
	}
	// The pending connection is still there.
	_ = pending.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := pending.Read(make([]byte, 1)); !os.IsTimeout(err) {
		t.Errorf("the handshaking connection was closed: %v", err)
	}
}

// Many peers connecting and leaving at once never get past the limit, and
// every connection is accounted for.
func TestServerMaxClientsPerHost_Churn(t *testing.T) {
	const limit, peers, rounds = 3, 12, 20
	m := &hardeningMetrics{}
	p := startHardened(t, "tcp", m, func(c *ServerConfig) {
		c.MaxClientsPerHost = limit
		c.MaxClients = 64
	})
	var over atomic.Bool
	stop := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		for {
			select {
			case <-stop:
				return
			default:
			}
			p.server.lock.Lock()
			if p.server.hostClientsLocked("127.0.0.1") > limit {
				over.Store(true)
			}
			p.server.lock.Unlock()
			time.Sleep(100 * time.Microsecond)
		}
	}()
	var served, dialled atomic.Int64
	var wg sync.WaitGroup
	for range peers {
		wg.Go(func() {
			for range rounds {
				conn, err := net.Dial("tcp", p.hostPort)
				if err != nil {
					continue
				}
				dialled.Add(1)
				_ = conn.SetDeadline(time.Now().Add(e2eWait))
				if _, err := conn.Write(rawRequestFrame(1, 1, byte(FCReadHoldingRegisters), u16(1, 1))); err == nil {
					if n, _ := conn.Read(make([]byte, 16)); n > 0 {
						served.Add(1)
					}
				}
				_ = conn.Close()
			}
		})
	}
	wg.Wait()
	close(stop)
	<-watched
	if over.Load() {
		t.Errorf("more than %d connections of one host were served at once", limit)
	}
	e2eEventually(t, "every connection to end", func() bool { return p.conns() == 0 })
	e2eEventually(t, "every close to be reported", func() bool {
		return int64(len(m.reasonsSnapshot())+len(m.rejections())) == dialled.Load()
	})
	if int64(len(m.reasonsSnapshot())) < served.Load() || served.Load() == 0 {
		t.Errorf("%d served, %d closes reported", served.Load(), len(m.reasonsSnapshot()))
	}
	for _, r := range m.rejections() {
		if r != RejectPerHostLimit {
			t.Errorf("rejection %v, want per-host limit only", r)
		}
	}
}

func TestServerHostClientsLocked(t *testing.T) {
	ms := &Server{tcpClients: pendingFrom("10.0.0.1", "10.0.0.2", "10.0.0.1", "::1")}
	for host, want := range map[string]uint{"10.0.0.1": 2, "10.0.0.2": 1, "::1": 1, "10.0.0.3": 0} {
		if got := ms.hostClientsLocked(host); got != want {
			t.Errorf("%s: %d, want %d", host, got, want)
		}
	}
}

// A client that connects and sends nothing is closed after
// FirstRequestTimeout; once it has sent a request, the idle timeout applies.
func TestServerFirstRequestTimeout(t *testing.T) {
	m := &hardeningMetrics{}
	p := startHardened(t, "tcp", m, func(c *ServerConfig) {
		c.FirstRequestTimeout = 100 * time.Millisecond
		c.Timeout = 30 * time.Second
	})
	silent := dialRaw(t, p.server)
	start := time.Now()
	wantClosedUnanswered(t, silent)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("closed after %v, want about 100 ms", d)
	}
	wantReason(t, &m.closeMetrics, silent.LocalAddr().String(), CloseFirstRequestTimeout, os.ErrDeadlineExceeded)

	active := dialRaw(t, p.server)
	wantRawServed(t, active)
	time.Sleep(300 * time.Millisecond)
	wantRawServed(t, active)
}

// A request that does not arrive in full within FrameTimeout of its first
// byte closes the connection; a request split over a short pause is served.
func TestServerFrameTimeout(t *testing.T) {
	m := &hardeningMetrics{}
	p := startHardened(t, "tcp", m, func(c *ServerConfig) {
		c.FrameTimeout = 150 * time.Millisecond
		c.Timeout = 30 * time.Second
	})
	frame := rawRequestFrame(1, 1, byte(FCReadHoldingRegisters), u16(1, 1))

	legit := dialRaw(t, p.server)
	for _, part := range [][]byte{frame[:1], frame[1:8], frame[8:]} {
		if _, err := legit.Write(part); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Millisecond)
	}
	readResponse(t, legit)
	// Idle for longer than FrameTimeout: not a frame in progress.
	time.Sleep(300 * time.Millisecond)
	wantRawServed(t, legit)
	_ = legit.Close()
	e2eEventually(t, "the first close", func() bool { return len(m.reasonsSnapshot()) == 1 })

	slow := dialRaw(t, p.server)
	if _, err := slow.Write(frame[:3]); err != nil {
		t.Fatal(err)
	}
	wantClosedUnanswered(t, slow)
	e2eEventually(t, "the second close", func() bool { return len(m.reasonsSnapshot()) == 2 })
	got := m.reasonsSnapshot()[1]
	if got.reason != CloseFrameTimeout || !errors.Is(got.err, os.ErrDeadlineExceeded) {
		t.Errorf("close %v (%v), want frame timeout", got.reason, got.err)
	}
}

// FrameTimeout also bounds a request that arrives while an earlier one is
// being served.
func TestServerFrameTimeout_WhileServing(t *testing.T) {
	m := &hardeningMetrics{}
	h := &ctxBlockingHandler{cancelled: make(chan time.Time, 1)}
	_, conn := startCallbackServer(t, h, func(c *ServerConfig) {
		c.Metrics = m
		c.FrameTimeout = 100 * time.Millisecond
	})
	sendReadRequest(t, conn)
	time.Sleep(20 * time.Millisecond)
	// The first bytes of a second request, while the first is being served.
	if _, err := conn.Write(rawRequestFrame(2, 1, byte(FCReadHoldingRegisters), u16(0, 1))[:4]); err != nil {
		t.Fatal(err)
	}
	sent := time.Now()
	// The handler's context is cancelled when the frame times out, not after
	// the idle timeout (30 s).
	select {
	case at := <-h.cancelled:
		if d := at.Sub(sent); d > 5*time.Second {
			t.Errorf("handler cancelled %v after the partial request", d)
		}
	case <-time.After(e2eWait):
		t.Fatal("the slow request did not end the connection")
	}
	e2eEventually(t, "the close", func() bool { return len(m.reasonsSnapshot()) == 1 })
	if got := m.reasonsSnapshot()[0]; got.reason != CloseFrameTimeout {
		t.Errorf("close %v (%v), want frame timeout", got.reason, got.err)
	}
}

// ctxBlockingHandler serves holding registers by waiting for its context to
// end, and sends the time it ended on cancelled.
type ctxBlockingHandler struct {
	fnHandler
	cancelled chan time.Time
}

func (h *ctxBlockingHandler) HandleHoldingRegisters(ctx context.Context, _ *HoldingRegistersRequest) ([]uint16, error) {
	<-ctx.Done()
	h.cancelled <- time.Now()
	return nil, ctx.Err()
}

// With both timeouts set, the frame timeout of a first request that started
// arriving wins when it ends first.
func TestServerFrameTimeout_BeforeFirstRequestTimeout(t *testing.T) {
	m := &hardeningMetrics{}
	p := startHardened(t, "tcp", m, func(c *ServerConfig) {
		c.FirstRequestTimeout = 2 * time.Second
		c.FrameTimeout = 50 * time.Millisecond
	})
	conn := dialRaw(t, p.server)
	if _, err := conn.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	wantClosedUnanswered(t, conn)
	wantReason(t, &m.closeMetrics, conn.LocalAddr().String(), CloseFrameTimeout, os.ErrDeadlineExceeded)
}

// Without the settings the server behaves as before: a silent client is
// governed by the idle timeout alone.
func TestServerHardening_DisabledByDefault(t *testing.T) {
	m := &hardeningMetrics{}
	p := startHardened(t, "tcp", m, func(c *ServerConfig) { c.Timeout = 150 * time.Millisecond })
	conns := make([]net.Conn, 4)
	for i := range conns {
		conns[i] = dialRaw(t, p.server)
	}
	if _, err := conns[0].Write([]byte{0, 1, 0}); err != nil {
		t.Fatal(err)
	}
	for _, c := range conns {
		wantClosedUnanswered(t, c)
	}
	e2eEventually(t, "the closes", func() bool { return len(m.reasonsSnapshot()) == len(conns) })
	for _, r := range m.reasonsSnapshot() {
		if r.reason != CloseIdleTimeout {
			t.Errorf("close %v, want idle timeout", r.reason)
		}
	}
	if got := m.rejections(); len(got) != 0 {
		t.Errorf("rejections %v without a per-host limit", got)
	}
}

func TestServerHardening_NegativeDurations(t *testing.T) {
	for _, mut := range []func(*ServerConfig){
		func(c *ServerConfig) { c.FirstRequestTimeout = -time.Second },
		func(c *ServerConfig) { c.FrameTimeout = -time.Second },
	} {
		conf := &ServerConfig{URL: "tcp://127.0.0.1:0"}
		mut(conf)
		if _, err := NewServer(conf, &fnHandler{}); !errors.Is(err, ErrConfigurationError) {
			t.Errorf("%+v: err = %v, want a configuration error", conf, err)
		}
	}
}

func TestHardeningReasonStrings(t *testing.T) {
	if got := RejectPerHostLimit.String(); got != "per-host limit" {
		t.Errorf("RejectPerHostLimit = %q", got)
	}
	if got := CloseFirstRequestTimeout.String(); got != "first request timeout" {
		t.Errorf("CloseFirstRequestTimeout = %q", got)
	}
	if got := CloseFrameTimeout.String(); got != "frame timeout" {
		t.Errorf("CloseFrameTimeout = %q", got)
	}
}
