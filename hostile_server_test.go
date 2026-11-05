// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// This file plays a hostile client against the go-modbus server over raw TCP
// (and TLS): malformed MBAP headers, pipelined bursts, unknown and
// exception-bit function codes, boundary quantities and byte counts for every
// function code the server serves, a seeded random sweep of PDUs and a
// connection flood. The server must answer with a specification exception or
// close the link: never panic, hang, leak goroutines or send a frame longer
// than the 260 bytes a Modbus/TCP ADU can have.

// hostileLogger records what the server logs, so a panic recovered by
// safeDispatch (which hides it behind a Server Device Failure answer) still
// fails the test.
type hostileLogger struct {
	mu     sync.Mutex
	panics []string
}

func (l *hostileLogger) Debugf(string, ...any) {}
func (l *hostileLogger) Infof(string, ...any)  {}
func (l *hostileLogger) Warnf(string, ...any)  {}
func (l *hostileLogger) Errorf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if strings.Contains(msg, "panic") {
		l.mu.Lock()
		l.panics = append(l.panics, msg)
		l.mu.Unlock()
	}
}

func (l *hostileLogger) check(t *testing.T) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, p := range l.panics {
		t.Errorf("server recovered a panic: %.600s", p)
	}
}

// hostileDevice implements the RequestHandler and every optional handler with
// well-behaved, deterministic answers, so that whatever the server does wrong
// is the server's doing.
type hostileDevice struct{}

func hostileReg(addr int) uint16 { return uint16(addr*31 + 7) }

func (hostileDevice) HandleCoils(_ context.Context, req *CoilsRequest) ([]bool, error) {
	out := make([]bool, req.Quantity)
	for i := range out {
		out[i] = (int(req.Addr)+i)%3 == 0
	}
	return out, nil
}

func (hostileDevice) HandleDiscreteInputs(_ context.Context, req *DiscreteInputsRequest) ([]bool, error) {
	out := make([]bool, req.Quantity)
	for i := range out {
		out[i] = (int(req.Addr)+i)%5 == 0
	}
	return out, nil
}

func (hostileDevice) HandleHoldingRegisters(_ context.Context, req *HoldingRegistersRequest) ([]uint16, error) {
	out := make([]uint16, req.Quantity)
	for i := range out {
		out[i] = hostileReg(int(req.Addr) + i)
	}
	return out, nil
}

func (hostileDevice) HandleInputRegisters(_ context.Context, req *InputRegistersRequest) ([]uint16, error) {
	out := make([]uint16, req.Quantity)
	for i := range out {
		out[i] = ^hostileReg(int(req.Addr) + i)
	}
	return out, nil
}

func (hostileDevice) HandleMaskWrite(context.Context, *MaskWriteRequest) error { return nil }

func (hostileDevice) HandleReadWriteRegisters(_ context.Context, req *ReadWriteRegistersRequest) ([]uint16, error) {
	out := make([]uint16, req.ReadQty)
	for i := range out {
		out[i] = hostileReg(int(req.ReadAddr) + i)
	}
	return out, nil
}

func (hostileDevice) HandleExceptionStatus(context.Context, *ExceptionStatusRequest) (uint8, error) {
	return 0xA5, nil
}

func (hostileDevice) HandleCommEventCounter(context.Context, *CommEventCounterRequest) (*CommEventCounterResponse, error) {
	return &CommEventCounterResponse{Status: 0, EventCount: 3}, nil
}

func (hostileDevice) HandleCommEventLog(context.Context, *CommEventLogRequest) (*CommEventLogResponse, error) {
	return &CommEventLogResponse{EventCount: 2, MessageCount: 2, Events: []byte{0x20, 0x00}}, nil
}

func (hostileDevice) HandleDeviceIdentification(context.Context, *DeviceIdentificationRequest) (*DeviceIdentificationResponse, error) {
	objs := []DeviceIdentificationObject{
		{ID: 0x00, Value: "otfabric"}, {ID: 0x01, Value: "hostile"}, {ID: 0x02, Value: "1.0"},
		{ID: 0x03, Value: "https://example.invalid"}, {ID: 0x04, Value: "go-modbus"},
	}
	// Enough extended objects to need several stream pages.
	for id := 0x80; id < 0x90; id++ {
		objs = append(objs, DeviceIdentificationObject{ID: DeviceIDObjectID(id), Value: strings.Repeat("x", 40)})
	}
	return &DeviceIdentificationResponse{ConformityLevel: 0x83, Objects: objs}, nil
}

func (hostileDevice) HandleDiagnostics(_ context.Context, req *DiagnosticsRequest) ([]byte, error) {
	return req.Data, nil
}

func (hostileDevice) HandleReportServerID(context.Context, *ReportServerIDRequest) ([]byte, error) {
	return []byte{0x2A, 0xFF}, nil
}

func (hostileDevice) HandleReadFileRecords(_ context.Context, req *ReadFileRecordsRequest) ([][]uint16, error) {
	out := make([][]uint16, len(req.Requests))
	for i, r := range req.Requests {
		out[i] = make([]uint16, r.RecordLength)
		for j := range out[i] {
			out[i][j] = hostileReg(int(r.RecordNumber) + j)
		}
	}
	return out, nil
}

func (hostileDevice) HandleWriteFileRecords(context.Context, *WriteFileRecordsRequest) error {
	return nil
}

func (hostileDevice) HandleReadFIFOQueue(_ context.Context, req *FIFOQueueRequest) ([]uint16, error) {
	n := int(req.Addr % 32) // 0..31 values
	out := make([]uint16, n)
	for i := range out {
		out[i] = hostileReg(i)
	}
	return out, nil
}

// hostileServer is a running server and how to reach it.
type hostileServer struct {
	srv  *Server
	addr string
	log  *hostileLogger
}

func hostileStartServer(t *testing.T, mod func(*ServerConfig)) *hostileServer {
	t.Helper()
	hs := &hostileServer{log: &hostileLogger{}}
	for range 10 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		hs.addr = ln.Addr().String()
		_ = ln.Close()
		conf := ServerConfig{URL: "tcp://" + hs.addr, MaxClients: 32, Timeout: 3 * time.Second, Logger: hs.log}
		if mod != nil {
			mod(&conf)
		}
		srv, err := NewServer(&conf, hostileDevice{})
		if err != nil {
			t.Fatalf("NewServer: %v", err)
		}
		if err = srv.Start(); err == nil {
			hs.srv = srv
			t.Cleanup(func() {
				_ = srv.Stop()
				hs.log.check(t)
			})
			return hs
		}
	}
	t.Fatal("cannot start server")
	return nil
}

func (hs *hostileServer) dial(t *testing.T) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", hs.addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// hostileFrame builds an MBAP frame with an arbitrary protocol ID and length.
func hostileFrame(txn, proto, length uint16, unit byte, body []byte) []byte {
	b := make([]byte, 7, 7+len(body))
	binary.BigEndian.PutUint16(b[0:], txn)
	binary.BigEndian.PutUint16(b[2:], proto)
	binary.BigEndian.PutUint16(b[4:], length)
	b[6] = unit
	return append(b, body...)
}

// hostileReadFrame reads one raw MBAP frame of at most 260 bytes; it fails the
// test on a frame the server must never send.
func hostileReadFrame(t *testing.T, conn net.Conn) (frame []byte, err error) {
	t.Helper()
	hdr := make([]byte, 7)
	if _, err = io.ReadFull(conn, hdr); err != nil {
		return nil, err
	}
	if proto := binary.BigEndian.Uint16(hdr[2:]); proto != 0 {
		t.Fatalf("server sent protocol ID 0x%04x", proto)
	}
	length := int(binary.BigEndian.Uint16(hdr[4:]))
	if length < 2 || 6+length > maxTCPFrameLength {
		t.Fatalf("server sent MBAP length %d (frame of %d bytes, max %d)", length, 6+length, maxTCPFrameLength)
	}
	body := make([]byte, length-1)
	if _, err = io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	return append(hdr, body...), nil
}

// hostileExpectClosed asserts the server closes conn within limit without
// sending a single byte.
func hostileExpectClosed(t *testing.T, conn net.Conn, limit time.Duration) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(limit))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if n > 0 {
		t.Fatalf("server answered % X, want the connection closed", buf[:n])
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("server kept the connection open for %v", limit)
	}
}

// hostileExpectServed asserts that a fresh, well-behaved connection is served.
func hostileExpectServed(t *testing.T, hs *hostileServer) {
	t.Helper()
	conn := hs.dial(t)
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(rawRequestFrame(0x4242, 1, 0x03, []byte{0, 10, 0, 1})); err != nil {
		t.Fatalf("write: %v", err)
	}
	frame, err := hostileReadFrame(t, conn)
	if err != nil {
		t.Fatalf("well-behaved request after the attack: %v", err)
	}
	want := rawRequestFrame(0x4242, 1, 0x03, []byte{2, byte(hostileReg(10) >> 8), byte(hostileReg(10))})
	if string(frame) != string(want) {
		t.Fatalf("well-behaved request: got % X, want % X", frame, want)
	}
}

func TestHostileServer_MBAPHeaderAbuse(t *testing.T) {
	baseline := chaosGoroutines()
	hs := hostileStartServer(t, func(c *ServerConfig) { c.Timeout = 400 * time.Millisecond })

	cases := []struct {
		name  string
		bytes []byte
		limit time.Duration // how long the server may take to close
	}{
		{"length 0", hostileFrame(1, 0, 0, 1, []byte{0x03, 0, 0, 0, 1}), time.Second},
		{"length 1", hostileFrame(1, 0, 1, 1, []byte{0x03, 0, 0, 0, 1}), time.Second},
		{"length 255", hostileFrame(1, 0, 255, 1, make([]byte, 254)), time.Second},
		{"length 0xFFFF", hostileFrame(1, 0, 0xFFFF, 1, make([]byte, 64)), time.Second},
		{"protocol ID 1", hostileFrame(1, 1, 6, 1, []byte{0x03, 0, 0, 0, 1}), time.Second},
		{"protocol ID 0xFFFF, length 0xFFFF", hostileFrame(1, 0xFFFF, 0xFFFF, 1, nil), time.Second},
		// The announced body never arrives in full: the idle timeout ends it.
		{"length 6, body cut short", hostileFrame(1, 0, 6, 1, []byte{0x03, 0}), 2 * time.Second},
		{"header cut short", []byte{0, 1, 0, 0, 0}, 2 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := hs.dial(t)
			if _, err := conn.Write(tc.bytes); err != nil {
				t.Fatalf("write: %v", err)
			}
			hostileExpectClosed(t, conn, tc.limit)
			hostileExpectServed(t, hs)
		})
	}
	_ = hs.srv.Stop()
	chaosLeakCheck(t, baseline)
}

// TestHostileServer_FrameAfterBadFrame: a valid request that follows a
// malformed one in the same write is not answered: the link is gone.
func TestHostileServer_FrameAfterBadFrame(t *testing.T) {
	hs := hostileStartServer(t, nil)
	conn := hs.dial(t)
	burst := append(hostileFrame(1, 0, 0x0200, 1, nil), rawRequestFrame(2, 1, 0x03, []byte{0, 0, 0, 1})...)
	if _, err := conn.Write(burst); err != nil {
		t.Fatalf("write: %v", err)
	}
	hostileExpectClosed(t, conn, 2*time.Second)
}

// TestHostileServer_PipelinedBurst sends far more requests than the server's
// pipeline depth in a single write, with reused and out-of-order transaction
// IDs. Every request must be answered, in order, under its own transaction ID.
func TestHostileServer_PipelinedBurst(t *testing.T) {
	hs := hostileStartServer(t, nil)
	conn := hs.dial(t)
	rng := rand.New(rand.NewSource(7))

	const n = 200
	type sent struct {
		txn  uint16
		addr uint16
		qty  uint16
	}
	var reqs []sent
	var burst []byte
	for i := range n {
		s := sent{txn: uint16(rng.Intn(4)), addr: uint16(rng.Intn(1000)), qty: uint16(1 + rng.Intn(125))}
		if i%7 == 0 {
			s.txn = 0xFFFF // the same ID again and again
		}
		reqs = append(reqs, s)
		burst = append(burst, rawRequestFrame(s.txn, 1, 0x03, []byte{byte(s.addr >> 8), byte(s.addr), byte(s.qty >> 8), byte(s.qty)})...)
	}

	writeErr := make(chan error, 1)
	go func() {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, err := conn.Write(burst)
		writeErr <- err
	}()
	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	for i, s := range reqs {
		frame, err := hostileReadFrame(t, conn)
		if err != nil {
			t.Fatalf("response %d: %v", i, err)
		}
		if txn := binary.BigEndian.Uint16(frame[0:]); txn != s.txn {
			t.Fatalf("response %d: transaction ID 0x%04x, want 0x%04x", i, txn, s.txn)
		}
		if frame[7] != 0x03 || int(frame[8]) != 2*int(s.qty) {
			t.Fatalf("response %d: % X", i, frame[:9])
		}
		if got := binary.BigEndian.Uint16(frame[9:]); got != hostileReg(int(s.addr)) {
			t.Fatalf("response %d: first register 0x%04x, want 0x%04x (answered out of order?)", i, got, hostileReg(int(s.addr)))
		}
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestHostileServer_UnknownAndExceptionBitFCs sends every function code the
// server does not serve, including those with the exception bit set (which a
// client must never send as a request). Each is answered with exception 1
// (Illegal Function) under the request's transaction and unit ID, whatever the
// payload, and the connection stays usable.
func TestHostileServer_UnknownAndExceptionBitFCs(t *testing.T) {
	hs := hostileStartServer(t, nil)
	served := map[byte]bool{1: true, 2: true, 3: true, 4: true, 5: true, 6: true, 7: true, 8: true,
		11: true, 12: true, 15: true, 16: true, 17: true, 20: true, 21: true, 22: true, 23: true, 24: true, 43: true}
	conn := hs.dial(t)
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	txn := uint16(0)
	for fc := 0; fc <= 0xFF; fc++ {
		if served[byte(fc)] {
			continue
		}
		for _, payload := range [][]byte{nil, {0, 0, 0, 1}, make([]byte, 252)} {
			txn++
			if _, err := conn.Write(rawRequestFrame(txn, 0x11, byte(fc), payload)); err != nil {
				t.Fatalf("FC 0x%02x: write: %v", fc, err)
			}
			frame, err := hostileReadFrame(t, conn)
			if err != nil {
				t.Fatalf("FC 0x%02x with %d payload bytes: %v", fc, len(payload), err)
			}
			want := rawRequestFrame(txn, 0x11, byte(fc)|0x80, []byte{byte(exIllegalFunction)})
			if string(frame) != string(want) {
				t.Fatalf("FC 0x%02x with %d payload bytes: got % X, want % X", fc, len(payload), frame, want)
			}
		}
	}
}

// hostileCase is one request and what the server must do with it.
type hostileCase struct {
	name    string
	fc      byte
	payload []byte
	// close: the server drops the link; exc != 0: it answers with that
	// exception; otherwise it answers with success, checked by ok when set.
	close bool
	exc   ExceptionCode
	ok    func(payload []byte) error
}

func hostileU16(vs ...int) []byte {
	var b []byte
	for _, v := range vs {
		b = append(b, byte(v>>8), byte(v))
	}
	return b
}

func hostileRunCases(t *testing.T, hs *hostileServer, cases []hostileCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn := hs.dial(t)
			_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
			if _, err := conn.Write(rawRequestFrame(0x0102, 0x21, tc.fc, tc.payload)); err != nil {
				t.Fatalf("write: %v", err)
			}
			frame, err := hostileReadFrame(t, conn)
			if tc.close {
				if err == nil {
					t.Fatalf("got % X, want the link closed", frame)
				}
				return
			}
			if err != nil {
				t.Fatalf("no answer: %v", err)
			}
			if binary.BigEndian.Uint16(frame) != 0x0102 || frame[6] != 0x21 {
				t.Fatalf("wrong transaction or unit ID: % X", frame[:7])
			}
			fc, payload := frame[7], frame[8:]
			if tc.exc != 0 {
				if fc != tc.fc|0x80 || len(payload) != 1 || ExceptionCode(payload[0]) != tc.exc {
					t.Fatalf("got FC 0x%02x payload % X, want exception %d", fc, payload, tc.exc)
				}
				return
			}
			if fc != tc.fc {
				t.Fatalf("got FC 0x%02x payload % X, want success", fc, payload)
			}
			if tc.ok != nil {
				if err := tc.ok(payload); err != nil {
					t.Fatal(err)
				}
			}
			// The connection is still usable.
			if _, err := conn.Write(rawRequestFrame(9, 0x21, 0x03, []byte{0, 0, 0, 1})); err != nil {
				t.Fatalf("follow-up write: %v", err)
			}
			if _, err := hostileReadFrame(t, conn); err != nil {
				t.Fatalf("follow-up request: %v", err)
			}
		})
	}
}

func hostileLen(n int) func([]byte) error {
	return func(p []byte) error {
		if len(p) != n {
			return fmt.Errorf("payload % X: want %d bytes", p, n)
		}
		return nil
	}
}

// TestHostileServer_QuantityAndAddressBoundaries: quantity 0, the limit, the
// limit + 1, and address + quantity = 65536 (the last register, valid) and
// 65537 (invalid) for every function code with a quantity
// (Modbus Application Protocol V1.1b3 §6).
func TestHostileServer_QuantityAndAddressBoundaries(t *testing.T) {
	hs := hostileStartServer(t, nil)
	var cases []hostileCase
	reads := []struct {
		fc    byte
		limit int
		bytes func(q int) int
	}{
		{0x01, 2000, func(q int) int { return 1 + (q+7)/8 }},
		{0x02, 2000, func(q int) int { return 1 + (q+7)/8 }},
		{0x03, 125, func(q int) int { return 1 + 2*q }},
		{0x04, 125, func(q int) int { return 1 + 2*q }},
	}
	for _, r := range reads {
		cases = append(cases,
			hostileCase{name: fmt.Sprintf("FC%02d qty 0", r.fc), fc: r.fc, payload: hostileU16(0, 0), exc: exIllegalDataValue},
			hostileCase{name: fmt.Sprintf("FC%02d qty limit", r.fc), fc: r.fc, payload: hostileU16(0, r.limit), ok: hostileLen(r.bytes(r.limit))},
			hostileCase{name: fmt.Sprintf("FC%02d qty limit+1", r.fc), fc: r.fc, payload: hostileU16(0, r.limit+1), exc: exIllegalDataValue},
			hostileCase{name: fmt.Sprintf("FC%02d qty 0xFFFF", r.fc), fc: r.fc, payload: hostileU16(0, 0xFFFF), exc: exIllegalDataValue},
			hostileCase{name: fmt.Sprintf("FC%02d ends at 0xFFFF", r.fc), fc: r.fc, payload: hostileU16(0xFFFF-9, 10), ok: hostileLen(r.bytes(10))},
			hostileCase{name: fmt.Sprintf("FC%02d ends past 0xFFFF", r.fc), fc: r.fc, payload: hostileU16(0xFFFF-8, 10), exc: exIllegalDataAddress},
			hostileCase{name: fmt.Sprintf("FC%02d 5 bytes", r.fc), fc: r.fc, payload: hostileU16(0, 1, 0)[:5], close: true},
		)
	}
	coils := func(q int) []byte {
		return append(hostileU16(0, q), append([]byte{byte((q + 7) / 8)}, make([]byte, (q+7)/8)...)...)
	}
	regs := func(q int) []byte {
		return append(hostileU16(0, q), append([]byte{byte(2 * q)}, make([]byte, 2*q)...)...)
	}
	cases = append(cases,
		hostileCase{name: "FC15 qty 1968", fc: 0x0F, payload: coils(1968), ok: hostileLen(4)},
		hostileCase{name: "FC15 qty 1969", fc: 0x0F, payload: coils(1969), exc: exIllegalDataValue},
		hostileCase{name: "FC15 qty 1 ends past 0xFFFF", fc: 0x0F, payload: append(hostileU16(0xFFFF, 2), 1, 0), exc: exIllegalDataAddress},
		hostileCase{name: "FC16 qty 123", fc: 0x10, payload: regs(123), ok: hostileLen(4)},
		// 124 registers do not fit a request PDU: byte count 248 + 6 = 254 > 253.
		hostileCase{name: "FC16 qty 124 byte count 248 no data", fc: 0x10, payload: append(hostileU16(0, 124), 248), exc: exIllegalDataValue},
		hostileCase{name: "FC16 byte count 0 qty 1", fc: 0x10, payload: append(hostileU16(0, 1), 0), exc: exIllegalDataValue},
		hostileCase{name: "FC16 last register", fc: 0x10, payload: append(hostileU16(0xFFFF, 1), 2, 0, 0), ok: hostileLen(4)},
		hostileCase{name: "FC23 read 125 write 121", fc: 0x17,
			payload: append(hostileU16(0, 125, 0, 121), append([]byte{242}, make([]byte, 242)...)...), ok: hostileLen(251)},
		hostileCase{name: "FC23 read 126", fc: 0x17, payload: append(hostileU16(0, 126, 0, 1), 2, 0, 0), exc: exIllegalDataValue},
		hostileCase{name: "FC23 write 122", fc: 0x17, payload: append(hostileU16(0, 1, 0, 122), 244), exc: exIllegalDataValue},
		hostileCase{name: "FC23 write ends past 0xFFFF", fc: 0x17, payload: append(hostileU16(0, 1, 0xFFFF, 2), 4, 0, 0, 0, 0), exc: exIllegalDataAddress},
		hostileCase{name: "FC23 read ends past 0xFFFF", fc: 0x17, payload: append(hostileU16(0xFFFF, 2, 0, 1), 2, 0, 0), exc: exIllegalDataAddress},
		hostileCase{name: "FC23 data longer than byte count", fc: 0x17, payload: append(hostileU16(0, 1, 0, 1), 2, 0, 0, 0), close: true},
		hostileCase{name: "FC05 0xFF01", fc: 0x05, payload: hostileU16(0, 0xFF01), exc: exIllegalDataValue},
		hostileCase{name: "FC05 0x00FF", fc: 0x05, payload: hostileU16(0, 0x00FF), exc: exIllegalDataValue},
		hostileCase{name: "FC05 at 0xFFFF on", fc: 0x05, payload: hostileU16(0xFFFF, 0xFF00), ok: hostileLen(4)},
		hostileCase{name: "FC06 at 0xFFFF", fc: 0x06, payload: hostileU16(0xFFFF, 0xBEEF), ok: hostileLen(4)},
		hostileCase{name: "FC06 5 bytes", fc: 0x06, payload: []byte{0, 0, 0, 0, 0}, close: true},
		hostileCase{name: "FC22 7 bytes", fc: 0x16, payload: make([]byte, 7), close: true},
		hostileCase{name: "FC22 at 0xFFFF", fc: 0x16, payload: hostileU16(0xFFFF, 0xFFFF, 0), ok: hostileLen(6)},
	)
	hostileRunCases(t, hs, cases)
}

// TestHostileServer_OtherFunctionCodes: malformed and boundary requests for
// FC07, FC08, FC11, FC12, FC17, FC20, FC21, FC24 and FC43.
func TestHostileServer_OtherFunctionCodes(t *testing.T) {
	hs := hostileStartServer(t, nil)
	fileSub := func(ref byte, file, rec, length int) []byte {
		return append([]byte{ref}, hostileU16(file, rec, length)...)
	}
	readFile := func(subs ...[]byte) []byte {
		var b []byte
		for _, s := range subs {
			b = append(b, s...)
		}
		return append([]byte{byte(len(b))}, b...)
	}
	writeSub := func(ref byte, file, rec, length int, data []byte) []byte {
		return append(append([]byte{ref}, hostileU16(file, rec, length)...), data...)
	}
	cases := []hostileCase{
		{name: "FC07", fc: 0x07, ok: hostileLen(1)},
		{name: "FC11", fc: 0x0B, ok: hostileLen(4)},
		{name: "FC12", fc: 0x0C, ok: hostileLen(9)},
		{name: "FC17", fc: 0x11, ok: hostileLen(3)},
		{name: "FC17 with data", fc: 0x11, payload: []byte{0}, close: true},

		{name: "FC08 empty", fc: 0x08, close: true},
		{name: "FC08 one byte", fc: 0x08, payload: []byte{0}, close: true},
		{name: "FC08 echo 250 bytes", fc: 0x08, payload: append([]byte{0, 0}, make([]byte, 250)...), ok: hostileLen(252)},
		{name: "FC08 vendor sub-function", fc: 0x08, payload: []byte{0xFF, 0xFF}, ok: hostileLen(2)},

		{name: "FC20 empty", fc: 0x14, close: true},
		{name: "FC20 byte count 0", fc: 0x14, payload: []byte{0}, exc: exIllegalDataValue},
		{name: "FC20 byte count disagrees", fc: 0x14, payload: append([]byte{14}, fileSub(6, 1, 0, 1)...), close: true},
		{name: "FC20 byte count 8", fc: 0x14, payload: append([]byte{8}, append(fileSub(6, 1, 0, 1), 0)...), exc: exIllegalDataValue},
		{name: "FC20 reference type 7", fc: 0x14, payload: readFile(fileSub(7, 1, 0, 1)), exc: exIllegalDataAddress},
		{name: "FC20 reference type 0", fc: 0x14, payload: readFile(fileSub(0, 1, 0, 1)), exc: exIllegalDataAddress},
		{name: "FC20 file 0", fc: 0x14, payload: readFile(fileSub(6, 0, 0, 1)), exc: exIllegalDataAddress},
		{name: "FC20 record 0x270F", fc: 0x14, payload: readFile(fileSub(6, 1, 0x270F, 1)), ok: hostileLen(5)},
		{name: "FC20 record 0x2710", fc: 0x14, payload: readFile(fileSub(6, 1, 0x2710, 1)), exc: exIllegalDataAddress},
		{name: "FC20 length 0", fc: 0x14, payload: readFile(fileSub(6, 1, 0, 0)), exc: exIllegalDataAddress},
		{name: "FC20 length 0xFFFF", fc: 0x14, payload: readFile(fileSub(6, 1, 0, 0xFFFF)), exc: exIllegalDataValue},
		// One record of 121 registers is 2 + 242 = 244 response bytes: fits.
		{name: "FC20 largest record", fc: 0x14, payload: readFile(fileSub(6, 1, 0, 121)), ok: hostileLen(245)},
		{name: "FC20 one register too many", fc: 0x14, payload: readFile(fileSub(6, 1, 0, 122)), exc: exIllegalDataValue},
		{name: "FC20 128 registers (sub-response length would wrap)", fc: 0x14, payload: readFile(fileSub(6, 1, 0, 128)), exc: exIllegalDataValue},
		// Byte count 0xF5 (35 sub-requests) is the largest allowed (V1.1b3 §6.14).
		{name: "FC20 35 sub-requests", fc: 0x14, payload: readFile(func() [][]byte {
			var s [][]byte
			for range 35 {
				s = append(s, fileSub(6, 1, 0, 1))
			}
			return s
		}()...), ok: hostileLen(1 + 35*4)},
		{name: "FC20 byte count 0xFC", fc: 0x14, payload: append([]byte{0xFC}, make([]byte, 0xFC)...), close: true},

		{name: "FC21 empty", fc: 0x15, close: true},
		{name: "FC21 byte count 8", fc: 0x15, payload: append([]byte{8}, make([]byte, 8)...), exc: exIllegalDataValue},
		{name: "FC21 ok", fc: 0x15, payload: readFile(writeSub(6, 1, 0, 1, []byte{1, 2})), ok: hostileLen(10)},
		{name: "FC21 reference type 5", fc: 0x15, payload: readFile(writeSub(5, 1, 0, 1, []byte{1, 2})), exc: exIllegalDataAddress},
		{name: "FC21 length runs past the data", fc: 0x15, payload: readFile(writeSub(6, 1, 0, 2, []byte{1, 2})), close: true},
		{name: "FC21 length 0xFFFF", fc: 0x15, payload: readFile(writeSub(6, 1, 0, 0xFFFF, []byte{1, 2})), close: true},
		{name: "FC21 trailing partial sub-request", fc: 0x15, payload: readFile(writeSub(6, 1, 0, 1, []byte{1, 2}), []byte{6, 0, 1}), close: true},
		{name: "FC21 record 0x2710", fc: 0x15, payload: readFile(writeSub(6, 1, 0x2710, 1, []byte{1, 2})), exc: exIllegalDataAddress},

		{name: "FC24 empty", fc: 0x18, close: true},
		{name: "FC24 one byte", fc: 0x18, payload: []byte{0}, close: true},
		{name: "FC24 three bytes", fc: 0x18, payload: []byte{0, 0, 0}, close: true},
		{name: "FC24 31 values", fc: 0x18, payload: hostileU16(31), ok: hostileLen(4 + 62)},
		{name: "FC24 empty queue", fc: 0x18, payload: hostileU16(32), ok: hostileLen(4)},

		{name: "FC43 empty", fc: 0x2B, close: true},
		{name: "FC43 MEI only", fc: 0x2B, payload: []byte{0x0E}, close: true},
		{name: "FC43 MEI 0x0D", fc: 0x2B, payload: []byte{0x0D, 1, 0}, exc: exIllegalFunction},
		{name: "FC43 MEI 0x0D alone", fc: 0x2B, payload: []byte{0x0D}, exc: exIllegalFunction},
		{name: "FC43 category 0", fc: 0x2B, payload: []byte{0x0E, 0, 0}, exc: exIllegalDataValue},
		{name: "FC43 category 0xFF", fc: 0x2B, payload: []byte{0x0E, 0xFF, 0}, exc: exIllegalDataValue},
		{name: "FC43 individual 0xFF", fc: 0x2B, payload: []byte{0x0E, 4, 0xFF}, exc: exIllegalDataAddress},
		{name: "FC43 stream from unknown object restarts", fc: 0x2B, payload: []byte{0x0E, 1, 0x7F}, ok: func(p []byte) error {
			if len(p) < 8 || p[5] != 3 || p[6] != 0 {
				return fmt.Errorf("payload % X: want the 3 basic objects from object 0", p)
			}
			return nil
		}},
	}
	hostileRunCases(t, hs, cases)
}

// TestHostileServer_DeviceIDContinuationWalk follows the server's own
// MoreFollows/NextObjectID chain from every possible start object and
// category: it must end, within a bounded number of pages, without repeating.
func TestHostileServer_DeviceIDContinuationWalk(t *testing.T) {
	hs := hostileStartServer(t, nil)
	conn := hs.dial(t)
	_ = conn.SetDeadline(time.Now().Add(8 * time.Second))
	txn := uint16(0)
	for cat := byte(1); cat <= 3; cat++ {
		for start := 0; start <= 0xFF; start += 17 {
			next := byte(start)
			seen := map[byte]bool{}
			for page := 0; ; page++ {
				if page > 32 {
					t.Fatalf("category %d from 0x%02x: more than 32 pages", cat, start)
				}
				txn++
				if _, err := conn.Write(rawRequestFrame(txn, 1, 0x2B, []byte{0x0E, cat, next})); err != nil {
					t.Fatalf("write: %v", err)
				}
				frame, err := hostileReadFrame(t, conn)
				if err != nil {
					t.Fatalf("category %d from 0x%02x page %d: %v", cat, start, page, err)
				}
				p := frame[8:]
				if frame[7] != 0x2B || len(p) < 6 {
					t.Fatalf("category %d from 0x%02x: % X", cat, start, frame)
				}
				if p[3] == 0 {
					break
				}
				if p[3] != 0xFF || seen[p[4]] {
					t.Fatalf("category %d from 0x%02x: MoreFollows 0x%02x NextObjectID 0x%02x loops", cat, start, p[3], p[4])
				}
				seen[p[4]] = true
				next = p[4]
			}
		}
	}
}

// hostileCheckResponse validates a success response the sweep received for a
// request; it returns an error for an answer the specification does not allow.
func hostileCheckResponse(fc byte, req, res []byte) error {
	switch fc {
	case 0x01, 0x02, 0x03, 0x04, 0x0C, 0x11, 0x14, 0x17:
		if len(res) < 1 || int(res[0]) != len(res)-1 {
			return fmt.Errorf("byte count disagrees with payload % X", res)
		}
	case 0x05, 0x06, 0x0F, 0x10:
		if len(res) != 4 || string(res) != string(req[:4]) {
			return fmt.Errorf("echo % X of % X", res, req)
		}
	case 0x16, 0x15:
		if string(res) != string(req) {
			return fmt.Errorf("echo % X of % X", res, req)
		}
	case 0x07:
		if len(res) != 1 {
			return fmt.Errorf("payload % X", res)
		}
	case 0x0B:
		if len(res) != 4 {
			return fmt.Errorf("payload % X", res)
		}
	case 0x08:
		if len(res) < 2 || string(res[:2]) != string(req[:2]) {
			return fmt.Errorf("sub-function not echoed: % X", res)
		}
	case 0x18:
		if len(res) < 4 || int(binary.BigEndian.Uint16(res)) != len(res)-2 || binary.BigEndian.Uint16(res[2:]) > 31 {
			return fmt.Errorf("FIFO payload % X", res)
		}
	case 0x2B:
		if len(res) < 6 || res[0] != 0x0E {
			return fmt.Errorf("MEI payload % X", res)
		}
	default:
		return fmt.Errorf("success answer to unserved FC 0x%02x", fc)
	}
	return nil
}

// TestHostileServer_RandomPDUSweep: seeded random requests, biased towards the
// served function codes and their boundary values, on a long-lived connection
// that is re-dialled whenever the server drops it. Every answer must be well
// formed, and no request may make the server panic.
func TestHostileServer_RandomPDUSweep(t *testing.T) {
	baseline := chaosGoroutines()
	hs := hostileStartServer(t, nil)
	rng := rand.New(rand.NewSource(20261105))
	fcs := []byte{1, 2, 3, 4, 5, 6, 7, 8, 11, 12, 15, 16, 17, 20, 21, 22, 23, 24, 43}
	interesting := []int{0, 1, 2, 7, 8, 0x7D, 0x7E, 0x7B, 0x7C, 0x79, 0x7A, 0x7F, 0x80, 0xF6, 0xF5, 0xFF, 0x270F, 0x2710, 0x07B0, 0x07B1, 0x07D0, 0x07D1, 0xFF00, 0xFFFE, 0xFFFF}
	pick16 := func() int {
		if rng.Intn(3) == 0 {
			return rng.Intn(0x10000)
		}
		return interesting[rng.Intn(len(interesting))]
	}

	var conn net.Conn
	redial := func() {
		if conn != nil {
			_ = conn.Close()
		}
		c, err := net.DialTimeout("tcp", hs.addr, 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		conn = c
	}
	redial()
	defer func() { _ = conn.Close() }()

	const rounds = 4000
	closes := 0
	for i := range rounds {
		var fc byte
		if rng.Intn(8) == 0 {
			fc = byte(rng.Intn(256))
		} else {
			fc = fcs[rng.Intn(len(fcs))]
		}
		var payload []byte
		switch rng.Intn(4) {
		case 0: // random bytes of random length
			payload = make([]byte, rng.Intn(253))
			rng.Read(payload)
		default: // fields from the interesting set, maybe a byte count, maybe data
			for range rng.Intn(5) {
				payload = append(payload, hostileU16(pick16())...)
			}
			if rng.Intn(2) == 0 {
				payload = append(payload, byte(pick16()))
			}
			extra := make([]byte, rng.Intn(64))
			rng.Read(extra)
			payload = append(payload, extra...)
			if len(payload) > 252 {
				payload = payload[:252]
			}
		}
		txn := uint16(i)
		unit := byte(rng.Intn(256))
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := conn.Write(rawRequestFrame(txn, unit, fc, payload)); err != nil {
			t.Fatalf("round %d: write: %v", i, err)
		}
		frame, err := hostileReadFrame(t, conn)
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				t.Fatalf("round %d: FC 0x%02x payload % X: no answer and no close", i, fc, payload)
			}
			closes++
			redial()
			continue
		}
		if binary.BigEndian.Uint16(frame) != txn || frame[6] != unit {
			t.Fatalf("round %d: header % X for txn %d unit %d", i, frame[:7], txn, unit)
		}
		resFC, res := frame[7], frame[8:]
		switch resFC {
		case fc | 0x80:
			if fc&0x80 == 0 || resFC == fc && len(res) == 1 && res[0] == byte(exIllegalFunction) {
				if len(res) != 1 || res[0] < 1 || res[0] > 4 {
					t.Fatalf("round %d: FC 0x%02x payload % X: exception payload % X", i, fc, payload, res)
				}
				if res[0] == byte(exServerDeviceFailure) {
					t.Errorf("round %d: FC 0x%02x payload % X: Server Device Failure from a well-behaved handler", i, fc, payload)
				}
				break
			}
			fallthrough
		case fc:
			if err := hostileCheckResponse(fc, payload, res); err != nil {
				t.Fatalf("round %d: FC 0x%02x payload % X: %v", i, fc, payload, err)
			}
		default:
			t.Fatalf("round %d: FC 0x%02x answered with FC 0x%02x", i, fc, resFC)
		}
	}
	t.Logf("%d requests, %d link closes", rounds, closes)
	_ = conn.Close()
	_ = hs.srv.Stop()
	chaosLeakCheck(t, baseline)
}

// TestHostileServer_ConnectionFlood opens many more connections than
// MaxClients. The excess is closed at once; the server never holds more than
// MaxClients; once the flood is gone the server serves again and holds no
// connection.
func TestHostileServer_ConnectionFlood(t *testing.T) {
	baseline := chaosGoroutines()
	const maxClients = 4
	hs := hostileStartServer(t, func(c *ServerConfig) { c.MaxClients = maxClients; c.Timeout = 2 * time.Second })
	sampler := chaosSampleConns(func() int { return chaosServerConns(hs.srv) }, maxClients)

	var conns []net.Conn
	for range 60 {
		c, err := net.DialTimeout("tcp", hs.addr, 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		conns = append(conns, c)
	}
	// Ask every connection for a register; count those that are served.
	served := 0
	for i, c := range conns {
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Write(rawRequestFrame(uint16(i), 1, 0x03, []byte{0, 0, 0, 1})); err != nil {
			continue
		}
		if _, err := hostileReadFrame(t, c); err == nil {
			served++
		}
	}
	if served == 0 || served > maxClients {
		t.Fatalf("%d of %d flood connections served, want 1..%d", served, len(conns), maxClients)
	}
	for _, c := range conns {
		_ = c.Close()
	}
	if !chaosEventually(3*time.Second, func() bool { return chaosServerConns(hs.srv) == 0 }) {
		t.Fatalf("server still holds %d connections after the flood left", chaosServerConns(hs.srv))
	}
	sampler.stop()
	hostileExpectServed(t, hs)
	_ = hs.srv.Stop()
	chaosLeakCheck(t, baseline)
}

// TestHostileServer_ClientNeverReads pipelines requests without reading a
// single response, so the server's writes block. The connection must be
// dropped once the write deadline (ServerConfig.Timeout) passes, and Shutdown
// must not wait on it beyond that.
func TestHostileServer_ClientNeverReads(t *testing.T) {
	baseline := chaosGoroutines()
	const timeout = 300 * time.Millisecond
	hs := hostileStartServer(t, func(c *ServerConfig) { c.Timeout = timeout })
	conn := hs.dial(t)
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4096)
	}
	req := rawRequestFrame(1, 1, 0x03, []byte{0, 0, 0, 125}) // 257-byte answers
	burst := make([]byte, 0, len(req)*4000)
	for range 4000 {
		burst = append(burst, req...)
	}
	go func() {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, _ = conn.Write(burst)
	}()
	if !chaosEventually(5*time.Second, func() bool { return chaosServerConns(hs.srv) == 0 }) {
		t.Fatalf("server still holds the connection of a client that never reads")
	}
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		done <- hs.srv.Shutdown(ctx)
	}()
	if err := <-done; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	_ = conn.Close()
	chaosLeakCheck(t, baseline)
}

// TestHostileServer_ShutdownWithStalledClients: Shutdown returns promptly
// while clients hold connections in the middle of a frame.
func TestHostileServer_ShutdownWithStalledClients(t *testing.T) {
	baseline := chaosGoroutines()
	hs := hostileStartServer(t, func(c *ServerConfig) { c.Timeout = time.Minute })
	for i := range 8 {
		conn := hs.dial(t)
		// Half a header, a header without its body, a body cut short...
		_, _ = conn.Write(rawRequestFrame(uint16(i), 1, 0x10, hostileU16(0, 100))[:3+i])
	}
	if !chaosEventually(2*time.Second, func() bool { return chaosServerConns(hs.srv) == 8 }) {
		t.Fatalf("server holds %d connections, want 8", chaosServerConns(hs.srv))
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := hs.srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown with stalled clients: %v", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Shutdown took %v", d)
	}
	chaosLeakCheck(t, baseline)
}

// TestHostileServer_TLSGarbage sends plain Modbus/TCP and random bytes to a
// tcp+tls server, and malformed MBAP frames inside an authenticated TLS
// session. Each connection ends without an answer, and the server goes on
// serving.
func TestHostileServer_TLSGarbage(t *testing.T) {
	baseline := chaosGoroutines()
	srv := chaosStartServer(t, "tcp+tls", hostileDevice{}, func(c *ServerConfig) {
		c.TLSHandshakeTimeout = 500 * time.Millisecond
		c.Timeout = 500 * time.Millisecond
	})
	rng := rand.New(rand.NewSource(3))
	for i := range 4 {
		conn, err := net.DialTimeout("tcp", srv.addr, 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		junk := rawRequestFrame(1, 1, 0x03, []byte{0, 0, 0, 1})
		if i > 0 {
			junk = make([]byte, 64*i)
			rng.Read(junk)
		}
		_, _ = conn.Write(junk)
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		if n, _ := io.Copy(io.Discard, conn); n > 0 && i == 0 {
			// A TLS alert is fine; a Modbus answer is not.
			t.Logf("server sent %d bytes (TLS alert) to plain Modbus/TCP", n)
		}
		_ = conn.Close()
	}

	conf := chaosClientConfig(t, "tcp+tls", srv.port(), chaosModeSingle, 2*time.Second, nil)
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{chaosTLSClientPair},
		RootCAs:      chaosTLSClientPool,
		ServerName:   "localhost",
		MinVersion:   tls.VersionTLS12,
	}
	for _, frame := range [][]byte{
		hostileFrame(1, 0, 0, 1, nil),
		hostileFrame(1, 7, 6, 1, []byte{3, 0, 0, 0, 1}),
		hostileFrame(1, 0, 0xFFFF, 1, make([]byte, 32)),
	} {
		raw, err := net.DialTimeout("tcp", srv.addr, 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		c := tls.Client(raw, tlsConf)
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		if err := c.Handshake(); err != nil {
			t.Fatalf("handshake: %v", err)
		}
		_, _ = c.Write(frame)
		buf := make([]byte, 16)
		if n, err := c.Read(buf); n > 0 {
			t.Fatalf("server answered malformed frame % X with % X", frame, buf[:n])
		} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatalf("server kept the TLS session open after malformed frame % X", frame)
		}
		_ = c.Close()
	}

	good := chaosOpenClient(t, conf)
	if _, err := good.ReadRegister(context.Background(), 1, 5, HoldingRegister); err != nil {
		t.Fatalf("well-behaved TLS client after the garbage: %v", err)
	}
	_ = good.Close()
	srv.stop()
	chaosLeakCheck(t, baseline)
}
