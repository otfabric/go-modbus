package modbus

import "context"

// ReadWithCodec reads registers at the given address and decodes them using the
// supplied codec. The number of registers read is codec.RegisterSpec().Count.
// Register data is read in wire order (big-endian per register); the codec
// applies any layout or interpretation. Does not use SetEncoding.
func ReadWithCodec[T any](
	mc *ModbusClient,
	ctx context.Context,
	unitID uint8,
	addr uint16,
	regType RegType,
	codec Decoder[T],
) (T, error) {
	var zero T
	if codec == nil {
		return zero, &CodecValueError{Codec: "codec", Reason: "codec must not be nil"}
	}
	spec := codec.RegisterSpec()
	if spec.Count == 0 {
		return zero, &CodecRegisterCountError{Codec: codec.ID(), Expected: spec, Actual: 0}
	}
	regs, err := mc.readRegistersForCodec(ctx, unitID, addr, spec.Count, regType)
	if err != nil {
		return zero, err
	}
	return DecodeRegisters(regs, codec)
}

// WriteWithCodec encodes value with the codec and writes the resulting registers
// at the given address. Register data is written in wire order (big-endian per
// register). Does not use SetEncoding.
func WriteWithCodec[T any](
	mc *ModbusClient,
	ctx context.Context,
	unitID uint8,
	addr uint16,
	value T,
	codec Encoder[T],
) error {
	if codec == nil {
		return &CodecValueError{Codec: "codec", Reason: "codec must not be nil"}
	}
	regs, err := EncodeRegisters(value, codec)
	if err != nil {
		return err
	}
	return mc.writeRegistersForCodec(ctx, unitID, addr, regs)
}

// ReadUint32WithLayout reads two registers at addr and decodes them as uint32
// using the given layout. Convenience wrapper around NewUint32Codec + ReadWithCodec.
func ReadUint32WithLayout(mc *ModbusClient, ctx context.Context, unitID uint8, addr uint16, regType RegType, layout RegisterLayout) (uint32, error) {
	codec, err := NewUint32Codec(layout)
	if err != nil {
		return 0, err
	}
	return ReadWithCodec(mc, ctx, unitID, addr, regType, codec)
}

// WriteUint32WithLayout encodes v as two registers using the given layout and
// writes them at addr. Convenience wrapper around NewUint32Codec + WriteWithCodec.
func WriteUint32WithLayout(mc *ModbusClient, ctx context.Context, unitID uint8, addr uint16, v uint32, layout RegisterLayout) error {
	codec, err := NewUint32Codec(layout)
	if err != nil {
		return err
	}
	return WriteWithCodec(mc, ctx, unitID, addr, v, codec)
}
