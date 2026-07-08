// SPDX-License-Identifier: MIT

package codec

import "fmt"

// Text codec behavioral contract:
//
// ASCII codecs (AsciiCodec, AsciiFixedCodec, AsciiReverseCodec):
//   - Decode: AsciiCodec/AsciiReverseCodec trim trailing spaces; AsciiFixedCodec preserves all bytes.
//   - Encode: The entire input string is validated for ASCII (bytes 0x00-0x7f); any non-ASCII byte
//     causes rejection. Overlong input is truncated to the codec width before encoding.
//   - Padding: AsciiCodec/AsciiReverseCodec right-pad with space to fixed length; AsciiFixedCodec right-pads with NUL.
//
// BCD codecs (BCDCodec, PackedBCDCodec):
//   - Decode: Return digit string (0-9). Encode: digits only; non-digits rejected. Pad with leading zeros to fixed length.
//   - Truncation: If input has more digits than the codec width, the rightmost (least-significant) digits are kept;
//     leading digits are dropped. This is a semantic choice: e.g. "12345" with width 4 becomes "2345".

func textCodecRejectZeroRegisters(registerCount uint16) error {
	if registerCount == 0 {
		return fmt.Errorf("%w: register count must be >= 1", ErrCodecValue)
	}
	return nil
}

// validateASCII returns an error if any byte in the entire input s is > 0x7f.
// The full string is validated so that non-ASCII in overlong input is rejected
// rather than silently truncated.
func validateASCII(s string, codecID string) error {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7f {
			return &CodecValueError{Codec: codecID, Reason: "ASCII codec accepts only bytes 0x00-0x7f"}
		}
	}
	return nil
}

// asciiCodec: high byte first per register; decode trims trailing spaces; encode right-pads with space.
type asciiCodec struct{ registerCount uint16 }

func (c asciiCodec) ID() string                 { return fmt.Sprintf("ascii/registers:%d", c.registerCount) }
func (c asciiCodec) Name() string               { return "ascii" }
func (c asciiCodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: c.registerCount} }
func (c asciiCodec) ByteSpec() ByteSpec         { return ByteSpec{Count: c.registerCount * 2} }

func (c asciiCodec) DecodeRegisters(regs []uint16) (string, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return "", err
	}
	raw := uint16sToBytes(BigEndian, regs)
	return bytesToAscii(raw), nil
}

func (c asciiCodec) EncodeRegisters(s string) ([]uint16, error) {
	byteCount := c.registerCount * 2
	if err := validateASCII(s, c.ID()); err != nil {
		return nil, err
	}
	b := make([]byte, byteCount)
	copy(b, s)
	for i := len(s); i < len(b); i++ {
		b[i] = ' '
	}
	return bytesToUint16s(BigEndian, b), nil
}

// asciiFixedCodec: same byte order as asciiCodec; decode preserves all bytes (no trim); encode right-pads with NUL.
type asciiFixedCodec struct{ registerCount uint16 }

func (c asciiFixedCodec) ID() string                 { return fmt.Sprintf("ascii_fixed/registers:%d", c.registerCount) }
func (c asciiFixedCodec) Name() string               { return "ascii_fixed" }
func (c asciiFixedCodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: c.registerCount} }
func (c asciiFixedCodec) ByteSpec() ByteSpec         { return ByteSpec{Count: c.registerCount * 2} }

func (c asciiFixedCodec) DecodeRegisters(regs []uint16) (string, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return "", err
	}
	raw := uint16sToBytes(BigEndian, regs)
	return string(raw), nil
}

func (c asciiFixedCodec) EncodeRegisters(s string) ([]uint16, error) {
	byteCount := c.registerCount * 2
	if err := validateASCII(s, c.ID()); err != nil {
		return nil, err
	}
	b := make([]byte, byteCount)
	copy(b, s)
	return bytesToUint16s(BigEndian, b), nil
}

// asciiReverseCodec: low byte first per register; decode trims trailing spaces; encode right-pads with space.
type asciiReverseCodec struct{ registerCount uint16 }

func (c asciiReverseCodec) ID() string {
	return fmt.Sprintf("ascii_reverse/registers:%d", c.registerCount)
}
func (c asciiReverseCodec) Name() string               { return "ascii_reverse" }
func (c asciiReverseCodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: c.registerCount} }
func (c asciiReverseCodec) ByteSpec() ByteSpec         { return ByteSpec{Count: c.registerCount * 2} }

func (c asciiReverseCodec) DecodeRegisters(regs []uint16) (string, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return "", err
	}
	raw := uint16sToBytes(BigEndian, regs)
	return bytesToAsciiReverse(raw), nil
}

func (c asciiReverseCodec) EncodeRegisters(s string) ([]uint16, error) {
	byteCount := c.registerCount * 2
	if err := validateASCII(s, c.ID()); err != nil {
		return nil, err
	}
	b := make([]byte, byteCount)
	copy(b, s)
	for i := len(s); i < len(b); i++ {
		b[i] = ' '
	}
	asciiToBytesReverseInPlace(b)
	return bytesToUint16s(BigEndian, b), nil
}

// asciiToBytesReverseInPlace reverses byte order within each 16-bit word in b (must be even length).
func asciiToBytesReverseInPlace(b []byte) {
	for i := 0; i+1 < len(b); i += 2 {
		b[i], b[i+1] = b[i+1], b[i]
	}
}

// bcdCodec: one byte per digit (0-9). Decode returns digit string; encode accepts digits only, pads with leading zeros.
type bcdCodec struct{ registerCount uint16 }

func (c bcdCodec) ID() string                 { return fmt.Sprintf("bcd/registers:%d", c.registerCount) }
func (c bcdCodec) Name() string               { return "bcd" }
func (c bcdCodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: c.registerCount} }
func (c bcdCodec) ByteSpec() ByteSpec         { return ByteSpec{Count: c.registerCount * 2} }

func (c bcdCodec) DecodeRegisters(regs []uint16) (string, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return "", err
	}
	raw := uint16sToBytes(BigEndian, regs)
	for i, b := range raw {
		if b > 9 {
			return "", &CodecValueError{Codec: c.ID(), Reason: fmt.Sprintf("invalid BCD byte at index %d: value %d not in 0-9", i, b)}
		}
	}
	return bytesToBCD(raw), nil
}

func (c bcdCodec) EncodeRegisters(s string) ([]uint16, error) {
	digitCount := int(c.registerCount * 2)
	for _, r := range s {
		if r < '0' || r > '9' {
			return nil, &CodecValueError{Codec: c.ID(), Reason: "BCD string must contain only digits 0-9"}
		}
	}
	if len(s) > digitCount {
		s = s[len(s)-digitCount:]
	}
	b, err := bcdToBytes(padLeadingZeros(s, digitCount))
	if err != nil {
		return nil, err
	}
	return bytesToUint16s(BigEndian, b), nil
}

// packedBCDCodec: two digits per byte (nibbles). Decode returns digit string; encode accepts digits only, pads with leading zeros.
type packedBCDCodec struct{ registerCount uint16 }

func (c packedBCDCodec) ID() string                 { return fmt.Sprintf("packed_bcd/registers:%d", c.registerCount) }
func (c packedBCDCodec) Name() string               { return "packed_bcd" }
func (c packedBCDCodec) RegisterSpec() RegisterSpec { return RegisterSpec{Count: c.registerCount} }
func (c packedBCDCodec) ByteSpec() ByteSpec         { return ByteSpec{Count: c.registerCount * 2} }

func (c packedBCDCodec) DecodeRegisters(regs []uint16) (string, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return "", err
	}
	raw := uint16sToBytes(BigEndian, regs)
	for i, b := range raw {
		if b>>4 > 9 || b&0x0f > 9 {
			return "", &CodecValueError{Codec: c.ID(), Reason: fmt.Sprintf("invalid packed BCD byte at index %d: nibble(s) not in 0-9", i)}
		}
	}
	return bytesToPackedBCD(raw), nil
}

func (c packedBCDCodec) EncodeRegisters(s string) ([]uint16, error) {
	digitCount := int(c.registerCount * 4)
	for _, r := range s {
		if r < '0' || r > '9' {
			return nil, &CodecValueError{Codec: c.ID(), Reason: "packed BCD string must contain only digits 0-9"}
		}
	}
	if len(s) > digitCount {
		s = s[len(s)-digitCount:]
	}
	s = padLeadingZeros(s, digitCount)
	b, err := packedBCDToBytes(s)
	if err != nil {
		return nil, err
	}
	return bytesToUint16s(BigEndian, b), nil
}

// signedPackedBCDCodec: packed BCD with trailing-nibble sign. Last nibble 0xC/0xD/0xF = negative; 0x0-0x9 = positive (and is the last digit).
// Decode returns signed digit string (e.g. "-1234" or "1234"). Encode accepts optional leading "-" and digits only; encode uses 0xC for negative.
type signedPackedBCDCodec struct{ registerCount uint16 }

func (c signedPackedBCDCodec) ID() string {
	return fmt.Sprintf("signed_packed_bcd/registers:%d", c.registerCount)
}
func (c signedPackedBCDCodec) Name() string { return "signed_packed_bcd" }
func (c signedPackedBCDCodec) RegisterSpec() RegisterSpec {
	return RegisterSpec{Count: c.registerCount}
}
func (c signedPackedBCDCodec) ByteSpec() ByteSpec { return ByteSpec{Count: c.registerCount * 2} }

func (c signedPackedBCDCodec) DecodeRegisters(regs []uint16) (string, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return "", err
	}
	raw := uint16sToBytes(BigEndian, regs)
	s, err := bytesToSignedPackedBCD(raw)
	if err != nil {
		return "", &CodecValueError{Codec: c.ID(), Reason: err.Error()}
	}
	return s, nil
}

func (c signedPackedBCDCodec) EncodeRegisters(s string) ([]uint16, error) {
	totalNibbles := int(c.registerCount * 4)
	b, err := signedPackedBCDToBytes(s, totalNibbles)
	if err != nil {
		return nil, &CodecValueError{Codec: c.ID(), Reason: err.Error()}
	}
	return bytesToUint16s(BigEndian, b), nil
}

// packedBCDReverseCodec: packed BCD with low byte first per register (byte-swap within each word on wire).
type packedBCDReverseCodec struct{ registerCount uint16 }

func (c packedBCDReverseCodec) ID() string {
	return fmt.Sprintf("packed_bcd_reverse/registers:%d", c.registerCount)
}
func (c packedBCDReverseCodec) Name() string { return "packed_bcd_reverse" }
func (c packedBCDReverseCodec) RegisterSpec() RegisterSpec {
	return RegisterSpec{Count: c.registerCount}
}
func (c packedBCDReverseCodec) ByteSpec() ByteSpec { return ByteSpec{Count: c.registerCount * 2} }

func (c packedBCDReverseCodec) DecodeRegisters(regs []uint16) (string, error) {
	if err := ValidateRegisterSpec(c.RegisterSpec(), regs, c.ID()); err != nil {
		return "", err
	}
	// Low byte first per register on wire: use LittleEndian to get logical byte order.
	raw := uint16sToBytes(LittleEndian, regs)
	for i, b := range raw {
		if b>>4 > 9 || b&0x0f > 9 {
			return "", &CodecValueError{Codec: c.ID(), Reason: fmt.Sprintf("invalid packed BCD byte at index %d: nibble(s) not in 0-9", i)}
		}
	}
	return bytesToPackedBCD(raw), nil
}

func (c packedBCDReverseCodec) EncodeRegisters(s string) ([]uint16, error) {
	digitCount := int(c.registerCount * 4)
	for _, r := range s {
		if r < '0' || r > '9' {
			return nil, &CodecValueError{Codec: c.ID(), Reason: "packed BCD string must contain only digits 0-9"}
		}
	}
	if len(s) > digitCount {
		s = s[len(s)-digitCount:]
	}
	s = padLeadingZeros(s, digitCount)
	b, err := packedBCDToBytes(s)
	if err != nil {
		return nil, err
	}
	return bytesToUint16s(LittleEndian, b), nil
}

func padLeadingZeros(s string, length int) string {
	if len(s) >= length {
		return s
	}
	b := make([]byte, length)
	for i := 0; i < length-len(s); i++ {
		b[i] = '0'
	}
	copy(b[length-len(s):], s)
	return string(b)
}

// NewAsciiCodec returns a codec for ASCII text: high byte first per register, decode trims trailing spaces, encode right-pads with space. registerCount must be >= 1.
func NewAsciiCodec(registerCount uint16) (Codec[string], error) {
	if err := textCodecRejectZeroRegisters(registerCount); err != nil {
		return nil, err
	}
	return asciiCodec{registerCount: registerCount}, nil
}

// NewAsciiFixedCodec returns a codec for fixed-width ASCII: no trim on decode, encode right-pads with NUL. registerCount must be >= 1.
func NewAsciiFixedCodec(registerCount uint16) (Codec[string], error) {
	if err := textCodecRejectZeroRegisters(registerCount); err != nil {
		return nil, err
	}
	return asciiFixedCodec{registerCount: registerCount}, nil
}

// NewAsciiReverseCodec returns a codec for ASCII with low byte first per register; decode trims trailing spaces. registerCount must be >= 1.
func NewAsciiReverseCodec(registerCount uint16) (Codec[string], error) {
	if err := textCodecRejectZeroRegisters(registerCount); err != nil {
		return nil, err
	}
	return asciiReverseCodec{registerCount: registerCount}, nil
}

// NewBCDCodec returns a codec for one-byte-per-digit BCD (0-9). Encode pads with leading zeros; non-digit input is rejected. registerCount must be >= 1.
func NewBCDCodec(registerCount uint16) (Codec[string], error) {
	if err := textCodecRejectZeroRegisters(registerCount); err != nil {
		return nil, err
	}
	return bcdCodec{registerCount: registerCount}, nil
}

// NewPackedBCDCodec returns a codec for packed BCD (two digits per byte). Encode pads with leading zeros; non-digit input is rejected. registerCount must be >= 1.
func NewPackedBCDCodec(registerCount uint16) (Codec[string], error) {
	if err := textCodecRejectZeroRegisters(registerCount); err != nil {
		return nil, err
	}
	return packedBCDCodec{registerCount: registerCount}, nil
}

// NewSignedPackedBCDCodec returns a codec for signed packed BCD: trailing nibble 0xC/0xD/0xF = negative, 0x0-0x9 = positive. Decode returns e.g. "-1234" or "1234"; encode accepts optional leading "-". registerCount must be >= 1.
func NewSignedPackedBCDCodec(registerCount uint16) (Codec[string], error) {
	if err := textCodecRejectZeroRegisters(registerCount); err != nil {
		return nil, err
	}
	return signedPackedBCDCodec{registerCount: registerCount}, nil
}

// NewPackedBCDReverseCodec returns a codec for packed BCD with low byte first per register (byte-swap within each word on wire). registerCount must be >= 1.
func NewPackedBCDReverseCodec(registerCount uint16) (Codec[string], error) {
	if err := textCodecRejectZeroRegisters(registerCount); err != nil {
		return nil, err
	}
	return packedBCDReverseCodec{registerCount: registerCount}, nil
}

func init() {
	registerTextDescriptors()
}

func registerTextDescriptors() {
	for _, n := range []uint16{1, 2, 3, 4, 6, 8, 12, 16, 20, 32, 48, 64} {
		registerCodecDescriptor(CodecDescriptor{
			ID:           fmt.Sprintf("ascii/registers:%d", n),
			Name:         "ascii",
			Family:       CodecFamilyText,
			ValueKind:    CodecValueString,
			RegisterSpec: RegisterSpec{Count: n},
			ByteSpec:     ByteSpec{Count: n * 2},
			Layouts:      nil,
		})
		registerCodecDescriptor(CodecDescriptor{
			ID:           fmt.Sprintf("ascii_fixed/registers:%d", n),
			Name:         "ascii_fixed",
			Family:       CodecFamilyText,
			ValueKind:    CodecValueString,
			RegisterSpec: RegisterSpec{Count: n},
			ByteSpec:     ByteSpec{Count: n * 2},
			Layouts:      nil,
		})
		registerCodecDescriptor(CodecDescriptor{
			ID:           fmt.Sprintf("ascii_reverse/registers:%d", n),
			Name:         "ascii_reverse",
			Family:       CodecFamilyText,
			ValueKind:    CodecValueString,
			RegisterSpec: RegisterSpec{Count: n},
			ByteSpec:     ByteSpec{Count: n * 2},
			Layouts:      nil,
		})
		registerCodecDescriptor(CodecDescriptor{
			ID:           fmt.Sprintf("bcd/registers:%d", n),
			Name:         "bcd",
			Family:       CodecFamilyBCD,
			ValueKind:    CodecValueString,
			RegisterSpec: RegisterSpec{Count: n},
			ByteSpec:     ByteSpec{Count: n * 2},
			Layouts:      nil,
		})
		registerCodecDescriptor(CodecDescriptor{
			ID:           fmt.Sprintf("packed_bcd/registers:%d", n),
			Name:         "packed_bcd",
			Family:       CodecFamilyBCD,
			ValueKind:    CodecValueString,
			RegisterSpec: RegisterSpec{Count: n},
			ByteSpec:     ByteSpec{Count: n * 2},
			Layouts:      nil,
		})
		registerCodecDescriptor(CodecDescriptor{
			ID:           fmt.Sprintf("signed_packed_bcd/registers:%d", n),
			Name:         "signed_packed_bcd",
			Family:       CodecFamilyBCD,
			ValueKind:    CodecValueString,
			RegisterSpec: RegisterSpec{Count: n},
			ByteSpec:     ByteSpec{Count: n * 2},
			Layouts:      nil,
		})
		registerCodecDescriptor(CodecDescriptor{
			ID:           fmt.Sprintf("packed_bcd_reverse/registers:%d", n),
			Name:         "packed_bcd_reverse",
			Family:       CodecFamilyBCD,
			ValueKind:    CodecValueString,
			RegisterSpec: RegisterSpec{Count: n},
			ByteSpec:     ByteSpec{Count: n * 2},
			Layouts:      nil,
		})
	}
}
