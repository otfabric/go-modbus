// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// Regression tests for how the server treats a bad request
// (server_pdu_errors.go): a framing error closes the connection; a PDU whose
// length disagrees with its function code or byte count is answered with
// exception 03 as a best effort and the connection is then closed; a
// request whose framing is consistent but whose values are not is answered
// with an exception and the connection kept. Up to v1.19.0 every PDU error
// closed the connection without an answer.

// countingDevice is hostileDevice with a count of the FC03 requests that
// reach its handler.
type countingDevice struct {
	hostileDevice
	fc03 *atomic.Int32
}

func (d countingDevice) HandleHoldingRegisters(ctx context.Context, req *HoldingRegistersRequest) ([]uint16, error) {
	d.fc03.Add(1)
	return d.hostileDevice.HandleHoldingRegisters(ctx, req)
}

// startPDUServer starts a server for h with metrics m (may be nil) and
// returns its address.
func startPDUServer(t *testing.T, h RequestHandler, m ServerMetrics) string {
	t.Helper()
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Timeout: 3 * time.Second, Metrics: m}, h)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := ms.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ms.Stop() })
	return ms.tcpListener.Addr().String()
}

func dialPDU(t *testing.T, addr string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	return conn
}

// mbapFrame is a request frame whose MBAP length is mbapLen, whatever the
// length of pdu.
func mbapFrame(txn uint16, mbapLen uint16, unit byte, pdu []byte) []byte {
	frame := []byte{byte(txn >> 8), byte(txn), 0, 0, byte(mbapLen >> 8), byte(mbapLen), unit}
	return append(frame, pdu...)
}

// wantAnsweredThenClosed reads one response, which must be want, and then
// the server's close (EOF, not a timeout), within a bound well below the
// server's 3 s timeout: the server does not wait for the bytes an MBAP
// length overcounts.
func wantAnsweredThenClosed(t *testing.T, conn net.Conn, want []byte) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	frame, err := hostileReadFrame(t, conn)
	if err != nil {
		t.Fatalf("want the answer % X, got %v", want, err)
	}
	if string(frame) != string(want) {
		t.Fatalf("got % X, want % X", frame, want)
	}
	n, err := conn.Read(make([]byte, 16))
	var ne net.Error
	if n != 0 || errors.As(err, &ne) && ne.Timeout() || err == nil {
		t.Fatalf("after the answer: read %d bytes, %v; want the connection closed", n, err)
	}
}

// TestServerPDUErrors_ErrorIdentity pins how the server's own request errors
// classify: every PDU error is a protocol error and an Illegal Data Value;
// the length errors close the connection; an exception function code is an
// Illegal Function.
func TestServerPDUErrors_ErrorIdentity(t *testing.T) {
	for _, tc := range []struct {
		err       *malformedRequestError
		closeLink bool
	}{
		{errPDUTruncated, true}, {errPDUShortOfByteCount, true}, {errPDUTrailing, true}, {errPDUBeyondByteCount, true},
		{errPDUSubRequest, false}, {errPDUOddData, false},
	} {
		if !errors.Is(tc.err, ErrProtocolError) || !errors.Is(tc.err, ErrIllegalDataValue) {
			t.Errorf("%v: want Is(ErrProtocolError) and Is(ErrIllegalDataValue)", tc.err)
		}
		if errors.Is(tc.err, ErrIllegalFunction) || errors.Is(tc.err, ErrIllegalDataAddress) {
			t.Errorf("%v matches a second exception", tc.err)
		}
		prefix := "malformed request PDU: "
		if tc.err.truncated {
			prefix = "truncated request PDU: "
		}
		if msg := tc.err.Error(); msg != prefix+tc.err.reason {
			t.Errorf("message %q, want %q", msg, prefix+tc.err.reason)
		}
		if code := mapErrorToExceptionCode(tc.err); code != exIllegalDataValue {
			t.Errorf("%v maps to exception %v, want Illegal Data Value", tc.err, code)
		}
		if tc.err.closeLink != tc.closeLink {
			t.Errorf("%v: closeLink %v, want %v", tc.err, tc.err.closeLink, tc.closeLink)
		}
	}
	if !errors.Is(errExceptionFCRequest, ErrIllegalFunction) || errors.Is(errExceptionFCRequest, ErrProtocolError) {
		t.Errorf("errExceptionFCRequest: want Is(ErrIllegalFunction) only")
	}
	if checkPDULength(nil, 0) != nil || checkPDULength([]byte{1}, 2) != errPDUTruncated || checkPDULength([]byte{1, 2, 3}, 2) != errPDUTrailing {
		t.Error("checkPDULength")
	}
	if checkDataLength(2, 2) != nil || checkDataLength(1, 2) != errPDUShortOfByteCount || checkDataLength(3, 2) != errPDUBeyondByteCount {
		t.Error("checkDataLength")
	}
}

// TestServerPDUErrors_FixedLengthTable: requestPDULength gives the PDU
// length of exactly the function codes whose requests have a fixed size, and
// the handlers accept a request of that length and no other.
func TestServerPDUErrors_FixedLengthTable(t *testing.T) {
	want := map[byte]int{0x01: 5, 0x02: 5, 0x03: 5, 0x04: 5, 0x05: 5, 0x06: 5, 0x07: 1, 0x0B: 1, 0x0C: 1, 0x11: 1, 0x16: 7, 0x18: 3}
	valid := map[byte][]byte{
		0x01: {0, 0, 0, 1}, 0x02: {0, 0, 0, 1}, 0x03: {0, 0, 0, 1}, 0x04: {0, 0, 0, 1}, 0x05: {0, 0, 0xFF, 0}, 0x06: {0, 0, 0, 1},
		0x07: nil, 0x0B: nil, 0x0C: nil, 0x11: nil, 0x16: {0, 0, 0, 0xFF, 0, 0}, 0x18: {0, 4},
	}
	for fc := range 256 {
		if got := requestPDULength(byte(fc)); got != want[byte(fc)] {
			t.Errorf("requestPDULength(0x%02x) = %d, want %d", fc, got, want[byte(fc)])
		}
	}
	dev := e2eNewDevice()
	dev.setUnit(0x2A)
	for fc, payload := range valid {
		if 1+len(payload) != want[fc] {
			t.Fatalf("FC 0x%02x: fixture of %d bytes", fc, 1+len(payload))
		}
		for _, p := range [][]byte{payload, append(append([]byte{}, payload...), 0), payload[:max(0, len(payload)-1)]} {
			tr, _ := serveOne(t, dev, FunctionCode(fc), p...)
			lengthOK := 1+len(p) == want[fc]
			if len(tr.responses) != 1 {
				t.Fatalf("FC 0x%02x % X: %d responses", fc, p, len(tr.responses))
			}
			res := tr.responses[0]
			isLengthErr := res.FunctionCode == byte(fc)|0x80 && res.Payload[0] == byte(exIllegalDataValue) && tr.closed
			if lengthOK == isLengthErr {
				t.Errorf("FC 0x%02x with %d bytes: response % X % X, closed %v", fc, 1+len(p), res.FunctionCode, res.Payload, tr.closed)
			}
		}
	}
	if !(&Server{handler: hostileDevice{}}).servesFixedLength(FCReportServerID) ||
		(&Server{handler: holdingOne()}).servesFixedLength(FCReportServerID) ||
		!(&Server{handler: holdingOne()}).servesFixedLength(FCReadHoldingRegisters) ||
		(&Server{handler: hostileDevice{}}).servesFixedLength(FCWriteMultipleRegisters) {
		t.Error("servesFixedLength")
	}
}

// TestServerPDUErrors_ValueErrorKeepsConnection sends, on one TCP
// connection, requests whose framing is consistent (the MBAP length, the
// function code's fields and the byte count agree) but whose values are not
// acceptable, each followed by a valid FC03: each is answered with its
// exception under its own transaction ID and the FC03 is served after it.
func TestServerPDUErrors_ValueErrorKeepsConnection(t *testing.T) {
	hs := hostileStartServer(t, nil)
	conn := hs.dial(t)
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	cases := []struct {
		name    string
		fc      byte
		payload []byte
		exc     ExceptionCode
	}{
		{"FC03 quantity 0", 0x03, []byte{0, 0, 0, 0}, exIllegalDataValue},
		{"FC03 quantity 126", 0x03, []byte{0, 0, 0, 126}, exIllegalDataValue},
		{"FC03 range beyond 65535", 0x03, []byte{0xFF, 0xFF, 0, 2}, exIllegalDataAddress},
		{"FC05 value 0x1234", 0x05, []byte{0, 0, 0x12, 0x34}, exIllegalDataValue},
		{"FC15 byte count disagrees with quantity", 0x0F, []byte{0, 0, 0, 1, 2, 1, 0}, exIllegalDataValue},
		{"FC16 byte count disagrees with quantity", 0x10, []byte{0, 0, 0, 1, 4, 0, 1, 0, 2}, exIllegalDataValue},
		{"FC20 byte count not a multiple of 7", 0x14, []byte{8, 6, 0, 1, 0, 0, 0, 1, 0}, exIllegalDataValue},
		{"FC21 sub-request overruns the byte count", 0x15, []byte{9, 6, 0, 1, 0, 0, 0, 2, 0, 1}, exIllegalDataValue},
		{"FC08 odd data", 0x08, []byte{0, 0, 0x12}, exIllegalDataValue},
		{"FC43 category 5", 0x2B, []byte{0x0E, 5, 0}, exIllegalDataValue},
		{"FC43 MEI type 0x0D", 0x2B, []byte{0x0D, 1, 0}, exIllegalFunction},
		{"FC 0x41 not implemented", 0x41, []byte{1, 2, 3}, exIllegalFunction},
	}
	txn := uint16(0x100)
	for _, tc := range cases {
		txn++
		if _, err := conn.Write(rawRequestFrame(txn, 0x21, tc.fc, tc.payload)); err != nil {
			t.Fatalf("%s: write: %v", tc.name, err)
		}
		frame, err := hostileReadFrame(t, conn)
		if err != nil {
			t.Fatalf("%s: %v (connection closed for a consistently framed request?)", tc.name, err)
		}
		want := rawRequestFrame(txn, 0x21, tc.fc|0x80, []byte{byte(tc.exc)})
		if string(frame) != string(want) {
			t.Fatalf("%s: got % X, want % X", tc.name, frame, want)
		}
		txn++
		hostileFollowUpFC03(t, conn, txn)
	}
}

// TestServerPDUErrors_LengthErrorAnsweredThenClosed: a request whose PDU is
// shorter or longer than its function code's fields, or than its byte count
// announces, is answered with exception 03 under its transaction ID; then the
// server closes the connection, with the close reason CloseProtocolError.
// The MBAP length agrees with the bytes sent in every case: the server cannot
// tell such a request from one whose MBAP length is wrong.
func TestServerPDUErrors_LengthErrorAnsweredThenClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fc      byte
		payload []byte
	}{
		{"FC03 trailing byte", 0x03, []byte{0, 0, 0, 1, 0xAA}},
		{"FC03 three bytes", 0x03, []byte{0, 0, 0}},
		{"FC05 trailing byte", 0x05, []byte{0, 0, 0xFF, 0, 0}},
		{"FC05 three bytes", 0x05, []byte{0, 0, 0xFF}},
		{"FC07 with data", 0x07, []byte{0x00}},
		{"FC11 with data", 0x0B, []byte{0x00}},
		{"FC12 with data", 0x0C, []byte{0x00}},
		{"FC17 with data", 0x11, []byte{0x00}},
		{"FC17 with three bytes", 0x11, []byte{1, 2, 3}},
		{"FC22 trailing byte", 0x16, []byte{0, 0, 0, 0xFF, 0, 0, 0}},
		{"FC24 trailing byte", 0x18, []byte{0, 0, 0}},
		{"FC24 one byte", 0x18, []byte{0}},
		{"FC08 one byte", 0x08, []byte{0}},
		{"FC15 data beyond the byte count", 0x0F, []byte{0, 0, 0, 1, 1, 1, 0}},
		{"FC15 data short of the byte count", 0x0F, []byte{0, 0, 0, 9, 2, 1}},
		{"FC16 without byte count", 0x10, []byte{0, 0, 0, 1}},
		{"FC16 data beyond the byte count", 0x10, []byte{0, 0, 0, 1, 2, 0, 1, 0}},
		{"FC16 data short of the byte count", 0x10, []byte{0, 0, 0, 1, 2, 0}},
		{"FC20 empty", 0x14, nil},
		{"FC20 data beyond the byte count", 0x14, []byte{7, 6, 0, 1, 0, 0, 0, 1, 0}},
		{"FC20 data short of the byte count", 0x14, []byte{14, 6, 0, 1, 0, 0, 0, 1}},
		{"FC21 data short of the byte count", 0x15, []byte{9, 6, 0, 1, 0, 0, 0, 1}},
		{"FC23 data beyond the byte count", 0x17, []byte{0, 0, 0, 1, 0, 0, 0, 1, 2, 0, 1, 0}},
		{"FC43 trailing byte", 0x2B, []byte{0x0E, 1, 0, 0}},
		{"FC43 MEI type only", 0x2B, []byte{0x0E}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &closeMetrics{}
			addr := startPDUServer(t, hostileDevice{}, m)
			conn := dialPDU(t, addr)
			if _, err := conn.Write(rawRequestFrame(7, 0x21, tc.fc, tc.payload)); err != nil {
				t.Fatal(err)
			}
			wantAnsweredThenClosed(t, conn, rawRequestFrame(7, 0x21, tc.fc|0x80, []byte{byte(exIllegalDataValue)}))
			wantReason(t, m, conn.LocalAddr().String(), CloseProtocolError, ErrProtocolError)
		})
	}
}

// TestServerPDUErrors_MBAPLengthMismatch: a request of a fixed-size function
// code whose MBAP length disagrees with that size is answered as soon as its
// function code is read, without waiting for or taking the bytes the length
// announces, and the connection is closed. A request sent after it, in the
// same write, is not served: its first bytes may have been taken as part of
// the bad one.
func TestServerPDUErrors_MBAPLengthMismatch(t *testing.T) {
	next := rawRequestFrame(2, 0x21, 0x03, []byte{0, 0, 0, 1})
	for _, tc := range []struct {
		name    string
		frame   []byte
		handler func(*atomic.Int32) RequestHandler
		want    []byte
	}{
		{
			// FC17 announced with two bytes of data, followed by the next
			// request: without the early check the server would take 00 02 of
			// the next request as that data.
			"FC17 overdeclared, next request follows",
			append(mbapFrame(1, 4, 0x21, []byte{0x11}), next...),
			nil,
			rawRequestFrame(1, 0x21, 0x91, []byte{byte(exIllegalDataValue)}),
		},
		{
			// The same with nothing after it: answered at once, not after the
			// server's 3 s timeout.
			"FC17 overdeclared, nothing follows",
			mbapFrame(1, 4, 0x21, []byte{0x11}),
			nil,
			rawRequestFrame(1, 0x21, 0x91, []byte{byte(exIllegalDataValue)}),
		},
		{
			"FC03 overdeclared by one",
			append(mbapFrame(1, 7, 0x21, []byte{0x03, 0, 0, 0, 1}), next...),
			nil,
			rawRequestFrame(1, 0x21, 0x83, []byte{byte(exIllegalDataValue)}),
		},
		{
			// The PDU's last byte is then read as the start of the next header.
			"FC03 underdeclared by one",
			append(mbapFrame(1, 5, 0x21, []byte{0x03, 0, 0, 0, 1}), next...),
			nil,
			rawRequestFrame(1, 0x21, 0x83, []byte{byte(exIllegalDataValue)}),
		},
		{
			"FC12 overdeclared",
			append(mbapFrame(1, 3, 0x21, []byte{0x0C}), next...),
			nil,
			rawRequestFrame(1, 0x21, 0x8C, []byte{byte(exIllegalDataValue)}),
		},
		{
			// A device without FC24 answers Illegal Function first, as it does
			// for a request of the right length.
			"FC24 overdeclared, not served",
			append(mbapFrame(1, 6, 0x21, []byte{0x18, 0, 0}), next...),
			func(n *atomic.Int32) RequestHandler {
				return &fnHandler{holding: func(*HoldingRegistersRequest) ([]uint16, error) { n.Add(1); return []uint16{1}, nil }}
			},
			rawRequestFrame(1, 0x21, 0x98, []byte{byte(exIllegalFunction)}),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var fc03 atomic.Int32
			var h RequestHandler = countingDevice{fc03: &fc03}
			if tc.handler != nil {
				h = tc.handler(&fc03)
			}
			m := &closeMetrics{}
			addr := startPDUServer(t, h, m)
			conn := dialPDU(t, addr)
			start := time.Now()
			if _, err := conn.Write(tc.frame); err != nil {
				t.Fatal(err)
			}
			wantAnsweredThenClosed(t, conn, tc.want)
			if d := time.Since(start); d > time.Second {
				t.Errorf("answered after %v: the server waited for the bytes the MBAP length announces", d)
			}
			wantReason(t, m, conn.LocalAddr().String(), CloseProtocolError, ErrProtocolError)
			if n := fc03.Load(); n != 0 {
				t.Errorf("the FC03 handler ran %d times after the bad request", n)
			}
		})
	}
}

// TestServerPDUErrors_Pipelined: requests written ahead of a bad one are
// served in order; the bad one is answered with exception 03; nothing after
// it is served, although the server may already have read it. An exception
// function code (not answered) does not end the pipeline.
func TestServerPDUErrors_Pipelined(t *testing.T) {
	var fc03 atomic.Int32
	addr := startPDUServer(t, countingDevice{fc03: &fc03}, nil)
	conn := dialPDU(t, addr)
	var batch []byte
	batch = append(batch, rawRequestFrame(1, 0x21, 0x03, []byte{0, 0, 0, 1})...)
	batch = append(batch, rawRequestFrame(2, 0x21, 0x83, []byte{0x02})...) // exception FC: skipped
	batch = append(batch, rawRequestFrame(3, 0x21, 0x03, []byte{0, 0, 0, 2})...)
	batch = append(batch, rawRequestFrame(4, 0x21, 0x03, []byte{0, 0, 0, 1, 0})...) // trailing byte
	batch = append(batch, rawRequestFrame(5, 0x21, 0x03, []byte{0, 0, 0, 3})...)
	batch = append(batch, rawRequestFrame(6, 0x21, 0x03, []byte{0, 0, 0, 4})...)
	if _, err := conn.Write(batch); err != nil {
		t.Fatal(err)
	}
	for _, w := range []struct {
		txn uint16
		fc  byte
		n   int // payload length
	}{{1, 0x03, 3}, {3, 0x03, 5}} {
		frame, err := hostileReadFrame(t, conn)
		if err != nil {
			t.Fatalf("txn %d: %v", w.txn, err)
		}
		if binary.BigEndian.Uint16(frame) != w.txn || frame[7] != w.fc || len(frame)-8 != w.n {
			t.Fatalf("txn %d: got % X, want FC 0x%02x with %d payload bytes", w.txn, frame, w.fc, w.n)
		}
	}
	wantAnsweredThenClosed(t, conn, rawRequestFrame(4, 0x21, 0x83, []byte{byte(exIllegalDataValue)}))
	if n := fc03.Load(); n != 2 {
		t.Errorf("FC03 handler ran %d times, want 2 (the requests before the bad one)", n)
	}
}

// TestServerPDUErrors_FullPipeline: a client that sends serverPipelineDepth
// valid requests in one write, and more after, gets every answer in order on
// the same connection.
func TestServerPDUErrors_FullPipeline(t *testing.T) {
	var fc03 atomic.Int32
	addr := startPDUServer(t, countingDevice{fc03: &fc03}, nil)
	conn := dialPDU(t, addr)
	for round := range 3 {
		var batch []byte
		for i := range serverPipelineDepth {
			txn := uint16(round*100 + i + 1)
			batch = append(batch, rawRequestFrame(txn, 0x21, 0x03, []byte{0, byte(i), 0, byte(1 + i%4)})...)
		}
		if _, err := conn.Write(batch); err != nil {
			t.Fatal(err)
		}
		for i := range serverPipelineDepth {
			txn := uint16(round*100 + i + 1)
			frame, err := hostileReadFrame(t, conn)
			if err != nil {
				t.Fatalf("round %d txn %d: %v", round, txn, err)
			}
			want := 1 + 2*(1+i%4) // byte count and registers
			if binary.BigEndian.Uint16(frame) != txn || frame[7] != 0x03 || len(frame)-8 != want {
				t.Fatalf("round %d txn %d: got % X", round, txn, frame)
			}
		}
	}
	if n := fc03.Load(); n != 3*serverPipelineDepth {
		t.Errorf("FC03 handler ran %d times, want %d", n, 3*serverPipelineDepth)
	}
}

// TestServerPDUErrors_NoGoroutineLeak: connections closed after a length
// error leave no goroutine behind, and Stop returns.
func TestServerPDUErrors_NoGoroutineLeak(t *testing.T) {
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Timeout: 3 * time.Second}, hostileDevice{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ms.Start(); err != nil {
		t.Fatal(err)
	}
	addr := ms.tcpListener.Addr().String()
	before := runtime.NumGoroutine()
	for i := range 20 {
		conn := dialPDU(t, addr)
		frame := rawRequestFrame(uint16(i), 0x21, 0x03, []byte{0, 0, 0, 1, 0})
		if i%2 == 1 {
			frame = append(mbapFrame(uint16(i), 4, 0x21, []byte{0x11}), 1, 2, 3)
		}
		if _, err := conn.Write(frame); err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, _ = io.Copy(io.Discard, conn)
		_ = conn.Close()
	}
	e2eEventually(t, "the connection goroutines to end", func() bool { return runtime.NumGoroutine() <= before+1 })
	done := make(chan struct{})
	go func() { _ = ms.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return")
	}
}

// TestServerPDUErrors_FramingErrors: MBAP headers the server cannot frame and
// frames cut short still close the connection, without a response and
// without waiting for (or allocating) the length a bad header announces.
func TestServerPDUErrors_FramingErrors(t *testing.T) {
	hs := hostileStartServer(t, nil)
	for _, tc := range []struct {
		name  string
		frame []byte
	}{
		{"length 0", hostileFrame(1, 0, 0, 0x21, nil)},
		{"length 1 (unit ID only)", hostileFrame(1, 0, 1, 0x21, nil)},
		{"length 0xFFFF", hostileFrame(1, 0, 0xFFFF, 0x21, []byte{0x03, 0, 0, 0, 1})},
		{"length 255 (one beyond a PDU)", hostileFrame(1, 0, 255, 0x21, make([]byte, 254))},
		{"protocol ID 1", hostileFrame(1, 1, 6, 0x21, []byte{0x03, 0, 0, 0, 1})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := hs.dial(t)
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err := conn.Write(tc.frame); err != nil {
				t.Fatal(err)
			}
			n, err := conn.Read(make([]byte, 16))
			if n != 0 || err == nil {
				t.Fatalf("read %d bytes, %v; want the connection closed without a response", n, err)
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				t.Fatal("the server waited instead of closing")
			}
		})
	}
	t.Run("frame cut short then closed", func(t *testing.T) {
		conn := hs.dial(t)
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		full := rawRequestFrame(1, 0x21, 0x03, []byte{0, 0, 0, 1})
		if _, err := conn.Write(full[:len(full)-2]); err != nil {
			t.Fatal(err)
		}
		_ = conn.(*net.TCPConn).CloseWrite()
		if n, err := io.ReadFull(conn, make([]byte, 1)); n != 0 || err == nil {
			t.Fatalf("read %d bytes, %v; want no response", n, err)
		}
	})
}

// TestServerPDUErrors_AllFunctionCodes dispatches every one of the 256
// function codes, with no data and with four bytes of it, to a device that
// implements every handler. A response never carries a function code other
// than the request's or the request's with 0x80 added; a request whose code
// already has the exception bit is not answered and does not close the link;
// a link closed is closed after exception 3.
func TestServerPDUErrors_AllFunctionCodes(t *testing.T) {
	dev := e2eNewDevice()
	dev.setUnit(0x2A)
	for fc := range 256 {
		for _, payload := range [][]byte{nil, {0, 0, 0, 1}} {
			tr, m := serveOne(t, dev, FunctionCode(fc), payload...)
			if fc&0x80 != 0 {
				if len(tr.responses) != 0 || tr.closed {
					t.Fatalf("FC 0x%02x: responses %+v, closed %v; want no answer and the link kept", fc, tr.responses, tr.closed)
				}
				checkEvents(t, m, []metricEvent{
					{kind: "request", unit: 0x2A, fc: FunctionCode(fc)},
					{kind: "error", unit: 0x2A, fc: FunctionCode(fc)},
				}, ErrIllegalFunction)
				continue
			}
			if tr.closed {
				// Only a PDU length error closes the link, after exception 03.
				if len(tr.responses) != 1 || tr.responses[0].FunctionCode != byte(fc)|0x80 ||
					len(tr.responses[0].Payload) != 1 || tr.responses[0].Payload[0] != byte(exIllegalDataValue) {
					t.Fatalf("FC 0x%02x % X: closed after %+v, want exception 3", fc, payload, tr.responses)
				}
				continue
			}
			if len(tr.responses) != 1 {
				t.Fatalf("FC 0x%02x % X: %d responses", fc, payload, len(tr.responses))
			}
			res := tr.responses[0]
			switch res.FunctionCode {
			case byte(fc):
			case byte(fc) | 0x80:
				if len(res.Payload) != 1 || !validExceptionCode(res.Payload[0]) {
					t.Fatalf("FC 0x%02x: exception payload % X", fc, res.Payload)
				}
			default:
				t.Fatalf("FC 0x%02x: response FC 0x%02x", fc, res.FunctionCode)
			}
		}
	}
	if n := len(dev.takeCalls()); n == 0 {
		t.Fatal("no request reached the device")
	}
}

// TestServerPDUErrors_RequestDuringDrain: a request that arrives while the
// server drains a connection it answered a length error on, from a client
// that then keeps the connection open, does not lift the drain's read
// deadline: the server still closes the connection. FC16 is a function code
// without a fixed size, so the reader goes on reading after the bad request.
func TestServerPDUErrors_RequestDuringDrain(t *testing.T) {
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Timeout: 30 * time.Second}, countingDevice{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := ms.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = ms.Stop() })
	addr := ms.tcpListener.Addr().String()
	// FC16 of one register with one byte beyond its data.
	bad := []byte{0x10, 0x00, 0x00, 0x00, 0x01, 0x02, 0x00, 0x01, 0xFF}
	valid := mbapFrame(2, 6, 1, []byte{0x03, 0x00, 0x00, 0x00, 0x01})
	for i := range 10 {
		conn := dialPDU(t, addr)
		if _, err := conn.Write(mbapFrame(1, uint16(len(bad)+1), 1, bad)); err != nil {
			t.Fatalf("write: %v", err)
		}
		frame, err := hostileReadFrame(t, conn)
		if err != nil || len(frame) != 9 || string(frame[7:]) != string([]byte{0x90, 0x03}) {
			t.Fatalf("round %d: % X, %v; want 90 03", i, frame, err)
		}
		time.Sleep(20 * time.Millisecond)
		_, _ = conn.Write(valid)
		if !chaosEventually(2*time.Second, func() bool { return chaosServerConns(ms) == 0 }) {
			t.Fatalf("round %d: the server still holds the connection the client keeps open", i)
		}
		_ = conn.Close()
	}
}

// TestServerPDUErrors_ExceptionFCRequestError: the error of a request whose
// function code has the exception bit set matches ErrIllegalFunction, as up
// to v1.19.0, and no other exception, and says why it is not answered.
func TestServerPDUErrors_ExceptionFCRequestError(t *testing.T) {
	var err error = exceptionFCRequestError{}
	if !errors.Is(err, ErrIllegalFunction) || errors.Is(err, ErrIllegalDataValue) || errors.Is(err, ErrProtocolError) {
		t.Errorf("%v: want Is(ErrIllegalFunction) only", err)
	}
	if msg := err.Error(); msg != "request function code has the exception bit (0x80) set: not answered" {
		t.Errorf("message %q", msg)
	}
}
