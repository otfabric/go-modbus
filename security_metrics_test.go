// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// secRecorder is a ServerMetrics and a ClientMetrics that records the calls of
// the optional security observation interfaces, and the lifecycle of the
// connections, in order.
type secRecorder struct {
	mu         sync.Mutex
	events     []string
	handshakes []TLSHandshakeInfo
	violations []ProtocolViolation
	accepts    []error
	anomalies  []ResponseAnomaly
	// onHandshake, when set, is called by OnTLSHandshake.
	onHandshake func(*TLSHandshakeInfo)
}

func (r *secRecorder) add(ev string) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *secRecorder) OnRequest(uint8, FunctionCode)                     {}
func (r *secRecorder) OnResponse(uint8, FunctionCode, time.Duration)     {}
func (r *secRecorder) OnTimeout(uint8, FunctionCode, time.Duration)      {}
func (r *secRecorder) OnError(uint8, FunctionCode, time.Duration, error) { r.add("error") }
func (r *secRecorder) OnConnectionOpened(addr, role string)              { r.add("opened " + role) }
func (r *secRecorder) OnConnectionClosed(string)                         { r.add("closed") }
func (r *secRecorder) OnAcceptError(err error, _ time.Duration) {
	r.mu.Lock()
	r.accepts = append(r.accepts, err)
	r.mu.Unlock()
}
func (r *secRecorder) OnConnectionCloseReason(_ string, c CloseReason, _ error) {
	r.add("reason " + c.String())
}

func (r *secRecorder) OnTLSHandshake(info TLSHandshakeInfo) {
	if r.onHandshake != nil {
		r.onHandshake(&info)
	}
	r.mu.Lock()
	r.handshakes = append(r.handshakes, info)
	r.events = append(r.events, "handshake")
	r.mu.Unlock()
}

func (r *secRecorder) OnProtocolViolation(v ProtocolViolation) {
	r.mu.Lock()
	r.violations = append(r.violations, v)
	r.events = append(r.events, "violation")
	r.mu.Unlock()
}

func (r *secRecorder) OnResponseAnomaly(a ResponseAnomaly) {
	r.mu.Lock()
	r.anomalies = append(r.anomalies, a)
	r.mu.Unlock()
}

func (r *secRecorder) snapshot() (events []string, hs []TLSHandshakeInfo, vs []ProtocolViolation, as []ResponseAnomaly) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...), append([]TLSHandshakeInfo(nil), r.handshakes...),
		append([]ProtocolViolation(nil), r.violations...), append([]ResponseAnomaly(nil), r.anomalies...)
}

func (r *secRecorder) handshakeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.handshakes)
}

// A completed handshake is reported before the connection is opened, with the
// client's verified certificate and role.
func TestTLSHandshakeMetrics_ServerCompleted(t *testing.T) {
	pki := e2ePKI(t)
	m := &secRecorder{}
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{server: func(c *ServerConfig) { c.Metrics = m }})
	if _, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1); err != nil {
		t.Fatalf("read: %v", err)
	}
	e2eEventually(t, "the handshake", func() bool { return m.handshakeCount() == 1 })
	events, hs, _, _ := m.snapshot()
	if len(events) < 2 || events[0] != "handshake" || events[1] != "opened operator" {
		t.Errorf("events %v: want the handshake right before the open", events)
	}
	h := hs[0]
	if h.Err != nil || !h.Verified || h.Role != "operator" || h.RoleErr != nil {
		t.Errorf("handshake %+v: want completed, verified, role operator", h)
	}
	if h.Version == 0 || h.CipherSuite == 0 || h.Duration <= 0 || h.ServerName != "localhost" {
		t.Errorf("handshake %+v: want the negotiated parameters and the SNI", h)
	}
	if h.RemoteAddr == "" || h.LocalAddr != p.hostPort {
		t.Errorf("addresses %q -> %q, want the client's and %q", h.RemoteAddr, h.LocalAddr, p.hostPort)
	}
	if len(h.PeerCertificates) != 1 || h.PeerCertificates[0].Subject.CommonName != "e2e client" {
		t.Fatalf("peer certificates %v, want the client's", h.PeerCertificates)
	}
	if _, err := h.PeerCertificates[0].Verify(x509.VerifyOptions{
		Roots: pki.ca.pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("the reported certificate does not verify: %v", err)
	}
}

// The certificates belong to the receiver: changing them changes neither the
// next report nor what the server does.
func TestTLSHandshakeMetrics_CertificatesAreCopies(t *testing.T) {
	m := &secRecorder{onHandshake: func(info *TLSHandshakeInfo) {
		for _, c := range info.PeerCertificates {
			c.Subject.CommonName = "tampered"
			c.Raw[len(c.Raw)-1] ^= 0xFF
			c.Extensions = nil
		}
	}}
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{server: func(c *ServerConfig) { c.Metrics = m }})
	for i := range 2 {
		c := p.newClient(nil)
		if err := c.Open(); err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		if _, err := c.ReadHoldingRegister(t.Context(), e2eUnit, 1); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if call := e2eOneCall(t, "read", dev); call.ClientRole != "operator" {
			t.Errorf("connection %d: handler saw role %q", i, call.ClientRole)
		}
		_ = c.Close()
	}
	e2eEventually(t, "the handshakes", func() bool { return m.handshakeCount() >= 2 })
	_, hs, _, _ := m.snapshot()
	for _, h := range hs {
		if h.Role != "operator" {
			t.Errorf("handshake %v: role %q", h.RemoteAddr, h.Role)
		}
	}
}

// A role extension the server cannot use is told apart from no role. (A
// certificate with two role extensions does not parse in crypto/x509, which
// refuses duplicate extensions: extractRole's own test covers it.)
func TestTLSHandshakeMetrics_RoleErrors(t *testing.T) {
	pki := e2ePKI(t)
	for _, tc := range []struct {
		name    string
		roles   []string
		wantErr bool
	}{
		{"no role", nil, false},
		{"printable string", []string{"printable:operator"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &secRecorder{}
			p := e2eStart(t, "tcp+tls", e2eNewDevice(), e2eOpts{noOpen: true, server: func(c *ServerConfig) { c.Metrics = m }})
			c := p.newClient(func(conf *Config) { conf.TLSClientCert = e2eIssue(t, pki.ca, tc.name, false, tc.roles...) })
			if err := c.Open(); err != nil {
				t.Fatalf("Open: %v", err)
			}
			if _, err := c.ReadHoldingRegister(t.Context(), e2eUnit, 1); err != nil {
				t.Fatalf("read: %v", err)
			}
			e2eEventually(t, "the handshake", func() bool { return m.handshakeCount() == 1 })
			_, hs, _, _ := m.snapshot()
			h := hs[0]
			if h.Err != nil || h.Role != "" {
				t.Errorf("handshake err %v role %q, want completed without a role", h.Err, h.Role)
			}
			if got := errors.Is(h.RoleErr, ErrInvalidRole); got != tc.wantErr {
				t.Errorf("RoleErr = %v, want ErrInvalidRole: %v", h.RoleErr, tc.wantErr)
			}
		})
	}
}

// A client whose certificate does not verify, and a peer that does not speak
// TLS, are reported as failed handshakes and never opened.
func TestTLSHandshakeMetrics_ServerFailed(t *testing.T) {
	pki := e2ePKI(t)
	m := &secRecorder{}
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{noOpen: true, server: func(c *ServerConfig) { c.Metrics = m; c.Logger = NopLogger() }})

	rogue := p.newClient(func(c *Config) {
		c.TLSClientCert = e2eIssue(t, pki.otherCA, "rogue client", false, "operator")
		c.Timeout = time.Second
	})
	if err := rogue.Open(); err == nil {
		_ = rogue.WriteRegister(t.Context(), e2eUnit, 1, 1)
	}
	e2eEventually(t, "the failed handshake", func() bool { return m.handshakeCount() == 1 })

	raw := dialRaw(t, p.server)
	if _, err := raw.Write([]byte("not a TLS client hello, not at all")); err != nil {
		t.Fatal(err)
	}
	wantClosedUnanswered(t, raw)
	e2eEventually(t, "the second failed handshake", func() bool { return m.handshakeCount() == 2 })

	events, hs, _, _ := m.snapshot()
	for _, h := range hs {
		if h.Err == nil || h.Verified || h.Role != "" || h.RemoteAddr == "" {
			t.Errorf("handshake %+v: want a failure without a role", h)
		}
	}
	if !strings.Contains(hs[0].Err.Error(), "certificate") {
		t.Errorf("rogue client: err %v, want the certificate error", hs[0].Err)
	}
	for _, ev := range events {
		if strings.HasPrefix(ev, "opened") {
			t.Errorf("events %v: a failed handshake was opened", events)
		}
	}
	if dev.callCount() != 0 {
		t.Error("a client whose handshake failed reached a handler")
	}
}

// A handshake that does not complete within TLSHandshakeTimeout fails with a
// deadline error.
func TestTLSHandshakeMetrics_ServerTimeout(t *testing.T) {
	m := &secRecorder{}
	p := e2eStart(t, "tcp+tls", e2eNewDevice(), e2eOpts{noOpen: true, server: func(c *ServerConfig) {
		c.Metrics = m
		c.TLSHandshakeTimeout = 50 * time.Millisecond
	}})
	raw := dialRaw(t, p.server)
	wantClosedUnanswered(t, raw)
	e2eEventually(t, "the handshake", func() bool { return m.handshakeCount() == 1 })
	_, hs, _, _ := m.snapshot()
	if !errors.Is(hs[0].Err, os.ErrDeadlineExceeded) {
		t.Errorf("err = %v, want os.ErrDeadlineExceeded", hs[0].Err)
	}
}

// OnTLSHandshake that panics is logged, and the connection is served.
func TestTLSHandshakeMetrics_PanicIsContained(t *testing.T) {
	log := &e2eLogger{}
	m := &secRecorder{onHandshake: func(*TLSHandshakeInfo) { panic("handshake hook") }}
	cm := &secRecorder{onHandshake: func(*TLSHandshakeInfo) { panic("client handshake hook") }}
	clog := &e2eLogger{}
	p := e2eStart(t, "tcp+tls", e2eNewDevice(), e2eOpts{
		server: func(c *ServerConfig) { c.Metrics = m; c.Logger = log },
		client: func(c *Config) { c.Metrics = cm; c.Logger = clog },
	})
	if _, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1); err != nil {
		t.Fatalf("read after the panics: %v", err)
	}
	e2eEventually(t, "the server log line", func() bool {
		return log.has("E ", "TLSHandshakeMetrics.OnTLSHandshake for", "did not return normally: handshake hook")
	})
	if !clog.has("E ", "TLSHandshakeMetrics.OnTLSHandshake did not return normally: client handshake hook") {
		t.Error("the client's panic was not logged")
	}
}

// Stop called from OnTLSHandshake returns, and the connection is not served.
func TestTLSHandshakeMetrics_StopFromCallback(t *testing.T) {
	stopped := make(chan error, 1)
	var ms *Server
	m := &secRecorder{}
	m.onHandshake = func(*TLSHandshakeInfo) { stopped <- ms.Stop() }
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{noOpen: true, server: func(c *ServerConfig) { c.Metrics = m }})
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
		t.Fatal("Stop from OnTLSHandshake did not return")
	}
	e2eEventually(t, "the connection to be dropped", func() bool { return p.conns() == 0 })
	events, _, _, _ := m.snapshot()
	if len(events) != 1 || events[0] != "handshake" {
		t.Errorf("events %v: want the handshake alone", events)
	}
	if dev.callCount() != 0 {
		t.Error("the connection was served after Stop")
	}
}

// The client reports its handshakes: completed with the server's verified
// certificate, and failed when it does not trust it.
func TestTLSHandshakeMetrics_Client(t *testing.T) {
	pki := e2ePKI(t)
	m := &secRecorder{}
	p := e2eStart(t, "tcp+tls", e2eNewDevice(), e2eOpts{client: func(c *Config) { c.Metrics = m }})
	if m.handshakeCount() != 1 {
		t.Fatalf("%d handshakes reported after Open, want 1", m.handshakeCount())
	}
	_, hs, _, _ := m.snapshot()
	h := hs[0]
	if h.Err != nil || !h.Verified || h.RemoteAddr != p.hostPort || h.Role != "" || h.RoleErr != nil {
		t.Errorf("handshake %+v: want completed and verified, to %s, without a role", h, p.hostPort)
	}
	if len(h.PeerCertificates) == 0 || h.PeerCertificates[0].Subject.CommonName != "e2e server" {
		t.Errorf("peer certificates %v, want the server's", h.PeerCertificates)
	}

	bad := &secRecorder{}
	c := p.newClient(func(conf *Config) {
		conf.Metrics = bad
		conf.TLSRootCAs = pki.otherCA.pool
	})
	err := c.Open()
	if err == nil {
		t.Fatal("Open with an untrusted server certificate succeeded")
	}
	_, hs, _, _ = bad.snapshot()
	if len(hs) != 1 || hs[0].Err == nil || hs[0].Verified || !errors.Is(err, hs[0].Err) {
		t.Errorf("handshakes %+v: want one failure, the error Open returned (%v)", hs, err)
	}
}

// The violations of the framing and of the structure of a function code are
// reported with what the server read and did; ordinary refusals are not.
func TestProtocolViolationMetrics(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame []byte
		want  ProtocolViolation
		match error
		// answered: the server answers before it closes (or keeps) the connection.
		answered bool
	}{
		{"protocol id", []byte{0, 7, 0, 1, 0, 6, 9, 3, 0, 0, 0, 1},
			ProtocolViolation{TransactionID: 7, ProtocolID: 1, Length: 6, UnitID: 9, Closed: true}, ErrUnknownProtocolID, false},
		{"mbap length", []byte{0, 8, 0, 0, 1, 0, 9},
			ProtocolViolation{TransactionID: 8, Length: 256, UnitID: 9, Closed: true}, ErrInvalidMBAPLength, false},
		{"fixed-size request too short", rawRequestFrame(9, 1, byte(FCReadHoldingRegisters), []byte{0, 0, 0}),
			ProtocolViolation{TransactionID: 9, Length: 5, UnitID: 1, FunctionCode: FCReadHoldingRegisters, HasFunctionCode: true,
				Exception: exIllegalDataValue, Closed: true}, ErrIllegalDataValue, true},
		{"byte count disagrees with the length", rawRequestFrame(10, 1, byte(FCWriteMultipleRegisters), []byte{0, 0, 0, 1, 2, 0, 1, 0xFF}),
			ProtocolViolation{TransactionID: 10, Length: 10, UnitID: 1, FunctionCode: FCWriteMultipleRegisters, HasFunctionCode: true,
				Exception: exIllegalDataValue, Closed: true}, ErrProtocolError, true},
		{"diagnostics data of odd length", rawRequestFrame(11, 1, byte(FCDiagnostics), []byte{0, 0, 1}),
			ProtocolViolation{TransactionID: 11, Length: 5, UnitID: 1, FunctionCode: FCDiagnostics, HasFunctionCode: true,
				Exception: exIllegalDataValue}, ErrProtocolError, true},
		{"exception function code", rawRequestFrame(12, 1, 0x83, []byte{0, 0, 0, 1}),
			ProtocolViolation{TransactionID: 12, Length: 6, UnitID: 1, FunctionCode: 0x83, HasFunctionCode: true}, ErrIllegalFunction, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &secRecorder{}
			p := e2eStart(t, "tcp", e2eNewDevice(), e2eOpts{noOpen: true, server: func(c *ServerConfig) { c.Metrics = m }})
			conn := dialRaw(t, p.server)
			if _, err := conn.Write(tc.frame); err != nil {
				t.Fatal(err)
			}
			if tc.want.Closed && !tc.answered {
				wantClosedUnanswered(t, conn)
			}
			e2eEventually(t, "the violation", func() bool { _, _, vs, _ := m.snapshot(); return len(vs) == 1 })
			if tc.want.Closed {
				e2eEventually(t, "the close", func() bool {
					ev, _, _, _ := m.snapshot()
					return len(ev) > 0 && strings.HasPrefix(ev[len(ev)-1], "reason")
				})
			}
			events, _, vs, _ := m.snapshot()
			got := vs[0]
			if !errors.Is(got.Err, tc.match) {
				t.Errorf("err = %v, want %v", got.Err, tc.match)
			}
			want := tc.want
			want.ClientAddr, want.Err = conn.LocalAddr().String(), got.Err
			if got != want {
				t.Errorf("violation\n got %+v\nwant %+v", got, want)
			}
			// After the request's OnError, before the close.
			wantOrder := []string{"opened ", "violation"}
			if tc.want.HasFunctionCode {
				wantOrder = []string{"opened ", "error", "violation"}
			}
			if tc.want.Closed {
				wantOrder = append(wantOrder, "closed", "reason "+map[bool]string{true: "protocol error", false: "malformed frame"}[tc.want.HasFunctionCode])
			}
			if strings.Join(events, ",") != strings.Join(wantOrder, ",") {
				t.Errorf("events %v, want %v", events, wantOrder)
			}
		})
	}
}

// Requests that the server refuses for their values or their function code
// are ordinary outcomes: no violation.
func TestProtocolViolationMetrics_NotForOrdinaryRefusals(t *testing.T) {
	m := &secRecorder{}
	p := e2eStart(t, "tcp", e2eNewDevice(), e2eOpts{noOpen: true, server: func(c *ServerConfig) { c.Metrics = m }})
	conn := dialRaw(t, p.server)
	for i, frame := range [][]byte{
		rawRequestFrame(1, 1, byte(FCReadHoldingRegisters), u16(0, 0)),      // quantity 0
		rawRequestFrame(2, 1, byte(FCReadHoldingRegisters), u16(0xFFFF, 2)), // beyond 0xFFFF
		rawRequestFrame(3, 1, 0x41, nil),                                    // a function code not served
	} {
		if _, err := conn.Write(frame); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 9)
		_ = conn.SetReadDeadline(time.Now().Add(e2eWait))
		if _, err := io.ReadFull(conn, buf); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if _, _, vs, _ := m.snapshot(); len(vs) != 0 {
		t.Errorf("violations %+v for ordinary refusals", vs)
	}
}

// A violation hook that panics is logged; one that stops the server ends the
// connection's callbacks.
func TestProtocolViolationMetrics_PanicAndStop(t *testing.T) {
	log := &e2eLogger{}
	pm := &panicViolations{}
	p := e2eStart(t, "tcp", e2eNewDevice(), e2eOpts{noOpen: true, server: func(c *ServerConfig) { c.Metrics = pm; c.Logger = log }})
	conn := dialRaw(t, p.server)
	if _, err := conn.Write([]byte{0, 1, 0, 1, 0, 2, 1, 3}); err != nil {
		t.Fatal(err)
	}
	wantClosedUnanswered(t, conn)
	e2eEventually(t, "the log line", func() bool {
		return log.has("E ", "ProtocolViolationMetrics.OnProtocolViolation for", "did not return normally: violation hook")
	})

	stopped := make(chan error, 1)
	sm := &stopViolations{}
	p2 := e2eStart(t, "tcp", e2eNewDevice(), e2eOpts{noOpen: true, server: func(c *ServerConfig) { c.Metrics = sm }})
	sm.stop = func() { stopped <- p2.server.Stop() }
	conn2 := dialRaw(t, p2.server)
	if _, err := conn2.Write([]byte{0, 1, 0, 1, 0, 2, 1, 3}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(e2eWait):
		t.Fatal("Stop from OnProtocolViolation did not return")
	}
	time.Sleep(20 * time.Millisecond)
	if events := sm.snapshot(); strings.Join(events, ",") != "opened ,violation" {
		t.Errorf("events %v: want no callback after the Stop", events)
	}
}

type panicViolations struct{ secRecorder }

func (*panicViolations) OnProtocolViolation(ProtocolViolation) { panic("violation hook") }

type stopViolations struct {
	secRecorder
	stop func()
}

func (s *stopViolations) OnProtocolViolation(v ProtocolViolation) {
	s.secRecorder.OnProtocolViolation(v)
	s.stop()
}

func (s *stopViolations) snapshot() []string {
	ev, _, _, _ := s.secRecorder.snapshot()
	return ev
}

// Accept errors are reported with the wait before the next attempt; a Stop
// from the hook ends the accept loop.
func TestAcceptErrorMetrics(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := &secRecorder{}
		ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Metrics: m, Logger: NopLogger()}, &fnHandler{})
		if err != nil {
			t.Fatal(err)
		}
		acceptErr := errors.New("accept tcp: too many open files")
		ln := &scriptedListener{script: []error{acceptErr, acceptErr}}
		run := &serverRun{listener: ln, wg: &sync.WaitGroup{}}
		run.ctx, run.cancel = context.WithCancel(context.Background())
		defer run.cancel()
		run.wg.Add(1)
		ms.acceptTCPClients(run)
		m.mu.Lock()
		got := append([]error(nil), m.accepts...)
		m.mu.Unlock()
		if len(got) != 2 || got[0] != acceptErr || got[1] != acceptErr {
			t.Errorf("accept errors %v, want the two of the listener", got)
		}
	})
}

// An accept error hook that stops the server ends the accept loop at once.
func TestAcceptErrorMetrics_StopFromHook(t *testing.T) {
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Logger: NopLogger()}, &fnHandler{})
	if err != nil {
		t.Fatal(err)
	}
	ln := &scriptedListener{repeat: errors.New("accept failed"), limit: 1000}
	ms.acceptMetrics = acceptStopper{ms: ms}
	ms.started = true
	run := &serverRun{listener: ln, wg: &sync.WaitGroup{}}
	run.ctx, run.cancel = context.WithCancel(context.Background())
	ms.run = run
	run.wg.Add(1)
	done := make(chan struct{})
	go func() { ms.acceptTCPClients(run); close(done) }()
	select {
	case <-done:
	case <-time.After(e2eWait):
		t.Fatal("the accept loop went on after a Stop from OnAcceptError")
	}
	if n := ln.count(); n != 1 {
		t.Errorf("%d Accept calls, want 1", n)
	}
}

type acceptStopper struct{ ms *Server }

func (a acceptStopper) OnAcceptError(error, time.Duration) { _ = a.ms.Stop() }

// The client reports the frames it skips and the bytes it discards.
func TestResponseAnomalyMetrics_MBAP(t *testing.T) {
	m := &secRecorder{}
	c, srv := scriptedMBAPClient(t, func(conf *Config) { conf.Metrics = m })
	go func() {
		hdr := make([]byte, 12)
		if _, err := io.ReadFull(srv, hdr); err != nil {
			return
		}
		txn := uint16(hdr[0])<<8 | uint16(hdr[1])
		// A late answer, a frame of another protocol, then the answer.
		_, _ = srv.Write(rawRequestFrame(txn-1, 1, 3, []byte{2, 0, 9}))
		_, _ = srv.Write([]byte{hdr[0], hdr[1], 0, 5, 0, 5, 1, 3, 2, 0, 8})
		_, _ = srv.Write(rawRequestFrame(txn, 1, 3, []byte{2, 0, 7}))
	}()
	v, err := c.ReadHoldingRegister(t.Context(), 1, 0)
	if err != nil || v != 7 {
		t.Fatalf("read = %d, %v; want 7", v, err)
	}
	_, _, _, as := m.snapshot()
	if len(as) != 2 {
		t.Fatalf("anomalies %+v, want 2", as)
	}
	want := ResponseAnomaly{Kind: AnomalyTransactionID, UnitID: 1, FunctionCode: FCReadHoldingRegisters,
		ExpectedTransactionID: as[0].ExpectedTransactionID, TransactionID: as[0].ExpectedTransactionID - 1}
	if as[0] != want {
		t.Errorf("first anomaly %+v, want %+v", as[0], want)
	}
	want = ResponseAnomaly{Kind: AnomalyProtocolID, UnitID: 1, FunctionCode: FCReadHoldingRegisters,
		ExpectedTransactionID: as[0].ExpectedTransactionID, TransactionID: as[0].ExpectedTransactionID, ProtocolID: 5}
	if as[1] != want {
		t.Errorf("second anomaly %+v, want %+v", as[1], want)
	}
}

func TestResponseAnomalyKindString(t *testing.T) {
	for k, want := range map[ResponseAnomalyKind]string{
		AnomalyTransactionID: "transaction id", AnomalyProtocolID: "protocol id",
		AnomalyDiscarded: "discarded", 0: "ResponseAnomalyKind(0)", 9: "ResponseAnomalyKind(9)",
	} {
		if got := k.String(); got != want {
			t.Errorf("%d: %q, want %q", k, got, want)
		}
	}
}

// Without the optional interfaces nothing is reported and nothing is built.
func TestSecurityMetrics_AbsentInterfaces(t *testing.T) {
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Metrics: &countingServerMetrics{}}, &fnHandler{})
	if err != nil {
		t.Fatal(err)
	}
	if ms.tlsMetrics != nil || ms.acceptMetrics != nil || ms.violationMetrics != nil {
		t.Error("optional interfaces found on a value that does not implement them")
	}
	c, err := New(Config{URL: "tcp://127.0.0.1:502", Metrics: &countingServerMetricsClient{}})
	if err != nil {
		t.Fatal(err)
	}
	if c.tlsMetrics != nil || c.anomalyMetrics != nil || c.anomalyFunc() != nil {
		t.Error("optional client interfaces found on a value that does not implement them")
	}
}

type countingServerMetrics struct{}

func (countingServerMetrics) OnRequest(uint8, FunctionCode)                     {}
func (countingServerMetrics) OnResponse(uint8, FunctionCode, time.Duration)     {}
func (countingServerMetrics) OnError(uint8, FunctionCode, time.Duration, error) {}

type countingServerMetricsClient struct{ countingServerMetrics }

func (countingServerMetricsClient) OnTimeout(uint8, FunctionCode, time.Duration) {}

// scriptedMBAPClient returns an open tcp client whose connection is the client
// end of a pipe, and the server end.
func scriptedMBAPClient(t *testing.T, mut func(*Config)) (*Client, net.Conn) {
	t.Helper()
	cli, srv := net.Pipe()
	t.Cleanup(func() { _ = srv.Close() })
	conf := Config{URL: "tcp://scripted:502", Timeout: e2eWait, Dialer: func(context.Context, string, string) (net.Conn, error) {
		return cli, nil
	}}
	if mut != nil {
		mut(&conf)
	}
	c, err := New(conf)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Open(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, srv
}

// errorLogger records what is logged at error level.
type errorLogger struct {
	mu     sync.Mutex
	errors []string
}

func (l *errorLogger) Debugf(string, ...any) {}
func (l *errorLogger) Infof(string, ...any)  {}
func (l *errorLogger) Warnf(string, ...any)  {}
func (l *errorLogger) Errorf(format string, args ...any) {
	l.mu.Lock()
	l.errors = append(l.errors, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *errorLogger) logged(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.errors {
		if strings.Contains(e, sub) {
			return true
		}
	}
	return false
}

type panickingAcceptMetrics struct{}

func (panickingAcceptMetrics) OnAcceptError(error, time.Duration) { panic("accept hook failed") }

// An accept error hook that panics is logged with its name, and the server
// goes on; without the hook nothing is called.
func TestAcceptErrorMetrics_PanicIsContained(t *testing.T) {
	logs := &errorLogger{}
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Logger: logs}, &fnHandler{})
	if err != nil {
		t.Fatal(err)
	}
	ms.reportAcceptError(errors.New("accept failed"), time.Millisecond) // no hook: nothing happens
	ms.acceptMetrics = panickingAcceptMetrics{}
	ms.reportAcceptError(errors.New("accept failed"), time.Millisecond)
	if !logs.logged("AcceptErrorMetrics.OnAcceptError did not return normally") {
		t.Errorf("panic not logged; logged %q", logs.errors)
	}
}

// A handshake that could not be started (the deadline of the accepted socket
// could not be set) is reported with the socket's addresses and its error,
// and never with a role.
func TestReportTLSHandshake_NotStarted(t *testing.T) {
	m := &secRecorder{}
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Logger: NopLogger()}, &fnHandler{})
	if err != nil {
		t.Fatal(err)
	}
	a, b := net.Pipe()
	defer func() { _ = a.Close(); _ = b.Close() }()
	startErr := errors.New("set deadline failed")
	ms.reportTLSHandshake(a, nil, 0, "operator", nil, startErr) // no hook: nothing happens
	ms.tlsMetrics = m
	ms.reportTLSHandshake(a, nil, time.Millisecond, "operator", nil, startErr)
	_, hs, _, _ := m.snapshot()
	if len(hs) != 1 {
		t.Fatalf("%d handshakes reported, want 1", len(hs))
	}
	got := hs[0]
	if got.Err != startErr || got.RemoteAddr != a.RemoteAddr().String() || got.LocalAddr != a.LocalAddr().String() ||
		got.Duration != time.Millisecond || got.Role != "" || got.Verified || len(got.PeerCertificates) != 0 {
		t.Errorf("handshake %+v, want the socket's addresses, the error and no role", got)
	}
}

// Violations are not built without the hook.
func TestReportRequestViolation_NoHook(t *testing.T) {
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Logger: NopLogger()}, &fnHandler{})
	if err != nil {
		t.Fatal(err)
	}
	ms.reportRequestViolation(&adu.Request{UnitID: 1, FunctionCode: 3}, 1, "addr", "", errors.New("bad"), 3, true)
}

// The certificates handed to TLSHandshakeMetrics are new values parsed from
// copies of the encoding; one that does not parse is left out.
func TestCopyCertificates(t *testing.T) {
	certPEM, keyPEM := newRoleClientCert("operator")
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	orig, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	broken := &x509.Certificate{Raw: []byte{0x30, 0x00}}
	got := copyCertificates([]*x509.Certificate{broken, orig})
	if len(got) != 1 || got[0] == orig || !got[0].Equal(orig) {
		t.Fatalf("copies %v, want one new certificate equal to the original", got)
	}
	got[0].Raw[0] ^= 0xFF
	if orig.Raw[0] == got[0].Raw[0] {
		t.Error("the copy shares its encoding with the original")
	}
	if copyCertificates(nil) != nil {
		t.Error("certificates copied from none")
	}
}

func TestAddrString(t *testing.T) {
	if s := addrString(nil); s != "" {
		t.Errorf("addrString(nil) = %q, want empty", s)
	}
	a := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 502}
	if s := addrString(a); s != "127.0.0.1:502" {
		t.Errorf("addrString = %q", s)
	}
}
