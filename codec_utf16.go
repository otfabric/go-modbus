package modbus

import (
	"fmt"
	"unicode/utf16"
)

// utf16BECodec: fixed-width UTF-16 big-endian. Each register holds one UTF-16 code unit (high byte first).
// Decode returns the decoded string (trailing NUL runes preserved as runes). Encode right-pads with NUL code units.
type utf16BECodec struct{ registerCount uint16 }

func (c utf16BECodec) ID() string                 { return fmt.Sprintf("utf16be/registers:%d", c.registerCount) }
func (c utf16BECodec) Name() string               { return "utf16be" }
func (c utf16BECodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: c.registerCount} }
func (c utf16BECodec) ByteSpec() ByteSpec         { return ByteSpec{Count: c.registerCount * 2} }

func (c utf16BECodec) DecodeRegisters(regs []uint16) (string, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return "", err
	}
	// Wire order is big-endian per register, so regs are already the code units.
	runes := utf16.Decode(regs)
	return string(runes), nil
}

func (c utf16BECodec) EncodeRegisters(s string) ([]uint16, error) {
	runes := []rune(s)
	if len(runes) > int(c.registerCount) {
		runes = runes[:c.registerCount]
	}
	codeUnits := utf16.Encode(runes)
	if len(codeUnits) > int(c.registerCount) {
		codeUnits = codeUnits[:c.registerCount]
	}
	out := make([]uint16, c.registerCount)
	copy(out, codeUnits)
	// Remainder is zero (NUL padding)
	return out, nil
}

// utf16LECodec: fixed-width UTF-16 little-endian. Each register holds one UTF-16 code unit (low byte first on wire).
// Decode returns the decoded string. Encode right-pads with NUL code units.
type utf16LECodec struct{ registerCount uint16 }

func (c utf16LECodec) ID() string                 { return fmt.Sprintf("utf16le/registers:%d", c.registerCount) }
func (c utf16LECodec) Name() string               { return "utf16le" }
func (c utf16LECodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: c.registerCount} }
func (c utf16LECodec) ByteSpec() ByteSpec         { return ByteSpec{Count: c.registerCount * 2} }

func (c utf16LECodec) DecodeRegisters(regs []uint16) (string, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return "", err
	}
	// Wire has low byte first per register: swap each to get code unit.
	codeUnits := make([]uint16, len(regs))
	for i, r := range regs {
		codeUnits[i] = (r >> 8) | (r << 8)
	}
	runes := utf16.Decode(codeUnits)
	return string(runes), nil
}

func (c utf16LECodec) EncodeRegisters(s string) ([]uint16, error) {
	runes := []rune(s)
	if len(runes) > int(c.registerCount) {
		runes = runes[:c.registerCount]
	}
	codeUnits := utf16.Encode(runes)
	out := make([]uint16, c.registerCount)
	for i, u := range codeUnits {
		if i >= int(c.registerCount) {
			break
		}
		out[i] = (u >> 8) | (u << 8)
	}
	return out, nil
}

// NewUTF16BECodec returns a codec for fixed-width UTF-16 big-endian (one code unit per register, high byte first). registerCount must be >= 1.
func NewUTF16BECodec(registerCount uint16) (Codec[string], error) {
	if err := textCodecRejectZeroRegisters(registerCount); err != nil {
		return nil, err
	}
	return utf16BECodec{registerCount: registerCount}, nil
}

// NewUTF16LECodec returns a codec for fixed-width UTF-16 little-endian (one code unit per register, low byte first on wire). registerCount must be >= 1.
func NewUTF16LECodec(registerCount uint16) (Codec[string], error) {
	if err := textCodecRejectZeroRegisters(registerCount); err != nil {
		return nil, err
	}
	return utf16LECodec{registerCount: registerCount}, nil
}

func init() {
	registerUTF16Descriptors()
}

func registerUTF16Descriptors() {
	for _, n := range []uint16{1, 2, 3, 4, 6, 8, 12, 16, 20, 32, 48, 64} {
		registerCodecDescriptor(CodecDescriptor{
			ID:           fmt.Sprintf("utf16be/registers:%d", n),
			Name:         "utf16be",
			Family:       CodecFamilyText,
			ValueKind:    CodecValueString,
			RegisterSpec: RegisterSpec{Count: n},
			ByteSpec:     ByteSpec{Count: n * 2},
			Layouts:      nil,
		})
		registerCodecDescriptor(CodecDescriptor{
			ID:           fmt.Sprintf("utf16le/registers:%d", n),
			Name:         "utf16le",
			Family:       CodecFamilyText,
			ValueKind:    CodecValueString,
			RegisterSpec: RegisterSpec{Count: n},
			ByteSpec:     ByteSpec{Count: n * 2},
			Layouts:      nil,
		})
	}
}
