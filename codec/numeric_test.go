// SPDX-License-Identifier: MIT

package codec

import (
	"errors"
	"math"
	"testing"
)

func TestNewUint32Codec_ValidLayout(t *testing.T) {
	c, err := NewUint32Codec(Layout32_4321)
	if err != nil {
		t.Fatal(err)
	}
	if c.RegisterSpec().Count != 2 {
		t.Errorf("RegisterSpec().Count = %d, want 2", c.RegisterSpec().Count)
	}
	v, err := DecodeRegisters([]uint16{0x1234, 0x5678}, c)
	if err != nil {
		t.Fatal(err)
	}
	if v != 0x12345678 {
		t.Errorf("DecodeRegisters = 0x%x, want 0x12345678", v)
	}
	regs, err := EncodeRegisters(uint32(0x12345678), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 2 || regs[0] != 0x1234 || regs[1] != 0x5678 {
		t.Errorf("EncodeRegisters = %v", regs)
	}
}

func TestNewUint32Codec_InvalidLayout(t *testing.T) {
	_, err := NewUint32Codec(Layout64_87654321)
	if err == nil {
		t.Fatal("expected error for wrong layout register count")
	}
	if !isErrCodecLayout(err) {
		t.Errorf("expected layout error, got %v", err)
	}
}

func isErrCodecLayout(err error) bool {
	for err != nil {
		if err == ErrCodecLayout {
			return true
		}
		type unwrap interface{ Unwrap() error }
		if u, ok := err.(unwrap); ok {
			err = u.Unwrap()
		} else {
			break
		}
	}
	return false
}

func TestUint32Codec_RoundTrip(t *testing.T) {
	c := MustNewUint32Codec(Layout32_2143)
	val := uint32(0xDEADBEEF)
	regs, err := EncodeRegisters(val, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != val {
		t.Errorf("round-trip: got 0x%x, want 0x%x", got, val)
	}
}

func TestFloat64Codec_RoundTrip(t *testing.T) {
	c := MustNewFloat64Codec(Layout64_87654321)
	val := 3.14159265358979
	regs, err := EncodeRegisters(val, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got-val) > 1e-10 {
		t.Errorf("round-trip: got %v, want %v", got, val)
	}
}

func TestUint48Codec_RoundTrip(t *testing.T) {
	c := MustNewUint48Codec(Layout48_654321)
	val := uint64(0x0000FFFFFFFFFFFF)
	regs, err := EncodeRegisters(val, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != val {
		t.Errorf("round-trip: got 0x%x, want 0x%x", got, val)
	}
}

func TestInt16Codec_Signed(t *testing.T) {
	c := MustNewInt16Codec(Layout16_21)
	regs, err := EncodeRegisters(int16(-1), c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != -1 {
		t.Errorf("DecodeRegisters = %d, want -1", got)
	}
}

func TestInt16SignMagnitudeCodec_Positive(t *testing.T) {
	c := NewInt16SignMagnitudeCodec()
	regs, err := EncodeRegisters(int16(12345), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 || regs[0] != 12345 {
		t.Errorf("regs = %v, want [12345]", regs)
	}
	got, _ := DecodeRegisters(regs, c)
	if got != 12345 {
		t.Errorf("got %d, want 12345", got)
	}
}

func TestInt16SignMagnitudeCodec_Negative(t *testing.T) {
	c := NewInt16SignMagnitudeCodec()
	v := int16(-9999)
	regs, err := EncodeRegisters(v, c)
	if err != nil {
		t.Fatal(err)
	}
	if regs[0]&0x8000 == 0 {
		t.Error("expected sign bit set")
	}
	if regs[0]&0x7FFF != 9999 {
		t.Errorf("magnitude = %d, want 9999", regs[0]&0x7FFF)
	}
	got, _ := DecodeRegisters(regs, c)
	if got != v {
		t.Errorf("got %d, want %d", got, v)
	}
}

func TestInt16SignMagnitudeCodec_RejectMagnitudeOverflow(t *testing.T) {
	c := NewInt16SignMagnitudeCodec()
	// -32768 has magnitude 32768 which exceeds 15 bits (max 32767)
	_, err := EncodeRegisters(int16(-32768), c)
	if err == nil {
		t.Fatal("expected error for magnitude 32768 > 32767")
	}
}

func TestUint16Codec_Layout12(t *testing.T) {
	c := MustNewUint16Codec(Layout16_12)
	v, err := DecodeRegisters([]uint16{0x3412}, c)
	if err != nil {
		t.Fatal(err)
	}
	if v != 0x1234 {
		t.Errorf("got 0x%x", v)
	}
	regs, _ := EncodeRegisters(uint16(0x1234), c)
	if len(regs) != 1 || regs[0] != 0x3412 {
		t.Errorf("EncodeRegisters = %v", regs)
	}
}

func TestInt48Codec_RoundTrip(t *testing.T) {
	c := MustNewInt48Codec(Layout48_654321)
	val := int64(-1)
	regs, err := EncodeRegisters(val, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != val {
		t.Errorf("round-trip: got %d, want %d", got, val)
	}
}

func TestUint64Codec_RoundTrip(t *testing.T) {
	c := MustNewUint64Codec(Layout64_87654321)
	val := uint64(0x0123456789ABCDEF)
	regs, err := EncodeRegisters(val, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != val {
		t.Errorf("round-trip: got 0x%x, want 0x%x", got, val)
	}
}

func TestInt64Codec_RoundTrip(t *testing.T) {
	c := MustNewInt64Codec(Layout64_21436587)
	val := int64(-1)
	regs, err := EncodeRegisters(val, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != val {
		t.Errorf("round-trip: got %d, want %d", got, val)
	}
}

func TestFloat32Codec_RoundTrip(t *testing.T) {
	c := MustNewFloat32Codec(Layout32_4321)
	val := float32(3.14)
	regs, err := EncodeRegisters(val, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(got-val)) > 1e-5 {
		t.Errorf("round-trip: got %v, want %v", got, val)
	}
}

func TestUint32Codec_Layout2143(t *testing.T) {
	c := MustNewUint32Codec(Layout32_2143)
	regs, err := EncodeRegisters(uint32(0x12345678), c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0x12345678 {
		t.Errorf("got 0x%x", got)
	}
	if c.ID() != "uint32/layout:2143" || c.Name() != "uint32" {
		t.Errorf("ID=%q Name=%q", c.ID(), c.Name())
	}
	if c.RegisterSpec().Count != 2 || c.ByteSpec().Count != 4 {
		t.Errorf("RegisterSpec=%v ByteSpec=%v", c.RegisterSpec(), c.ByteSpec())
	}
}

func TestInt32Codec_Layout2143(t *testing.T) {
	c := MustNewInt32Codec(Layout32_2143)
	regs, err := EncodeRegisters(int32(-1), c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != -1 {
		t.Errorf("got %d", got)
	}
}

func TestUint48Codec_Layout214365(t *testing.T) {
	c := MustNewUint48Codec(Layout48_214365)
	val := uint64(0x0000AABBCCDDEEFF)
	regs, err := EncodeRegisters(val, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != val {
		t.Errorf("got 0x%x, want 0x%x", got, val)
	}
	_ = c.ID()
	_ = c.Name()
}

func TestInt48Codec_NegativeValue(t *testing.T) {
	c := MustNewInt48Codec(Layout48_654321)
	// Value with bit 47 set to trigger sign extension in DecodeRegisters
	val := int64(-1)
	regs, err := EncodeRegisters(val, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != val {
		t.Errorf("got %d, want %d", got, val)
	}
}

func TestFloat32Codec_Layout2143(t *testing.T) {
	c := MustNewFloat32Codec(Layout32_2143)
	val := float32(-1.5)
	regs, err := EncodeRegisters(val, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(got-val)) > 1e-5 {
		t.Errorf("got %v, want %v", got, val)
	}
}

func TestFloat64Codec_Layout21436587(t *testing.T) {
	c := MustNewFloat64Codec(Layout64_21436587)
	val := 1.0
	regs, err := EncodeRegisters(val, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got-val) > 1e-10 {
		t.Errorf("got %v, want %v", got, val)
	}
	_ = c.RegisterSpec()
	_ = c.ByteSpec()
}

func TestNewUint16Codec_WrongLayout(t *testing.T) {
	_, err := NewUint16Codec(Layout32_4321)
	if err == nil {
		t.Fatal("expected error for layout with RegisterCount != 1")
	}
	var le *CodecLayoutError
	if !errors.As(err, &le) {
		t.Errorf("expected CodecLayoutError, got %T", err)
	}
	if le.Codec != "uint16" {
		t.Errorf("Codec = %q", le.Codec)
	}
}

func TestNewInt32Codec_WrongLayout(t *testing.T) {
	_, err := NewInt32Codec(Layout64_87654321)
	if err == nil {
		t.Fatal("expected error for wrong layout")
	}
	if !errors.Is(err, ErrCodecLayout) {
		t.Errorf("got %v", err)
	}
}

func TestDescriptorConsistency_NumericCodecs(t *testing.T) {
	// For each registered descriptor that has a known layout, build the codec and assert RegisterSpec/ByteSpec match.
	all := AvailableCodecDescriptors()
	for _, d := range all {
		if d.Family != CodecFamilyInteger && d.Family != CodecFamilyFloat {
			continue
		}
		if len(d.Layouts) != 1 {
			continue
		}
		layout := d.Layouts[0].Layout
		// Build codec by type and layout
		var spec RegisterSpec
		var byteSpec ByteSpec
		switch d.ValueKind {
		case CodecValueUint16:
			c, err := NewUint16Codec(layout)
			if err != nil {
				t.Errorf("descriptor %s: %v", d.ID, err)
				continue
			}
			spec, byteSpec = c.RegisterSpec(), c.ByteSpec()
		case CodecValueInt16:
			c, err := NewInt16Codec(layout)
			if err != nil {
				t.Errorf("descriptor %s: %v", d.ID, err)
				continue
			}
			spec, byteSpec = c.RegisterSpec(), c.ByteSpec()
		case CodecValueUint32:
			c, err := NewUint32Codec(layout)
			if err != nil {
				t.Errorf("descriptor %s: %v", d.ID, err)
				continue
			}
			spec, byteSpec = c.RegisterSpec(), c.ByteSpec()
		case CodecValueInt32:
			c, _ := NewInt32Codec(layout)
			spec, byteSpec = c.RegisterSpec(), c.ByteSpec()
		case CodecValueFloat32:
			c, _ := NewFloat32Codec(layout)
			spec, byteSpec = c.RegisterSpec(), c.ByteSpec()
		case CodecValueUint48:
			c, _ := NewUint48Codec(layout)
			spec, byteSpec = c.RegisterSpec(), c.ByteSpec()
		case CodecValueInt48:
			c, _ := NewInt48Codec(layout)
			spec, byteSpec = c.RegisterSpec(), c.ByteSpec()
		case CodecValueUint64:
			c, _ := NewUint64Codec(layout)
			spec, byteSpec = c.RegisterSpec(), c.ByteSpec()
		case CodecValueInt64:
			c, _ := NewInt64Codec(layout)
			spec, byteSpec = c.RegisterSpec(), c.ByteSpec()
		case CodecValueFloat64:
			c, _ := NewFloat64Codec(layout)
			spec, byteSpec = c.RegisterSpec(), c.ByteSpec()
		default:
			continue
		}
		if spec != d.RegisterSpec {
			t.Errorf("descriptor %s: RegisterSpec mismatch: codec %+v, descriptor %+v", d.ID, spec, d.RegisterSpec)
		}
		if byteSpec != d.ByteSpec {
			t.Errorf("descriptor %s: ByteSpec mismatch: codec %+v, descriptor %+v", d.ID, byteSpec, d.ByteSpec)
		}
	}
}
