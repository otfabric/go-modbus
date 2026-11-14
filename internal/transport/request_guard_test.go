// SPDX-License-Identifier: MIT

package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/logging"
	"github.com/otfabric/go-modbus/internal/protocol"
)

// recordingConn is a net.Conn that records what is written to it, reads from a
// fixed byte string and counts its calls. onDeadline, if set, runs in every
// SetDeadline before it returns.
type recordingConn struct {
	r          *bytes.Reader
	written    bytes.Buffer
	reads      int
	deadlines  int
	onDeadline func()
}

func newRecordingConn(in []byte) *recordingConn {
	return &recordingConn{r: bytes.NewReader(in)}
}

func (c *recordingConn) Read(p []byte) (int, error) {
	c.reads++
	return c.r.Read(p)
}
func (c *recordingConn) Write(p []byte) (int, error) { return c.written.Write(p) }
func (c *recordingConn) Close() error                { return nil }
func (c *recordingConn) LocalAddr() net.Addr         { return &net.TCPAddr{} }
func (c *recordingConn) RemoteAddr() net.Addr        { return &net.TCPAddr{} }
func (c *recordingConn) SetDeadline(time.Time) error {
	c.deadlines++
	if c.onDeadline != nil {
		c.onDeadline()
	}
	return nil
}
func (c *recordingConn) SetReadDeadline(time.Time) error  { return nil }
func (c *recordingConn) SetWriteDeadline(time.Time) error { return nil }

// framing describes one client framing for the tests below.
type framing struct {
	name string
	new  func(c *recordingConn) Transport
	// frameLen is the length on the wire of a request with n payload bytes.
	frameLen func(n int) int
	// tooLong is the error for a request that does not fit a frame.
	tooLong error
}

func framings() []framing {
	return []framing{
		{
			name:     "tcp",
			new:      func(c *recordingConn) Transport { return NewTCP(c, time.Second, logging.NopLogger()) },
			frameLen: func(n int) int { return adu.MBAPHeaderLength + 1 + n },
			tooLong:  protocol.ErrInvalidMBAPLength,
		},
		{
			name:     "rtu",
			new:      func(c *recordingConn) Transport { return rawRTU(c) },
			frameLen: func(n int) int { return 2 + n + 2 },
			tooLong:  protocol.ErrProtocolError,
		},
		{
			name:     "ascii",
			new:      func(c *recordingConn) Transport { return NewASCII(c, time.Second, logging.NopLogger()) },
			frameLen: func(n int) int { return 1 + 2*(2+n+1) + 2 },
			tooLong:  protocol.ErrProtocolError,
		},
	}
}

// maxRequestPayload is the largest request payload (the PDU without its
// function code) of every framing: 253 bytes of PDU, MODBUS Application
// Protocol V1.1b3, section 4.1.
const maxRequestPayload = 252

// A request whose payload fits a frame is written in full; one that does not
// is refused by every framing, with nothing written.
func TestExecuteRequest_PayloadSizeBoundary(t *testing.T) {
	sizes := []struct {
		n    int
		fits bool
	}{
		{0, true},
		{1, true},
		{maxRequestPayload - 1, true},
		{maxRequestPayload, true},
		{maxRequestPayload + 1, false},
		{maxRequestPayload + 2, false},
		{256, false},
		{1002, false},
		{0xFFFF, false},
		{0x10000, false},
	}
	for _, f := range framings() {
		for _, sz := range sizes {
			conn := newRecordingConn(nil)
			req := &adu.Request{UnitID: 0x11, FunctionCode: byte(protocol.FCDiagnostics), Payload: make([]byte, sz.n)}
			_, err := f.new(conn).ExecuteRequest(context.Background(), req)
			if sz.fits {
				// No response is scripted: the request went out and the read failed.
				if !errors.Is(err, io.EOF) {
					t.Errorf("%s, %d payload bytes: %v, want the request sent and io.EOF from the read", f.name, sz.n, err)
				}
				if got, want := conn.written.Len(), f.frameLen(sz.n); got != want {
					t.Errorf("%s, %d payload bytes: %d bytes written, want a frame of %d", f.name, sz.n, got, want)
				}
				continue
			}
			if !errors.Is(err, f.tooLong) {
				t.Errorf("%s, %d payload bytes: %v, want %v", f.name, sz.n, err, f.tooLong)
			}
			if n := conn.written.Len(); n != 0 {
				t.Errorf("%s, %d payload bytes: %d bytes written, want none", f.name, sz.n, n)
			}
			if conn.reads != 0 {
				t.Errorf("%s, %d payload bytes: %d reads, want none", f.name, sz.n, conn.reads)
			}
		}
	}
}

// The largest RTU and ASCII requests are frames of exactly the maximum length.
func TestExecuteRequest_LargestRequestIsAFullFrame(t *testing.T) {
	for _, f := range framings() {
		want := map[string]int{"tcp": adu.MBAPHeaderLength - 1 + adu.MBAPLengthMax, "rtu": adu.MaxRTUFrameLength, "ascii": adu.MaxASCIIFrameLength}[f.name]
		if got := f.frameLen(maxRequestPayload); got != want {
			t.Errorf("%s: a request with %d payload bytes is %d bytes long, want the frame maximum %d", f.name, maxRequestPayload, got, want)
		}
	}
}

// A context that is cancelled while the transport sets the request deadline is
// honoured before anything is written. The session layer interrupts a request
// by setting the link's deadline in the past; the request deadline replaces
// that, so the transport looks at the context again once it has set it.
func TestExecuteRequest_CancelledWhileDeadlineIsSet(t *testing.T) {
	for _, f := range framings() {
		t.Run(f.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			conn := newRecordingConn(nil)
			conn.onDeadline = cancel
			req := &adu.Request{UnitID: 0x11, FunctionCode: byte(protocol.FCReadHoldingRegisters), Payload: []byte{0, 0, 0, 1}}
			res, err := f.new(conn).ExecuteRequest(ctx, req)
			if !errors.Is(err, context.Canceled) || res != nil {
				t.Fatalf("ExecuteRequest: %v, %v; want context.Canceled", res, err)
			}
			if conn.deadlines != 1 {
				t.Errorf("%d deadlines set, want 1 (the request deadline)", conn.deadlines)
			}
			if n := conn.written.Len(); n != 0 {
				t.Errorf("%d bytes written after the cancellation, want none", n)
			}
			if conn.reads != 0 {
				t.Errorf("%d reads after the cancellation, want none", conn.reads)
			}
		})
	}
}

// expiringContext reports context.DeadlineExceeded from Err once expire has
// been called.
type expiringContext struct {
	context.Context
	expired atomic.Bool
}

func (c *expiringContext) expire() { c.expired.Store(true) }

func (c *expiringContext) Err() error {
	if c.expired.Load() {
		return context.DeadlineExceeded
	}
	return nil
}

// The same for a context whose deadline passes at that moment: the transport
// reports the context's error (the session layer maps it to a request timeout).
func TestExecuteRequest_DeadlineExpiredWhileDeadlineIsSet(t *testing.T) {
	for _, f := range framings() {
		t.Run(f.name, func(t *testing.T) {
			ctx := &expiringContext{Context: context.Background()}
			conn := newRecordingConn(nil)
			conn.onDeadline = ctx.expire
			req := &adu.Request{UnitID: 0x11, FunctionCode: byte(protocol.FCReadHoldingRegisters), Payload: []byte{0, 0, 0, 1}}
			_, err := f.new(conn).ExecuteRequest(ctx, req)
			if !errors.Is(err, context.DeadlineExceeded) || !os.IsTimeout(err) {
				t.Fatalf("ExecuteRequest: %v, want context.DeadlineExceeded, a timeout", err)
			}
			if n := conn.written.Len(); n != 0 {
				t.Errorf("%d bytes written, want none", n)
			}
		})
	}
}

// The drain before a request sets deadlines of its own, in a loop. A context
// cancelled while it does so ends the drain before its next read, the request
// is not written, and the drain is still owed to the next request.
func TestExecuteRequest_CancelledWhileDraining(t *testing.T) {
	type serial interface {
		Transport
		Resync()
	}
	state := func(tr serial) (drain, verify bool) {
		switch v := tr.(type) {
		case *RTU:
			return v.drain, v.verify
		case *ASCII:
			return v.drain, v.verify
		}
		return false, false
	}
	transports := []struct {
		name string
		new  func(c *recordingConn) serial
	}{
		{"rtu", func(c *recordingConn) serial { return rawRTU(c) }},
		{"ascii", func(c *recordingConn) serial { return NewASCII(c, time.Second, logging.NopLogger()) }},
	}
	req := &adu.Request{UnitID: 0x11, FunctionCode: byte(protocol.FCReadHoldingRegisters), Payload: []byte{0, 0, 0, 1}}
	for _, tc := range transports {
		t.Run(tc.name+"/drain", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// Bytes of an earlier exchange are waiting on the link.
			conn := newRecordingConn([]byte{1, 2, 3, 4})
			conn.onDeadline = cancel
			tr := tc.new(conn)
			tr.Resync()
			if _, err := tr.ExecuteRequest(ctx, req); !errors.Is(err, context.Canceled) {
				t.Fatalf("ExecuteRequest: %v, want context.Canceled", err)
			}
			if conn.deadlines != 1 || conn.reads != 0 || conn.written.Len() != 0 {
				t.Errorf("%d deadlines, %d reads, %d bytes written; want 1, 0 and 0", conn.deadlines, conn.reads, conn.written.Len())
			}
			if drain, _ := state(tr); !drain {
				t.Error("the drain that was cut short is no longer pending")
			}

			// The next request, under a context that lasts, drains first.
			conn.onDeadline = nil
			_, err := tr.ExecuteRequest(context.Background(), req)
			if !errors.Is(err, io.EOF) {
				t.Fatalf("next request: %v, want it sent and io.EOF from the read", err)
			}
			if conn.r.Len() != 0 {
				t.Errorf("%d stale bytes left on the link, want them discarded before the request", conn.r.Len())
			}
			if conn.written.Len() == 0 {
				t.Error("the next request was not written")
			}
		})
		t.Run(tc.name+"/verify", func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			conn := newRecordingConn(nil)
			tr := tc.new(conn)
			switch v := tr.(type) {
			case *RTU:
				v.verify = true
			case *ASCII:
				v.verify = true
			}
			conn.onDeadline = cancel
			if _, err := tr.ExecuteRequest(ctx, req); !errors.Is(err, context.Canceled) {
				t.Fatalf("ExecuteRequest: %v, want context.Canceled", err)
			}
			if conn.reads != 0 || conn.written.Len() != 0 {
				t.Errorf("%d reads, %d bytes written; want none", conn.reads, conn.written.Len())
			}
			if _, verify := state(tr); !verify {
				t.Error("the quiet-line check that was cut short is no longer pending")
			}
		})
	}
}

// drainLink stops at a context that ends between two reads, and reports what
// it discarded until then.
func TestDrainLink_StopsWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := newRecordingConn(bytes.Repeat([]byte{0xAA}, 3000))
	conn.onDeadline = func() {
		if conn.deadlines == 3 {
			cancel()
		}
	}
	// Windows long enough that only the context can end the drain here.
	n := drainLink(ctx, conn, time.Hour, time.Hour, time.Hour)
	if conn.reads != 2 || n != 2*1024 {
		t.Fatalf("drainLink read %d times and discarded %d bytes, want 2 reads of 1024 bytes before the cancellation", conn.reads, n)
	}
}
