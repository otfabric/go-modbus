package codec

import (
	"errors"
	"testing"
)

// minimalUint16Codec is a test codec: one register, no permutation.
type minimalUint16Codec struct{}

func (minimalUint16Codec) ID() string   { return "uint16/test" }
func (minimalUint16Codec) Name() string { return "uint16" }
func (minimalUint16Codec) RegisterSpec() RegisterSpec {
	return RegisterSpec{Count: 1}
}
func (minimalUint16Codec) ByteSpec() ByteSpec {
	return ByteSpec{Count: 2}
}
func (c minimalUint16Codec) DecodeRegisters(regs []uint16) (uint16, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return 0, err
	}
	return regs[0], nil
}
func (c minimalUint16Codec) EncodeRegisters(v uint16) ([]uint16, error) {
	return []uint16{v}, nil
}

func TestDecodeRegisters_OK(t *testing.T) {
	codec := minimalUint16Codec{}
	v, err := DecodeRegisters([]uint16{0x1234}, codec)
	if err != nil {
		t.Fatal(err)
	}
	if v != 0x1234 {
		t.Errorf("got 0x%x, want 0x1234", v)
	}
}

func TestDecodeRegisters_WrongCount(t *testing.T) {
	codec := minimalUint16Codec{}
	_, err := DecodeRegisters([]uint16{0x12, 0x34}, codec)
	if err == nil {
		t.Fatal("expected error for wrong register count")
	}
	var e *CodecRegisterCountError
	if !errors.As(err, &e) {
		t.Errorf("expected CodecRegisterCountError, got %T", err)
	}
	// Cover Error() and Unwrap()
	if e.Error() == "" {
		t.Error("Error() should return non-empty string")
	}
	if !errors.Is(err, ErrCodecRegisterCount) {
		t.Error("Unwrap should be ErrCodecRegisterCount")
	}
}

func TestEncodeRegisters_OK(t *testing.T) {
	codec := minimalUint16Codec{}
	regs, err := EncodeRegisters(uint16(0x5678), codec)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 || regs[0] != 0x5678 {
		t.Errorf("got %v, want [0x5678]", regs)
	}
}

type badEncoderCodec struct{}

func (badEncoderCodec) ID() string                 { return "bad/encoder" }
func (badEncoderCodec) Name() string               { return "bad" }
func (badEncoderCodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 1} }
func (badEncoderCodec) ByteSpec() ByteSpec         { return ByteSpec{Count: 2} }
func (badEncoderCodec) DecodeRegisters([]uint16) (uint16, error) {
	return 0, nil
}
func (badEncoderCodec) EncodeRegisters(uint16) ([]uint16, error) {
	return []uint16{0x11, 0x22}, nil // wrong count: 2 instead of 1
}

func TestEncodeRegisters_ValidatesCodecOutput(t *testing.T) {
	c := badEncoderCodec{}
	_, err := EncodeRegisters(uint16(0), c)
	if err == nil {
		t.Fatal("expected error when codec returns wrong register count")
	}
	if !errors.Is(err, ErrCodecRegisterCount) {
		t.Errorf("want ErrCodecRegisterCount, got %v", err)
	}
}

func TestEncodeRegisters_Validation(t *testing.T) {
	// EncodeRegisters validates the codec's output; a buggy codec that returns wrong count would be caught
	// Here we just ensure EncodeRegisters returns what the codec returns when valid
	codec := minimalUint16Codec{}
	regs, err := EncodeRegisters(uint16(0), codec)
	if err != nil {
		t.Fatal(err)
	}
	if len(regs) != 1 {
		t.Errorf("len(regs) = %d, want 1", len(regs))
	}
}

func TestCodecErrorTypes_ErrorAndUnwrap(t *testing.T) {
	// CodecLayoutError
	layout, err := NewRegisterLayout(2, 4, 3, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	e := &CodecLayoutError{Codec: "test", Layout: layout, Reason: "bad"}
	if e.Error() == "" {
		t.Error("CodecLayoutError.Error() empty")
	}
	if e.Unwrap() != ErrCodecLayout {
		t.Error("CodecLayoutError.Unwrap() want ErrCodecLayout")
	}
	// CodecByteCountError
	e2 := &CodecByteCountError{Codec: "bytes", Expected: ByteSpec{Count: 4}, Actual: 3}
	if e2.Error() == "" {
		t.Error("CodecByteCountError.Error() empty")
	}
	if e2.Unwrap() != ErrCodecByteCount {
		t.Error("CodecByteCountError.Unwrap() want ErrCodecByteCount")
	}
	// CodecValueError
	e3 := &CodecValueError{Codec: "ascii", Reason: "non-ASCII"}
	if e3.Error() == "" {
		t.Error("CodecValueError.Error() empty")
	}
	if e3.Unwrap() != ErrCodecValue {
		t.Error("CodecValueError.Unwrap() want ErrCodecValue")
	}
}
