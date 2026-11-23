// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// serverTapRecorder records the exchanges a server taps and calls onPDU for each.
type serverTapRecorder struct {
	mu    sync.Mutex
	exs   []ServerPDUExchange
	ctxs  []context.Context
	onPDU func(*ServerPDUExchange)
}

func (r *serverTapRecorder) OnServerPDU(ctx context.Context, ex ServerPDUExchange) {
	r.mu.Lock()
	r.exs = append(r.exs, ex)
	r.ctxs = append(r.ctxs, ctx)
	r.mu.Unlock()
	if r.onPDU != nil {
		r.onPDU(&ex)
	}
}

func (r *serverTapRecorder) snapshot() []ServerPDUExchange {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ServerPDUExchange(nil), r.exs...)
}

func startTapServer(t *testing.T, h RequestHandler, tap ServerPDUTap, mut func(*ServerConfig)) (*Server, net.Conn) {
	t.Helper()
	ms, conn := startCallbackServer(t, h, func(c *ServerConfig) {
		c.PDUTap = tap
		if mut != nil {
			mut(c)
		}
	})
	t.Cleanup(func() { _ = ms.Stop() })
	return ms, conn
}

func TestServerPDUTap_Exchange(t *testing.T) {
	tap := &serverTapRecorder{}
	_, conn := startTapServer(t, holdingOne(), tap, nil)
	before := time.Now()
	sendReadRequest(t, conn)
	readResponse(t, conn)
	e2eEventually(t, "the exchange", func() bool { return len(tap.snapshot()) == 1 })
	ex := tap.snapshot()[0]
	if ex.ClientAddr != conn.LocalAddr().String() || ex.ClientRole != "" || ex.UnitID != 1 {
		t.Errorf("exchange %+v", ex)
	}
	if want := []byte{byte(FCReadHoldingRegisters), 0, 0, 0, 1}; !bytes.Equal(ex.Request, want) {
		t.Errorf("Request % x, want % x", ex.Request, want)
	}
	if want := []byte{byte(FCReadHoldingRegisters), 2, 0x01, 0x02}; !bytes.Equal(ex.Response, want) {
		t.Errorf("Response % x, want % x", ex.Response, want)
	}
	if ex.Received.Before(before) || ex.Answered.Before(ex.Received) {
		t.Errorf("Received %v, Answered %v, sent after %v", ex.Received, ex.Answered, before)
	}
	tap.mu.Lock()
	ctx := tap.ctxs[0]
	tap.mu.Unlock()
	if ctx == nil {
		t.Error("nil context")
	}
}

// Requests are tapped in the order of the connection, and the server's own
// exception answers are tapped too.
func TestServerPDUTap_OrderAndServerExceptions(t *testing.T) {
	tap := &serverTapRecorder{}
	_, conn := startTapServer(t, holdingOne(), tap, nil)
	var frames []byte
	frames = append(frames, rawRequestFrame(1, 1, byte(FCReadHoldingRegisters), u16(0, 1))...)
	frames = append(frames, rawRequestFrame(2, 1, 0x41, nil)...) // no handler serves 0x41
	frames = append(frames, rawRequestFrame(3, 1, byte(FCReadHoldingRegisters), u16(0, 1))...)
	if _, err := conn.Write(frames); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(e2eWait))
	if _, err := io.ReadFull(conn, make([]byte, 11+9+11)); err != nil {
		t.Fatalf("responses: %v", err)
	}
	e2eEventually(t, "three exchanges", func() bool { return len(tap.snapshot()) == 3 })
	got := tap.snapshot()
	if got[0].Request[0] != byte(FCReadHoldingRegisters) || got[2].Request[0] != byte(FCReadHoldingRegisters) {
		t.Errorf("order: % x, % x, % x", got[0].Request, got[1].Request, got[2].Request)
	}
	// 0x41 | 0x80, Illegal Function (Modbus Application Protocol V1.1b3 § 7).
	if want := []byte{0xC1, 0x01}; !bytes.Equal(got[1].Response, want) {
		t.Errorf("exception Response % x, want % x", got[1].Response, want)
	}
}

// No response: a handler that returns ErrNoResponse or ErrProtocolError is
// tapped with a nil Response. A request whose MBAP length disagrees with its
// function code is tapped with the exception it is answered with and, as its
// Request, the function code alone: the rest was not read.
func TestServerPDUTap_NoResponse(t *testing.T) {
	t.Run("ErrNoResponse", func(t *testing.T) {
		tap := &serverTapRecorder{}
		h := &fnHandler{holding: func(*HoldingRegistersRequest) ([]uint16, error) { return nil, ErrNoResponse }}
		_, conn := startTapServer(t, h, tap, nil)
		sendReadRequest(t, conn)
		e2eEventually(t, "the exchange", func() bool { return len(tap.snapshot()) == 1 })
		if ex := tap.snapshot()[0]; ex.Response != nil || len(ex.Request) != 5 {
			t.Errorf("exchange %+v, want the request and no response", ex)
		}
	})
	t.Run("handler protocol error", func(t *testing.T) {
		tap := &serverTapRecorder{}
		h := &fnHandler{holding: func(*HoldingRegistersRequest) ([]uint16, error) { return nil, ErrProtocolError }}
		_, conn := startTapServer(t, h, tap, nil)
		sendReadRequest(t, conn)
		wantClosedUnanswered(t, conn)
		e2eEventually(t, "the exchange", func() bool { return len(tap.snapshot()) == 1 })
		if ex := tap.snapshot()[0]; ex.Response != nil || len(ex.Request) != 5 {
			t.Errorf("exchange %+v, want the request and no response", ex)
		}
	})
	t.Run("MBAP length error", func(t *testing.T) {
		tap := &serverTapRecorder{}
		_, conn := startTapServer(t, holdingOne(), tap, nil)
		if _, err := conn.Write(rawRequestFrame(1, 1, byte(FCReadHoldingRegisters), []byte{0, 0, 0})); err != nil {
			t.Fatal(err)
		}
		wantAnsweredThenClosed(t, conn, rawRequestFrame(1, 1, 0x83, []byte{byte(exIllegalDataValue)}))
		e2eEventually(t, "the exchange", func() bool { return len(tap.snapshot()) == 1 })
		if ex := tap.snapshot()[0]; !bytes.Equal(ex.Response, []byte{0x83, 0x03}) || !bytes.Equal(ex.Request, []byte{3}) {
			t.Errorf("exchange %+v, want the function code and the exception", ex)
		}
	})
}

// The exchange's slices are the tap's: changing them does not change what the
// server sends.
func TestServerPDUTap_OwnsCopies(t *testing.T) {
	tap := &serverTapRecorder{onPDU: func(ex *ServerPDUExchange) {
		for i := range ex.Response {
			ex.Response[i] = 0xEE
		}
		for i := range ex.Request {
			ex.Request[i] = 0xEE
		}
	}}
	_, conn := startTapServer(t, holdingOne(), tap, nil)
	sendReadRequest(t, conn)
	_ = conn.SetReadDeadline(time.Now().Add(e2eWait))
	resp := make([]byte, 11)
	if _, err := io.ReadFull(conn, resp); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(resp[7:], []byte{byte(FCReadHoldingRegisters), 2, 0x01, 0x02}) {
		t.Errorf("response % x changed by the tap", resp)
	}
}

// A tap that panics is logged; the response is sent as if it had returned.
func TestServerPDUTap_Panics(t *testing.T) {
	tap := ServerPDUTapFunc(func(context.Context, ServerPDUExchange) { panic("recorder full") })
	_, conn := startTapServer(t, holdingOne(), tap, func(c *ServerConfig) { c.Logger = NopLogger() })
	for range 2 {
		sendReadRequest(t, conn)
		readResponse(t, conn)
	}
}

// A Stop called from the tap returns; the response is not written and nothing
// more is reported on the connection.
func TestServerPDUTap_StopFromTap(t *testing.T) {
	stopped := make(chan error, 1)
	m := &closeMetrics{}
	var ms *Server
	tap := &serverTapRecorder{onPDU: func(*ServerPDUExchange) { stopped <- ms.Stop() }}
	ms, conn := startTapServer(t, holdingOne(), tap, func(c *ServerConfig) { c.Metrics = m })
	sendReadRequest(t, conn)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop from the tap: %v", err)
		}
	case <-time.After(e2eWait):
		t.Fatal("Stop from the tap did not return")
	}
	wantClosedUnanswered(t, conn)
	time.Sleep(50 * time.Millisecond)
	if got := m.eventsSnapshot(); len(got) != 1 {
		t.Errorf("events %v, want only the open", got)
	}
}

// A Shutdown called from the handler leaves the request untapped.
func TestServerPDUTap_NotAfterShutdownFromHandler(t *testing.T) {
	tap := &serverTapRecorder{}
	var ms *Server
	stopped := make(chan error, 1)
	h := &fnHandler{holding: func(*HoldingRegistersRequest) ([]uint16, error) {
		stopped <- ms.Stop()
		return []uint16{1}, nil
	}}
	ms, conn := startTapServer(t, h, tap, nil)
	sendReadRequest(t, conn)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop from the handler: %v", err)
		}
	case <-time.After(e2eWait):
		t.Fatal("Stop from the handler did not return")
	}
	wantClosedUnanswered(t, conn)
	if got := tap.snapshot(); len(got) != 0 {
		t.Errorf("tapped %+v after the Shutdown", got)
	}
}

// The role of a tcp+tls client reaches the tap.
func TestServerPDUTap_TLS(t *testing.T) {
	tap := &serverTapRecorder{}
	dev := e2eNewDevice()
	dev.setHolding(1, 0x0101)
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{server: func(c *ServerConfig) { c.PDUTap = tap }})
	if _, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1); err != nil {
		t.Fatalf("read: %v", err)
	}
	e2eEventually(t, "the exchange", func() bool { return len(tap.snapshot()) == 1 })
	ex := tap.snapshot()[0]
	if ex.ClientAddr == "" || ex.UnitID != e2eUnit || !bytes.Equal(ex.Response, []byte{3, 2, 0x01, 0x01}) {
		t.Errorf("exchange %+v", ex)
	}
}
