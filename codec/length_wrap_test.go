// SPDX-License-Identifier: MIT

package codec

import (
	"errors"
	"testing"
)

// A slice 65536 elements longer than expected has the expected length modulo
// 2^16. Every length check must compare the full length, or such a slice passes.
const lengthWrap = 1 << 16

func TestValidateRegisterSpec_LengthDoesNotWrap(t *testing.T) {
	err := ValidateRegisterSpec(RegisterSpec{Count: 2}, make([]uint16, 2+lengthWrap), "test")
	var ce *CodecRegisterCountError
	if !errors.As(err, &ce) {
		t.Fatalf("2+65536 registers for a 2-register spec: got %v, want *CodecRegisterCountError", err)
	}
	if ce.Actual != 0xFFFF {
		t.Errorf("Actual = %d, want 65535 (saturated)", ce.Actual)
	}
}

func TestValidateByteSpec_LengthDoesNotWrap(t *testing.T) {
	err := ValidateByteSpec(ByteSpec{Count: 4}, make([]byte, 4+lengthWrap), "test")
	var ce *CodecByteCountError
	if !errors.As(err, &ce) {
		t.Fatalf("4+65536 bytes for a 4-byte spec: got %v, want *CodecByteCountError", err)
	}
	if ce.Actual != 0xFFFF {
		t.Errorf("Actual = %d, want 65535 (saturated)", ce.Actual)
	}
}

func TestBytesCodecs_EncodeLengthDoesNotWrap(t *testing.T) {
	bc, err := NewBytesCodec(4)
	if err != nil {
		t.Fatal(err)
	}
	if regs, err := bc.EncodeRegisters(make([]byte, 4+lengthWrap)); err == nil {
		t.Errorf("bytes codec accepted 4+65536 bytes and returned %d registers, want 2 or an error", len(regs))
	}
	uc, err := NewUint8SliceCodec(4)
	if err != nil {
		t.Fatal(err)
	}
	if regs, err := uc.EncodeRegisters(make([]uint8, 4+lengthWrap)); err == nil {
		t.Errorf("uint8 slice codec accepted 4+65536 bytes and returned %d registers, want 2 or an error", len(regs))
	}
}

func TestPermuteBytes_LengthDoesNotWrap(t *testing.T) {
	if _, err := PermuteBytesDecode(make([]byte, 4+lengthWrap), Layout32_4321); !errors.Is(err, ErrEncodingError) {
		t.Errorf("PermuteBytesDecode with 4+65536 bytes: got %v, want ErrEncodingError", err)
	}
	if _, err := PermuteBytesEncode(make([]byte, 4+lengthWrap), Layout32_4321); !errors.Is(err, ErrEncodingError) {
		t.Errorf("PermuteBytesEncode with 4+65536 bytes: got %v, want ErrEncodingError", err)
	}
}
