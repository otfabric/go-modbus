// SPDX-License-Identifier: MIT

package codec

import (
	"errors"
	"testing"
)

func TestValidateRegisterSpec_OK(t *testing.T) {
	err := ValidateRegisterSpec(RegisterSpec{Count: 2}, []uint16{0x1234, 0x5678}, "")
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestValidateRegisterSpec_Mismatch(t *testing.T) {
	err := ValidateRegisterSpec(RegisterSpec{Count: 2}, []uint16{0x1234}, "test-codec")
	if err == nil {
		t.Fatal("expected error")
	}
	var e *CodecRegisterCountError
	if !errors.As(err, &e) {
		t.Errorf("expected CodecRegisterCountError, got %T", err)
	}
	if !errors.Is(err, ErrCodecRegisterCount) {
		t.Errorf("expected ErrCodecRegisterCount, got %v", err)
	}
	if e.Codec != "test-codec" || e.Actual != 1 || e.Expected.Count != 2 {
		t.Errorf("Codec=%q Expected.Count=%d Actual=%d", e.Codec, e.Expected.Count, e.Actual)
	}
}

func TestValidateByteSpec_OK(t *testing.T) {
	err := ValidateByteSpec(ByteSpec{Count: 4}, []byte{1, 2, 3, 4}, "")
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestValidateByteSpec_Mismatch(t *testing.T) {
	err := ValidateByteSpec(ByteSpec{Count: 4}, []byte{1, 2, 3}, "test-byte-codec")
	if err == nil {
		t.Fatal("expected error")
	}
	var e *CodecByteCountError
	if !errors.As(err, &e) {
		t.Errorf("expected CodecByteCountError, got %T", err)
	}
	if !errors.Is(err, ErrCodecByteCount) {
		t.Errorf("expected ErrCodecByteCount, got %v", err)
	}
	if e.Codec != "test-byte-codec" || e.Actual != 3 || e.Expected.Count != 4 {
		t.Errorf("Codec=%q Expected.Count=%d Actual=%d", e.Codec, e.Expected.Count, e.Actual)
	}
}
