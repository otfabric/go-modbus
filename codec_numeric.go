package modbus

import (
	"encoding/binary"
	"fmt"
	"math"
)

// uint16Codec encodes/decodes a single 16-bit register with optional byte permutation.
type uint16Codec struct{ layout RegisterLayout }

func (c uint16Codec) ID() string                 { return "uint16/layout:" + c.layout.String() }
func (c uint16Codec) Name() string               { return "uint16" }
func (c uint16Codec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 1} }
func (c uint16Codec) ByteSpec() ByteSpec         { return ByteSpec{Count: 2} }

func (c uint16Codec) DecodeRegisters(regs []uint16) (uint16, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return 0, err
	}
	raw := uint16sToBytes(BigEndian, regs)
	canonical, err := PermuteBytesDecode(raw, c.layout)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(canonical), nil
}

func (c uint16Codec) EncodeRegisters(v uint16) ([]uint16, error) {
	canonical := make([]byte, 2)
	binary.BigEndian.PutUint16(canonical, v)
	raw, err := PermuteBytesEncode(canonical, c.layout)
	if err != nil {
		return nil, err
	}
	return bytesToUint16s(BigEndian, raw), nil
}

// int16Codec encodes/decodes a single 16-bit register as signed.
type int16Codec struct{ layout RegisterLayout }

func (c int16Codec) ID() string                 { return "int16/layout:" + c.layout.String() }
func (c int16Codec) Name() string               { return "int16" }
func (c int16Codec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 1} }
func (c int16Codec) ByteSpec() ByteSpec         { return ByteSpec{Count: 2} }

func (c int16Codec) DecodeRegisters(regs []uint16) (int16, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return 0, err
	}
	raw := uint16sToBytes(BigEndian, regs)
	canonical, err := PermuteBytesDecode(raw, c.layout)
	if err != nil {
		return 0, err
	}
	return int16(binary.BigEndian.Uint16(canonical)), nil
}

func (c int16Codec) EncodeRegisters(v int16) ([]uint16, error) {
	canonical := make([]byte, 2)
	binary.BigEndian.PutUint16(canonical, uint16(v))
	raw, err := PermuteBytesEncode(canonical, c.layout)
	if err != nil {
		return nil, err
	}
	return bytesToUint16s(BigEndian, raw), nil
}

// int16SignMagnitudeCodec: one register, bit 15 = sign (1 = negative), bits 0–14 = magnitude. Not two's complement.
type int16SignMagnitudeCodec struct{}

func (c int16SignMagnitudeCodec) ID() string                 { return "int16_sign_magnitude" }
func (c int16SignMagnitudeCodec) Name() string               { return "int16_sign_magnitude" }
func (c int16SignMagnitudeCodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 1} }
func (c int16SignMagnitudeCodec) ByteSpec() ByteSpec         { return ByteSpec{Count: 2} }

func (c int16SignMagnitudeCodec) DecodeRegisters(regs []uint16) (int16, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return 0, err
	}
	u := regs[0]
	mag := int16(u & 0x7FFF)
	if u&0x8000 != 0 {
		return -mag, nil
	}
	return mag, nil
}

func (c int16SignMagnitudeCodec) EncodeRegisters(v int16) ([]uint16, error) {
	mag := uint16(v)
	if v < 0 {
		mag = uint16(-v)
	}
	if mag > 0x7FFF {
		return nil, &CodecValueError{Codec: c.ID(), Reason: "magnitude exceeds 15 bits (max 32767)"}
	}
	if v < 0 {
		mag |= 0x8000
	}
	return []uint16{mag}, nil
}

// uint32Codec encodes/decodes two registers as uint32.
type uint32Codec struct{ layout RegisterLayout }

func (c uint32Codec) ID() string                 { return "uint32/layout:" + c.layout.String() }
func (c uint32Codec) Name() string               { return "uint32" }
func (c uint32Codec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 2} }
func (c uint32Codec) ByteSpec() ByteSpec         { return ByteSpec{Count: 4} }

func (c uint32Codec) DecodeRegisters(regs []uint16) (uint32, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return 0, err
	}
	raw := uint16sToBytes(BigEndian, regs)
	canonical, err := PermuteBytesDecode(raw, c.layout)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(canonical), nil
}

func (c uint32Codec) EncodeRegisters(v uint32) ([]uint16, error) {
	canonical := make([]byte, 4)
	binary.BigEndian.PutUint32(canonical, v)
	raw, err := PermuteBytesEncode(canonical, c.layout)
	if err != nil {
		return nil, err
	}
	return bytesToUint16s(BigEndian, raw), nil
}

// int32Codec encodes/decodes two registers as int32.
type int32Codec struct{ layout RegisterLayout }

func (c int32Codec) ID() string                 { return "int32/layout:" + c.layout.String() }
func (c int32Codec) Name() string               { return "int32" }
func (c int32Codec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 2} }
func (c int32Codec) ByteSpec() ByteSpec         { return ByteSpec{Count: 4} }

func (c int32Codec) DecodeRegisters(regs []uint16) (int32, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return 0, err
	}
	raw := uint16sToBytes(BigEndian, regs)
	canonical, err := PermuteBytesDecode(raw, c.layout)
	if err != nil {
		return 0, err
	}
	return int32(binary.BigEndian.Uint32(canonical)), nil
}

func (c int32Codec) EncodeRegisters(v int32) ([]uint16, error) {
	canonical := make([]byte, 4)
	binary.BigEndian.PutUint32(canonical, uint32(v))
	raw, err := PermuteBytesEncode(canonical, c.layout)
	if err != nil {
		return nil, err
	}
	return bytesToUint16s(BigEndian, raw), nil
}

// uint48Codec encodes/decodes three registers as 48-bit value in uint64.
type uint48Codec struct{ layout RegisterLayout }

func (c uint48Codec) ID() string                 { return "uint48/layout:" + c.layout.String() }
func (c uint48Codec) Name() string               { return "uint48" }
func (c uint48Codec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 3} }
func (c uint48Codec) ByteSpec() ByteSpec         { return ByteSpec{Count: 6} }

func canonicalToUint48(canonical []byte) uint64 {
	return uint64(canonical[0])<<40 | uint64(canonical[1])<<32 |
		uint64(canonical[2])<<24 | uint64(canonical[3])<<16 |
		uint64(canonical[4])<<8 | uint64(canonical[5])
}

func uint48ToCanonical(v uint64) []byte {
	v = v & 0xFFFFFFFFFFFF
	return []byte{
		byte(v >> 40), byte(v >> 32), byte(v >> 24),
		byte(v >> 16), byte(v >> 8), byte(v),
	}
}

func (c uint48Codec) DecodeRegisters(regs []uint16) (uint64, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return 0, err
	}
	raw := uint16sToBytes(BigEndian, regs)
	canonical, err := PermuteBytesDecode(raw, c.layout)
	if err != nil {
		return 0, err
	}
	return canonicalToUint48(canonical), nil
}

func (c uint48Codec) EncodeRegisters(v uint64) ([]uint16, error) {
	canonical := uint48ToCanonical(v)
	raw, err := PermuteBytesEncode(canonical, c.layout)
	if err != nil {
		return nil, err
	}
	return bytesToUint16s(BigEndian, raw), nil
}

// int48Codec encodes/decodes three registers as signed 48-bit in int64.
type int48Codec struct{ layout RegisterLayout }

func (c int48Codec) ID() string                 { return "int48/layout:" + c.layout.String() }
func (c int48Codec) Name() string               { return "int48" }
func (c int48Codec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 3} }
func (c int48Codec) ByteSpec() ByteSpec         { return ByteSpec{Count: 6} }

func (c int48Codec) DecodeRegisters(regs []uint16) (int64, error) {
	u, err := uint48Codec(c).DecodeRegisters(regs)
	if err != nil {
		return 0, err
	}
	if u&(1<<47) != 0 {
		return int64(u | ^uint64(0x0000FFFFFFFFFFFF)), nil
	}
	return int64(u), nil
}

func (c int48Codec) EncodeRegisters(v int64) ([]uint16, error) {
	return uint48Codec(c).EncodeRegisters(uint64(v) & 0xFFFFFFFFFFFF)
}

// uint64Codec encodes/decodes four registers as uint64.
type uint64Codec struct{ layout RegisterLayout }

func (c uint64Codec) ID() string                 { return "uint64/layout:" + c.layout.String() }
func (c uint64Codec) Name() string               { return "uint64" }
func (c uint64Codec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 4} }
func (c uint64Codec) ByteSpec() ByteSpec         { return ByteSpec{Count: 8} }

func (c uint64Codec) DecodeRegisters(regs []uint16) (uint64, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return 0, err
	}
	raw := uint16sToBytes(BigEndian, regs)
	canonical, err := PermuteBytesDecode(raw, c.layout)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(canonical), nil
}

func (c uint64Codec) EncodeRegisters(v uint64) ([]uint16, error) {
	canonical := make([]byte, 8)
	binary.BigEndian.PutUint64(canonical, v)
	raw, err := PermuteBytesEncode(canonical, c.layout)
	if err != nil {
		return nil, err
	}
	return bytesToUint16s(BigEndian, raw), nil
}

// int64Codec encodes/decodes four registers as int64.
type int64Codec struct{ layout RegisterLayout }

func (c int64Codec) ID() string                 { return "int64/layout:" + c.layout.String() }
func (c int64Codec) Name() string               { return "int64" }
func (c int64Codec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 4} }
func (c int64Codec) ByteSpec() ByteSpec         { return ByteSpec{Count: 8} }

func (c int64Codec) DecodeRegisters(regs []uint16) (int64, error) {
	u, err := uint64Codec(c).DecodeRegisters(regs)
	if err != nil {
		return 0, err
	}
	return int64(u), nil
}

func (c int64Codec) EncodeRegisters(v int64) ([]uint16, error) {
	return uint64Codec(c).EncodeRegisters(uint64(v))
}

// float32Codec encodes/decodes two registers as float32.
type float32Codec struct{ layout RegisterLayout }

func (c float32Codec) ID() string                 { return "float32/layout:" + c.layout.String() }
func (c float32Codec) Name() string               { return "float32" }
func (c float32Codec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 2} }
func (c float32Codec) ByteSpec() ByteSpec         { return ByteSpec{Count: 4} }

func (c float32Codec) DecodeRegisters(regs []uint16) (float32, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return 0, err
	}
	raw := uint16sToBytes(BigEndian, regs)
	canonical, err := PermuteBytesDecode(raw, c.layout)
	if err != nil {
		return 0, err
	}
	return math.Float32frombits(binary.BigEndian.Uint32(canonical)), nil
}

func (c float32Codec) EncodeRegisters(v float32) ([]uint16, error) {
	canonical := make([]byte, 4)
	binary.BigEndian.PutUint32(canonical, math.Float32bits(v))
	raw, err := PermuteBytesEncode(canonical, c.layout)
	if err != nil {
		return nil, err
	}
	return bytesToUint16s(BigEndian, raw), nil
}

// float64Codec encodes/decodes four registers as float64.
type float64Codec struct{ layout RegisterLayout }

func (c float64Codec) ID() string                 { return "float64/layout:" + c.layout.String() }
func (c float64Codec) Name() string               { return "float64" }
func (c float64Codec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: 4} }
func (c float64Codec) ByteSpec() ByteSpec         { return ByteSpec{Count: 8} }

func (c float64Codec) DecodeRegisters(regs []uint16) (float64, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return 0, err
	}
	raw := uint16sToBytes(BigEndian, regs)
	canonical, err := PermuteBytesDecode(raw, c.layout)
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(binary.BigEndian.Uint64(canonical)), nil
}

func (c float64Codec) EncodeRegisters(v float64) ([]uint16, error) {
	canonical := make([]byte, 8)
	binary.BigEndian.PutUint64(canonical, math.Float64bits(v))
	raw, err := PermuteBytesEncode(canonical, c.layout)
	if err != nil {
		return nil, err
	}
	return bytesToUint16s(BigEndian, raw), nil
}

// layoutMustMatch validates that layout has the expected register count.
func layoutMustMatch(layout RegisterLayout, want uint16, codecName string) error {
	if layout.RegisterCount() != want {
		return &CodecLayoutError{
			Codec:  codecName,
			Layout: layout,
			Reason: fmt.Sprintf("requires %d register(s), layout has %d", want, layout.RegisterCount()),
		}
	}
	return nil
}

// NewUint16Codec returns a codec for one register. Layout must have RegisterCount() == 1.
func NewUint16Codec(layout RegisterLayout) (Codec[uint16], error) {
	if err := layoutMustMatch(layout, 1, "uint16"); err != nil {
		return nil, err
	}
	return uint16Codec{layout: layout}, nil
}

// MustNewUint16Codec is like NewUint16Codec but panics on error.
func MustNewUint16Codec(layout RegisterLayout) Codec[uint16] {
	c, err := NewUint16Codec(layout)
	if err != nil {
		panic(err)
	}
	return c
}

// NewInt16Codec returns a codec for one register. Layout must have RegisterCount() == 1.
func NewInt16Codec(layout RegisterLayout) (Codec[int16], error) {
	if err := layoutMustMatch(layout, 1, "int16"); err != nil {
		return nil, err
	}
	return int16Codec{layout: layout}, nil
}

func MustNewInt16Codec(layout RegisterLayout) Codec[int16] {
	c, err := NewInt16Codec(layout)
	if err != nil {
		panic(err)
	}
	return c
}

// NewInt16SignMagnitudeCodec returns a codec for one register as sign-magnitude: bit 15 = sign (1 = negative), bits 0–14 = magnitude. Not two's complement.
func NewInt16SignMagnitudeCodec() Codec[int16] {
	return int16SignMagnitudeCodec{}
}

// NewUint32Codec returns a codec for two registers. Layout must have RegisterCount() == 2.
func NewUint32Codec(layout RegisterLayout) (Codec[uint32], error) {
	if err := layoutMustMatch(layout, 2, "uint32"); err != nil {
		return nil, err
	}
	return uint32Codec{layout: layout}, nil
}

func MustNewUint32Codec(layout RegisterLayout) Codec[uint32] {
	c, err := NewUint32Codec(layout)
	if err != nil {
		panic(err)
	}
	return c
}

// NewInt32Codec returns a codec for two registers. Layout must have RegisterCount() == 2.
func NewInt32Codec(layout RegisterLayout) (Codec[int32], error) {
	if err := layoutMustMatch(layout, 2, "int32"); err != nil {
		return nil, err
	}
	return int32Codec{layout: layout}, nil
}

func MustNewInt32Codec(layout RegisterLayout) Codec[int32] {
	c, err := NewInt32Codec(layout)
	if err != nil {
		panic(err)
	}
	return c
}

// NewUint48Codec returns a codec for three registers (48-bit as uint64). Layout must have RegisterCount() == 3.
func NewUint48Codec(layout RegisterLayout) (Codec[uint64], error) {
	if err := layoutMustMatch(layout, 3, "uint48"); err != nil {
		return nil, err
	}
	return uint48Codec{layout: layout}, nil
}

func MustNewUint48Codec(layout RegisterLayout) Codec[uint64] {
	c, err := NewUint48Codec(layout)
	if err != nil {
		panic(err)
	}
	return c
}

// NewInt48Codec returns a codec for three registers (48-bit as int64). Layout must have RegisterCount() == 3.
func NewInt48Codec(layout RegisterLayout) (Codec[int64], error) {
	if err := layoutMustMatch(layout, 3, "int48"); err != nil {
		return nil, err
	}
	return int48Codec{layout: layout}, nil
}

func MustNewInt48Codec(layout RegisterLayout) Codec[int64] {
	c, err := NewInt48Codec(layout)
	if err != nil {
		panic(err)
	}
	return c
}

// NewUint64Codec returns a codec for four registers. Layout must have RegisterCount() == 4.
func NewUint64Codec(layout RegisterLayout) (Codec[uint64], error) {
	if err := layoutMustMatch(layout, 4, "uint64"); err != nil {
		return nil, err
	}
	return uint64Codec{layout: layout}, nil
}

func MustNewUint64Codec(layout RegisterLayout) Codec[uint64] {
	c, err := NewUint64Codec(layout)
	if err != nil {
		panic(err)
	}
	return c
}

// NewInt64Codec returns a codec for four registers. Layout must have RegisterCount() == 4.
func NewInt64Codec(layout RegisterLayout) (Codec[int64], error) {
	if err := layoutMustMatch(layout, 4, "int64"); err != nil {
		return nil, err
	}
	return int64Codec{layout: layout}, nil
}

func MustNewInt64Codec(layout RegisterLayout) Codec[int64] {
	c, err := NewInt64Codec(layout)
	if err != nil {
		panic(err)
	}
	return c
}

// NewFloat32Codec returns a codec for two registers. Layout must have RegisterCount() == 2.
func NewFloat32Codec(layout RegisterLayout) (Codec[float32], error) {
	if err := layoutMustMatch(layout, 2, "float32"); err != nil {
		return nil, err
	}
	return float32Codec{layout: layout}, nil
}

func MustNewFloat32Codec(layout RegisterLayout) Codec[float32] {
	c, err := NewFloat32Codec(layout)
	if err != nil {
		panic(err)
	}
	return c
}

// NewFloat64Codec returns a codec for four registers. Layout must have RegisterCount() == 4.
func NewFloat64Codec(layout RegisterLayout) (Codec[float64], error) {
	if err := layoutMustMatch(layout, 4, "float64"); err != nil {
		return nil, err
	}
	return float64Codec{layout: layout}, nil
}

func MustNewFloat64Codec(layout RegisterLayout) Codec[float64] {
	c, err := NewFloat64Codec(layout)
	if err != nil {
		panic(err)
	}
	return c
}

// numericRegEntry describes one codec type to register for a given layout.
type numericRegEntry struct {
	IDSuffix  string
	Name      string
	Family    CodecFamily
	ValueKind CodecValueKind
}

// registerNumericLayout registers one descriptor per entry for the given layout,
// reducing boilerplate when adding layouts or numeric families.
func registerNumericLayout(layout RegisterLayout, entries []numericRegEntry) {
	regCount := layout.RegisterCount()
	byteCount := regCount * 2
	for _, e := range entries {
		registerCodecDescriptor(CodecDescriptor{
			ID:           e.IDSuffix + "/layout:" + layout.String(),
			Name:         e.Name,
			Family:       e.Family,
			ValueKind:    e.ValueKind,
			RegisterSpec: RegisterSpec{Count: regCount},
			ByteSpec:     ByteSpec{Count: byteCount},
			Layouts:      []RegisterLayoutDescriptor{{Name: layout.String(), Common: true, Layout: layout}},
		})
	}
}

// Register numeric codec descriptors for common layouts (source of truth for discovery).
func init() {
	registerNumericDescriptors()
}

func registerNumericDescriptors() {
	for _, name := range []string{"21", "12"} {
		registerNumericLayout(mustLayoutForName(1, name), []numericRegEntry{
			{"uint16", "uint16", CodecFamilyInteger, CodecValueUint16},
			{"int16", "int16", CodecFamilyInteger, CodecValueInt16},
		})
	}
	for _, name := range []string{"4321", "3412", "2143", "1234"} {
		registerNumericLayout(mustLayoutForName(2, name), []numericRegEntry{
			{"uint32", "uint32", CodecFamilyInteger, CodecValueUint32},
			{"int32", "int32", CodecFamilyInteger, CodecValueInt32},
			{"float32", "float32", CodecFamilyFloat, CodecValueFloat32},
		})
	}
	for _, name := range []string{"654321", "563412", "214365", "123456"} {
		registerNumericLayout(mustLayoutForName(3, name), []numericRegEntry{
			{"uint48", "uint48", CodecFamilyInteger, CodecValueUint48},
			{"int48", "int48", CodecFamilyInteger, CodecValueInt48},
		})
	}
	for _, name := range []string{"87654321", "78563412", "21436587", "12345678"} {
		registerNumericLayout(mustLayoutForName(4, name), []numericRegEntry{
			{"uint64", "uint64", CodecFamilyInteger, CodecValueUint64},
			{"int64", "int64", CodecFamilyInteger, CodecValueInt64},
			{"float64", "float64", CodecFamilyFloat, CodecValueFloat64},
		})
	}
	registerCodecDescriptor(CodecDescriptor{
		ID:           "int16_sign_magnitude",
		Name:         "int16_sign_magnitude",
		Family:       CodecFamilyInteger,
		ValueKind:    CodecValueInt16,
		RegisterSpec: RegisterSpec{Count: 1},
		ByteSpec:     ByteSpec{Count: 2},
		Layouts:      nil,
	})
}

func mustLayoutForName(registerCount uint16, name string) RegisterLayout {
	switch registerCount {
	case 1:
		switch name {
		case "21":
			return Layout16_21
		case "12":
			return Layout16_12
		}
	case 2:
		switch name {
		case "4321":
			return Layout32_4321
		case "3412":
			return Layout32_3412
		case "2143":
			return Layout32_2143
		case "1234":
			return Layout32_1234
		}
	case 3:
		switch name {
		case "654321":
			return Layout48_654321
		case "563412":
			return Layout48_563412
		case "214365":
			return Layout48_214365
		case "123456":
			return Layout48_123456
		}
	case 4:
		switch name {
		case "87654321":
			return Layout64_87654321
		case "78563412":
			return Layout64_78563412
		case "21436587":
			return Layout64_21436587
		case "12345678":
			return Layout64_12345678
		}
	}
	panic("modbus: unknown layout " + name + " for " + fmt.Sprint(registerCount) + " register(s)")
}
