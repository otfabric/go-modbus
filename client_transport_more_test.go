// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// rtuReadHoldingRequest is the RTU frame for "unit 0x11, FC03, addr 0x006B, qty 2".
var rtuReadHoldingRequest = adu.AssembleRTUFrame(0x11, 0x03, []byte{0x00, 0x6B, 0x00, 0x02})

// startRTUOverTCPSlave accepts connections and answers each 8-byte RTU
// request with respond's result. It records every request frame it receives.
func startRTUOverTCPSlave(t *testing.T, respond func(frame []byte) []byte) (addr string, frames func() [][]byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var mu sync.Mutex
	var seen [][]byte
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				for {
					frame := make([]byte, 8)
					if _, err := io.ReadFull(conn, frame); err != nil {
						return
					}
					mu.Lock()
					seen = append(seen, frame)
					mu.Unlock()
					if _, err := conn.Write(respond(frame)); err != nil {
						return
					}
				}
			}()
		}
	}()
	return ln.Addr().String(), func() [][]byte {
		mu.Lock()
		defer mu.Unlock()
		return append([][]byte(nil), seen...)
	}
}

func TestRTUOverTCPClient_RoundTrip(t *testing.T) {
	addr, frames := startRTUOverTCPSlave(t, func([]byte) []byte {
		return adu.AssembleRTUFrame(0x11, 0x03, []byte{0x04, 0xAE, 0x41, 0x56, 0x52})
	})
	c, err := New(Config{URL: "rtuovertcp://" + addr, Speed: 115200, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = c.Close() }()

	regs, err := c.ReadRegisters(context.Background(), 0x11, 0x006B, 2, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisters: %v", err)
	}
	if len(regs) != 2 || regs[0] != 0xAE41 || regs[1] != 0x5652 {
		t.Fatalf("regs = %#v, want [0xAE41 0x5652]", regs)
	}
	got := frames()
	if len(got) != 1 || !bytes.Equal(got[0], rtuReadHoldingRequest) {
		t.Fatalf("frames on the wire % X, want % X", got, rtuReadHoldingRequest)
	}
	if id := c.LastObservedTransactionID(); id != 0 {
		t.Errorf("LastObservedTransactionID = %d on an RTU transport, want 0", id)
	}
	if info := c.Info(); !info.IsOpen || info.Transport != TransportRTUOverTCP || info.Endpoint != addr {
		t.Errorf("unexpected Info %+v", info)
	}
}

func TestRTUOverTCPClient_BadCRCAndException(t *testing.T) {
	var n atomic.Int32
	addr, _ := startRTUOverTCPSlave(t, func([]byte) []byte {
		switch n.Add(1) {
		case 1:
			frame := adu.AssembleRTUFrame(0x11, 0x03, []byte{0x04, 0xAE, 0x41, 0x56, 0x52})
			frame[len(frame)-1] ^= 0xFF
			return frame
		case 2:
			return adu.AssembleRTUFrame(0x11, 0x83, []byte{0x02})
		default:
			return adu.AssembleRTUFrame(0x11, 0x03, []byte{0x04, 0x00, 0x01, 0x00, 0x02})
		}
	})
	c, err := New(Config{URL: "rtuovertcp://" + addr, Speed: 115200, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	if _, err := c.ReadRegisters(ctx, 0x11, 0x006B, 2, HoldingRegister); !errors.Is(err, ErrBadCRC) {
		t.Fatalf("corrupted frame: want ErrBadCRC, got %v", err)
	}
	if _, err := c.ReadRegisters(ctx, 0x11, 0x006B, 2, HoldingRegister); !errors.Is(err, ErrIllegalDataAddress) {
		t.Fatalf("exception frame: want ErrIllegalDataAddress, got %v", err)
	}
	// The link stays usable after both failures.
	regs, err := c.ReadRegisters(ctx, 0x11, 0x006B, 2, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisters after errors: %v", err)
	}
	if len(regs) != 2 || regs[0] != 1 || regs[1] != 2 {
		t.Fatalf("regs = %v, want [1 2]", regs)
	}
}

func TestRTUOverUDPClient_RoundTrip(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer func() { _ = pc.Close() }()

	reqs := make(chan []byte, 4)
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			reqs <- append([]byte(nil), buf[:n]...)
			_, _ = pc.WriteTo(adu.AssembleRTUFrame(0x11, 0x03, []byte{0x04, 0xAE, 0x41, 0x56, 0x52}), from)
		}
	}()

	c, err := New(Config{URL: "rtuoverudp://" + pc.LocalAddr().String(), Speed: 115200, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = c.Close() }()

	regs, err := c.ReadRegisters(context.Background(), 0x11, 0x006B, 2, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisters: %v", err)
	}
	if len(regs) != 2 || regs[0] != 0xAE41 || regs[1] != 0x5652 {
		t.Fatalf("regs = %#v, want [0xAE41 0x5652]", regs)
	}
	select {
	case got := <-reqs:
		if !bytes.Equal(got, rtuReadHoldingRequest) {
			t.Fatalf("datagram % X, want % X", got, rtuReadHoldingRequest)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("slave never saw the request")
	}
	if info := c.Info(); info.Transport != TransportRTUOverUDP || !info.IsOpen {
		t.Errorf("unexpected Info %+v", info)
	}
}

func TestClientOpen_UDPDialErrors(t *testing.T) {
	for _, scheme := range []string{"udp", "rtuoverudp"} {
		t.Run(scheme, func(t *testing.T) {
			c, err := New(Config{URL: scheme + "://127.0.0.1:99999", DialTimeout: time.Second})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			err = c.Open()
			var opErr *net.OpError
			if !errors.As(err, &opErr) || opErr.Op != "dial" {
				t.Fatalf("Open: want a dial *net.OpError, got %T: %v", err, err)
			}
			if c.Info().IsOpen {
				t.Fatal("client reports open after a failed dial")
			}
			if _, err := c.ReadCoil(context.Background(), 1, 0); !errors.Is(err, ErrClientNotOpen) {
				t.Fatalf("request after failed Open: want ErrClientNotOpen, got %v", err)
			}
		})
	}
}

func TestNewUDPSockWrapper_RejectsNonUDPConn(t *testing.T) {
	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close(); _ = c2.Close() }()
	if usw, err := newUDPSockWrapper(c1); err == nil || usw != nil {
		t.Fatalf("got (%v, %v), want an error for a non-UDP connection", usw, err)
	}
}

func TestClientOpen_UnknownTransportType(t *testing.T) {
	c, err := New(Config{URL: "tcp://127.0.0.1:502"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.transportType = transportType(0xEE)
	if err := c.Open(); !errors.Is(err, ErrConfigurationError) {
		t.Fatalf("Open: want ErrConfigurationError, got %v", err)
	}
	info := c.Info()
	if info.IsOpen || info.Transport != "" || info.PoolEnabled {
		t.Fatalf("unexpected Info %+v", info)
	}
}

func TestClientInfo_TLS(t *testing.T) {
	c, err := New(Config{
		URL:           "tcp+tls://plc.example:802",
		TLSClientCert: &tls.Certificate{},
		TLSRootCAs:    x509.NewCertPool(),
		MaxConns:      4,
		MinConns:      2,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	info := c.Info()
	if info.Transport != TransportTCPOverTLS || info.Endpoint != "plc.example:802" || info.IsOpen {
		t.Fatalf("unexpected Info %+v", info)
	}
	// Pooling is not supported over TLS: the pool settings are neutralised.
	if info.PoolEnabled || info.MaxConns != 1 || c.conf.MinConns != 0 {
		t.Fatalf("pool settings not reset for TLS: info=%+v MinConns=%d", info, c.conf.MinConns)
	}
	if got := c.dialTimeout(); got != 15*time.Second {
		t.Fatalf("default TLS dial timeout = %v, want 15s", got)
	}
}

func TestNew_SerialStopBitDefaults(t *testing.T) {
	cases := []struct {
		url      string
		parity   Parity
		stopBits uint
		want     uint
		dataBits uint
	}{
		{"rtu:///dev/ttyS0", ParityNone, 0, 2, 8},
		{"rtu:///dev/ttyS0", ParityEven, 0, 1, 8},
		{"rtu:///dev/ttyS0", ParityOdd, 0, 1, 8},
		{"rtu:///dev/ttyS0", ParityEven, 2, 2, 8},
		{"ascii:///dev/ttyS0", ParityNone, 0, 2, 7},
		{"ascii:///dev/ttyS0", ParityEven, 0, 1, 7},
	}
	for _, tc := range cases {
		c, err := New(Config{URL: tc.url, Parity: tc.parity, StopBits: tc.stopBits})
		if err != nil {
			t.Fatalf("New(%s): %v", tc.url, err)
		}
		if c.conf.StopBits != tc.want || c.conf.DataBits != tc.dataBits || c.conf.Speed != 19200 {
			t.Errorf("%s parity=%d stopBits=%d: got %d data bits, %d stop bits, %d bps; want %d data bits, %d stop bits, 19200 bps",
				tc.url, tc.parity, tc.stopBits, c.conf.DataBits, c.conf.StopBits, c.conf.Speed, tc.dataBits, tc.want)
		}
	}
}

// connCounter is a TCP listener that tracks how many connections were
// accepted and how many of those the peer has closed again.
type connCounter struct {
	ln       net.Listener
	accepted atomic.Int32
	closed   atomic.Int32
}

func startConnCounter(t *testing.T) *connCounter {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	cc := &connCounter{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			cc.accepted.Add(1)
			go func() {
				_, _ = io.Copy(io.Discard, conn)
				_ = conn.Close()
				cc.closed.Add(1)
			}()
		}
	}()
	return cc
}

// waitFor polls cond until it holds or the timeout expires.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestClientOpen_MinConnsClampedToMaxConns(t *testing.T) {
	cc := startConnCounter(t)
	c, err := New(Config{URL: "tcp://" + cc.ln.Addr().String(), MinConns: 7, MaxConns: 3})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if info := c.Info(); !info.PoolEnabled || info.MaxConns != 3 {
		t.Fatalf("unexpected Info %+v", info)
	}
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	waitFor(t, "3 pre-warmed connections", func() bool { return cc.accepted.Load() == 3 })
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitFor(t, "all pooled connections to be closed", func() bool { return cc.closed.Load() == 3 })
	if got := cc.accepted.Load(); got != 3 {
		t.Fatalf("%d connections dialled, want 3 (MinConns clamped to MaxConns)", got)
	}
}

func TestClientOpen_IsIdempotent(t *testing.T) {
	cc := startConnCounter(t)
	c, err := New(Config{URL: "tcp://" + cc.ln.Addr().String()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := c.Open(); err != nil {
			t.Fatalf("Open #%d: %v", i, err)
		}
	}
	waitFor(t, "first connection", func() bool { return cc.accepted.Load() >= 1 })
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitFor(t, "connection close", func() bool { return cc.closed.Load() == 1 })
	if got := cc.accepted.Load(); got != 1 {
		t.Fatalf("repeated Open dialled %d connections, want 1", got)
	}

	// The client can be re-opened after Close, which dials afresh.
	if err := c.Open(); err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	waitFor(t, "second connection", func() bool { return cc.accepted.Load() == 2 })
	_ = c.Close()
}

// Concurrent Open calls may each dial, but only one engine may survive: the
// connections of the losers must be closed rather than leaked.
func TestClientOpen_ConcurrentCallsDoNotLeakConnections(t *testing.T) {
	cc := startConnCounter(t)
	c, err := New(Config{URL: "tcp://" + cc.ln.Addr().String()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const n = 16
	start := make(chan struct{})
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			<-start
			errs <- c.Open()
		}()
	}
	close(start)
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("Open: %v", err)
		}
	}
	if !c.Info().IsOpen {
		t.Fatal("client not open after concurrent Open calls")
	}
	// Every surplus connection is closed while the client is still open.
	waitFor(t, "surplus connections to be closed", func() bool {
		return cc.accepted.Load() >= 1 && cc.accepted.Load()-cc.closed.Load() == 1
	})
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitFor(t, "the last connection to be closed", func() bool { return cc.accepted.Load() == cc.closed.Load() })
}

// A write timeout leaves a TLS connection in an unusable state, so the
// wrapper closes the underlying socket instead of letting callers retry on it.
func TestTLSSockWrapper_WriteTimeoutClosesSocket(t *testing.T) {
	c1, c2 := net.Pipe()
	defer func() { _ = c2.Close() }()
	tsw := newTLSSockWrapper(tls.Client(c1, &tls.Config{ServerName: "plc.example", MinVersion: tls.VersionTLS12}))

	// Nobody reads on the peer side, so the handshake's first write blocks
	// until the deadline.
	if err := tsw.SetDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatalf("SetDeadline: %v", err)
	}
	n, err := tsw.Write([]byte{0x01})
	var netErr net.Error
	if n != 0 || !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("Write = (%d, %v), want a timeout error", n, err)
	}
	// The peer observes the close.
	_ = c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c2.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("underlying socket not closed after write timeout: peer read returned %v", err)
	}
}
