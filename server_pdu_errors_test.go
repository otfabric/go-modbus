// SPDX-License-Identifier: MIT

package modbus

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// Regression tests for the separation of framing errors, truncated PDUs and
// malformed but complete PDUs (server_pdu_errors.go). Up to v1.19.0 every one
// of them closed the connection.

// TestServerPDUErrors_ErrorIdentity pins how the server's own request errors
// classify: a truncated PDU is a protocol error only, a malformed complete
// one also an Illegal Data Value, and an exception function code an Illegal
// Function.
func TestServerPDUErrors_ErrorIdentity(t *testing.T) {
	for _, err := range []error{errPDUTruncated, errPDUShortOfByteCount} {
		if !errors.Is(err, ErrProtocolError) || errors.Is(err, ErrIllegalDataValue) {
			t.Errorf("%v: Is(ErrProtocolError) %v, Is(ErrIllegalDataValue) %v; want true, false",
				err, errors.Is(err, ErrProtocolError), errors.Is(err, ErrIllegalDataValue))
		}
		if code := mapErrorToExceptionCode(err); code != exServerDeviceFailure {
			// Never sent: a truncated PDU closes the link. Pinned so a change
			// of the mapping is noticed.
			t.Errorf("%v maps to exception %v", err, code)
		}
	}
	for _, err := range []error{errPDUTrailing, errPDUBeyondByteCount, errPDUSubRequest, errPDUOddData} {
		if !errors.Is(err, ErrProtocolError) || !errors.Is(err, ErrIllegalDataValue) {
			t.Errorf("%v: want Is(ErrProtocolError) and Is(ErrIllegalDataValue)", err)
		}
		if errors.Is(err, ErrIllegalFunction) || errors.Is(err, ErrIllegalDataAddress) {
			t.Errorf("%v matches a second exception", err)
		}
		if code := mapErrorToExceptionCode(err); code != exIllegalDataValue {
			t.Errorf("%v maps to exception %v, want Illegal Data Value", err, code)
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

// TestServerPDUErrors_ExceptionKeepsConnection sends, on one TCP connection, a
// complete but invalid request of each function code, each followed by a
// valid FC03: the invalid one is answered with the expected exception under
// its own transaction ID and the FC03 is served normally after it.
func TestServerPDUErrors_ExceptionKeepsConnection(t *testing.T) {
	hs := hostileStartServer(t, nil)
	conn := hs.dial(t)
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	cases := []struct {
		name    string
		fc      byte
		payload []byte
		exc     ExceptionCode
	}{
		{"FC03 trailing byte", 0x03, []byte{0, 0, 0, 1, 0xAA}, exIllegalDataValue},
		{"FC03 quantity 0", 0x03, []byte{0, 0, 0, 0}, exIllegalDataValue},
		{"FC05 trailing byte", 0x05, []byte{0, 0, 0xFF, 0, 0}, exIllegalDataValue},
		{"FC05 value 0x1234", 0x05, []byte{0, 0, 0x12, 0x34}, exIllegalDataValue},
		{"FC15 data beyond the byte count", 0x0F, []byte{0, 0, 0, 1, 1, 1, 0}, exIllegalDataValue},
		{"FC15 byte count disagrees with quantity", 0x0F, []byte{0, 0, 0, 1, 2, 1, 0}, exIllegalDataValue},
		{"FC16 data beyond the byte count", 0x10, []byte{0, 0, 0, 1, 2, 0, 1, 0}, exIllegalDataValue},
		{"FC16 byte count disagrees with quantity", 0x10, []byte{0, 0, 0, 1, 4, 0, 1, 0, 2}, exIllegalDataValue},
		{"FC17 with data", 0x11, []byte{0x00}, exIllegalDataValue},
		{"FC07 with data", 0x07, []byte{0x00}, exIllegalDataValue},
		{"FC11 with data", 0x0B, []byte{0x00}, exIllegalDataValue},
		{"FC12 with data", 0x0C, []byte{0x00}, exIllegalDataValue},
		{"FC20 data beyond the byte count", 0x14, []byte{7, 6, 0, 1, 0, 0, 0, 1, 0}, exIllegalDataValue},
		{"FC20 byte count not a multiple of 7", 0x14, []byte{8, 6, 0, 1, 0, 0, 0, 1, 0}, exIllegalDataValue},
		{"FC21 sub-request overruns the byte count", 0x15, []byte{9, 6, 0, 1, 0, 0, 0, 2, 0, 1}, exIllegalDataValue},
		{"FC24 trailing byte", 0x18, []byte{0, 0, 0}, exIllegalDataValue},
		{"FC43 trailing byte", 0x2B, []byte{0x0E, 1, 0, 0}, exIllegalDataValue},
		{"FC43 category 5", 0x2B, []byte{0x0E, 5, 0}, exIllegalDataValue},
	}
	txn := uint16(0x100)
	for _, tc := range cases {
		txn++
		if _, err := conn.Write(rawRequestFrame(txn, 0x21, tc.fc, tc.payload)); err != nil {
			t.Fatalf("%s: write: %v", tc.name, err)
		}
		frame, err := hostileReadFrame(t, conn)
		if err != nil {
			t.Fatalf("%s: %v (connection closed for a complete request?)", tc.name, err)
		}
		want := rawRequestFrame(txn, 0x21, tc.fc|0x80, []byte{byte(tc.exc)})
		if string(frame) != string(want) {
			t.Fatalf("%s: got % X, want % X", tc.name, frame, want)
		}
		txn++
		hostileFollowUpFC03(t, conn, txn)
	}
}

// TestServerPDUErrors_TruncatedClosesConnection: a PDU with fewer bytes than
// its function code's fields, or than its byte count announces, is a
// synchronization error. It is not answered and the connection is closed,
// as up to v1.19.0, with the close reason CloseProtocolError.
func TestServerPDUErrors_TruncatedClosesConnection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fc      byte
		payload []byte
	}{
		{"FC03 three bytes", 0x03, []byte{0, 0, 0}},
		{"FC05 three bytes", 0x05, []byte{0, 0, 0xFF}},
		{"FC15 data short of the byte count", 0x0F, []byte{0, 0, 0, 9, 2, 1}},
		{"FC16 without byte count", 0x10, []byte{0, 0, 0, 1}},
		{"FC16 data short of the byte count", 0x10, []byte{0, 0, 0, 1, 2, 0}},
		{"FC43 MEI type only", 0x2B, []byte{0x0E}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &closeMetrics{}
			_, conn := startCloseServer(t, m, nil)
			if _, err := conn.Write(rawRequestFrame(1, 1, tc.fc, tc.payload)); err != nil {
				t.Fatal(err)
			}
			wantClosedUnanswered(t, conn)
			wantReason(t, m, conn.LocalAddr().String(), CloseProtocolError, ErrProtocolError)
		})
	}
	// The optional function codes, on a device that serves them (a device
	// that does not answers Illegal Function first).
	hs := hostileStartServer(t, nil)
	for _, tc := range []struct {
		name    string
		fc      byte
		payload []byte
	}{
		{"FC08 one byte", 0x08, []byte{0}},
		{"FC20 empty", 0x14, nil},
		{"FC20 data short of the byte count", 0x14, []byte{14, 6, 0, 1, 0, 0, 0, 1}},
		{"FC21 data short of the byte count", 0x15, []byte{9, 6, 0, 1, 0, 0, 0, 1}},
		{"FC24 one byte", 0x18, []byte{0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := hs.dial(t)
			if _, err := conn.Write(rawRequestFrame(1, 0x21, tc.fc, tc.payload)); err != nil {
				t.Fatal(err)
			}
			wantClosedUnanswered(t, conn)
		})
	}
}

// TestServerPDUErrors_Pipelined writes valid, complete-but-invalid and
// exception-FC requests in one write: each is answered (or, for the
// exception FC, skipped) in order under its own transaction ID, and the
// parser stays in step.
func TestServerPDUErrors_Pipelined(t *testing.T) {
	hs := hostileStartServer(t, nil)
	conn := hs.dial(t)
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	var batch []byte
	batch = append(batch, rawRequestFrame(1, 0x21, 0x03, []byte{0, 0, 0, 1})...)
	batch = append(batch, rawRequestFrame(2, 0x21, 0x03, []byte{0, 0, 0, 1, 0})...) // trailing byte
	batch = append(batch, rawRequestFrame(3, 0x21, 0x83, []byte{0x02})...)          // exception FC
	batch = append(batch, rawRequestFrame(4, 0x21, 0x11, []byte{1, 2, 3})...)       // FC17 with data
	batch = append(batch, rawRequestFrame(5, 0x21, 0x03, []byte{0, 0, 0, 2})...)
	if _, err := conn.Write(batch); err != nil {
		t.Fatal(err)
	}
	want := []struct {
		txn uint16
		fc  byte
		n   int // payload length
	}{{1, 0x03, 3}, {2, 0x83, 1}, {4, 0x91, 1}, {5, 0x03, 5}}
	for _, w := range want {
		frame, err := hostileReadFrame(t, conn)
		if err != nil {
			t.Fatalf("txn %d: %v", w.txn, err)
		}
		if binary.BigEndian.Uint16(frame) != w.txn || frame[7] != w.fc || len(frame)-8 != w.n {
			t.Fatalf("txn %d: got % X, want FC 0x%02x with %d payload bytes", w.txn, frame, w.fc, w.n)
		}
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
// no handler runs for one.
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
				// Only a truncated PDU closes the link.
				if len(tr.responses) != 0 {
					t.Fatalf("FC 0x%02x: answered and closed", fc)
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
