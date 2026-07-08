// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/protocol"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// readMBAPFrame reads one complete MBAP frame from conn. Returns the full frame
// (6-byte header + PDU) or an error.
func readMBAPFrame(conn net.Conn) ([]byte, error) {
	header := make([]byte, 6)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	pduLen := int(header[4])<<8 | int(header[5])
	if pduLen < 1 {
		return nil, io.ErrUnexpectedEOF
	}
	body := make([]byte, pduLen)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	return append(header, body...), nil
}

// writeMBAPException writes an MBAP exception frame for the given FC.
func writeMBAPException(conn net.Conn, txid []byte, unitID, fc, exCode byte) error {
	_, err := conn.Write([]byte{
		txid[0], txid[1], 0x00, 0x00, 0x00, 0x03,
		unitID, fc | 0x80, exCode,
	})
	return err
}

// writeMBAPNormal writes an MBAP normal-response frame.
func writeMBAPNormal(conn net.Conn, txid []byte, unitID, fc byte, payload []byte) error {
	length := uint16ToBytes(BigEndian, uint16(2+len(payload)))
	frame := append(append([]byte{txid[0], txid[1], 0x00, 0x00}, length...), unitID, fc)
	frame = append(frame, payload...)
	_, err := conn.Write(frame)
	return err
}

// ---------------------------------------------------------------------------
// SupportsFunction
// ---------------------------------------------------------------------------

// TestSupportsFunction_FC03_NormalResponse verifies true when server returns normal FC03 response.
func TestSupportsFunction_FC03_NormalResponse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid := frame[0:2]
			unitID := frame[6]
			fc := frame[7]
			if fc == byte(FCReadHoldingRegisters) {
				_ = writeMBAPNormal(sock, txid, unitID, fc, []byte{0x02, 0x00, 0x00})
			} else {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
			}
		}
	}()

	client, err := New(Config{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	ok, err := client.SupportsFunction(context.Background(), 1, FCReadHoldingRegisters)
	if err != nil {
		t.Fatalf("SupportsFunction: %v", err)
	}
	if !ok {
		t.Fatal("expected true (FC03 normal response)")
	}
}

// TestSupportsFunction_FC03_ExceptionResponse verifies true when server returns valid exception.
func TestSupportsFunction_FC03_ExceptionResponse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid := frame[0:2]
			unitID := frame[6]
			fc := frame[7]
			_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
		}
	}()

	client, err := New(Config{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	ok, err := client.SupportsFunction(context.Background(), 1, FCReadHoldingRegisters)
	if err != nil {
		t.Fatalf("SupportsFunction: %v", err)
	}
	if !ok {
		t.Fatal("expected true (exception = device recognises FC)")
	}
}

// TestSupportsFunction_WrongUnitID verifies false when response has wrong unit ID.
func TestSupportsFunction_WrongUnitID(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid := frame[0:2]
			fc := frame[7]
			wrongUnitId := byte(0x99)
			_ = writeMBAPException(sock, txid, wrongUnitId, fc, byte(exIllegalDataAddress))
		}
	}()

	client, err := New(Config{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	ok, err := client.SupportsFunction(context.Background(), 1, FCReadHoldingRegisters)
	if ok {
		t.Fatal("expected false when response unit ID does not match")
	}
	if !errors.Is(err, ErrBadUnitID) {
		t.Fatalf("expected ErrBadUnitID, got %v", err)
	}
}

// TestSupportsFunction_UnsupportedFC verifies (false, ErrUnexpectedParameters) for unknown FC.
func TestSupportsFunction_UnsupportedFC(t *testing.T) {
	client, err := New(Config{URL: "tcp://127.0.0.1:1", Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_ = client.Open()
	defer func() { _ = client.Close() }()

	// Use a function code that is not in the detection probe set (e.g. FC 0x16 Mask Write Register).
	ok, err := client.SupportsFunction(context.Background(), 1, FCMaskWriteRegister)
	if err == nil {
		t.Fatal("expected error for unsupported FC")
	}
	if !errors.Is(err, ErrUnexpectedParameters) {
		t.Errorf("expected ErrUnexpectedParameters, got %v", err)
	}
	if ok {
		t.Fatal("expected false for unsupported FC")
	}
}

// TestSupportsFunction_ContextCanceled verifies error when context is canceled.
func TestSupportsFunction_ContextCanceled(t *testing.T) {
	client, err := New(Config{URL: "tcp://127.0.0.1:1", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_ = client.Open()
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = client.SupportsFunction(ctx, 1, FCReadHoldingRegisters)
	if err == nil {
		t.Fatal("expected error when context is canceled")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// SupportsDeviceIdentification
// ---------------------------------------------------------------------------

// TestSupportsDeviceIdentification_FC43_NormalResponse verifies true when server returns valid FC43 response.
func TestSupportsDeviceIdentification_FC43_NormalResponse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid := frame[0:2]
			unitID := frame[6]
			fc := frame[7]
			if fc == byte(FCEncapsulatedInterface) {
				payload := []byte{
					byte(MEIReadDeviceIdentification),
					ReadDeviceIDBasic,
					0x01, 0x00, 0x00,
					0x01,
					0x00, 0x03, 'A', 'B', 'C',
				}
				_ = writeMBAPNormal(sock, txid, unitID, fc, payload)
			} else {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
			}
		}
	}()

	client, err := New(Config{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	ok, err := client.SupportsDeviceIdentification(context.Background(), 1)
	if err != nil {
		t.Fatalf("SupportsDeviceIdentification: %v", err)
	}
	if !ok {
		t.Fatal("expected true (FC43 normal response)")
	}
}

// TestSupportsDeviceIdentification_FC43_ExceptionResponse verifies true when server returns FC43 exception.
func TestSupportsDeviceIdentification_FC43_ExceptionResponse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid := frame[0:2]
			unitID := frame[6]
			fc := frame[7]
			_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
		}
	}()

	client, err := New(Config{
		URL:     "tcp://" + ln.Addr().String(),
		Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	ok, err := client.SupportsDeviceIdentification(context.Background(), 1)
	if err != nil {
		t.Fatalf("SupportsDeviceIdentification: %v", err)
	}
	if !ok {
		t.Fatal("expected true (FC43 exception = device recognises FC)")
	}
}

// TestSupportsDeviceIdentification_ContextCanceled verifies error when context is canceled.
func TestSupportsDeviceIdentification_ContextCanceled(t *testing.T) {
	client, err := New(Config{URL: "tcp://127.0.0.1:1", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_ = client.Open()
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = client.SupportsDeviceIdentification(ctx, 1)
	if err == nil {
		t.Fatal("expected error when context is canceled")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// isValidModbusException unit tests
// ---------------------------------------------------------------------------

func TestIsValidModbusException(t *testing.T) {
	tests := []struct {
		name    string
		reqFC   uint8
		resFC   uint8
		payload []byte
		want    bool
	}{
		{"valid exception 0x01", 0x03, 0x83, []byte{0x01}, true},
		{"valid exception 0x02", 0x03, 0x83, []byte{0x02}, true},
		{"valid exception 0x0B", 0x2B, 0xAB, []byte{0x0B}, true},
		{"normal response", 0x03, 0x03, []byte{0x02, 0x00, 0x00}, false},
		{"wrong FC", 0x03, 0x84, []byte{0x01}, false},
		{"empty payload", 0x03, 0x83, []byte{}, false},
		{"extra payload", 0x03, 0x83, []byte{0x01, 0x02}, false},
		{"out of range 0x00", 0x03, 0x83, []byte{0x00}, false},
		{"out of range 0x0C", 0x03, 0x83, []byte{0x0C}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqFC := FunctionCode(tt.reqFC)
			res := protocol.Response{FunctionCode: FunctionCode(tt.resFC), Payload: tt.payload}
			if got := protocol.IsValidModbusException(reqFC, res); got != tt.want {
				t.Errorf("IsValidModbusException() = %v, want %v", got, tt.want)
			}
		})
	}
}
