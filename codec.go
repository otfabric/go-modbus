package modbus

import (
	"errors"
	"fmt"
)

//
// Register and byte shape (v1: fixed-width only)
//

// RegisterSpec declares the fixed register count for a codec. v1 uses only
// fixed width; variable-width forms are a future extension.
type RegisterSpec struct {
	Count uint16
}

// ByteSpec declares the fixed byte count for a codec. Used for discovery and
// validation; transport remains register-native.
type ByteSpec struct {
	Count uint16
}

//
// Codec interfaces (Decoder / Encoder / Codec)
//

// Decoder decodes raw registers into a typed value.
type Decoder[T any] interface {
	ID() string
	Name() string
	RegisterSpec() RegisterSpec
	ByteSpec() ByteSpec
	DecodeRegisters(regs []uint16) (T, error)
}

// Encoder encodes a typed value into raw registers.
type Encoder[T any] interface {
	ID() string
	Name() string
	RegisterSpec() RegisterSpec
	ByteSpec() ByteSpec
	EncodeRegisters(value T) ([]uint16, error)
}

// Codec is a combined decoder and encoder for type T. Transport uses Codec for
// ReadWithCodec / WriteWithCodec; internally Decoder and Encoder allow
// read-only or write-only use later if needed.
type Codec[T any] interface {
	Decoder[T]
	Encoder[T]
}

//
// Sentinel errors
//

var (
	ErrCodecRegisterCount = errors.New("modbus: codec register count mismatch")
	ErrCodecLayout        = errors.New("modbus: invalid codec layout")
	ErrCodecValue         = errors.New("modbus: invalid codec value")
	ErrCodecByteCount     = errors.New("modbus: codec byte count mismatch")
	ErrUnknownCodec       = errors.New("modbus: unknown codec")
	ErrEncodingError      = errors.New("modbus: encoding error")
)

//
// Typed errors (use errors.As for diagnostics)
//

// CodecRegisterCountError is returned when the number of registers does not
// match the codec contract.
type CodecRegisterCountError struct {
	Codec    string
	Expected RegisterSpec
	Actual   uint16
}

func (e *CodecRegisterCountError) Error() string {
	return fmt.Sprintf("modbus: codec register count mismatch: %s expected %d registers, got %d", e.Codec, e.Expected.Count, e.Actual)
}

func (e *CodecRegisterCountError) Unwrap() error { return ErrCodecRegisterCount }

// CodecLayoutError is returned when a layout is invalid for the codec.
type CodecLayoutError struct {
	Codec  string
	Layout RegisterLayout
	Reason string
}

func (e *CodecLayoutError) Error() string {
	return "modbus: invalid codec layout: " + e.Codec + " layout=" + e.Layout.String() + ": " + e.Reason
}

func (e *CodecLayoutError) Unwrap() error { return ErrCodecLayout }

// CodecByteCountError is returned when the number of bytes does not match the
// codec contract. Used by ValidateByteSpec for offline/byte-oriented tooling.
type CodecByteCountError struct {
	Codec    string
	Expected ByteSpec
	Actual   uint16
}

func (e *CodecByteCountError) Error() string {
	return fmt.Sprintf("modbus: codec byte count mismatch: %s expected %d bytes, got %d", e.Codec, e.Expected.Count, e.Actual)
}

func (e *CodecByteCountError) Unwrap() error { return ErrCodecByteCount }

// CodecValueError is returned when a value is invalid for encode or decode.
type CodecValueError struct {
	Codec  string
	Reason string
}

func (e *CodecValueError) Error() string {
	return "modbus: invalid codec value: " + e.Codec + ": " + e.Reason
}

func (e *CodecValueError) Unwrap() error { return ErrCodecValue }

//
// Generic decode/encode helpers
//

// DecodeRegisters decodes raw registers using the given codec. It validates
// register count against the codec's RegisterSpec before calling the codec.
func DecodeRegisters[T any](regs []uint16, codec Decoder[T]) (T, error) {
	var zero T
	if codec == nil {
		return zero, &CodecValueError{Codec: "codec", Reason: "codec must not be nil"}
	}
	spec := codec.RegisterSpec()
	if err := ValidateRegisterSpec(spec, regs, codec.ID()); err != nil {
		return zero, err
	}
	return codec.DecodeRegisters(regs)
}

// EncodeRegisters encodes a value to raw registers using the given codec, then
// validates the result against the codec's RegisterSpec.
func EncodeRegisters[T any](value T, codec Encoder[T]) ([]uint16, error) {
	if codec == nil {
		return nil, &CodecValueError{Codec: "codec", Reason: "codec must not be nil"}
	}
	regs, err := codec.EncodeRegisters(value)
	if err != nil {
		return nil, err
	}
	if err := ValidateRegisterSpec(codec.RegisterSpec(), regs, codec.ID()); err != nil {
		return nil, err
	}
	return regs, nil
}
