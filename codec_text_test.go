package modbus

import (
	"errors"
	"strings"
	"testing"
)

func TestNewAsciiCodec_RejectZero(t *testing.T) {
	_, err := NewAsciiCodec(0)
	if err == nil {
		t.Fatal("expected error for registerCount 0")
	}
	if !errors.Is(err, ErrCodecValue) {
		t.Errorf("expected ErrCodecValue, got %v", err)
	}
}

func TestNewAsciiCodec_RoundTrip(t *testing.T) {
	c, err := NewAsciiCodec(4)
	if err != nil {
		t.Fatal(err)
	}
	s := "AB"
	regs, err := EncodeRegisters(s, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != "AB" {
		t.Errorf("round-trip: got %q, want AB", got)
	}
}

func TestAsciiCodec_TrimTrailingSpaces(t *testing.T) {
	c := mustTextCodec(t, "ascii", 2)
	regs := []uint16{0x4142, 0x4320}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ABC" {
		t.Errorf("DecodeRegisters (trim spaces) = %q, want ABC", got)
	}
}

func TestAsciiFixedCodec_PreserveSpaces(t *testing.T) {
	c, err := NewAsciiFixedCodec(2)
	if err != nil {
		t.Fatal(err)
	}
	regs := []uint16{0x4142, 0x4320}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ABC " {
		t.Errorf("DecodeRegisters (preserve) = %q, want ABC ", got)
	}
}

func TestAsciiReverseCodec_RoundTrip(t *testing.T) {
	c, err := NewAsciiReverseCodec(2)
	if err != nil {
		t.Fatal(err)
	}
	s := "AB"
	regs, err := EncodeRegisters(s, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != "AB" {
		t.Errorf("round-trip: got %q", got)
	}
}

func TestBCDCodec_RoundTrip(t *testing.T) {
	c, err := NewBCDCodec(2)
	if err != nil {
		t.Fatal(err)
	}
	s := "1234"
	regs, err := EncodeRegisters(s, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != s {
		t.Errorf("round-trip: got %q, want %q", got, s)
	}
}

func TestBCDCodec_RejectNonDigit(t *testing.T) {
	c, err := NewBCDCodec(2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = EncodeRegisters("12a4", c)
	if err == nil {
		t.Fatal("expected error for non-digit")
	}
}

func TestPackedBCDCodec_RoundTrip(t *testing.T) {
	c, err := NewPackedBCDCodec(2)
	if err != nil {
		t.Fatal(err)
	}
	// 2 registers = 4 bytes = 8 digits; full width round-trip
	s := "12345678"
	regs, err := EncodeRegisters(s, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != s {
		t.Errorf("round-trip: got %q, want %q", got, s)
	}
}

func TestPackedBCDCodec_RejectNonDigit(t *testing.T) {
	c, err := NewPackedBCDCodec(2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = EncodeRegisters("12x4", c)
	if err == nil {
		t.Fatal("expected error for non-digit")
	}
}

func TestAsciiCodec_RejectNonASCII(t *testing.T) {
	c, err := NewAsciiCodec(2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = EncodeRegisters("café", c)
	if err == nil {
		t.Fatal("expected error for non-ASCII (UTF-8 multi-byte)")
	}
}

func TestAsciiCodec_RejectNonASCII_BeyondWidth(t *testing.T) {
	// Full input is validated; non-ASCII beyond the codec width must still be rejected.
	c, err := NewAsciiCodec(2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = EncodeRegisters("ABé", c)
	if err == nil {
		t.Fatal("expected error when non-ASCII appears after valid prefix")
	}
}

func TestAsciiCodec_OverlongASCII_Truncated(t *testing.T) {
	// Overlong but all-ASCII input is truncated to width, not rejected.
	c, err := NewAsciiCodec(1)
	if err != nil {
		t.Fatal(err)
	}
	regs, err := EncodeRegisters("ABCD", c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != "AB" {
		t.Errorf("overlong ASCII truncated to width: got %q, want AB", got)
	}
}

func TestSignedPackedBCDCodec_PositiveRoundTrip(t *testing.T) {
	c, err := NewSignedPackedBCDCodec(2)
	if err != nil {
		t.Fatal(err)
	}
	s := "1234"
	regs, err := EncodeRegisters(s, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != s {
		t.Errorf("round-trip positive: got %q, want %q", got, s)
	}
}

func TestSignedPackedBCDCodec_NegativeRoundTrip(t *testing.T) {
	c, err := NewSignedPackedBCDCodec(2)
	if err != nil {
		t.Fatal(err)
	}
	s := "-1234"
	regs, err := EncodeRegisters(s, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != s {
		t.Errorf("round-trip negative: got %q, want %q", got, s)
	}
}

func TestSignedPackedBCDCodec_RejectNonDigit(t *testing.T) {
	c, err := NewSignedPackedBCDCodec(1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = EncodeRegisters("-12a4", c)
	if err == nil {
		t.Fatal("expected error for non-digit in signed packed BCD")
	}
}

// TestSignedPackedBCDCodec_SignNibbleRules verifies documented sign nibble semantics:
// decode accepts 0xC, 0xD, 0xF as negative; encode emits only 0xC for negative.
func TestSignedPackedBCDCodec_SignNibbleRules(t *testing.T) {
	c, err := NewSignedPackedBCDCodec(2) // 4 bytes = 8 nibbles; 7 digits + sign for negative
	if err != nil {
		t.Fatal(err)
	}
	// Encode "-123" → negative uses 0xC in trailing nibble only
	regs, err := EncodeRegisters("-123", c)
	if err != nil {
		t.Fatal(err)
	}
	raw := regsToBytes(regs)
	lastNibble := raw[len(raw)-1] & 0x0F
	if lastNibble != 0x0C {
		t.Errorf("encode negative: trailing nibble = 0x%X, want 0xC (canonical)", lastNibble)
	}
	// Decode: 0xC, 0xD, 0xF in trailing nibble must all yield negative
	for _, signNibble := range []byte{0x0C, 0x0D, 0x0F} {
		rawNeg := make([]byte, len(raw))
		copy(rawNeg, raw)
		rawNeg[len(rawNeg)-1] = (rawNeg[len(rawNeg)-1] & 0xF0) | signNibble
		regsIn := bytesToRegs(rawNeg)
		got, err := DecodeRegisters(regsIn, c)
		if err != nil {
			t.Errorf("decode with sign nibble 0x%X: %v", signNibble, err)
			continue
		}
		if !strings.HasPrefix(got, "-") {
			t.Errorf("decode with sign nibble 0x%X: got %q, want negative", signNibble, got)
		}
	}
}

func regsToBytes(regs []uint16) []byte {
	out := make([]byte, 0, len(regs)*2)
	for _, r := range regs {
		out = append(out, byte(r>>8), byte(r))
	}
	return out
}

func bytesToRegs(b []byte) []uint16 {
	out := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		out = append(out, uint16(b[i])<<8|uint16(b[i+1]))
	}
	return out
}

func TestPackedBCDReverseCodec_RoundTrip(t *testing.T) {
	c, err := NewPackedBCDReverseCodec(2)
	if err != nil {
		t.Fatal(err)
	}
	s := "12345678"
	regs, err := EncodeRegisters(s, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != s {
		t.Errorf("round-trip: got %q, want %q", got, s)
	}
}

func TestPackedBCDReverseCodec_ByteOrder(t *testing.T) {
	// Reverse = low byte first per register. "12" pads to 4 digits = "0012", bytes [0x00, 0x12], LE word = 0x1200.
	c, err := NewPackedBCDReverseCodec(1)
	if err != nil {
		t.Fatal(err)
	}
	regs, err := EncodeRegisters("12", c)
	if err != nil {
		t.Fatal(err)
	}
	if regs[0] != 0x1200 {
		t.Errorf("packed_bcd_reverse encode: reg[0] = 0x%04x, want 0x1200 (LE)", regs[0])
	}
	got, _ := DecodeRegisters(regs, c)
	if got != "0012" {
		t.Errorf("got %q, want 0012 (leading zero padded)", got)
	}
}

func TestTextCodec_ZeroRegistersRejected(t *testing.T) {
	for name, fn := range map[string]func(uint16) (Codec[string], error){
		"ascii":              NewAsciiCodec,
		"ascii_fixed":        NewAsciiFixedCodec,
		"ascii_reverse":      NewAsciiReverseCodec,
		"bcd":                NewBCDCodec,
		"packed_bcd":         NewPackedBCDCodec,
		"signed_packed_bcd":  NewSignedPackedBCDCodec,
		"packed_bcd_reverse": NewPackedBCDReverseCodec,
	} {
		_, err := fn(0)
		if err == nil {
			t.Errorf("%s(0): expected error", name)
		}
	}
}

func mustTextCodec(t *testing.T, kind string, n uint16) Codec[string] {
	t.Helper()
	var c Codec[string]
	var err error
	switch kind {
	case "ascii":
		c, err = NewAsciiCodec(n)
	case "ascii_fixed":
		c, err = NewAsciiFixedCodec(n)
	case "ascii_reverse":
		c, err = NewAsciiReverseCodec(n)
	case "bcd":
		c, err = NewBCDCodec(n)
	case "packed_bcd":
		c, err = NewPackedBCDCodec(n)
	default:
		t.Fatalf("unknown kind %s", kind)
	}
	if err != nil {
		t.Fatal(err)
	}
	return c
}
