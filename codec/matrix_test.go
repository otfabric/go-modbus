// SPDX-License-Identifier: MIT

// matrix_test.go implements the codec test matrix: layout validation, codec construction,
// round-trip (incl. edge values), negative shape, descriptor consistency, discovery filtering,
// and transport integration.

package codec

import (
	"errors"
	"math"
	"testing"
)

// --- §15A: Codec construction (constructors reject invalid args) ---

func TestMatrix_CodecConstruction_RejectInvalid(t *testing.T) {
	t.Run("NewAsciiCodec(0)", func(t *testing.T) {
		_, err := NewAsciiCodec(0)
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, ErrCodecValue) {
			t.Errorf("expected ErrCodecValue, got %v", err)
		}
	})
	t.Run("NewBytesCodec(0)", func(t *testing.T) {
		_, err := NewBytesCodec(0)
		if err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("NewBytesCodec_odd", func(t *testing.T) {
		_, err := NewBytesCodec(3)
		if err == nil {
			t.Fatal("expected error for odd byteCount")
		}
	})
}

// --- §15A: Encode/decode round-trip with edge values ---

func TestMatrix_RoundTrip_EdgeValues(t *testing.T) {
	t.Run("uint32_zero", func(t *testing.T) {
		c := MustNewUint32Codec(Layout32_4321)
		regs, _ := EncodeRegisters(uint32(0), c)
		got, err := DecodeRegisters(regs, c)
		if err != nil {
			t.Fatal(err)
		}
		if got != 0 {
			t.Errorf("got %d", got)
		}
	})
	t.Run("uint32_max", func(t *testing.T) {
		c := MustNewUint32Codec(Layout32_4321)
		v := uint32(0xFFFFFFFF)
		regs, _ := EncodeRegisters(v, c)
		got, err := DecodeRegisters(regs, c)
		if err != nil {
			t.Fatal(err)
		}
		if got != v {
			t.Errorf("got 0x%x, want 0x%x", got, v)
		}
	})
	t.Run("int32_min", func(t *testing.T) {
		c := MustNewInt32Codec(Layout32_4321)
		v := int32(math.MinInt32)
		regs, _ := EncodeRegisters(v, c)
		got, err := DecodeRegisters(regs, c)
		if err != nil {
			t.Fatal(err)
		}
		if got != v {
			t.Errorf("got %d, want %d", got, v)
		}
	})
	t.Run("float32_NaN", func(t *testing.T) {
		c := MustNewFloat32Codec(Layout32_4321)
		v := float32(math.NaN())
		regs, err := EncodeRegisters(v, c)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeRegisters(regs, c)
		if err != nil {
			t.Fatal(err)
		}
		if !math.IsNaN(float64(got)) {
			t.Errorf("expected NaN, got %f", got)
		}
	})
	t.Run("float64_NaN", func(t *testing.T) {
		c := MustNewFloat64Codec(Layout64_87654321)
		v := math.NaN()
		regs, err := EncodeRegisters(v, c)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeRegisters(regs, c)
		if err != nil {
			t.Fatal(err)
		}
		if !math.IsNaN(got) {
			t.Errorf("expected NaN, got %f", got)
		}
	})
}

// --- §15A: Negative shape (wrong register count → ErrCodecRegisterCount, errors.As) ---

func TestMatrix_NegativeShape_WrongRegisterCount(t *testing.T) {
	codec := MustNewUint32Codec(Layout32_4321)
	_, err := DecodeRegisters([]uint16{0x1234}, codec)
	if err == nil {
		t.Fatal("expected error for wrong register count")
	}
	var e *CodecRegisterCountError
	if !errors.As(err, &e) {
		t.Errorf("expected CodecRegisterCountError, got %T", err)
	}
	if !errors.Is(err, ErrCodecRegisterCount) {
		t.Errorf("expected ErrCodecRegisterCount, got %v", err)
	}
	if e.Expected.Count != 2 || e.Actual != 1 {
		t.Errorf("Expected.Count=%d Actual=%d", e.Expected.Count, e.Actual)
	}
}

// --- §15A: Discovery filtering ---

func TestMatrix_Discovery_ForRegisterCount(t *testing.T) {
	got := CodecDescriptorsForRegisterCount(2)
	for i, d := range got {
		if d.RegisterSpec.Count != 2 {
			t.Errorf("descriptor[%d] %s: RegisterSpec.Count = %d, want 2", i, d.ID, d.RegisterSpec.Count)
		}
	}
}

func TestMatrix_Discovery_ForByteCount(t *testing.T) {
	got := CodecDescriptorsForByteCount(4)
	for i, d := range got {
		if d.ByteSpec.Count != 4 {
			t.Errorf("descriptor[%d] %s: ByteSpec.Count = %d, want 4", i, d.ID, d.ByteSpec.Count)
		}
	}
}

func TestMatrix_Discovery_FindCodecDescriptors(t *testing.T) {
	got := FindCodecDescriptors(CodecQuery{
		RegisterCount: 2,
		Family:        CodecFamilyInteger,
	})
	for i, d := range got {
		if d.RegisterSpec.Count != 2 {
			t.Errorf("descriptor[%d] RegisterSpec.Count = %d, want 2", i, d.RegisterSpec.Count)
		}
		if d.Family != CodecFamilyInteger {
			t.Errorf("descriptor[%d] Family = %v, want CodecFamilyInteger", i, d.Family)
		}
	}
}

// --- §15A: Descriptor consistency (text/BCD: build from descriptor, assert spec) ---

func TestMatrix_DescriptorConsistency_TextAndBCD(t *testing.T) {
	all := AvailableCodecDescriptors()
	for _, d := range all {
		if d.Family != CodecFamilyText && d.Family != CodecFamilyBCD {
			continue
		}
		n := d.RegisterSpec.Count
		var spec RegisterSpec
		var byteSpec ByteSpec
		switch d.Name {
		case "ascii":
			c, err := NewAsciiCodec(n)
			if err != nil {
				t.Errorf("descriptor %s: %v", d.ID, err)
				continue
			}
			spec, byteSpec = c.RegisterSpec(), c.ByteSpec()
		case "ascii_fixed":
			c, _ := NewAsciiFixedCodec(n)
			spec, byteSpec = c.RegisterSpec(), c.ByteSpec()
		case "ascii_reverse":
			c, _ := NewAsciiReverseCodec(n)
			spec, byteSpec = c.RegisterSpec(), c.ByteSpec()
		case "bcd":
			c, _ := NewBCDCodec(n)
			spec, byteSpec = c.RegisterSpec(), c.ByteSpec()
		case "packed_bcd":
			c, _ := NewPackedBCDCodec(n)
			spec, byteSpec = c.RegisterSpec(), c.ByteSpec()
		default:
			continue
		}
		if spec != d.RegisterSpec {
			t.Errorf("descriptor %s: RegisterSpec mismatch", d.ID)
		}
		if byteSpec != d.ByteSpec {
			t.Errorf("descriptor %s: ByteSpec mismatch", d.ID)
		}
	}
}
