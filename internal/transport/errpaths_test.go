// SPDX-License-Identifier: MIT

package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/logging"
	"github.com/otfabric/go-modbus/internal/protocol"
)

// hookLink is an RTULink whose SetDeadline and exhausted-Read behaviour can be
// programmed per call.
type hookLink struct {
	scriptedLink
	deadlineCalls int
	deadlineFn    func(call int) error
	// tail is consumed one entry per Read once the scripted chunks are
	// exhausted; a nil entry yields (0, nil).
	tail []error
}

func (l *hookLink) SetDeadline(time.Time) error {
	l.deadlineCalls++
	if l.deadlineFn != nil {
		return l.deadlineFn(l.deadlineCalls)
	}
	return nil
}

func (l *hookLink) Read(p []byte) (int, error) {
	if len(l.chunks) == 0 && len(l.tail) > 0 {
		err := l.tail[0]
		l.tail = l.tail[1:]
		return 0, err
	}
	return l.scriptedLink.Read(p)
}

// rawRTU builds an RTU transport without the constructor's link flush, with
// timings short enough to keep error-recovery sleeps negligible.
func rawRTU(link RTULink) *RTU {
	return &RTU{
		Logger:  logging.NopLogger(),
		Link:    link,
		Timeout: time.Second,
		t1:      time.Microsecond,
		t35:     4 * time.Microsecond,
	}
}

func rtuReq() *adu.Request {
	return &adu.Request{UnitID: 0x11, FunctionCode: byte(protocol.FCReadHoldingRegisters), Payload: []byte{0x00, 0x6B, 0x00, 0x03}}
}

func TestNewRTU_TimingAndInitialFlush(t *testing.T) {
	cases := []struct {
		speed   uint
		wantT1  time.Duration
		wantT35 time.Duration
	}{
		{9600, 11 * time.Second / 9600, (11 * time.Second / 9600) * 35 / 10},
		{19200, 11 * time.Second / 19200, 1750 * time.Microsecond},
		{115200, 11 * time.Second / 115200, 1750 * time.Microsecond},
	}
	for _, tc := range cases {
		link := &hookLink{scriptedLink: scriptedLink{chunks: [][]byte{{0xDE, 0xAD}}, readErr: os.ErrDeadlineExceeded}}
		rt := NewRTU(link, "", tc.speed, time.Second, logging.NopLogger())
		if rt.t1 != tc.wantT1 || rt.t35 != tc.wantT35 {
			t.Errorf("speed %d: t1=%v t35=%v, want %v / %v", tc.speed, rt.t1, rt.t35, tc.wantT1, tc.wantT35)
		}
		if len(link.chunks) != 0 {
			t.Errorf("speed %d: stale bytes were not flushed from the link", tc.speed)
		}
		if link.deadlineCalls != 1 {
			t.Errorf("speed %d: flush set %d deadlines, want 1", tc.speed, link.deadlineCalls)
		}
	}
}

func TestRTUExecuteRequest_Scripted(t *testing.T) {
	want := []byte{0x06, 0xAE, 0x41, 0x56, 0x52, 0x43, 0x40}
	link := &hookLink{scriptedLink: scriptedLink{
		chunks:  [][]byte{adu.AssembleRTUFrame(0x11, byte(protocol.FCReadHoldingRegisters), want)},
		readErr: os.ErrDeadlineExceeded,
	}}
	rt := rawRTU(link)
	res, err := rt.ExecuteRequest(context.Background(), rtuReq())
	if err != nil {
		t.Fatalf("ExecuteRequest: %v", err)
	}
	if res.UnitID != 0x11 || res.FunctionCode != byte(protocol.FCReadHoldingRegisters) || !bytes.Equal(res.Payload, want) {
		t.Fatalf("unexpected response %+v", res)
	}
	if got, wantTX := link.written.Bytes(), []byte{0x11, 0x03, 0x00, 0x6B, 0x00, 0x03, 0x76, 0x87}; !bytes.Equal(got, wantTX) {
		t.Fatalf("TX frame % X, want % X", got, wantTX)
	}
	if rt.lastActivity.IsZero() {
		t.Fatal("lastActivity not updated after a successful exchange")
	}
}

func TestRTUExecuteRequest_SetDeadlineError(t *testing.T) {
	sentinel := errors.New("deadline unsupported")
	link := &hookLink{deadlineFn: func(int) error { return sentinel }}
	rt := rawRTU(link)
	if _, err := rt.ExecuteRequest(context.Background(), rtuReq()); !errors.Is(err, sentinel) {
		t.Fatalf("want deadline error, got %v", err)
	}
	if link.written.Len() != 0 {
		t.Fatalf("request was written although the deadline could not be set: % X", link.written.Bytes())
	}
}

func TestRTUExecuteRequest_WriteError(t *testing.T) {
	sentinel := errors.New("write failed")
	rt := rawRTU(&hookLink{scriptedLink: scriptedLink{writeErr: sentinel}})
	if _, err := rt.ExecuteRequest(context.Background(), rtuReq()); !errors.Is(err, sentinel) {
		t.Fatalf("want write error, got %v", err)
	}
}

// A cancelled context aborts the pre-transmit inter-frame wait before any
// byte is put on the wire.
func TestRTUExecuteRequest_CancelledDuringInterFrameGap(t *testing.T) {
	link := &hookLink{}
	rt := rawRTU(link)
	rt.lastActivity = time.Now().Add(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := rt.ExecuteRequest(ctx, rtuReq()); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if link.written.Len() != 0 {
		t.Fatalf("request was written despite cancellation: % X", link.written.Bytes())
	}
}

// A cancelled context aborts the post-transmit turnaround wait: the request
// has been sent but no response is read.
func TestRTUExecuteRequest_CancelledAfterTransmit(t *testing.T) {
	link := &hookLink{scriptedLink: scriptedLink{
		chunks: [][]byte{adu.AssembleRTUFrame(0x11, byte(protocol.FCReadHoldingRegisters), []byte{0x02, 0x00, 0x01})},
	}}
	rt := rawRTU(link)
	rt.t1 = time.Hour // makes the computed end-of-transmission lie far in the future
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := rt.ExecuteRequest(ctx, rtuReq()); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if link.written.Len() != 8 {
		t.Fatalf("expected the 8-byte request on the wire, got % X", link.written.Bytes())
	}
	if len(link.chunks) != 1 {
		t.Fatal("response bytes were consumed after cancellation")
	}
}

// Framing errors trigger a link flush so that the remainder of a corrupt
// frame cannot be mistaken for the next response.
func TestRTUExecuteRequest_FramingErrorFlushesLink(t *testing.T) {
	// Header promises 4 data bytes but only 2 arrive before the link times
	// out; trailing garbage shows up afterwards.
	link := &hookLink{
		scriptedLink: scriptedLink{
			chunks:  [][]byte{{0x11, 0x03, 0x04, 0xAA, 0xBB}},
			readErr: io.EOF,
		},
	}
	rt := rawRTU(link)
	_, err := rt.ExecuteRequest(context.Background(), rtuReq())
	if !errors.Is(err, protocol.ErrShortFrame) {
		t.Fatalf("want ErrShortFrame, got %v", err)
	}
	// One deadline for the request, one for the post-error flush.
	if link.deadlineCalls != 2 {
		t.Fatalf("SetDeadline calls = %d, want 2 (request + flush)", link.deadlineCalls)
	}
}

func TestRTUReadFrame_ErrorPaths(t *testing.T) {
	sentinel := errors.New("link broke")
	fifoFC := byte(protocol.FCReadFIFOQueue)

	// fifoFrame returns a FIFO response announcing byteCount, with body bytes appended.
	fifoHeader := func(byteCount uint16) []byte {
		return []byte{0x11, fifoFC, byte(byteCount >> 8), byte(byteCount)}
	}

	cases := []struct {
		name    string
		chunks  [][]byte
		readErr error
		want    error
	}{
		{"header cut short", [][]byte{{0x11, 0x03}}, io.EOF, protocol.ErrShortFrame},
		{"header cut short by link error", [][]byte{{0x11}}, sentinel, protocol.ErrShortFrame},
		{"no bytes, link error", nil, sentinel, sentinel},
		{"unknown function code", [][]byte{{0x11, 0x63, 0x00}}, io.EOF, protocol.ErrProtocolError},
		{"body read fails", [][]byte{{0x11, 0x03, 0x04}, {0xAA}}, sentinel, sentinel},
		{"body cut short", [][]byte{{0x11, 0x03, 0x04}, {0xAA}}, io.EOF, protocol.ErrShortFrame},
		{"announced length exceeds max frame", [][]byte{{0x11, 0x03, 0xFF}}, io.EOF, protocol.ErrProtocolError},
		{"fifo: missing low count byte, eof", [][]byte{{0x11, fifoFC, 0x00}}, io.EOF, protocol.ErrShortFrame},
		{"fifo: missing low count byte, link error", [][]byte{{0x11, fifoFC, 0x00}}, sentinel, sentinel},
		{"fifo: count exceeds max frame", [][]byte{fifoHeader(0x0100)}, io.EOF, protocol.ErrProtocolError},
		{"fifo: body read fails", [][]byte{fifoHeader(6), {0x00, 0x02}}, sentinel, sentinel},
		{"fifo: body cut short", [][]byte{fifoHeader(6), {0x00, 0x02}}, io.EOF, protocol.ErrShortFrame},
		{"fifo: body missing", [][]byte{fifoHeader(6)}, io.EOF, protocol.ErrShortFrame},
		{"body missing", [][]byte{{0x11, 0x03, 0x04}}, io.EOF, protocol.ErrShortFrame},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := rawRTU(&hookLink{scriptedLink: scriptedLink{chunks: tc.chunks, readErr: tc.readErr}})
			res, err := rt.readRTUFrame()
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v (res=%+v)", tc.want, err, res)
			}
			if res != nil {
				t.Fatalf("got a response alongside the error: %+v", res)
			}
		})
	}
}

func TestRTUReadFrame_VariableLength(t *testing.T) {
	diag := adu.AssembleRTUFrame(0x11, byte(protocol.FCDiagnostics), []byte{0x00, 0x00, 0x12, 0x34})
	sentinel := errors.New("link broke")

	t.Run("silence ends frame", func(t *testing.T) {
		link := &hookLink{scriptedLink: scriptedLink{chunks: splitEvery(diag, 1), readErr: os.ErrDeadlineExceeded}}
		res, err := rawRTU(link).readRTUFrame()
		if err != nil {
			t.Fatalf("readRTUFrame: %v", err)
		}
		if res.UnitID != 0x11 || res.FunctionCode != byte(protocol.FCDiagnostics) ||
			!bytes.Equal(res.Payload, []byte{0x00, 0x00, 0x12, 0x34}) {
			t.Fatalf("unexpected response %+v", res)
		}
	})
	t.Run("empty poll ends frame", func(t *testing.T) {
		// A serial link reports its poll timeout as (0, nil).
		link := &hookLink{scriptedLink: scriptedLink{chunks: splitEvery(diag, 1)}, tail: []error{nil}}
		res, err := rawRTU(link).readRTUFrame()
		if err != nil {
			t.Fatalf("readRTUFrame: %v", err)
		}
		if !bytes.Equal(res.Payload, []byte{0x00, 0x00, 0x12, 0x34}) {
			t.Fatalf("unexpected response %+v", res)
		}
	})
	t.Run("link error mid frame", func(t *testing.T) {
		link := &hookLink{scriptedLink: scriptedLink{chunks: [][]byte{diag[:3], diag[3:5]}, readErr: sentinel}}
		if _, err := rawRTU(link).readRTUFrame(); !errors.Is(err, sentinel) {
			t.Fatalf("want link error, got %v", err)
		}
	})
	t.Run("deadline cannot be set", func(t *testing.T) {
		link := &hookLink{
			scriptedLink: scriptedLink{chunks: [][]byte{diag[:3], diag[3:]}},
			deadlineFn:   func(int) error { return sentinel },
		}
		if _, err := rawRTU(link).readRTUFrame(); !errors.Is(err, sentinel) {
			t.Fatalf("want deadline error, got %v", err)
		}
	})
	t.Run("oversized frame is rejected", func(t *testing.T) {
		// A talker that never falls silent fills the buffer; the result
		// cannot carry a valid CRC.
		noise := bytes.Repeat([]byte{0x55}, adu.MaxRTUFrameLength)
		link := &hookLink{scriptedLink: scriptedLink{chunks: [][]byte{diag[:3], noise}, readErr: os.ErrDeadlineExceeded}}
		if _, err := rawRTU(link).readRTUFrame(); !errors.Is(err, protocol.ErrBadCRC) {
			t.Fatalf("want ErrBadCRC, got %v", err)
		}
	})
}

func TestTCPExecuteRequest_ClosedSocket(t *testing.T) {
	c1, c2 := net.Pipe()
	defer func() { _ = c2.Close() }()
	tt := NewTCP(c1, time.Second, logging.NopLogger())
	_ = c1.Close()
	if _, err := tt.ExecuteRequest(context.Background(), rtuReq()); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("want io.ErrClosedPipe, got %v", err)
	}
	if tt.lastTxnID != 0 {
		t.Fatalf("transaction id advanced to %d although nothing was sent", tt.lastTxnID)
	}
	if _, _, err := tt.ReadRequest(); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("ReadRequest: want io.ErrClosedPipe, got %v", err)
	}
}

func TestTCPExecuteRequest_PeerClosedBeforeWrite(t *testing.T) {
	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close() }()
	_ = c2.Close()
	tt := NewTCP(c1, time.Second, logging.NopLogger())
	if _, err := tt.ExecuteRequest(context.Background(), rtuReq()); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("want io.ErrClosedPipe, got %v", err)
	}
}

func TestTCPExecuteRequest_TruncatedBody(t *testing.T) {
	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close() }()
	go func() {
		buf := make([]byte, 64)
		_, _ = c2.Read(buf)
		// MBAP header announcing 5 body bytes, followed by only 2.
		_, _ = c2.Write([]byte{0x00, 0x01, 0x00, 0x00, 0x00, 0x06, 0x11, 0x03, 0x02})
		_ = c2.Close()
	}()
	tt := NewTCP(c1, time.Second, logging.NopLogger())
	if _, err := tt.ExecuteRequest(context.Background(), rtuReq()); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("want io.ErrUnexpectedEOF, got %v", err)
	}
}

// Noise received before the start delimiter in the same read as the beginning
// of a frame is dropped while the partial frame is kept.
func TestASCIIExecuteRequest_NoiseAndPartialFrameInOneRead(t *testing.T) {
	full := asciiRes()
	first := append([]byte("\x00garbage"), full[:5]...)
	at := NewASCII(&scriptedLink{chunks: [][]byte{first, full[5:]}}, time.Second, logging.NopLogger())
	res, err := at.ExecuteRequest(context.Background(), asciiReq())
	if err != nil {
		t.Fatalf("ExecuteRequest: %v", err)
	}
	checkASCIIRes(t, res)
}

func TestRTUWriteResponse_WriteError(t *testing.T) {
	sentinel := errors.New("write failed")
	rt := rawRTU(&hookLink{scriptedLink: scriptedLink{writeErr: sentinel}})
	err := rt.WriteResponse(&adu.Response{UnitID: 0x11, FunctionCode: 0x03, Payload: []byte{0x02, 0x00, 0x01}})
	if !errors.Is(err, sentinel) {
		t.Fatalf("want write error, got %v", err)
	}
	if !rt.lastActivity.IsZero() {
		t.Fatal("lastActivity advanced although nothing was transmitted")
	}
}

// failingWriteConn is a net.Conn whose writes always fail.
type failingWriteConn struct {
	net.Conn
	err error
}

func (c failingWriteConn) Write([]byte) (int, error) { return 0, c.err }

func TestTCPExecuteRequest_WriteError(t *testing.T) {
	c1, c2 := net.Pipe()
	defer func() { _ = c1.Close(); _ = c2.Close() }()
	sentinel := errors.New("connection reset")
	tt := NewTCP(failingWriteConn{Conn: c1, err: sentinel}, time.Second, logging.NopLogger())

	res, err := tt.ExecuteRequest(context.Background(), rtuReq())
	if !errors.Is(err, sentinel) || res != nil {
		t.Fatalf("got (%+v, %v), want (nil, write error)", res, err)
	}
	if werr := tt.WriteResponse(&adu.Response{UnitID: 1, FunctionCode: 3}); !errors.Is(werr, sentinel) {
		t.Fatalf("WriteResponse: want write error, got %v", werr)
	}
}
