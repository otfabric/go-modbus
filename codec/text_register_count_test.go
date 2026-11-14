// SPDX-License-Identifier: MIT

package codec

import (
	"errors"
	"testing"
)

// The text, BCD and UTF-16 codecs hold two bytes per register and report their
// byte count in ByteSpec, a 16-bit field: a constructor accepts 1..32767
// registers, and every size computed from the register count is the full
// value, not its low 16 bits.

type textCodecCtor struct {
	name string
	new  func(uint16) (Codec[string], error)
	// zero is a value the codec encodes whatever its width.
	zero string
	// slowEncode marks the codec whose encoder is quadratic in its width:
	// it is encoded only at the small widths.
	slowEncode bool
}

func textCodecCtors() []textCodecCtor {
	return []textCodecCtor{
		{name: "ascii", new: NewAsciiCodec, zero: "A"},
		{name: "ascii_fixed", new: NewAsciiFixedCodec, zero: "A"},
		{name: "ascii_reverse", new: NewAsciiReverseCodec, zero: "A"},
		{name: "bcd", new: NewBCDCodec, zero: "1"},
		{name: "packed_bcd", new: NewPackedBCDCodec, zero: "1"},
		{name: "signed_packed_bcd", new: NewSignedPackedBCDCodec, zero: "1", slowEncode: true},
		{name: "packed_bcd_reverse", new: NewPackedBCDReverseCodec, zero: "1"},
		{name: "utf16be", new: NewUTF16BECodec, zero: "A"},
		{name: "utf16le", new: NewUTF16LECodec, zero: "A"},
	}
}

func TestTextCodecs_RegisterCountBoundaries(t *testing.T) {
	counts := []struct {
		n  uint16
		ok bool
	}{
		{0, false},
		{1, true},
		{2, true},
		{125, true},   // the most registers one Modbus read returns
		{16383, true}, // 4 × 16383 nibbles is the last nibble count that fits 16 bits
		{16384, true}, // 4 × 16384 does not
		{32766, true},
		{32767, true},  // 2 × 32767 bytes is the last byte count that fits ByteSpec
		{32768, false}, // 2 × 32768 does not
		{40000, false},
		{65534, false},
		{65535, false},
	}
	for _, ctor := range textCodecCtors() {
		for _, tc := range counts {
			c, err := ctor.new(tc.n)
			if !tc.ok {
				if !errors.Is(err, ErrCodecValue) || c != nil {
					t.Errorf("%s with %d registers: codec %v, error %v; want ErrCodecValue", ctor.name, tc.n, c, err)
				}
				continue
			}
			if err != nil {
				t.Errorf("%s with %d registers: %v", ctor.name, tc.n, err)
				continue
			}
			if got := c.RegisterSpec().Count; got != tc.n {
				t.Errorf("%s with %d registers: RegisterSpec %d", ctor.name, tc.n, got)
			}
			if got, want := int(c.ByteSpec().Count), 2*int(tc.n); got != want {
				t.Errorf("%s with %d registers: ByteSpec %d, want %d", ctor.name, tc.n, got, want)
			}
			if ctor.slowEncode && tc.n > 125 {
				continue
			}
			// Called on the codec itself, without the length check of codec.EncodeRegisters.
			regs, err := c.EncodeRegisters(ctor.zero)
			if err != nil || len(regs) != int(tc.n) {
				t.Errorf("%s with %d registers: EncodeRegisters returned %d registers, %v; want %d", ctor.name, tc.n, len(regs), err, tc.n)
				continue
			}
			if _, err := EncodeRegisters(ctor.zero, c); err != nil {
				t.Errorf("%s with %d registers: codec.EncodeRegisters: %v", ctor.name, tc.n, err)
			}
			if _, err := c.DecodeRegisters(regs); err != nil {
				t.Errorf("%s with %d registers: decoding what was encoded: %v", ctor.name, tc.n, err)
			}
		}
	}
}

// The widest value survives a round trip at a width whose nibble count needs
// more than 16 bits.
func TestTextCodecs_RoundTripAtWideWidths(t *testing.T) {
	const n = 16384
	digits := make([]byte, 4*n)
	for i := range digits {
		digits[i] = '0' + byte(i%9) + 1
	}
	for _, ctor := range []struct {
		name string
		new  func(uint16) (Codec[string], error)
		in   string
	}{
		{"ascii", NewAsciiCodec, string(digits[:2*n])},
		{"bcd", NewBCDCodec, string(digits[:2*n])},
		{"packed_bcd", NewPackedBCDCodec, string(digits)},
		{"packed_bcd_reverse", NewPackedBCDReverseCodec, string(digits)},
	} {
		c, err := ctor.new(n)
		if err != nil {
			t.Fatalf("%s: %v", ctor.name, err)
		}
		regs, err := c.EncodeRegisters(ctor.in)
		if err != nil || len(regs) != n {
			t.Fatalf("%s: EncodeRegisters returned %d registers, %v; want %d", ctor.name, len(regs), err, n)
		}
		out, err := c.DecodeRegisters(regs)
		if err != nil || out != ctor.in {
			t.Fatalf("%s: round trip of %d characters returned %d characters, %v", ctor.name, len(ctor.in), len(out), err)
		}
	}
}

func TestTextByteCount(t *testing.T) {
	for n, want := range map[uint16]int{0: 0, 1: 2, 32767: 65534, 32768: 65536, 65535: 131070} {
		if got := textByteCount(n); got != want {
			t.Errorf("textByteCount(%d) = %d, want %d", n, got, want)
		}
	}
	if got := textByteSpec(maxTextRegisterCount).Count; got != 65534 {
		t.Errorf("textByteSpec(%d) = %d, want 65534", maxTextRegisterCount, got)
	}
}
