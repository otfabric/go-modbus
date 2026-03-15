package modbus

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestReadWithRuntimeCodec_ZeroCount_ReturnsCodecError(t *testing.T) {
	client, err := NewClient(&ClientConfiguration{URL: "tcp://127.0.0.1:1", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	rd := AsRuntimeDecoder(zeroCountDecoder{}, CodecValueString)
	_, err = ReadWithRuntimeCodec(client, context.Background(), 1, 0, HoldingRegister, rd)
	if err == nil {
		t.Fatal("expected error for zero register count")
	}
	var e *CodecRegisterCountError
	if !errors.As(err, &e) {
		t.Errorf("expected CodecRegisterCountError, got %T", err)
	}
}

func TestDecodeWithDescriptor_RoundTrip(t *testing.T) {
	desc, ok := CodecDescriptorByID("uint32/layout:4321")
	if !ok {
		t.Fatal("descriptor not found")
	}
	regs := []uint16{0x1234, 0x5678}
	got, err := DecodeWithDescriptor(regs, desc)
	if err != nil {
		t.Fatalf("DecodeWithDescriptor: %v", err)
	}
	if v, ok := got.(uint32); !ok || v != 0x12345678 {
		t.Errorf("DecodeWithDescriptor = %v (%T), want uint32(0x12345678)", got, got)
	}
}

func TestDecodeWithDescriptor_NoLayouts_ReturnsError(t *testing.T) {
	desc := CodecDescriptor{ID: "uint32/layout:4321", ValueKind: CodecValueUint32, Layouts: nil}
	_, err := DecodeWithDescriptor([]uint16{0x1234, 0x5678}, desc)
	if err == nil {
		t.Fatal("expected error when descriptor has no layouts")
	}
	if !errors.Is(err, ErrCodecLayout) {
		t.Errorf("want ErrCodecLayout, got %v", err)
	}
}

func TestEncodeWithDescriptor_NoLayouts_ReturnsError(t *testing.T) {
	desc := CodecDescriptor{ID: "uint32/layout:4321", ValueKind: CodecValueUint32, Layouts: nil}
	_, err := EncodeWithDescriptor(uint32(1), desc)
	if err == nil {
		t.Fatal("expected error when descriptor has no layouts")
	}
}

func TestEncodeWithDescriptor_TypeMismatch_ReturnsError(t *testing.T) {
	desc, ok := CodecDescriptorByID("uint32/layout:4321")
	if !ok {
		t.Fatal("descriptor not found")
	}
	_, err := EncodeWithDescriptor("wrong type", desc)
	if err == nil {
		t.Fatal("expected error for type mismatch")
	}
	var ce *CodecValueError
	if !errors.As(err, &ce) {
		t.Errorf("expected *CodecValueError, got %T", err)
	}
}

func TestDecodeRegistersAny_CountMismatch_ReturnsError(t *testing.T) {
	rc := MustRuntimeCodecByID("uint32/layout:4321")
	_, err := DecodeRegistersAny([]uint16{0x1234}, rc) // only 1 register, need 2
	if err == nil {
		t.Fatal("expected error for count mismatch")
	}
	var e *CodecRegisterCountError
	if !errors.As(err, &e) {
		t.Errorf("expected CodecRegisterCountError, got %T", err)
	}
}

func TestReadWithRuntimeCodec_WriteWithRuntimeCodec_Integration(t *testing.T) {
	addr, cleanup := writeMockServer(t, false, true)
	defer cleanup()

	client, err := NewClient(&ClientConfiguration{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	rc := MustRuntimeCodecByID("uint32/layout:4321")
	err = WriteWithRuntimeCodec(client, context.Background(), 1, 0, uint32(0x12345678), rc)
	if err != nil {
		t.Fatalf("WriteWithRuntimeCodec: %v", err)
	}
}

func TestReadWithRuntimeCodec_Integration(t *testing.T) {
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
			txid, unitID, fc := frame[0:2], frame[6], frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			qty := int(frame[10])<<8 | int(frame[11])
			if qty == 2 {
				payload := []byte{0x04, 0x12, 0x34, 0x56, 0x78}
				_ = writeMBAPRegs(sock, txid, unitID, fc, payload)
			} else {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := NewClient(&ClientConfiguration{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	rc := MustRuntimeCodecByID("uint32/layout:4321")
	got, err := ReadWithRuntimeCodec(client, context.Background(), 1, 0, HoldingRegister, rc)
	if err != nil {
		t.Fatalf("ReadWithRuntimeCodec: %v", err)
	}
	if v, ok := got.(uint32); !ok || v != 0x12345678 {
		t.Errorf("ReadWithRuntimeCodec = %v (%T), want uint32(0x12345678)", got, got)
	}
}
