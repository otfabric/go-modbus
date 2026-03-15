package modbus

import "context"

// ReadWithRuntimeCodec reads registers at the given address and decodes them
// using the supplied runtime decoder. The number of registers read is
// codec.RegisterSpec().Count. Register data is read in wire order (big-endian
// per register). Does not use SetEncoding.
func ReadWithRuntimeCodec(
	mc *ModbusClient,
	ctx context.Context,
	unitID uint8,
	addr uint16,
	regType RegType,
	codec RuntimeDecoder,
) (any, error) {
	if codec == nil {
		return nil, &CodecValueError{Codec: "codec", Reason: "codec must not be nil"}
	}
	spec := codec.RegisterSpec()
	if spec.Count == 0 {
		return nil, &CodecRegisterCountError{Codec: codec.ID(), Expected: spec, Actual: 0}
	}
	regs, err := mc.readRegistersForCodec(ctx, unitID, addr, spec.Count, regType)
	if err != nil {
		return nil, err
	}
	return DecodeRegistersAny(regs, codec)
}

// WriteWithRuntimeCodec encodes value with the runtime encoder and writes the
// resulting registers at the given address. Register data is written in wire
// order (big-endian per register). Wrong value type returns *CodecValueError.
// Does not use SetEncoding.
func WriteWithRuntimeCodec(
	mc *ModbusClient,
	ctx context.Context,
	unitID uint8,
	addr uint16,
	value any,
	codec RuntimeEncoder,
) error {
	if codec == nil {
		return &CodecValueError{Codec: "codec", Reason: "codec must not be nil"}
	}
	regs, err := EncodeRegistersAny(value, codec)
	if err != nil {
		return err
	}
	return mc.writeRegistersForCodec(ctx, unitID, addr, regs)
}

// DecodeWithDescriptor builds a runtime codec from the descriptor and decodes
// the registers. Offline only (no client). Returns an error if the descriptor
// is unknown or if decode fails.
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
// the value to registers. Offline only (no client). Returns an error if the
// descriptor is unknown, if the value type does not match the codec, or if
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
