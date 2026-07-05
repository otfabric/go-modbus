package modbus

import (
	"net"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// This file drives our own server through malformed and boundary requests over
// raw TCP, asserting it responds with the correct Modbus exception or drops the
// link on a framing/protocol error (ErrProtocolError). It complements the happy
// path exercised by conformance_test.go and covers the server handler error
// branches directly.

// startRawServer starts a TCP Modbus server backed by handler and returns its
// listen address plus a cleanup function. Unlike startPair it does not create a
// client, so tests can craft raw frames.
func startRawServer(t *testing.T, handler RequestHandler) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		t.Fatalf("SplitHostPort: %v", err)
	}
	_ = ln.Close()

	addr := "127.0.0.1:" + port
	server, err := NewServer(&ServerConfig{URL: "tcp://" + addr, MaxClients: 16}, handler)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return addr, func() { _ = server.Stop() }
}

// rawRequestFrame builds a Modbus/TCP request frame.
func rawRequestFrame(txid uint16, unitID, fc byte, payload []byte) []byte {
	length := uint16ToBytes(BigEndian, uint16(2+len(payload)))
	frame := []byte{byte(txid >> 8), byte(txid), 0x00, 0x00, length[0], length[1], unitID, fc}
	return append(frame, payload...)
}

// rawExchange dials addr, sends one request, and returns the parsed response.
// closed is true when the server dropped the connection without replying (the
// expected behavior for ErrProtocolError).
func rawExchange(t *testing.T, addr string, unitID, fc byte, payload []byte) (res *adu.Response, closed bool) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(rawRequestFrame(0x0001, unitID, fc, payload)); err != nil {
		t.Fatalf("write: %v", err)
	}
	r, err := readMBAPResponse(conn)
	if err != nil {
		return nil, true
	}
	return r, false
}

func TestB2B_Adversarial_ServerBranches(t *testing.T) {
	addr, cleanup := startRawServer(t, newRefDevice())
	defer cleanup()

	cases := []struct {
		name      string
		unitID    byte
		fc        byte
		payload   []byte
		wantClose bool
		wantExc   ExceptionCode
	}{
		// --- Framing / protocol errors: server drops the link ---
		{"FC01 short payload", refUnitID, 0x01, []byte{0, 0, 0}, true, 0},
		{"FC03 short payload", refUnitID, 0x03, []byte{0, 0, 0}, true, 0},
		{"FC05 wrong length", refUnitID, 0x05, []byte{0, 0, 0xff}, true, 0},
		{"FC06 wrong length", refUnitID, 0x06, []byte{0, 0, 0}, true, 0},
		{"FC15 too short", refUnitID, 0x0f, []byte{0, 0, 0, 1, 1}, true, 0},
		{"FC15 byte-count mismatch", refUnitID, 0x0f, []byte{0, 0, 0, 8, 0x02, 0xff}, true, 0},
		{"FC15 data-length mismatch", refUnitID, 0x0f, []byte{0, 0, 0, 16, 0x02, 0xff}, true, 0},
		{"FC16 too short", refUnitID, 0x10, []byte{0, 0, 0, 1, 2}, true, 0},
		{"FC16 byte-count mismatch", refUnitID, 0x10, []byte{0, 0, 0, 1, 0x04, 0x00, 0x01}, true, 0},
		{"FC16 data-length mismatch", refUnitID, 0x10, []byte{0, 0, 0, 2, 0x04, 0x00, 0x01, 0x00}, true, 0},
		{"FC22 wrong length", refUnitID, 0x16, []byte{0, 0, 0, 0, 0}, true, 0},
		{"FC23 too short", refUnitID, 0x17, []byte{0, 0, 0, 1, 0, 0, 0, 1, 2}, true, 0},
		{"FC23 byte-count mismatch", refUnitID, 0x17, []byte{0, 0, 0, 1, 0, 0, 0, 1, 0x04, 0x00, 0x01}, true, 0},
		{"FC43 short payload", refUnitID, 0x2b, []byte{0x0e, 0x01}, true, 0},

		// --- Semantic errors: server returns a Modbus exception ---
		{"FC01 quantity zero", refUnitID, 0x01, []byte{0, 0, 0, 0}, false, exIllegalDataValue},
		{"FC01 quantity too large", refUnitID, 0x01, []byte{0, 0, 0x08, 0x00}, false, exIllegalDataValue},
		{"FC01 addr overflow", refUnitID, 0x01, []byte{0xff, 0xff, 0, 2}, false, exIllegalDataAddress},
		{"FC03 quantity zero", refUnitID, 0x03, []byte{0, 0, 0, 0}, false, exIllegalDataValue},
		{"FC03 addr overflow", refUnitID, 0x03, []byte{0xff, 0xff, 0, 2}, false, exIllegalDataAddress},
		{"FC05 invalid value", refUnitID, 0x05, []byte{0, 0, 0x01, 0x00}, false, exIllegalDataValue},
		{"FC15 quantity zero", refUnitID, 0x0f, []byte{0, 0, 0, 0, 0, 0}, false, exIllegalDataValue},
		{"FC16 quantity zero", refUnitID, 0x10, []byte{0, 0, 0, 0, 0, 0}, false, exIllegalDataValue},
		{"FC22 addr out of range", refUnitID, 0x16, []byte{0x0f, 0xff, 0, 0, 0, 0}, false, exIllegalDataAddress},
		{"FC23 readQty zero", refUnitID, 0x17, []byte{0, 0, 0, 0, 0, 0, 0, 1, 0x02, 0x00, 0x01}, false, exIllegalDataValue},
		{"FC43 illegal category", refUnitID, 0x2b, []byte{0x0e, 0x05, 0x00}, false, exIllegalDataValue},
		{"FC43 wrong MEI type", refUnitID, 0x2b, []byte{0x0d, 0x01, 0x00}, false, exIllegalFunction},
		{"FC43 individual unknown object", refUnitID, 0x2b, []byte{0x0e, 0x04, 0x7e}, false, exIllegalDataAddress},
		{"unknown FC", refUnitID, 0x65, []byte{0x00}, false, exIllegalFunction},
		{"wrong unit id", 0x02, 0x01, []byte{0, 0, 0, 1}, false, exIllegalFunction},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			res, closed := rawExchange(t, addr, tc.unitID, tc.fc, tc.payload)
			if tc.wantClose {
				if !closed {
					t.Fatalf("expected link close, got response %+v", res)
				}
				return
			}
			if closed {
				t.Fatal("expected exception response, got link close")
			}
			if res.UnitID != tc.unitID {
				t.Errorf("unit ID = 0x%02x, want 0x%02x", res.UnitID, tc.unitID)
			}
			if res.FunctionCode != tc.fc|0x80 {
				t.Fatalf("FC = 0x%02x, want exception 0x%02x", res.FunctionCode, tc.fc|0x80)
			}
			if len(res.Payload) != 1 || ExceptionCode(res.Payload[0]) != tc.wantExc {
				t.Fatalf("exception payload = %v, want [0x%02x]", res.Payload, byte(tc.wantExc))
			}
		})
	}
}
