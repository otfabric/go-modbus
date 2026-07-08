// SPDX-License-Identifier: MIT

package transport

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/logging"
	"github.com/otfabric/go-modbus/internal/protocol"
)

func TestSleepCtxCompletes(t *testing.T) {
	err := sleepCtx(context.Background(), 2*time.Millisecond)
	if err != nil {
		t.Errorf("sleepCtx: want nil, got %v", err)
	}
}

func TestSleepCtxCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	err := sleepCtx(ctx, 10*time.Second)
	if err != context.Canceled {
		t.Errorf("sleepCtx: want context.Canceled, got %v", err)
	}
}

func TestSleepCtxCancelledDuringSleep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	err := sleepCtx(ctx, 10*time.Second)
	if err != context.Canceled {
		t.Errorf("sleepCtx: want context.Canceled, got %v", err)
	}
}

func TestSleepCtxZeroDuration(t *testing.T) {
	err := sleepCtx(context.Background(), 0)
	if err != nil {
		t.Errorf("sleepCtx(0): want nil, got %v", err)
	}
	err = sleepCtx(context.Background(), -1)
	if err != nil {
		t.Errorf("sleepCtx(-1): want nil, got %v", err)
	}
}

func TestRTUClose(t *testing.T) {
	p1, p2 := net.Pipe()
	rt := NewRTU(p2, "", 9600, 10*time.Millisecond, logging.NopLogger())
	if err := rt.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	_ = p1.Close()
}

func TestRTUExecuteRequest(t *testing.T) {
	p1, p2 := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 256)
		n, err := p1.Read(buf)
		if err != nil || n < 6 {
			return
		}
		// Echo back a valid FC06 response (unit 0x31, addr 0x1234, value 0x5678)
		resp := adu.AssembleRTUFrame(0x31, byte(protocol.FCWriteSingleRegister), []byte{0x12, 0x34, 0x56, 0x78})
		_, _ = p1.Write(resp)
	}()

	rt := NewRTU(p2, "", 9600, 50*time.Millisecond, logging.NopLogger())
	req := &adu.Request{UnitID: 0x31, FunctionCode: byte(protocol.FCWriteSingleRegister), Payload: []byte{0x12, 0x34, 0x56, 0x78}}
	res, err := rt.ExecuteRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("ExecuteRequest: %v", err)
	}
	if res.UnitID != 0x31 || res.FunctionCode != byte(protocol.FCWriteSingleRegister) {
		t.Errorf("got unit=%02x fc=%02x", res.UnitID, res.FunctionCode)
	}
	want := []byte{0x12, 0x34, 0x56, 0x78}
	if len(res.Payload) != len(want) {
		t.Fatalf("payload len %d want %d", len(res.Payload), len(want))
	}
	for i := range want {
		if res.Payload[i] != want[i] {
			t.Errorf("payload[%d]=%02x want %02x", i, res.Payload[i], want[i])
		}
	}
	<-done
	_ = p1.Close()
	_ = p2.Close()
}

func TestRTUExecuteRequestTimeout(t *testing.T) {
	p1, p2 := net.Pipe()
	// Server never writes a response
	rt := NewRTU(p2, "", 9600, 5*time.Millisecond, logging.NopLogger())
	req := &adu.Request{UnitID: 0x31, FunctionCode: byte(protocol.FCWriteSingleRegister), Payload: []byte{0x12, 0x34}}
	_, err := rt.ExecuteRequest(context.Background(), req)
	if err == nil {
		t.Fatal("ExecuteRequest: want timeout error, got nil")
	}
	if !os.IsTimeout(err) {
		t.Errorf("ExecuteRequest: want timeout error, got %v", err)
	}
	_ = p1.Close()
	_ = p2.Close()
}

func TestRTUT35GapEnforcedBetweenRequests(t *testing.T) {
	p1, p2 := net.Pipe()
	rt := NewRTU(p2, "", 9600, 100*time.Millisecond, logging.NopLogger())

	respondFC06 := func() {
		buf := make([]byte, 256)
		n, err := p1.Read(buf)
		if err != nil || n < 6 {
			return
		}
		resp := adu.AssembleRTUFrame(0x01, byte(protocol.FCWriteSingleRegister), []byte{0x00, 0x00, 0x00, 0x01})
		_, _ = p1.Write(resp)
	}

	req := &adu.Request{UnitID: 0x01, FunctionCode: byte(protocol.FCWriteSingleRegister), Payload: []byte{0x00, 0x00, 0x00, 0x01}}

	go respondFC06()
	_, err := rt.ExecuteRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	if rt.lastActivity.IsZero() {
		t.Fatal("lastActivity should be set after successful request")
	}
	firstActivity := rt.lastActivity

	go respondFC06()
	start := time.Now()
	_, err = rt.ExecuteRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	elapsed := time.Since(start)

	gap := rt.lastActivity.Sub(firstActivity)
	if gap < rt.t35 {
		t.Errorf("inter-request gap %v < t3.5 %v", gap, rt.t35)
	}
	_ = elapsed

	_ = p1.Close()
	_ = p2.Close()
}

func TestRTULastActivityAfterTimeout(t *testing.T) {
	p1, p2 := net.Pipe()
	rt := NewRTU(p2, "", 9600, 5*time.Millisecond, logging.NopLogger())

	req := &adu.Request{UnitID: 0x01, FunctionCode: byte(protocol.FCWriteSingleRegister), Payload: []byte{0x00, 0x00}}
	_, err := rt.ExecuteRequest(context.Background(), req)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !os.IsTimeout(err) {
		t.Fatalf("expected timeout, got %v", err)
	}
	if !rt.lastActivity.IsZero() {
		t.Errorf("lastActivity should remain zero after timeout (no valid RX), got %v", rt.lastActivity)
	}
	_ = p1.Close()
	_ = p2.Close()
}

func TestRTULastActivityUpdatedAfterBadCRC(t *testing.T) {
	p1, p2 := net.Pipe()
	go func() {
		buf := make([]byte, 256)
		n, _ := p1.Read(buf)
		if n < 6 {
			return
		}
		bad := []byte{0x01, byte(protocol.FCWriteSingleRegister), 0x00, 0x00, 0x00, 0x01, 0xFF, 0xFF}
		_, _ = p1.Write(bad)
	}()

	rt := NewRTU(p2, "", 9600, 100*time.Millisecond, logging.NopLogger())
	req := &adu.Request{UnitID: 0x01, FunctionCode: byte(protocol.FCWriteSingleRegister), Payload: []byte{0x00, 0x00, 0x00, 0x01}}
	_, err := rt.ExecuteRequest(context.Background(), req)
	if err != protocol.ErrBadCRC {
		t.Fatalf("expected ErrBadCRC, got %v", err)
	}
	if rt.lastActivity.IsZero() {
		t.Error("lastActivity should be updated after non-timeout error (bad CRC)")
	}
	_ = p1.Close()
	_ = p2.Close()
}

func TestSerialCharTime(t *testing.T) {
	got9600 := SerialCharTime(9600)
	want9600 := 11 * time.Second / 9600
	if got9600 != want9600 {
		t.Errorf("SerialCharTime(9600) = %v, want %v", got9600, want9600)
	}
	got19200 := SerialCharTime(19200)
	if got19200 >= got9600 {
		t.Errorf("SerialCharTime(19200) = %v should be < %v", got19200, got9600)
	}
}

func TestRTUReadFrame(t *testing.T) {
	p1, p2 := net.Pipe()
	txchan := make(chan []byte, 4)
	go feedPipe(t, txchan, p1)

	txchan <- []byte{0xfa, 0x8d, 0xcc, 0x1b, 0xf9}
	rt := NewRTU(p2, "", 9600, 10*time.Millisecond, logging.NopLogger())
	_ = p2.SetDeadline(time.Now().Add(100 * time.Millisecond))

	txchan <- []byte{0x31, 0x82, 0x02, 0xc1, 0x6e}
	res, err := rt.readRTUFrame()
	if err != nil {
		t.Fatalf("readRTUFrame: %v", err)
	}
	if res.UnitID != 0x31 || res.FunctionCode != 0x82 {
		t.Errorf("got unit=%02x fc=%02x", res.UnitID, res.FunctionCode)
	}
	if len(res.Payload) != 1 || res.Payload[0] != 0x02 {
		t.Errorf("got payload %v", res.Payload)
	}

	txchan <- []byte{0x30, 0x82, 0x12, 0xc0, 0xa2}
	_, err = rt.readRTUFrame()
	if err != protocol.ErrBadCRC {
		t.Errorf("want ErrBadCRC, got %v", err)
	}

	txchan <- []byte{0x31, 0x03, 0x04, 0x11, 0x22, 0x33, 0x44, 0x7b, 0xc5}
	res, err = rt.readRTUFrame()
	if err != nil {
		t.Fatalf("readRTUFrame: %v", err)
	}
	if res.UnitID != 0x31 || res.FunctionCode != byte(protocol.FCReadHoldingRegisters) {
		t.Errorf("got unit=%02x fc=%02x", res.UnitID, res.FunctionCode)
	}
	want := []byte{0x04, 0x11, 0x22, 0x33, 0x44}
	if len(res.Payload) != len(want) {
		t.Fatalf("payload len %d want %d", len(res.Payload), len(want))
	}
	for i := range want {
		if res.Payload[i] != want[i] {
			t.Errorf("payload[%d]=%02x want %02x", i, res.Payload[i], want[i])
		}
	}

	close(txchan)
	_ = p1.Close()
	_ = p2.Close()
}
