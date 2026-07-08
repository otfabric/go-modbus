// SPDX-License-Identifier: MIT

package codec

import (
	"context"

	"github.com/otfabric/go-modbus/internal/protocol"
)

// RegType selects holding vs input registers for read operations.
// This is an alias for protocol.RegType, identical to modbus.RegType.
type RegType = protocol.RegType

const (
	HoldingRegister = protocol.HoldingRegister
	InputRegister   = protocol.InputRegister
)

// RegisterReader can read Modbus registers. *modbus.Client satisfies this.
type RegisterReader interface {
	ReadRegisters(ctx context.Context, unitID uint8, addr uint16, quantity uint16, regType RegType) ([]uint16, error)
}

// RegisterWriter can write Modbus registers. *modbus.Client satisfies this.
type RegisterWriter interface {
	WriteRegisters(ctx context.Context, unitID uint8, addr uint16, values []uint16) error
}

// ReadFromClient reads registers at the given address and decodes them using the
// supplied typed codec. The number of registers read is dec.RegisterSpec().Count.
// Register data is read in wire order (big-endian per register); the codec applies
// any layout or interpretation.
func ReadFromClient[T any](
	r RegisterReader,
	ctx context.Context,
	unitID uint8,
	addr uint16,
	regType RegType,
	dec Decoder[T],
) (T, error) {
	var zero T
	if dec == nil {
		return zero, &CodecValueError{Codec: "codec", Reason: "codec must not be nil"}
	}
	spec := dec.RegisterSpec()
	if spec.Count == 0 {
		return zero, &CodecRegisterCountError{Codec: dec.ID(), Expected: spec, Actual: 0}
	}
	regs, err := r.ReadRegisters(ctx, unitID, addr, spec.Count, regType)
	if err != nil {
		return zero, err
	}
	return DecodeRegisters(regs, dec)
}

// WriteToClient encodes value with the codec and writes the resulting registers
// at the given address. Register data is written in wire order (big-endian per
// register).
func WriteToClient[T any](
	w RegisterWriter,
	ctx context.Context,
	unitID uint8,
	addr uint16,
	value T,
	enc Encoder[T],
) error {
	if enc == nil {
		return &CodecValueError{Codec: "codec", Reason: "codec must not be nil"}
	}
	regs, err := EncodeRegisters(value, enc)
	if err != nil {
		return err
	}
	return w.WriteRegisters(ctx, unitID, addr, regs)
}

// ReadRuntimeFromClient reads registers and decodes them using a runtime decoder.
func ReadRuntimeFromClient(
	r RegisterReader,
	ctx context.Context,
	unitID uint8,
	addr uint16,
	regType RegType,
	dec RuntimeDecoder,
) (any, error) {
	if dec == nil {
		return nil, &CodecValueError{Codec: "codec", Reason: "codec must not be nil"}
	}
	spec := dec.RegisterSpec()
	if spec.Count == 0 {
		return nil, &CodecRegisterCountError{Codec: dec.ID(), Expected: spec, Actual: 0}
	}
	regs, err := r.ReadRegisters(ctx, unitID, addr, spec.Count, regType)
	if err != nil {
		return nil, err
	}
	return DecodeRegistersAny(regs, dec)
}

// WriteRuntimeToClient encodes a value with the runtime encoder and writes the
// resulting registers. Wrong value type returns *CodecValueError.
func WriteRuntimeToClient(
	w RegisterWriter,
	ctx context.Context,
	unitID uint8,
	addr uint16,
	value any,
	enc RuntimeEncoder,
) error {
	if enc == nil {
		return &CodecValueError{Codec: "codec", Reason: "codec must not be nil"}
	}
	regs, err := EncodeRegistersAny(value, enc)
	if err != nil {
		return err
	}
	return w.WriteRegisters(ctx, unitID, addr, regs)
}

// DecodeWithDescriptor builds a runtime codec from the descriptor and decodes
// the registers. Offline only (no client needed). Returns an error if the
// descriptor is unknown or if decode fails.
func DecodeWithDescriptor(regs []uint16, desc CodecDescriptor) (any, error) {
	if desc.ID == "" {
		return nil, &CodecValueError{Codec: "codec", Reason: "descriptor must not be zero"}
	}
	rc, err := RuntimeCodecFromDescriptor(desc)
	if err != nil {
		return nil, err
	}
	return DecodeRegistersAny(regs, rc)
}

// EncodeWithDescriptor builds a runtime codec from the descriptor and encodes
// the value to registers. Offline only (no client needed). Returns an error if
// the descriptor is unknown, if the value type does not match the codec, or if
// encode fails.
func EncodeWithDescriptor(value any, desc CodecDescriptor) ([]uint16, error) {
	if desc.ID == "" {
		return nil, &CodecValueError{Codec: "codec", Reason: "descriptor must not be zero"}
	}
	rc, err := RuntimeCodecFromDescriptor(desc)
	if err != nil {
		return nil, err
	}
	return EncodeRegistersAny(value, rc)
}

// ReadUint32FromClient reads two registers at addr and decodes them as uint32
// using the given layout. Convenience wrapper around NewUint32Codec + ReadFromClient.
func ReadUint32FromClient(r RegisterReader, ctx context.Context, unitID uint8, addr uint16, regType RegType, layout RegisterLayout) (uint32, error) {
	c, err := NewUint32Codec(layout)
	if err != nil {
		return 0, err
	}
	return ReadFromClient(r, ctx, unitID, addr, regType, c)
}

// WriteUint32ToClient encodes v as two registers using the given layout and
// writes them at addr. Convenience wrapper around NewUint32Codec + WriteToClient.
func WriteUint32ToClient(w RegisterWriter, ctx context.Context, unitID uint8, addr uint16, v uint32, layout RegisterLayout) error {
	c, err := NewUint32Codec(layout)
	if err != nil {
		return err
	}
	return WriteToClient(w, ctx, unitID, addr, v, c)
}
