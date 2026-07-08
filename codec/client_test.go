// SPDX-License-Identifier: MIT

package codec

import (
	"context"
	"errors"
	"testing"
)

// mockRW implements both RegisterReader and RegisterWriter for testing.
type mockRW struct {
	regs    []uint16
	err     error
	written []uint16
}

func (m *mockRW) ReadRegisters(_ context.Context, _ uint8, _ uint16, qty uint16, _ RegType) ([]uint16, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.regs[:qty], nil
}

func (m *mockRW) WriteRegisters(_ context.Context, _ uint8, _ uint16, values []uint16) error {
	if m.err != nil {
		return m.err
	}
	m.written = values
	return nil
}

func TestReadFromClient(t *testing.T) {
	mock := &mockRW{regs: []uint16{0x0000, 0x002A}}
	c, err := NewUint32Codec(Layout32_4321)
	if err != nil {
		t.Fatal(err)
	}
	v, err := ReadFromClient(mock, context.Background(), 1, 100, HoldingRegister, c)
	if err != nil {
		t.Fatal(err)
	}
	if v != 42 {
		t.Errorf("got %d, want 42", v)
	}
}

func TestReadFromClient_NilCodec(t *testing.T) {
	mock := &mockRW{regs: []uint16{0, 0}}
	_, err := ReadFromClient[uint32](mock, context.Background(), 1, 0, HoldingRegister, nil)
	if err == nil {
		t.Fatal("expected error for nil codec")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Errorf("want ErrCodecValue, got %v", err)
	}
}

func TestReadFromClient_ReadError(t *testing.T) {
	readErr := errors.New("read failed")
	mock := &mockRW{regs: []uint16{0, 0}, err: readErr}
	c, err := NewUint32Codec(Layout32_4321)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ReadFromClient(mock, context.Background(), 1, 0, HoldingRegister, c)
	if !errors.Is(err, readErr) {
		t.Errorf("want readErr, got %v", err)
	}
}

func TestWriteToClient(t *testing.T) {
	mock := &mockRW{}
	c, err := NewUint32Codec(Layout32_4321)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteToClient(mock, context.Background(), 1, 100, uint32(42), c); err != nil {
		t.Fatal(err)
	}
	if len(mock.written) != 2 {
		t.Fatalf("written len=%d, want 2", len(mock.written))
	}
	if mock.written[0] != 0x0000 || mock.written[1] != 0x002A {
		t.Errorf("written=%v, want [0x0000, 0x002A]", mock.written)
	}
}

func TestWriteToClient_NilCodec(t *testing.T) {
	mock := &mockRW{}
	err := WriteToClient[uint32](mock, context.Background(), 1, 0, 0, nil)
	if err == nil {
		t.Fatal("expected error for nil codec")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Errorf("want ErrCodecValue, got %v", err)
	}
}

func TestWriteToClient_WriteError(t *testing.T) {
	writeErr := errors.New("write failed")
	mock := &mockRW{err: writeErr}
	c, err := NewUint32Codec(Layout32_4321)
	if err != nil {
		t.Fatal(err)
	}
	err = WriteToClient(mock, context.Background(), 1, 0, uint32(42), c)
	if !errors.Is(err, writeErr) {
		t.Errorf("want writeErr, got %v", err)
	}
}

func TestReadRuntimeFromClient(t *testing.T) {
	mock := &mockRW{regs: []uint16{0x0000, 0x002A}}
	c, err := NewUint32Codec(Layout32_4321)
	if err != nil {
		t.Fatal(err)
	}
	rc := AsRuntimeCodec(c, CodecValueUint32)
	v, err := ReadRuntimeFromClient(mock, context.Background(), 1, 100, HoldingRegister, rc)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := v.(uint32)
	if !ok {
		t.Fatalf("expected uint32, got %T", v)
	}
	if got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}

func TestWriteRuntimeToClient(t *testing.T) {
	mock := &mockRW{}
	c, err := NewUint32Codec(Layout32_4321)
	if err != nil {
		t.Fatal(err)
	}
	rc := AsRuntimeCodec(c, CodecValueUint32)
	if err := WriteRuntimeToClient(mock, context.Background(), 1, 100, uint32(42), rc); err != nil {
		t.Fatal(err)
	}
	if len(mock.written) != 2 {
		t.Fatalf("written len=%d, want 2", len(mock.written))
	}
	if mock.written[0] != 0x0000 || mock.written[1] != 0x002A {
		t.Errorf("written=%v, want [0x0000, 0x002A]", mock.written)
	}
}

func TestReadRuntimeFromClient_NilCodec(t *testing.T) {
	mock := &mockRW{regs: []uint16{0, 0}}
	_, err := ReadRuntimeFromClient(mock, context.Background(), 1, 0, HoldingRegister, nil)
	if err == nil {
		t.Fatal("expected error for nil codec")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Errorf("want ErrCodecValue, got %v", err)
	}
}

func TestWriteRuntimeToClient_NilCodec(t *testing.T) {
	mock := &mockRW{}
	err := WriteRuntimeToClient(mock, context.Background(), 1, 0, uint32(0), nil)
	if err == nil {
		t.Fatal("expected error for nil codec")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Errorf("want ErrCodecValue, got %v", err)
	}
}

func TestDecodeWithDescriptor(t *testing.T) {
	desc, ok := CodecDescriptorByID("uint32/layout:4321")
	if !ok {
		t.Fatal("descriptor uint32/layout:4321 not found")
	}
	v, err := DecodeWithDescriptor([]uint16{0x0000, 0x002A}, desc)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := v.(uint32)
	if !ok {
		t.Fatalf("expected uint32, got %T", v)
	}
	if got != 42 {
		t.Errorf("got %d, want 42", got)
	}
}

func TestDecodeWithDescriptor_ZeroDescriptor(t *testing.T) {
	_, err := DecodeWithDescriptor([]uint16{0, 0}, CodecDescriptor{})
	if err == nil {
		t.Fatal("expected error for zero descriptor")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Errorf("want ErrCodecValue, got %v", err)
	}
}

func TestEncodeWithDescriptor(t *testing.T) {
	desc, ok := CodecDescriptorByID("uint32/layout:4321")
	if !ok {
		t.Fatal("descriptor uint32/layout:4321 not found")
	}
	regs, err := EncodeWithDescriptor(uint32(42), desc)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 2 {
		t.Fatalf("len(regs)=%d, want 2", len(regs))
	}
	if regs[0] != 0x0000 || regs[1] != 0x002A {
		t.Errorf("regs=%v, want [0x0000, 0x002A]", regs)
	}
}

func TestEncodeWithDescriptor_ZeroDescriptor(t *testing.T) {
	_, err := EncodeWithDescriptor(uint32(0), CodecDescriptor{})
	if err == nil {
		t.Fatal("expected error for zero descriptor")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Errorf("want ErrCodecValue, got %v", err)
	}
}

func TestReadUint32FromClient(t *testing.T) {
	mock := &mockRW{regs: []uint16{0x0000, 0x002A}}
	v, err := ReadUint32FromClient(mock, context.Background(), 1, 100, HoldingRegister, Layout32_4321)
	if err != nil {
		t.Fatal(err)
	}
	if v != 42 {
		t.Errorf("got %d, want 42", v)
	}
}

func TestWriteUint32ToClient(t *testing.T) {
	mock := &mockRW{}
	if err := WriteUint32ToClient(mock, context.Background(), 1, 100, 42, Layout32_4321); err != nil {
		t.Fatal(err)
	}
	if len(mock.written) != 2 {
		t.Fatalf("written len=%d, want 2", len(mock.written))
	}
	if mock.written[0] != 0x0000 || mock.written[1] != 0x002A {
		t.Errorf("written=%v, want [0x0000, 0x002A]", mock.written)
	}
}

func TestBytesCodecNames(t *testing.T) {
	tests := []struct {
		name string
		fn   func() string
	}{
		{"bytesCodec", func() string { c, _ := NewBytesCodec(4); return c.Name() }},
		{"uint8SliceCodec", func() string { c, _ := NewUint8SliceCodec(4); return c.Name() }},
		{"ipAddrCodec", func() string { return NewIPAddrCodec().Name() }},
		{"ipv6AddrCodec", func() string { return NewIPv6AddrCodec().Name() }},
		{"eui48Codec", func() string { return NewEUI48Codec().Name() }},
		{"eui64Codec", func() string { return NewEUI64Codec().Name() }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name := tt.fn()
			if name == "" {
				t.Error("Name() returned empty string")
			}
		})
	}
}
