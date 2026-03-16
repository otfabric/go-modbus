package codec

import (
	"bytes"
	"errors"
	"testing"
)

func TestPermuteBytesDecode_4321(t *testing.T) {
	// Layout 4321: raw order is already MSB..LSB
	raw := []byte{0x12, 0x34, 0x56, 0x78}
	canonical, err := PermuteBytesDecode(raw, Layout32_4321)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, raw) {
		t.Errorf("Layout32_4321: got %x, want %x", canonical, raw)
	}
}

func TestPermuteBytesDecode_2143(t *testing.T) {
	// Layout 2143: raw is [word1_lo, word1_hi, word0_lo, word0_hi] -> canonical [word0_hi, word0_lo, word1_hi, word1_lo] = MSB..LSB
	// positions [2,1,4,3]: raw[0]=pos2, raw[1]=pos1, raw[2]=pos4, raw[3]=pos3
	// So MSB (pos4) at raw[2], pos3 at raw[3], pos2 at raw[0], pos1 at raw[1]
	// canonical[0]=raw[2], canonical[1]=raw[3], canonical[2]=raw[0], canonical[3]=raw[1]
	raw := []byte{0x56, 0x78, 0x12, 0x34}
	canonical, err := PermuteBytesDecode(raw, Layout32_2143)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x12, 0x34, 0x56, 0x78}
	if !bytes.Equal(canonical, want) {
		t.Errorf("got %x, want %x", canonical, want)
	}
}

func TestPermuteBytesEncode_4321(t *testing.T) {
	canonical := []byte{0x12, 0x34, 0x56, 0x78}
	raw, err := PermuteBytesEncode(canonical, Layout32_4321)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, canonical) {
		t.Errorf("Layout32_4321: got %x, want %x", raw, canonical)
	}
}

func TestPermuteBytesEncode_2143(t *testing.T) {
	canonical := []byte{0x12, 0x34, 0x56, 0x78}
	raw, err := PermuteBytesEncode(canonical, Layout32_2143)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0x56, 0x78, 0x12, 0x34}
	if !bytes.Equal(raw, want) {
		t.Errorf("got %x, want %x", raw, want)
	}
}

func TestPermuteBytes_RoundTrip(t *testing.T) {
	layouts := []RegisterLayout{Layout32_4321, Layout32_2143, Layout64_87654321, Layout64_21436587}
	for _, layout := range layouts {
		byteCount := layout.RegisterCount() * 2
		canonical := make([]byte, byteCount)
		for i := uint16(0); i < byteCount; i++ {
			canonical[i] = byte(0xA0 + i)
		}
		raw, err := PermuteBytesEncode(canonical, layout)
		if err != nil {
			t.Fatalf("%s encode: %v", layout.String(), err)
		}
		got, err := PermuteBytesDecode(raw, layout)
		if err != nil {
			t.Fatalf("%s decode: %v", layout.String(), err)
		}
		if !bytes.Equal(got, canonical) {
			t.Errorf("%s round-trip: got %x, want %x", layout.String(), got, canonical)
		}
	}
}

func TestPermuteBytesDecode_WrongLength(t *testing.T) {
	_, err := PermuteBytesDecode([]byte{1, 2, 3}, Layout32_4321)
	if err == nil {
		t.Fatal("expected error for wrong length")
	}
	if !errors.Is(err, ErrEncodingError) {
		t.Errorf("expected ErrEncodingError, got %v", err)
	}
}

func TestPermuteBytesEncode_WrongLength(t *testing.T) {
	_, err := PermuteBytesEncode([]byte{1, 2, 3}, Layout32_4321)
	if err == nil {
		t.Fatal("expected error for wrong length")
	}
}
