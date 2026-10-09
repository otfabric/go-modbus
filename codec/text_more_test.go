// SPDX-License-Identifier: MIT

package codec

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"testing"
)

// textCtor describes one register-count-parameterised string codec constructor.
type textCtor struct {
	name string
	new  func(uint16) (Codec[string], error)
}

func textCtors() []textCtor {
	return []textCtor{
		{"ascii", NewAsciiCodec},
		{"ascii_fixed", NewAsciiFixedCodec},
		{"ascii_reverse", NewAsciiReverseCodec},
		{"bcd", NewBCDCodec},
		{"packed_bcd", NewPackedBCDCodec},
		{"signed_packed_bcd", NewSignedPackedBCDCodec},
		{"packed_bcd_reverse", NewPackedBCDReverseCodec},
		{"utf16be", NewUTF16BECodec},
		{"utf16le", NewUTF16LECodec},
	}
}

func mustStringCodec(t *testing.T, name string, n uint16) Codec[string] {
	t.Helper()
	for _, tc := range textCtors() {
		if tc.name == name {
			c, err := tc.new(n)
			if err != nil {
				t.Fatalf("%s(%d): %v", name, n, err)
			}
			return c
		}
	}
	t.Fatalf("unknown string codec %q", name)
	return nil
}

func TestStringCodecs_MetaAndRegisterCount(t *testing.T) {
	for _, tc := range textCtors() {
		t.Run(tc.name, func(t *testing.T) {
			for _, n := range []uint16{1, 3, 8} {
				c := mustStringCodec(t, tc.name, n)
				wantID := fmt.Sprintf("%s/registers:%d", tc.name, n)
				assertMeta(t, c, wantID, tc.name, n)
				assertDirectCountError[string](t, c)
			}
			if _, err := tc.new(0); !errors.Is(err, ErrCodecValue) {
				t.Errorf("zero registers: want ErrCodecValue, got %v", err)
			}
		})
	}
}

func TestStringCodecs_ExactVectors(t *testing.T) {
	cases := []struct {
		codec string
		n     uint16
		in    string
		regs  []uint16
		back  string // decoded form of regs
	}{
		// ascii: high byte first, space padded, trailing spaces trimmed on decode.
		{"ascii", 2, "AB", []uint16{0x4142, 0x2020}, "AB"},
		{"ascii", 2, "ABCDE", []uint16{0x4142, 0x4344}, "ABCD"},
		// ascii_fixed: NUL padded, nothing trimmed.
		{"ascii_fixed", 2, "AB", []uint16{0x4142, 0x0000}, "AB\x00\x00"},
		{"ascii_fixed", 2, "A B ", []uint16{0x4120, 0x4220}, "A B "},
		{"ascii_fixed", 1, "ABC", []uint16{0x4142}, "AB"},
		// ascii_reverse: low byte first per register, space padded.
		{"ascii_reverse", 2, "ABC", []uint16{0x4241, 0x2043}, "ABC"},
		{"ascii_reverse", 1, "ABC", []uint16{0x4241}, "AB"},
		// bcd: one digit per byte, left zero padded, keeps rightmost digits.
		{"bcd", 2, "12", []uint16{0x0000, 0x0102}, "0012"},
		{"bcd", 1, "12345", []uint16{0x0405}, "45"},
		// packed_bcd: one digit per nibble.
		{"packed_bcd", 2, "123", []uint16{0x0000, 0x0123}, "00000123"},
		{"packed_bcd", 1, "123456", []uint16{0x3456}, "3456"},
		// packed_bcd_reverse: as packed_bcd with bytes swapped within each register.
		{"packed_bcd_reverse", 2, "12345678", []uint16{0x3412, 0x7856}, "12345678"},
		{"packed_bcd_reverse", 1, "123456", []uint16{0x5634}, "3456"},
		// signed_packed_bcd: negative replaces the last nibble with 0xC.
		{"signed_packed_bcd", 1, "12345", []uint16{0x2345}, "2345"},
		{"signed_packed_bcd", 1, "-12345", []uint16{0x345C}, "-345"},
		{"signed_packed_bcd", 2, "-7", []uint16{0x0000, 0x007C}, "-7"},
		{"signed_packed_bcd", 2, "0", []uint16{0x0000, 0x0000}, "0"},
		// utf16: one code unit per register, NUL padded.
		{"utf16be", 3, "Aé", []uint16{0x0041, 0x00E9, 0x0000}, "Aé\x00"},
		{"utf16le", 3, "Aé", []uint16{0x4100, 0xE900, 0x0000}, "Aé\x00"},
		{"utf16be", 2, "😀", []uint16{0xD83D, 0xDE00}, "😀"},
		{"utf16le", 2, "😀", []uint16{0x3DD8, 0x00DE}, "😀"},
		// Truncation must not split a surrogate pair: the lone high surrogate is dropped.
		{"utf16be", 2, "a😀", []uint16{0x0061, 0x0000}, "a\x00"},
		{"utf16le", 2, "a😀", []uint16{0x6100, 0x0000}, "a\x00"},
		{"utf16be", 1, "😀", []uint16{0x0000}, "\x00"},
	}
	for _, tc := range cases {
		c := mustStringCodec(t, tc.codec, tc.n)
		got, err := EncodeRegisters(tc.in, c)
		if err != nil {
			t.Errorf("%s: encode %q: %v", c.ID(), tc.in, err)
			continue
		}
		if !slices.Equal(got, tc.regs) {
			t.Errorf("%s: encode %q = %04X, want %04X", c.ID(), tc.in, got, tc.regs)
		}
		back, err := DecodeRegisters(tc.regs, c)
		if err != nil {
			t.Errorf("%s: decode %04X: %v", c.ID(), tc.regs, err)
			continue
		}
		if back != tc.back {
			t.Errorf("%s: decode %04X = %q, want %q", c.ID(), tc.regs, back, tc.back)
		}
	}
}

func TestStringCodecs_EncodeRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		codec string
		in    string
	}{
		{"ascii", "caf\xe9"},
		{"ascii_fixed", "é"},
		{"ascii_fixed", "ABCD\x80"}, // beyond the codec width but still rejected
		{"ascii_reverse", "\xff"},
		{"bcd", "12a4"},
		{"bcd", "-1"},
		{"packed_bcd", "12 4"},
		{"packed_bcd_reverse", "12x4"},
		{"packed_bcd_reverse", "١٢"}, // non-ASCII digits
		{"signed_packed_bcd", "+12"},
		{"signed_packed_bcd", "--1"},
		{"signed_packed_bcd", "1-2"},
	}
	for _, tc := range cases {
		c := mustStringCodec(t, tc.codec, 2)
		regs, err := EncodeRegisters(tc.in, c)
		if regs != nil {
			t.Errorf("%s: encode %q returned registers %04X alongside error", c.ID(), tc.in, regs)
		}
		assertValueError(t, err, c.ID())
	}
}

func TestStringCodecs_DecodeRejectsInvalidRegisters(t *testing.T) {
	cases := []struct {
		codec string
		regs  []uint16
	}{
		{"bcd", []uint16{0x0A01}},
		{"bcd", []uint16{0x0110}},
		{"packed_bcd", []uint16{0x1A34}},
		{"packed_bcd", []uint16{0x12F4}},
		{"packed_bcd_reverse", []uint16{0x341A}},
		{"packed_bcd_reverse", []uint16{0xB412}},
		{"signed_packed_bcd", []uint16{0xA123}}, // high nibble of byte 0
		{"signed_packed_bcd", []uint16{0x1B34}}, // low nibble of a non-final byte
		{"signed_packed_bcd", []uint16{0x123A}}, // final nibble is neither digit nor sign
		{"signed_packed_bcd", []uint16{0x123E}},
		{"signed_packed_bcd", []uint16{0x12CC}}, // sign nibble in a digit position
	}
	for _, tc := range cases {
		c := mustStringCodec(t, tc.codec, 1)
		s, err := DecodeRegisters(tc.regs, c)
		if s != "" {
			t.Errorf("%s: decode %04X returned %q alongside error", c.ID(), tc.regs, s)
		}
		assertValueError(t, err, c.ID())
	}
}

func TestSignedPackedBCDHelpers(t *testing.T) {
	if s, err := bytesToSignedPackedBCD(nil); err != nil || s != "" {
		t.Errorf("bytesToSignedPackedBCD(nil) = %q, %v; want \"\", nil", s, err)
	}
	for _, tc := range []struct {
		in   []byte
		want string
	}{
		{[]byte{0x12, 0x3C}, "-123"},
		{[]byte{0x12, 0x3D}, "-123"},
		{[]byte{0x12, 0x3F}, "-123"},
		{[]byte{0x00, 0x12}, "12"},
		{[]byte{0x00, 0x00}, "0"},
	} {
		got, err := bytesToSignedPackedBCD(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("bytesToSignedPackedBCD(%X) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}

	for _, tc := range []struct {
		in      string
		nibbles int
		want    []byte
	}{
		{"1234", 4, []byte{0x12, 0x34}},
		{"-123", 4, []byte{0x12, 0x3C}},
		{"-1", 4, []byte{0x00, 0x1C}},
		{"987654", 4, []byte{0x76, 0x54}}, // keeps the rightmost digits
		{"-98765", 4, []byte{0x76, 0x5C}},
		{"", 4, []byte{0x00, 0x00}},
	} {
		got, err := signedPackedBCDToBytes(tc.in, tc.nibbles)
		if err != nil || !bytes.Equal(got, tc.want) {
			t.Errorf("signedPackedBCDToBytes(%q, %d) = %X, %v; want %X", tc.in, tc.nibbles, got, err, tc.want)
		}
	}
	if _, err := signedPackedBCDToBytes("12x", 4); !errors.Is(err, errBCDDigit) {
		t.Errorf("signedPackedBCDToBytes(non-digit): want errBCDDigit, got %v", err)
	}
}

func TestAsciiToBytesReverse_OddLengthIsPadded(t *testing.T) {
	if got, want := asciiToBytesReverse("ABC"), []byte{'B', 'A', 0, 'C'}; !bytes.Equal(got, want) {
		t.Errorf("asciiToBytesReverse(ABC) = %q, want %q", got, want)
	}
	if got, want := asciiToBytesReverse("ABCD"), []byte("BADC"); !bytes.Equal(got, want) {
		t.Errorf("asciiToBytesReverse(ABCD) = %q, want %q", got, want)
	}
}

func TestTruncateUTF16CodeUnits(t *testing.T) {
	cases := []struct {
		units []uint16
		max   int
		want  []uint16
	}{
		{[]uint16{0x41, 0x42}, 4, []uint16{0x41, 0x42}},
		{[]uint16{0x41, 0x42, 0x43}, 2, []uint16{0x41, 0x42}},
		{[]uint16{0x41, 0xD83D, 0xDE00}, 2, []uint16{0x41}},
		{[]uint16{0xD83D, 0xDE00, 0x41}, 2, []uint16{0xD83D, 0xDE00}},
		{[]uint16{0xDBFF, 0xDFFF}, 1, []uint16{}},
		{[]uint16{0x41}, 0, []uint16{}},
	}
	for _, tc := range cases {
		if got := truncateUTF16CodeUnits(tc.units, tc.max); !slices.Equal(got, tc.want) {
			t.Errorf("truncateUTF16CodeUnits(%04X, %d) = %04X, want %04X", tc.units, tc.max, got, tc.want)
		}
	}
}

func TestUint64ByteHelpers_AllOrders(t *testing.T) {
	const v = uint64(0x0102030405060708)
	cases := []struct {
		name string
		e    Endianness
		w    WordOrder
		want []byte
	}{
		{"BE/high", BigEndian, HighWordFirst, []byte{1, 2, 3, 4, 5, 6, 7, 8}},
		{"BE/low", BigEndian, LowWordFirst, []byte{7, 8, 5, 6, 3, 4, 1, 2}},
		{"LE/low", LittleEndian, LowWordFirst, []byte{8, 7, 6, 5, 4, 3, 2, 1}},
		{"LE/high", LittleEndian, HighWordFirst, []byte{2, 1, 4, 3, 6, 5, 8, 7}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := uint64ToBytes(tc.e, tc.w, v)
			if !bytes.Equal(got, tc.want) {
				t.Errorf("uint64ToBytes = %X, want %X", got, tc.want)
			}
			// Two consecutive values: the second is all-ones except the lowest bit.
			second := uint64ToBytes(tc.e, tc.w, 0xFFFFFFFFFFFFFFFE)
			vals := bytesToUint64s(tc.e, tc.w, append(append([]byte{}, tc.want...), second...))
			if !slices.Equal(vals, []uint64{v, 0xFFFFFFFFFFFFFFFE}) {
				t.Errorf("bytesToUint64s = %X, want [%X FFFFFFFFFFFFFFFE]", vals, v)
			}
			signed := bytesToInt64s(tc.e, tc.w, second)
			if !slices.Equal(signed, []int64{-2}) {
				t.Errorf("bytesToInt64s = %v, want [-2]", signed)
			}
		})
	}
}

func TestBytesToInt48s_SignExtension(t *testing.T) {
	in := []byte{
		0x00, 0x00, 0x00, 0x00, 0x00, 0x05,
		0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFE,
		0x7F, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
		0x80, 0x00, 0x00, 0x00, 0x00, 0x00,
	}
	want := []int64{5, -2, 1<<47 - 1, -(1 << 47)}
	if got := bytesToInt48s(BigEndian, HighWordFirst, in); !slices.Equal(got, want) {
		t.Errorf("bytesToInt48s = %v, want %v", got, want)
	}
}
