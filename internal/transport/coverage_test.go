// SPDX-License-Identifier: MIT

package transport

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/logging"
	"github.com/otfabric/go-modbus/internal/protocol"
)

// --- RTU WriteResponse ---

func TestRTUWriteResponse(t *testing.T) {
	p1, p2 := net.Pipe()
	rt := NewRTU(p2, "", 9600, 50*time.Millisecond, logging.NopLogger())
	// Reset deadline after NewRTU's discardLink
	_ = p2.SetDeadline(time.Time{})

	done := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 256)
		n, _ := p1.Read(buf)
		done <- buf[:n]
	}()

	res := &adu.Response{UnitID: 0x31, FunctionCode: byte(protocol.FCWriteSingleRegister), Payload: []byte{0x12, 0x34}}
	err := rt.WriteResponse(res)
	if err != nil {
		t.Fatalf("WriteResponse: %v", err)
	}

	got := <-done
	expected := adu.AssembleRTUFrame(0x31, byte(protocol.FCWriteSingleRegister), []byte{0x12, 0x34})
	if !bytes.Equal(got, expected) {
		t.Errorf("WriteResponse frame mismatch:\n  got:  % X\n  want: % X", got, expected)
	}

	_ = p1.Close()
	_ = p2.Close()
}

// --- RTU ReadRequest ---

func TestRTUReadRequest(t *testing.T) {
	p1, p2 := net.Pipe()
	rt := NewRTU(p2, "", 9600, 50*time.Millisecond, logging.NopLogger())

	_, _, err := rt.ReadRequest()
	if err == nil {
		t.Error("ReadRequest should return error (unimplemented)")
	}

	_ = p1.Close()
	_ = p2.Close()
}

// --- RTU readVariableLengthResponse (diagnostics path) ---

func TestRTUReadVariableLengthDiagnostics(t *testing.T) {
	p1, p2 := net.Pipe()
	rt := NewRTU(p2, "", 9600, 50*time.Millisecond, logging.NopLogger())

	// Build a valid FC08 diagnostics response frame:
	// unit=0x01, fc=0x08, sub-function 0x00 0x00, data 0x12 0x34
	payload := []byte{0x00, 0x00, 0x12, 0x34}
	frame := adu.AssembleRTUFrame(0x01, byte(protocol.FCDiagnostics), payload)

	go func() {
		// Feed the frame byte by byte with small delay to allow the
		// variable-length reader to accumulate bytes then time out.
		for _, b := range frame {
			_, _ = p1.Write([]byte{b})
			time.Sleep(50 * time.Microsecond)
		}
	}()

	_ = p2.SetDeadline(time.Now().Add(500 * time.Millisecond))
	res, err := rt.readRTUFrame()
	if err != nil {
		t.Fatalf("readRTUFrame (diagnostics): %v", err)
	}
	if res.UnitID != 0x01 || res.FunctionCode != byte(protocol.FCDiagnostics) {
		t.Errorf("got unit=%02x fc=%02x", res.UnitID, res.FunctionCode)
	}
	if !bytes.Equal(res.Payload, payload) {
		t.Errorf("payload mismatch: got % X, want % X", res.Payload, payload)
	}

	_ = p1.Close()
	_ = p2.Close()
}

// --- RTU readVariableLengthResponse (encapsulated path) ---

func TestRTUReadVariableLengthEncapsulated(t *testing.T) {
	p1, p2 := net.Pipe()
	rt := NewRTU(p2, "", 9600, 50*time.Millisecond, logging.NopLogger())

	// Build a valid FC2B (encapsulated interface) response frame:
	// unit=0x01, fc=0x2B, MEI type 0x0E, rest of payload
	payload := []byte{0x0E, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x03, 0x41, 0x42, 0x43}
	frame := adu.AssembleRTUFrame(0x01, byte(protocol.FCEncapsulatedInterface), payload)

	go func() {
		for _, b := range frame {
			_, _ = p1.Write([]byte{b})
			time.Sleep(50 * time.Microsecond)
		}
	}()

	_ = p2.SetDeadline(time.Now().Add(500 * time.Millisecond))
	res, err := rt.readRTUFrame()
	if err != nil {
		t.Fatalf("readRTUFrame (encapsulated): %v", err)
	}
	if res.UnitID != 0x01 || res.FunctionCode != byte(protocol.FCEncapsulatedInterface) {
		t.Errorf("got unit=%02x fc=%02x", res.UnitID, res.FunctionCode)
	}
	if !bytes.Equal(res.Payload, payload) {
		t.Errorf("payload mismatch: got % X, want % X", res.Payload, payload)
	}

	_ = p1.Close()
	_ = p2.Close()
}

func TestRTUReadVariableLengthDiagnostics_ShortFrame(t *testing.T) {
	p1, p2 := net.Pipe()
	rt := NewRTU(p2, "", 9600, 50*time.Millisecond, logging.NopLogger())

	// Send just the 3-byte header for FC08, then close to trigger short frame
	go func() {
		// unitID=0x01, fc=0x08, one byte of sub-function
		_, _ = p1.Write([]byte{0x01, byte(protocol.FCDiagnostics), 0x00})
		time.Sleep(10 * time.Millisecond)
		_ = p1.Close()
	}()

	_ = p2.SetDeadline(time.Now().Add(500 * time.Millisecond))
	_, err := rt.readRTUFrame()
	if err == nil {
		t.Error("expected error for short diagnostics frame")
	}

	_ = p2.Close()
}

func TestRTUReadVariableLengthEncapsulated_BadCRC(t *testing.T) {
	p1, p2 := net.Pipe()
	rt := NewRTU(p2, "", 9600, 50*time.Millisecond, logging.NopLogger())

	// Build a frame and corrupt the CRC
	payload := []byte{0x0E, 0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x03, 0x41, 0x42, 0x43}
	frame := adu.AssembleRTUFrame(0x01, byte(protocol.FCEncapsulatedInterface), payload)
	frame[len(frame)-1] ^= 0xFF // corrupt CRC

	go func() {
		for _, b := range frame {
			_, _ = p1.Write([]byte{b})
			time.Sleep(50 * time.Microsecond)
		}
	}()

	_ = p2.SetDeadline(time.Now().Add(500 * time.Millisecond))
	_, err := rt.readRTUFrame()
	if !errors.Is(err, protocol.ErrBadCRC) {
		t.Errorf("expected ErrBadCRC, got %v", err)
	}

	_ = p1.Close()
	_ = p2.Close()
}

// --- RTU readRTUFrame FIFO path ---

func TestRTUReadRTUFrame_FIFO(t *testing.T) {
	p1, p2 := net.Pipe()
	txchan := make(chan []byte, 4)
	go feedPipe(t, txchan, p1)

	rt := NewRTU(p2, "", 9600, 50*time.Millisecond, logging.NopLogger())
	_ = p2.SetDeadline(time.Now().Add(500 * time.Millisecond))

	// FC18 (ReadFIFOQueue) response:
	// The third byte is the first byte of the 2-byte "byte count" field.
	// The transport reads byte 4, then calculates bytesNeeded from bytes 3+4.
	// Response: unit=0x01, fc=0x18, fifo_byte_count_hi=0x00, fifo_byte_count_lo=0x06,
	//           fifo_count_hi=0x00, fifo_count_lo=0x02, reg1_hi=0x11, reg1_lo=0x22, reg2_hi=0x33, reg2_lo=0x44
	payload := []byte{0x00, 0x06, 0x00, 0x02, 0x11, 0x22, 0x33, 0x44}
	frame := adu.AssembleRTUFrame(0x01, byte(protocol.FCReadFIFOQueue), payload)
	txchan <- frame

	res, err := rt.readRTUFrame()
	if err != nil {
		t.Fatalf("readRTUFrame (FIFO): %v", err)
	}
	if res.UnitID != 0x01 || res.FunctionCode != byte(protocol.FCReadFIFOQueue) {
		t.Errorf("got unit=%02x fc=%02x", res.UnitID, res.FunctionCode)
	}
	if !bytes.Equal(res.Payload, payload) {
		t.Errorf("payload mismatch: got % X, want % X", res.Payload, payload)
	}

	close(txchan)
	_ = p1.Close()
	_ = p2.Close()
}

func TestRTUReadRTUFrame_FIFO_BadCRC(t *testing.T) {
	p1, p2 := net.Pipe()
	txchan := make(chan []byte, 4)
	go feedPipe(t, txchan, p1)

	rt := NewRTU(p2, "", 9600, 50*time.Millisecond, logging.NopLogger())
	_ = p2.SetDeadline(time.Now().Add(500 * time.Millisecond))

	payload := []byte{0x00, 0x06, 0x00, 0x02, 0x11, 0x22, 0x33, 0x44}
	frame := adu.AssembleRTUFrame(0x01, byte(protocol.FCReadFIFOQueue), payload)
	frame[len(frame)-1] ^= 0xFF // corrupt CRC
	txchan <- frame

	_, err := rt.readRTUFrame()
	if !errors.Is(err, protocol.ErrBadCRC) {
		t.Errorf("expected ErrBadCRC, got %v", err)
	}

	close(txchan)
	_ = p1.Close()
	_ = p2.Close()
}

// --- RTU ExecuteRequest error paths ---

func TestRTUExecuteRequest_BadCRC_DiscardsLink(t *testing.T) {
	p1, p2 := net.Pipe()
	rt := NewRTU(p2, "", 9600, 100*time.Millisecond, logging.NopLogger())

	go func() {
		// Read the outgoing request
		buf := make([]byte, 256)
		_, _ = p1.Read(buf)

		// Send back a response with a bad CRC
		resp := adu.AssembleRTUFrame(0x31, byte(protocol.FCWriteSingleRegister), []byte{0x12, 0x34, 0x56, 0x78})
		resp[len(resp)-1] ^= 0xFF // corrupt CRC
		_, _ = p1.Write(resp)
	}()

	req := &adu.Request{UnitID: 0x31, FunctionCode: byte(protocol.FCWriteSingleRegister), Payload: []byte{0x12, 0x34, 0x56, 0x78}}
	_, err := rt.ExecuteRequest(context.Background(), req)
	if !errors.Is(err, protocol.ErrBadCRC) {
		t.Errorf("expected ErrBadCRC, got %v", err)
	}

	_ = p1.Close()
	_ = p2.Close()
}

func TestRTUExecuteRequest_ContextDeadline(t *testing.T) {
	p1, p2 := net.Pipe()
	rt := NewRTU(p2, "", 9600, 5*time.Second, logging.NopLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	go func() {
		buf := make([]byte, 256)
		_, _ = p1.Read(buf)
		// Never respond
	}()

	req := &adu.Request{UnitID: 0x31, FunctionCode: byte(protocol.FCWriteSingleRegister), Payload: []byte{0x12, 0x34}}
	_, err := rt.ExecuteRequest(ctx, req)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}

	_ = p1.Close()
	_ = p2.Close()
}

// --- RTU NewRTU with low baud rate ---

func TestNewRTU_LowBaudRate(t *testing.T) {
	p1, p2 := net.Pipe()
	rt := NewRTU(p2, "", 4800, 50*time.Millisecond, logging.NopLogger())
	if rt.t35 <= 1750*time.Microsecond {
		t.Errorf("low baud rate t35 should be > 1750us, got %v", rt.t35)
	}
	_ = p1.Close()
	_ = p2.Close()
}

// --- TCP ExecuteRequest MBAP length overflow ---

func TestTCPExecuteRequest_MBAPLengthOverflow(t *testing.T) {
	p1, p2 := net.Pipe()
	tt := NewTCP(p2, 10*time.Millisecond, logging.NopLogger())

	bigPayload := make([]byte, adu.MBAPLengthMax)
	req := &adu.Request{UnitID: 0x31, FunctionCode: 0x10, Payload: bigPayload}
	_, err := tt.ExecuteRequest(context.Background(), req)
	if !errors.Is(err, protocol.ErrInvalidMBAPLength) {
		t.Errorf("expected ErrInvalidMBAPLength, got %v", err)
	}

	_ = p1.Close()
	_ = p2.Close()
}

func TestTCPExecuteRequest_ContextDeadline(t *testing.T) {
	p1, p2 := net.Pipe()
	tt := NewTCP(p2, 5*time.Second, logging.NopLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	go func() {
		buf := make([]byte, 256)
		_, _ = p1.Read(buf)
		// Never respond
	}()

	req := &adu.Request{UnitID: 0x31, FunctionCode: 0x06, Payload: []byte{0x12, 0x34}}
	_, err := tt.ExecuteRequest(ctx, req)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	_ = p1.Close()
	_ = p2.Close()
}

// --- TCP readResponse with ErrUnknownProtocolID loop ---

func TestTCPReadResponse_UnknownProtocolIDLoop(t *testing.T) {
	p1, p2 := net.Pipe()
	txchan := make(chan []byte, 15)
	go feedPipe(t, txchan, p1)

	tt := NewTCP(p2, 100*time.Millisecond, logging.NopLogger())
	tt.lastTxnID = 0x0001

	// Send frames with wrong protocol ID (0x0001 instead of 0x0000).
	// Body must have valid length so discardBytes works correctly.
	for i := 0; i < maxAnomalies; i++ {
		// MBAP header: txnID=0x0001, protocolID=0x0001 (wrong), length=0x0004
		frame := []byte{0x00, 0x01, 0x00, 0x01, 0x00, 0x04, 0x31, 0x06, 0x12, 0x34}
		txchan <- frame
	}

	_, err := tt.readResponse()
	if !errors.Is(err, protocol.ErrUnknownProtocolID) {
		t.Errorf("expected ErrUnknownProtocolID, got %v", err)
	}

	close(txchan)
	_ = p1.Close()
	_ = p2.Close()
}

// --- RTU readRTUFrame short frame on normal path ---

func TestRTUReadRTUFrame_ShortPayload(t *testing.T) {
	p1, p2 := net.Pipe()
	rt := NewRTU(p2, "", 9600, 50*time.Millisecond, logging.NopLogger())
	_ = p2.SetDeadline(time.Now().Add(500 * time.Millisecond))

	go func() {
		// Send 3-byte header for FC03 with byteCount=4, but only 2 payload bytes + CRC
		_, _ = p1.Write([]byte{0x01, byte(protocol.FCReadHoldingRegisters), 0x04})
		// Need 4 + 2 = 6 more bytes, but send only 4
		_, _ = p1.Write([]byte{0x11, 0x22, 0x33, 0x44})
		_ = p1.Close()
	}()

	_, err := rt.readRTUFrame()
	if err == nil {
		t.Error("expected error for short payload")
	}

	_ = p2.Close()
}
