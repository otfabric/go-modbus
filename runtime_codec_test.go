package modbus

import (
	"errors"
	"testing"
)

func TestRuntimeDecoderAdapter_Delegates(t *testing.T) {
	c := minimalUint16Codec{}
	rd := AsRuntimeDecoder(c, CodecValueUint16)
	if rd.ValueKind() != CodecValueUint16 {
		t.Errorf("ValueKind() = %v, want CodecValueUint16", rd.ValueKind())
	}
	if rd.ID() != c.ID() || rd.Name() != c.Name() {
		t.Errorf("ID/Name not delegated: got %q %q", rd.ID(), rd.Name())
	}
	got, err := rd.DecodeRegistersAny([]uint16{0x1234})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := got.(uint16)
	if !ok {
		t.Fatalf("DecodeRegistersAny returned %T, want uint16", got)
	}
	if v != 0x1234 {
		t.Errorf("got 0x%x, want 0x1234", v)
	}
}

func TestRuntimeEncoderAdapter_Delegates(t *testing.T) {
	c := minimalUint16Codec{}
	re := AsRuntimeEncoder(c, CodecValueUint16)
	regs, err := re.EncodeRegistersAny(uint16(0x5678))
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 || regs[0] != 0x5678 {
		t.Errorf("got %v, want [0x5678]", regs)
	}
}

func TestRuntimeEncoderAdapter_TypeMismatch_ReturnsError(t *testing.T) {
	c := minimalUint16Codec{}
	re := AsRuntimeEncoder(c, CodecValueUint16)
	_, err := re.EncodeRegistersAny("wrong type")
	if err == nil {
		t.Fatal("expected error for type mismatch")
	}
	var ce *CodecValueError
	if !errors.As(err, &ce) {
		t.Errorf("expected *CodecValueError, got %T: %v", err, err)
	}
	if ce.Codec != c.ID() {
		t.Errorf("Codec = %q, want %q", ce.Codec, c.ID())
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Errorf("expected unwrap ErrCodecValue, got %v", err)
	}
}

func TestRuntimeCodecAdapter_ValueKindPreserved(t *testing.T) {
	c := minimalUint16Codec{}
	rc := AsRuntimeCodec(c, CodecValueUint16)
	if rc.ValueKind() != CodecValueUint16 {
		t.Errorf("ValueKind() = %v, want CodecValueUint16", rc.ValueKind())
	}
	if rc.ID() != c.ID() {
		t.Errorf("ID() = %q, want %q", rc.ID(), c.ID())
	}
}

func TestRuntimeCodecAdapter_DecodeRegistersAny_ReturnsCorrectConcreteValue(t *testing.T) {
	c := minimalUint16Codec{}
	rc := AsRuntimeCodec(c, CodecValueUint16)
	got, err := rc.DecodeRegistersAny([]uint16{0xBEEF})
	if err != nil {
		t.Fatal(err)
	}
	v, ok := got.(uint16)
	if !ok {
		t.Fatalf("DecodeRegistersAny returned %T, want uint16", got)
	}
	if v != 0xBEEF {
		t.Errorf("got 0x%x, want 0xBEEF", v)
	}
}

func TestRuntimeCodecAdapter_EncodeRegistersAny_ValidatesDynamicType(t *testing.T) {
	c := minimalUint16Codec{}
	rc := AsRuntimeCodec(c, CodecValueUint16)
	_, err := rc.EncodeRegistersAny(int32(42))
	if err == nil {
		t.Fatal("expected error for wrong type (int32)")
	}
	var ce *CodecValueError
	if !errors.As(err, &ce) {
		t.Errorf("expected *CodecValueError, got %T", err)
	}
}

func TestRuntimeCodecAdapter_RoundTrip(t *testing.T) {
	c := minimalUint16Codec{}
	rc := AsRuntimeCodec(c, CodecValueUint16)
	regs := []uint16{0x1234}
	decoded, err := rc.DecodeRegistersAny(regs)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := rc.EncodeRegistersAny(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != 1 || encoded[0] != regs[0] {
		t.Errorf("round-trip got %v, want %v", encoded, regs)
	}
}
