// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"
)

// closeEvent is one call of OnConnectionCloseReason.
type closeEvent struct {
	addr   string
	reason CloseReason
	err    error
}

// closeMetrics records the connections opened, closed and the reasons they
// were closed for, in order, and calls onReason for each reason.
type closeMetrics struct {
	lifecycleMetrics
	cmu      sync.Mutex
	reasons  []closeEvent
	onReason func(closeEvent)
}

func (m *closeMetrics) OnConnectionCloseReason(addr string, reason CloseReason, err error) {
	m.evMu.Lock()
	m.events = append(m.events, "reason "+addr+" "+reason.String())
	m.evMu.Unlock()
	m.cmu.Lock()
	m.reasons = append(m.reasons, closeEvent{addr, reason, err})
	m.cmu.Unlock()
	if m.onReason != nil {
		m.onReason(closeEvent{addr, reason, err})
	}
}

func (m *closeMetrics) reasonsSnapshot() []closeEvent {
	m.cmu.Lock()
	defer m.cmu.Unlock()
	return append([]closeEvent(nil), m.reasons...)
}

// wantReason waits for the one close reason of addr and checks it and its
// error.
func wantReason(t *testing.T, m *closeMetrics, addr string, reason CloseReason, want error) {
	t.Helper()
	e2eEventually(t, "the close reason", func() bool { return len(m.reasonsSnapshot()) > 0 })
	got := m.reasonsSnapshot()
	if len(got) != 1 || got[0].addr != addr || got[0].reason != reason {
		t.Fatalf("close reasons %+v, want one %v for %s", got, reason, addr)
	}
	if !errors.Is(got[0].err, want) {
		t.Errorf("err = %v, want %v", got[0].err, want)
	}
	ev := m.eventsSnapshot()
	if n := len(ev); n < 2 || ev[n-2] != "closed "+addr || ev[n-1] != "reason "+addr+" "+reason.String() {
		t.Errorf("events %v: want the reason right after the close", ev)
	}
}

func startCloseServer(t *testing.T, m *closeMetrics, mut func(*ServerConfig)) (*Server, net.Conn) {
	t.Helper()
	ms, conn := startCallbackServer(t, holdingOne(), func(c *ServerConfig) {
		c.Metrics = m
		if mut != nil {
			mut(c)
		}
	})
	t.Cleanup(func() { _ = ms.Stop() })
	return ms, conn
}

func readResponse(t *testing.T, conn net.Conn) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(e2eWait))
	if _, err := io.ReadFull(conn, make([]byte, 11)); err != nil {
		t.Fatalf("response: %v", err)
	}
}

func TestServerCloseReason_ClientClosed(t *testing.T) {
	m := &closeMetrics{}
	_, conn := startCloseServer(t, m, nil)
	sendReadRequest(t, conn)
	readResponse(t, conn)
	addr := conn.LocalAddr().String()
	_ = conn.Close()
	wantReason(t, m, addr, CloseClientClosed, io.EOF)
}

func TestServerCloseReason_ClientClosedMidRequest(t *testing.T) {
	m := &closeMetrics{}
	_, conn := startCloseServer(t, m, nil)
	frame := rawRequestFrame(1, 1, byte(FCReadHoldingRegisters), u16(0, 1))
	if _, err := conn.Write(frame[:5]); err != nil {
		t.Fatal(err)
	}
	addr := conn.LocalAddr().String()
	time.Sleep(50 * time.Millisecond) // let the server start reading the header
	_ = conn.Close()
	wantReason(t, m, addr, CloseClientClosed, io.ErrUnexpectedEOF)
}

func TestServerCloseReason_IdleTimeout(t *testing.T) {
	m := &closeMetrics{}
	_, conn := startCloseServer(t, m, func(c *ServerConfig) { c.Timeout = 100 * time.Millisecond })
	wantClosedUnanswered(t, conn)
	wantReason(t, m, conn.LocalAddr().String(), CloseIdleTimeout, os.ErrDeadlineExceeded)
}

func TestServerCloseReason_MalformedFrame(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame []byte
		want  error
	}{
		// Protocol identifier 1, length 6.
		{"protocol id", []byte{0, 1, 0, 1, 0, 6, 1, 3, 0, 0, 0, 1}, ErrUnknownProtocolID},
		// Length 0.
		{"length", []byte{0, 1, 0, 0, 0, 0, 1}, ErrInvalidMBAPLength},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &closeMetrics{}
			tap := &serverTapRecorder{}
			_, conn := startCloseServer(t, m, func(c *ServerConfig) { c.PDUTap = tap })
			if _, err := conn.Write(tc.frame); err != nil {
				t.Fatal(err)
			}
			wantClosedUnanswered(t, conn)
			wantReason(t, m, conn.LocalAddr().String(), CloseMalformedFrame, tc.want)
			if got := tap.snapshot(); len(got) != 0 {
				t.Errorf("an unframed request was tapped: %+v", got)
			}
		})
	}
}

func TestServerCloseReason_ProtocolError(t *testing.T) {
	m := &closeMetrics{}
	_, conn := startCloseServer(t, m, nil)
	// FC03 with three data bytes instead of four: answered with Illegal Data
	// Value, then closed.
	if _, err := conn.Write(rawRequestFrame(1, 1, byte(FCReadHoldingRegisters), []byte{0, 0, 0})); err != nil {
		t.Fatal(err)
	}
	wantAnsweredThenClosed(t, conn, rawRequestFrame(1, 1, 0x83, []byte{byte(exIllegalDataValue)}))
	wantReason(t, m, conn.LocalAddr().String(), CloseProtocolError, ErrProtocolError)
}

// A handler that returns ErrProtocolError: closed without an answer.
func TestServerCloseReason_HandlerProtocolError(t *testing.T) {
	m := &closeMetrics{}
	h := &fnHandler{holding: func(*HoldingRegistersRequest) ([]uint16, error) { return nil, ErrProtocolError }}
	_, conn := startCallbackServer(t, h, func(c *ServerConfig) { c.Metrics = m })
	sendReadRequest(t, conn)
	wantClosedUnanswered(t, conn)
	wantReason(t, m, conn.LocalAddr().String(), CloseProtocolError, ErrProtocolError)
}

func TestServerCloseReason_ReadFailed(t *testing.T) {
	m := &closeMetrics{}
	_, conn := startCloseServer(t, m, nil)
	sendReadRequest(t, conn)
	readResponse(t, conn)
	addr := conn.LocalAddr().String()
	// A close with a zero linger time resets the connection.
	if err := conn.(*net.TCPConn).SetLinger(0); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	e2eEventually(t, "the close reason", func() bool { return len(m.reasonsSnapshot()) > 0 })
	got := m.reasonsSnapshot()
	if len(got) != 1 || got[0].addr != addr || got[0].reason != CloseReadFailed || got[0].err == nil {
		t.Fatalf("close reasons %+v, want one read failed with its error", got)
	}
}

func TestServerCloseReason_ServerStopped(t *testing.T) {
	m := &closeMetrics{}
	ms, conn := startCloseServer(t, m, nil)
	sendReadRequest(t, conn)
	readResponse(t, conn)
	if err := ms.Stop(); err != nil {
		t.Fatal(err)
	}
	// Reported before Stop returns.
	got := m.reasonsSnapshot()
	if len(got) != 1 || got[0].reason != CloseServerStopped || got[0].err == nil {
		t.Fatalf("close reasons %+v, want one server stopped with the socket's error", got)
	}
}

func TestServerCloseReason_TLS(t *testing.T) {
	m := &closeMetrics{}
	dev := e2eNewDevice()
	dev.setHolding(1, 0x0101)
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{server: func(c *ServerConfig) { c.Metrics = m }})
	if _, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1); err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := p.client.Close(); err != nil {
		t.Fatal(err)
	}
	e2eEventually(t, "the close reason", func() bool { return len(m.reasonsSnapshot()) > 0 })
	if got := m.reasonsSnapshot(); len(got) != 1 || got[0].reason != CloseClientClosed {
		t.Fatalf("close reasons %+v, want one client closed", got)
	}
}

// A connection whose TLS handshake fails was never opened, so it has no close
// reason either.
func TestServerCloseReason_TLSHandshakeFailedNotReported(t *testing.T) {
	m := &closeMetrics{}
	p := e2eStart(t, "tcp+tls", e2eNewDevice(), e2eOpts{noOpen: true, server: func(c *ServerConfig) { c.Metrics = m }})
	raw := dialRaw(t, p.server)
	if _, err := raw.Write([]byte("not a TLS client hello, not at all")); err != nil {
		t.Fatal(err)
	}
	wantClosedUnanswered(t, raw)
	if err := p.server.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := m.eventsSnapshot(); len(got) != 0 {
		t.Errorf("events %v for a connection that never completed its handshake", got)
	}
}

// A Stop called from OnConnectionClosed returns, and OnConnectionCloseReason is
// not called after it.
func TestServerCloseReason_StopFromClosed(t *testing.T) {
	stopped := make(chan error, 1)
	m := &stopOnClose{stopped: stopped}
	var ms *Server
	var conn net.Conn
	ms, conn = startCallbackServer(t, holdingOne(), func(c *ServerConfig) { c.Metrics = m })
	m.ms = ms
	sendReadRequest(t, conn)
	readResponse(t, conn)
	_ = conn.Close()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop from OnConnectionClosed: %v", err)
		}
	case <-time.After(e2eWait):
		t.Fatal("Stop from OnConnectionClosed did not return")
	}
	time.Sleep(50 * time.Millisecond)
	if got := m.reasonsSnapshot(); len(got) != 0 {
		t.Errorf("close reasons %+v after the Shutdown", got)
	}
}

type stopOnClose struct {
	closeMetrics
	ms      *Server
	stopped chan error
}

func (m *stopOnClose) OnConnectionClosed(addr string) {
	m.closeMetrics.OnConnectionClosed(addr)
	m.stopped <- m.ms.Stop()
}

// An OnConnectionCloseReason that panics is logged; the server goes on.
func TestServerCloseReason_Panics(t *testing.T) {
	m := &closeMetrics{onReason: func(closeEvent) { panic("metrics backend down") }}
	ms, first := startCloseServer(t, m, func(c *ServerConfig) { c.Logger = NopLogger() })
	_ = first.Close()
	e2eEventually(t, "the first close reason", func() bool { return len(m.reasonsSnapshot()) == 1 })
	second := dialRaw(t, ms)
	sendReadRequest(t, second)
	readResponse(t, second)
}

func TestCloseReasonString(t *testing.T) {
	for r, want := range map[CloseReason]string{
		CloseClientClosed:   "client closed",
		CloseIdleTimeout:    "idle timeout",
		CloseMalformedFrame: "malformed frame",
		CloseProtocolError:  "protocol error",
		CloseWriteFailed:    "write failed",
		CloseReadFailed:     "read failed",
		CloseServerStopped:  "server stopped",
		0:                   "CloseReason(0)",
		99:                  "CloseReason(99)",
	} {
		if got := r.String(); got != want {
			t.Errorf("CloseReason(%d).String() = %q, want %q", r, got, want)
		}
	}
}

// The classification of the error that ended a connection's reader or a
// response write.
func TestCloseReasonClassification(t *testing.T) {
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	running := context.Background()
	reset := errors.New("connection reset by peer")
	for _, tc := range []struct {
		name   string
		reason CloseReason
		err    error
		got    func() (CloseReason, error)
	}{
		{"read stopped", CloseServerStopped, net.ErrClosed, func() (CloseReason, error) { return readCloseReason(stopped, net.ErrClosed) }},
		{"read eof", CloseClientClosed, io.EOF, func() (CloseReason, error) { return readCloseReason(running, io.EOF) }},
		{"read unexpected eof", CloseClientClosed, io.ErrUnexpectedEOF, func() (CloseReason, error) { return readCloseReason(running, io.ErrUnexpectedEOF) }},
		{"read deadline", CloseIdleTimeout, os.ErrDeadlineExceeded, func() (CloseReason, error) {
			return readCloseReason(running, fmt.Errorf("read: %w", os.ErrDeadlineExceeded))
		}},
		{"read mbap length", CloseMalformedFrame, ErrInvalidMBAPLength, func() (CloseReason, error) { return readCloseReason(running, ErrInvalidMBAPLength) }},
		{"read protocol id", CloseMalformedFrame, ErrUnknownProtocolID, func() (CloseReason, error) { return readCloseReason(running, ErrUnknownProtocolID) }},
		{"read other", CloseReadFailed, reset, func() (CloseReason, error) { return readCloseReason(running, reset) }},
		{"write stopped", CloseServerStopped, net.ErrClosed, func() (CloseReason, error) { return writeCloseReason(stopped, net.ErrClosed) }},
		{"write failed", CloseWriteFailed, os.ErrDeadlineExceeded, func() (CloseReason, error) {
			return writeCloseReason(running, os.ErrDeadlineExceeded)
		}},
	} {
		reason, err := tc.got()
		if reason != tc.reason || !errors.Is(err, tc.err) || (tc.err == nil) != (err == nil) {
			t.Errorf("%s: %v, %v; want %v, %v", tc.name, reason, err, tc.reason, tc.err)
		}
	}
}

// The first cause set is kept.
func TestCloseCauseFirstWins(t *testing.T) {
	var c closeCause
	c.set(CloseProtocolError, ErrProtocolError)
	c.set(CloseReadFailed, net.ErrClosed)
	if r, err := c.get(); r != CloseProtocolError || err != ErrProtocolError {
		t.Errorf("got %v, %v", r, err)
	}
}
