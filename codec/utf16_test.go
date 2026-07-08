// SPDX-License-Identifier: MIT

package codec

import (
	"testing"
)

func TestUTF16BECodec_RoundTrip(t *testing.T) {
	c, err := NewUTF16BECodec(2)
	if err != nil {
		t.Fatal(err)
	}
	s := "A"
	regs, err := EncodeRegisters(s, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	// Fixed-width: trailing NUL code unit is preserved as rune
	if len(got) < 1 || got[0] != 'A' {
		t.Errorf("round-trip: got %q, want A (with optional NUL padding)", got)
	}
	if len(regs) != 2 {
		t.Errorf("expected 2 registers (padded), got %d", len(regs))
	}
	if regs[0] != 0x0041 {
		t.Errorf("regs[0] = 0x%04x, want 0x0041", regs[0])
	}
}

func TestUTF16LECodec_RoundTrip(t *testing.T) {
	c, err := NewUTF16LECodec(2)
	if err != nil {
		t.Fatal(err)
	}
	s := "A"
	regs, err := EncodeRegisters(s, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 1 || got[0] != 'A' {
		t.Errorf("round-trip: got %q, want A (with optional NUL padding)", got)
	}
	// LE: code unit 0x0041 stored as 0x4100 in register
	if regs[0] != 0x4100 {
		t.Errorf("regs[0] = 0x%04x, want 0x4100 (LE)", regs[0])
	}
}

func TestUTF16BECodec_RejectZeroRegisters(t *testing.T) {
	_, err := NewUTF16BECodec(0)
	if err == nil {
		t.Fatal("expected error for 0 registers")
	}
}

func TestUTF16LECodec_RejectZeroRegisters(t *testing.T) {
	_, err := NewUTF16LECodec(0)
	if err == nil {
		t.Fatal("expected error for 0 registers")
	}
}

func TestUTF16BECodec_TruncateOverlong(t *testing.T) {
	c, err := NewUTF16BECodec(1)
	if err != nil {
		t.Fatal(err)
	}
	// "AB" = 2 runes, but we only have 1 register; encode truncates
	regs, err := EncodeRegisters("AB", c)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 {
		t.Fatalf("expected 1 register, got %d", len(regs))
	}
	got, _ := DecodeRegisters(regs, c)
	if got != "A" {
		t.Errorf("got %q, want A", got)
	}
}

// TestUTF16BECodec_FullWidthPreservesEmbeddedNUL verifies that decode preserves full width
// and does not stop at the first NUL; embedded NUL survives.
func TestUTF16BECodec_FullWidthPreservesEmbeddedNUL(t *testing.T) {
	c, err := NewUTF16BECodec(3)
	if err != nil {
		t.Fatal(err)
	}
	// A, NUL, B = 3 code units
	s := "A\x00B"
	regs, err := EncodeRegisters(s, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeRegisters(regs, c)
	if err != nil {
		t.Fatal(err)
	}
	if got != s {
		t.Errorf("decode: got %q (len %d), want %q (embedded NUL preserved)", got, len(got), s)
	}
	if len(got) != 3 {
		t.Errorf("full width: len(got) = %d, want 3", len(got))
	}
}
