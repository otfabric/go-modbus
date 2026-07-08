// SPDX-License-Identifier: MIT

package transport

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/logging"
	"github.com/otfabric/go-modbus/internal/protocol"
)

func feedPipe(t *testing.T, in chan []byte, out interface{ Write([]byte) (int, error) }) {
	for buf := range in {
		if _, err := out.Write(buf); err != nil {
			t.Errorf("feedPipe write: %v", err)
			return
		}
	}
}

func TestTCPClose(t *testing.T) {
	p1, p2 := net.Pipe()
	tt := NewTCP(p2, 10*time.Millisecond, logging.NopLogger())
	if err := tt.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	_ = p1.Close()
}

func TestTCPTransactionIDMismatchExhausted(t *testing.T) {
	p1, p2 := net.Pipe()
	txchan := make(chan []byte, 15)
	go feedPipe(t, txchan, p1)

	tt := NewTCP(p2, 10*time.Millisecond, logging.NopLogger())
	tt.lastTxnID = 0x0001

	// Send 10 responses with wrong transaction IDs (0x0002 through 0x000B)
	// Each must be a valid MBAP frame so we don't hit ErrUnknownProtocolID path
	for i := 0; i < 10; i++ {
		wrongTxn := uint16(0x0002 + i)
		frame := adu.AssembleMBAP(wrongTxn, 0x31, 0x06, []byte{0x12, 0x34})
		txchan <- frame
	}

	_, err := tt.readResponse()
	if !errors.Is(err, protocol.ErrBadTransactionID) {
		t.Errorf("readResponse: want ErrBadTransactionID, got %v", err)
	}

	close(txchan)
	_ = p1.Close()
	_ = p2.Close()
}

func TestTCPReadResponse(t *testing.T) {
	p1, p2 := net.Pipe()
	txchan := make(chan []byte, 4)
	go feedPipe(t, txchan, p1)

	tt := NewTCP(p2, 10*time.Millisecond, logging.NopLogger())
	tt.lastTxnID = 0x9218

	txchan <- []byte{
		0x92, 0x18, 0x00, 0x00, 0x00, 0x04,
		0x31, 0x06, 0x12, 0x34,
	}
	res, err := tt.readResponse()
	if err != nil {
		t.Fatalf("readResponse: %v", err)
	}
	if res.UnitID != 0x31 || res.FunctionCode != 0x06 || res.TransactionID != 0x9218 {
		t.Errorf("got unit=%02x fc=%02x txn=%04x", res.UnitID, res.FunctionCode, res.TransactionID)
	}
	if len(res.Payload) != 2 || res.Payload[0] != 0x12 || res.Payload[1] != 0x34 {
		t.Errorf("got payload %v", res.Payload)
	}

	// Skip wrong txn, then match
	txchan <- []byte{0x92, 0x19, 0x00, 0x00, 0x00, 0x04, 0x31, 0x06, 0x12, 0x34}
	txchan <- []byte{0x92, 0x18, 0x00, 0x00, 0x00, 0x04, 0x39, 0x02, 0x10, 0x01}
	res, err = tt.readResponse()
	if err != nil {
		t.Fatalf("readResponse: %v", err)
	}
	if res.UnitID != 0x39 || res.FunctionCode != 0x02 {
		t.Errorf("got unit=%02x fc=%02x", res.UnitID, res.FunctionCode)
	}
	if len(res.Payload) != 2 || res.Payload[0] != 0x10 || res.Payload[1] != 0x01 {
		t.Errorf("got payload %v", res.Payload)
	}

	// Invalid protocol then invalid length
	txchan <- []byte{0x92, 0x18, 0x00, 0x01, 0x00, 0x04, 0x31, 0x06, 0x12, 0x34}
	txchan <- []byte{0x92, 0x18, 0x00, 0x00, 0x00, 0x01, 0x31}
	_, err = tt.readResponse()
	if !errors.Is(err, protocol.ErrInvalidMBAPLength) {
		t.Errorf("want ErrInvalidMBAPLength, got %v", err)
	}

	close(txchan)
	_ = p1.Close()
	_ = p2.Close()
}

func TestTCPReadRequest(t *testing.T) {
	p1, p2 := net.Pipe()
	txchan := make(chan []byte, 4)
	go feedPipe(t, txchan, p1)

	tt := NewTCP(p2, 10*time.Millisecond, logging.NopLogger())
	tt.lastTxnID = 0x0a00

	txchan <- []byte{0x0a, 0x00, 0x00, 0x01, 0x00, 0x04, 0x31, 0x06, 0x12, 0x34}
	_, _, err := tt.ReadRequest()
	if !errors.Is(err, protocol.ErrUnknownProtocolID) {
		t.Errorf("want ErrUnknownProtocolID, got %v", err)
	}

	txchan <- []byte{0x0a, 0x00, 0x00, 0x00, 0x00, 0x01, 0x31}
	_, _, err = tt.ReadRequest()
	if !errors.Is(err, protocol.ErrInvalidMBAPLength) {
		t.Errorf("want ErrInvalidMBAPLength, got %v", err)
	}

	txchan <- []byte{0x92, 0x18, 0x00, 0x00, 0x00, 0x0a, 0xfa, 0x04, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb}
	req, txnID, err := tt.ReadRequest()
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	if req.UnitID != 0xfa || req.FunctionCode != 0x04 || txnID != 0x9218 {
		t.Errorf("got unit=%02x fc=%02x txn=%04x", req.UnitID, req.FunctionCode, txnID)
	}
	want := []byte{0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb}
	if len(req.Payload) != len(want) {
		t.Fatalf("payload len %d want %d", len(req.Payload), len(want))
	}
	for i := range want {
		if req.Payload[i] != want[i] {
			t.Errorf("payload[%d]=%02x want %02x", i, req.Payload[i], want[i])
		}
	}

	close(txchan)
	_ = p1.Close()
	_ = p2.Close()
}

func TestTCPExecuteRequest(t *testing.T) {
	p1, p2 := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 20)
		n, _ := p1.Read(buf)
		if n < 7 {
			return
		}
		// Echo back a simple response (txn from request is in buf[0:2])
		txn1, txn2 := buf[0], buf[1]
		resp := []byte{txn1, txn2, 0x00, 0x00, 0x00, 0x04, 0x31, 0x06, 0x12, 0x34}
		_, _ = p1.Write(resp)
	}()

	tt := NewTCP(p2, 10*time.Millisecond, logging.NopLogger())
	req := &adu.Request{UnitID: 0x31, FunctionCode: 0x06, Payload: []byte{0x12, 0x34}}
	res, err := tt.ExecuteRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("ExecuteRequest: %v", err)
	}
	if res.UnitID != 0x31 || res.FunctionCode != 0x06 {
		t.Errorf("got unit=%02x fc=%02x", res.UnitID, res.FunctionCode)
	}
	if len(res.Payload) != 2 || res.Payload[0] != 0x12 || res.Payload[1] != 0x34 {
		t.Errorf("got payload %v", res.Payload)
	}
	<-done
	_ = p1.Close()
	_ = p2.Close()
}

func TestTCPWriteResponse(t *testing.T) {
	p1, p2 := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Transport writes to p1, so we read from p2 (other end of pipe)
		expected := []byte{
			0xc0, 0x1f, 0x00, 0x00, 0x00, 0x0b,
			0x17, 0x06, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xf4,
		}
		rxbuf := make([]byte, len(expected))
		n, err := p2.Read(rxbuf)
		if err != nil || n != len(expected) {
			t.Errorf("read: n=%d err=%v", n, err)
			return
		}
		for i, b := range expected {
			if rxbuf[i] != b {
				t.Errorf("at %d: got %02x want %02x", i, rxbuf[i], b)
			}
		}
	}()

	tt := NewTCP(p1, 10*time.Millisecond, logging.NopLogger())
	tt.lastTxnID = 0xc01f
	err := tt.WriteResponse(&adu.Response{
		UnitID: 0x17, FunctionCode: 0x06,
		Payload: []byte{0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xf4},
	})
	if err != nil {
		t.Fatalf("WriteResponse: %v", err)
	}
	<-done
	_ = p1.Close()
	_ = p2.Close()
}
