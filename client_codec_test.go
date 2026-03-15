package modbus

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// zeroCountDecoder is a test Decoder with RegisterSpec.Count == 0.
type zeroCountDecoder struct{}

func (zeroCountDecoder) ID() string                               { return "test/zero" }
func (zeroCountDecoder) Name() string                             { return "zero" }
func (zeroCountDecoder) RegisterSpec() RegisterSpec               { return RegisterSpec{Count: 0} }
func (zeroCountDecoder) ByteSpec() ByteSpec                       { return ByteSpec{Count: 0} }
func (zeroCountDecoder) DecodeRegisters([]uint16) (string, error) { return "", nil }

func TestReadUint32WithLayout_InvalidLayout(t *testing.T) {
	client, _ := NewClient(&ClientConfiguration{URL: "tcp://127.0.0.1:1", Timeout: time.Second})
	_ = client.Open()
	defer func() { _ = client.Close() }()
	_, err := ReadUint32WithLayout(client, context.Background(), 1, 0, HoldingRegister, RegisterLayout{})
	if err == nil {
		t.Fatal("expected error for invalid layout")
	}
	if !errors.Is(err, ErrCodecLayout) {
		var le *CodecLayoutError
		if errors.As(err, &le) {
			t.Logf("got CodecLayoutError: %v", le)
		}
	}
}

func TestWriteUint32WithLayout_InvalidLayout(t *testing.T) {
	client, _ := NewClient(&ClientConfiguration{URL: "tcp://127.0.0.1:1", Timeout: time.Second})
	_ = client.Open()
	defer func() { _ = client.Close() }()
	err := WriteUint32WithLayout(client, context.Background(), 1, 0, 0, RegisterLayout{})
	if err == nil {
		t.Fatal("expected error for invalid layout")
	}
}

func TestReadWithCodec_ZeroCount_ReturnsCodecError(t *testing.T) {
	client, err := NewClient(&ClientConfiguration{URL: "tcp://127.0.0.1:1", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ReadWithCodec(client, context.Background(), 1, 0, HoldingRegister, zeroCountDecoder{})
	if err == nil {
		t.Fatal("expected error for zero register count")
	}
	var e *CodecRegisterCountError
	if !errors.As(err, &e) {
		t.Errorf("expected CodecRegisterCountError, got %T", err)
	}
	if e.Actual != 0 || e.Codec != "test/zero" {
		t.Errorf("Codec=%q Actual=%d", e.Codec, e.Actual)
	}
}

func TestReadWithCodec_Integration(t *testing.T) {
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
				// Two registers: 0x1234, 0x5678 -> uint32 0x12345678 with layout 4321
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

	codec := MustNewUint32Codec(Layout32_4321)
	got, err := ReadWithCodec(client, context.Background(), 1, 0, HoldingRegister, codec)
	if err != nil {
		t.Fatalf("ReadWithCodec: %v", err)
	}
	if got != 0x12345678 {
		t.Errorf("ReadWithCodec = 0x%x, want 0x12345678", got)
	}
}

func TestWriteWithCodec_Integration(t *testing.T) {
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

	codec := MustNewUint32Codec(Layout32_4321)
	err = WriteWithCodec(client, context.Background(), 1, 0, uint32(0x12345678), codec)
	if err != nil {
		t.Fatalf("WriteWithCodec: %v", err)
	}
}

func TestReadUint32WithLayout_Integration(t *testing.T) {
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

	got, err := ReadUint32WithLayout(client, context.Background(), 1, 0, HoldingRegister, Layout32_4321)
	if err != nil {
		t.Fatalf("ReadUint32WithLayout: %v", err)
	}
	if got != 0x12345678 {
		t.Errorf("ReadUint32WithLayout = 0x%x, want 0x12345678", got)
	}
}

func TestWriteUint32WithLayout_Integration(t *testing.T) {
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

	err = WriteUint32WithLayout(client, context.Background(), 1, 0, 0xDEADBEEF, Layout32_4321)
	if err != nil {
		t.Fatalf("WriteUint32WithLayout: %v", err)
	}
}
