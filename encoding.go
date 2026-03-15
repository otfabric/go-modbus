package modbus

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
)

func uint16ToBytes(endianness Endianness, in uint16) (out []byte) {
	out = make([]byte, 2)
	switch endianness {
	case BigEndian:
		binary.BigEndian.PutUint16(out, in)
	case LittleEndian:
		binary.LittleEndian.PutUint16(out, in)
	}

	return
}

func uint16sToBytes(endianness Endianness, in []uint16) (out []byte) {
	for i := range in {
		out = append(out, uint16ToBytes(endianness, in[i])...)
	}

	return
}

func bytesToUint16(endianness Endianness, in []byte) (out uint16) {
	switch endianness {
	case BigEndian:
		out = binary.BigEndian.Uint16(in)
	case LittleEndian:
		out = binary.LittleEndian.Uint16(in)
	}

	return
}

// bytesToUint16s converts bytes to uint16s in the given byte order. len(in) must be even;
// otherwise the final slice index would be out of range (callers must ensure aligned input).
func bytesToUint16s(endianness Endianness, in []byte) (out []uint16) {
	for i := 0; i < len(in); i += 2 {
		out = append(out, bytesToUint16(endianness, in[i:i+2]))
	}

	return
}

func bytesToUint32s(endianness Endianness, wordOrder WordOrder, in []byte) (out []uint32) {
	var u32 uint32

	for i := 0; i < len(in); i += 4 {
		switch endianness {
		case BigEndian:
			if wordOrder == HighWordFirst {
				u32 = binary.BigEndian.Uint32(in[i : i+4])
			} else {
				u32 = binary.BigEndian.Uint32(
					[]byte{in[i+2], in[i+3], in[i+0], in[i+1]})
			}
		case LittleEndian:
			if wordOrder == LowWordFirst {
				u32 = binary.LittleEndian.Uint32(in[i : i+4])
			} else {
				u32 = binary.LittleEndian.Uint32(
					[]byte{in[i+2], in[i+3], in[i+0], in[i+1]})
			}
		}

		out = append(out, u32)
	}

	return
}

func uint32ToBytes(endianness Endianness, wordOrder WordOrder, in uint32) (out []byte) {
	out = make([]byte, 4)

	switch endianness {
	case BigEndian:
		binary.BigEndian.PutUint32(out, in)

		// swap words if needed
		if wordOrder == LowWordFirst {
			out[0], out[1], out[2], out[3] = out[2], out[3], out[0], out[1]
		}
	case LittleEndian:
		binary.LittleEndian.PutUint32(out, in)

		// swap words if needed
		if wordOrder == HighWordFirst {
			out[0], out[1], out[2], out[3] = out[2], out[3], out[0], out[1]
		}
	}

	return
}

func bytesToFloat32s(endianness Endianness, wordOrder WordOrder, in []byte) (out []float32) {
	var u32s = bytesToUint32s(endianness, wordOrder, in)

	for _, u32 := range u32s {
		out = append(out, math.Float32frombits(u32))
	}

	return
}

func float32ToBytes(endianness Endianness, wordOrder WordOrder, in float32) (out []byte) {
	out = uint32ToBytes(endianness, wordOrder, math.Float32bits(in))

	return
}

func bytesToUint64s(endianness Endianness, wordOrder WordOrder, in []byte) (out []uint64) {
	var u64 uint64

	for i := 0; i < len(in); i += 8 {
		switch endianness {
		case BigEndian:
			if wordOrder == HighWordFirst {
				u64 = binary.BigEndian.Uint64(in[i : i+8])
			} else {
				u64 = binary.BigEndian.Uint64(
					[]byte{in[i+6], in[i+7], in[i+4], in[i+5],
						in[i+2], in[i+3], in[i+0], in[i+1]})
			}
		case LittleEndian:
			if wordOrder == LowWordFirst {
				u64 = binary.LittleEndian.Uint64(in[i : i+8])
			} else {
				u64 = binary.LittleEndian.Uint64(
					[]byte{in[i+6], in[i+7], in[i+4], in[i+5],
						in[i+2], in[i+3], in[i+0], in[i+1]})
			}
		}

		out = append(out, u64)
	}

	return
}

func uint64ToBytes(endianness Endianness, wordOrder WordOrder, in uint64) (out []byte) {
	out = make([]byte, 8)

	switch endianness {
	case BigEndian:
		binary.BigEndian.PutUint64(out, in)

		// swap words if needed
		if wordOrder == LowWordFirst {
			out[0], out[1], out[2], out[3], out[4], out[5], out[6], out[7] =
				out[6], out[7], out[4], out[5], out[2], out[3], out[0], out[1]
		}
	case LittleEndian:
		binary.LittleEndian.PutUint64(out, in)

		// swap words if needed
		if wordOrder == HighWordFirst {
			out[0], out[1], out[2], out[3], out[4], out[5], out[6], out[7] =
				out[6], out[7], out[4], out[5], out[2], out[3], out[0], out[1]
		}
	}

	return
}

func bytesToFloat64s(endianness Endianness, wordOrder WordOrder, in []byte) (out []float64) {
	var u64s = bytesToUint64s(endianness, wordOrder, in)

	for _, u64 := range u64s {
		out = append(out, math.Float64frombits(u64))
	}

	return
}

func float64ToBytes(endianness Endianness, wordOrder WordOrder, in float64) (out []byte) {
	out = uint64ToBytes(endianness, wordOrder, math.Float64bits(in))

	return
}

func encodeBools(in []bool) (out []byte) {
	var byteCount uint
	var i uint

	byteCount = uint(len(in)) / 8
	if len(in)%8 != 0 {
		byteCount++
	}

	out = make([]byte, byteCount)
	for i = 0; i < uint(len(in)); i++ {
		if in[i] {
			out[i/8] |= (0x01 << (i % 8))
		}
	}

	return
}

func decodeBools(quantity uint16, in []byte) (out []bool) {
	var i uint
	for i = 0; i < uint(quantity); i++ {
		out = append(out, (((in[i/8] >> (i % 8)) & 0x01) == 0x01))
	}

	return
}

// bytesToInt16s reinterprets each pair of bytes as a signed 16-bit integer.
func bytesToInt16s(endianness Endianness, in []byte) (out []int16) {
	for _, u := range bytesToUint16s(endianness, in) {
		out = append(out, int16(u))
	}

	return
}

// bytesToInt32s reinterprets each group of 4 bytes as a signed 32-bit integer.
func bytesToInt32s(endianness Endianness, wordOrder WordOrder, in []byte) (out []int32) {
	for _, u := range bytesToUint32s(endianness, wordOrder, in) {
		out = append(out, int32(u))
	}

	return
}

// bytesToInt64s reinterprets each group of 8 bytes as a signed 64-bit integer.
func bytesToInt64s(endianness Endianness, wordOrder WordOrder, in []byte) (out []int64) {
	for _, u := range bytesToUint64s(endianness, wordOrder, in) {
		out = append(out, int64(u))
	}

	return
}

// bytesToUint48s decodes groups of 6 bytes (3 registers) into unsigned 48-bit
// values returned as uint64. Endianness controls byte order within each 16-bit
// word; wordOrder controls which word is most significant.
func bytesToUint48s(endianness Endianness, wordOrder WordOrder, in []byte) (out []uint64) {
	var u48 uint64

	for i := 0; i+5 < len(in); i += 6 {
		switch endianness {
		case BigEndian:
			if wordOrder == HighWordFirst {
				// W0 (MSW) … W2 (LSW), each word big-endian.
				u48 = uint64(in[i])<<40 | uint64(in[i+1])<<32 |
					uint64(in[i+2])<<24 | uint64(in[i+3])<<16 |
					uint64(in[i+4])<<8 | uint64(in[i+5])
			} else {
				// W2 (MSW) … W0 (LSW), each word big-endian.
				u48 = uint64(in[i+4])<<40 | uint64(in[i+5])<<32 |
					uint64(in[i+2])<<24 | uint64(in[i+3])<<16 |
					uint64(in[i])<<8 | uint64(in[i+1])
			}
		case LittleEndian:
			if wordOrder == LowWordFirst {
				// W0 (LSW) … W2 (MSW), each word little-endian.
				u48 = uint64(in[i+5])<<40 | uint64(in[i+4])<<32 |
					uint64(in[i+3])<<24 | uint64(in[i+2])<<16 |
					uint64(in[i+1])<<8 | uint64(in[i])
			} else {
				// W0 (MSW) … W2 (LSW), each word little-endian.
				u48 = uint64(in[i+1])<<40 | uint64(in[i])<<32 |
					uint64(in[i+3])<<24 | uint64(in[i+2])<<16 |
					uint64(in[i+5])<<8 | uint64(in[i+4])
			}
		}

		out = append(out, u48)
	}

	return
}

// bytesToInt48s decodes groups of 6 bytes (3 registers) into signed 48-bit
// values returned as int64. The 48-bit result is sign-extended to 64 bits.
func bytesToInt48s(endianness Endianness, wordOrder WordOrder, in []byte) (out []int64) {
	for _, u48 := range bytesToUint48s(endianness, wordOrder, in) {
		// sign-extend bit 47 into the upper 16 bits.
		if u48&(1<<47) != 0 {
			out = append(out, int64(u48|^uint64(0x0000ffffffffffff)))
		} else {
			out = append(out, int64(u48))
		}
	}

	return
}

// bytesToAscii decodes raw register bytes as ASCII text.
// The high byte of each register is the first character, the low byte the second.
// Trailing spaces are stripped from the returned string.
func bytesToAscii(in []byte) string {
	return strings.TrimRight(string(in), " ")
}

// bytesToAsciiReverse decodes raw register bytes as ASCII text with the byte
// order within each 16-bit word reversed: the low byte is the first character
// and the high byte is the second. Trailing spaces are stripped.
func bytesToAsciiReverse(in []byte) string {
	swapped := make([]byte, len(in))

	for i := 0; i+1 < len(in); i += 2 {
		swapped[i], swapped[i+1] = in[i+1], in[i]
	}

	return strings.TrimRight(string(swapped), " ")
}

// bytesToBCD decodes raw register bytes as Binary Coded Decimal (BCD).
// Each byte encodes exactly one decimal digit (value 0–9). Returns a string
// of decimal digits, most-significant digit first.
func bytesToBCD(in []byte) string {
	var sb strings.Builder

	for _, b := range in {
		sb.WriteByte('0' + b%10)
	}

	return sb.String()
}

// bytesToPackedBCD decodes raw register bytes as Packed BCD.
// Each nibble encodes one decimal digit (value 0–9): the high nibble is the
// more-significant digit. Returns a string of decimal digits, most-significant
// digit first.
func bytesToPackedBCD(in []byte) string {
	var sb strings.Builder

	for _, b := range in {
		sb.WriteByte('0' + (b>>4)%10)
		sb.WriteByte('0' + (b&0x0f)%10)
	}

	return sb.String()
}

// uint48ToBytes encodes a 48-bit value (lower 48 bits of u48) into 6 bytes for 3 registers.
// Endianness and wordOrder must match the inverse of bytesToUint48s.
func uint48ToBytes(endianness Endianness, wordOrder WordOrder, u48 uint64) (out []byte) {
	u48 = u48 & 0xFFFFFFFFFFFF
	out = make([]byte, 6)
	switch endianness {
	case BigEndian:
		if wordOrder == HighWordFirst {
			out[0] = byte(u48 >> 40)
			out[1] = byte(u48 >> 32)
			out[2] = byte(u48 >> 24)
			out[3] = byte(u48 >> 16)
			out[4] = byte(u48 >> 8)
			out[5] = byte(u48)
		} else {
			out[0] = byte(u48 >> 8)
			out[1] = byte(u48)
			out[2] = byte(u48 >> 24)
			out[3] = byte(u48 >> 16)
			out[4] = byte(u48 >> 40)
			out[5] = byte(u48 >> 32)
		}
	case LittleEndian:
		if wordOrder == LowWordFirst {
			out[0] = byte(u48)
			out[1] = byte(u48 >> 8)
			out[2] = byte(u48 >> 16)
			out[3] = byte(u48 >> 24)
			out[4] = byte(u48 >> 32)
			out[5] = byte(u48 >> 40)
		} else {
			out[0] = byte(u48 >> 32)
			out[1] = byte(u48 >> 40)
			out[2] = byte(u48 >> 16)
			out[3] = byte(u48 >> 24)
			out[4] = byte(u48)
			out[5] = byte(u48 >> 8)
		}
	}
	return out
}

// asciiToBytes produces register bytes for ASCII: high byte of each register = first char, low = second.
// Length is padded to even (one zero byte) if needed.
func asciiToBytes(s string) []byte {
	b := []byte(s)
	if len(b)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

// asciiToBytesReverse produces register bytes for ASCII with byte order reversed per 16-bit word
// (low byte first, then high byte). Same convention as ReadAsciiReverse. Padded to even length if needed.
func asciiToBytesReverse(s string) []byte {
	b := []byte(s)
	if len(b)%2 == 1 {
		b = append(b, 0)
	}
	for i := 0; i+1 < len(b); i += 2 {
		b[i], b[i+1] = b[i+1], b[i]
	}
	return b
}

var errBCDDigit = errors.New("modbus: BCD string must contain only digits 0-9")

// bcdToBytes encodes a string of decimal digits (0-9) as one byte per digit, MSB first.
func bcdToBytes(s string) ([]byte, error) {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		if r < '0' || r > '9' {
			return nil, errBCDDigit
		}
		out = append(out, byte(r-'0'))
	}
	return out, nil
}

// packedBCDToBytes encodes a string of decimal digits (0-9) as packed BCD (two digits per byte, high nibble first).
// If len(s) is odd, the last nibble is 0.
func packedBCDToBytes(s string) ([]byte, error) {
	for _, r := range s {
		if r < '0' || r > '9' {
			return nil, errBCDDigit
		}
	}
	n := (len(s) + 1) / 2
	out := make([]byte, n)
	for i := 0; i < len(s); i += 2 {
		hi := s[i] - '0'
		lo := byte(0)
		if i+1 < len(s) {
			lo = s[i+1] - '0'
		}
		out[i/2] = (hi << 4) | lo
	}
	return out, nil
}

// bytesToSignedPackedBCD decodes packed BCD with trailing-nibble sign: last nibble 0xC/0xD/0xF = negative.
// Returns a signed digit string (e.g. "-1234" or "1234"). Magnitude uses all nibbles except the sign nibble when negative.
func bytesToSignedPackedBCD(in []byte) (string, error) {
	if len(in) == 0 {
		return "", nil
	}
	lastByte := in[len(in)-1]
	lastNibble := lastByte & 0x0f
	negative := lastNibble == 0x0c || lastNibble == 0x0d || lastNibble == 0x0f
	var sb strings.Builder
	for i, b := range in {
		hi, lo := b>>4, b&0x0f
		if hi > 9 {
			return "", fmt.Errorf("modbus: invalid signed packed BCD nibble at byte %d high: %d", i, hi)
		}
		sb.WriteByte('0' + hi)
		isLastNibble := i == len(in)-1 && negative
		if !isLastNibble {
			if lo > 9 {
				return "", fmt.Errorf("modbus: invalid signed packed BCD nibble at byte %d low: %d", i, lo)
			}
			sb.WriteByte('0' + lo)
		} else if lo != 0x0c && lo != 0x0d && lo != 0x0f {
			// Positive: last nibble is a digit
			if lo > 9 {
				return "", fmt.Errorf("modbus: invalid signed packed BCD nibble at byte %d low: %d", i, lo)
			}
			sb.WriteByte('0' + lo)
		}
	}
	s := sb.String()
	s = strings.TrimLeft(s, "0")
	if s == "" {
		s = "0"
	}
	if negative {
		return "-" + s, nil
	}
	return s, nil
}

// signedPackedBCDToBytes encodes a signed digit string (optional leading "-") as packed BCD with trailing sign nibble.
// totalNibbles includes the sign nibble: negative uses (totalNibbles-1) digits + 0xC; positive uses all totalNibbles as digits.
func signedPackedBCDToBytes(s string, totalNibbles int) ([]byte, error) {
	negative := strings.HasPrefix(s, "-")
	if negative {
		s = strings.TrimPrefix(s, "-")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return nil, errBCDDigit
		}
	}
	digitCount := totalNibbles
	if negative {
		digitCount = totalNibbles - 1
	}
	if len(s) > digitCount {
		s = s[len(s)-digitCount:]
	}
	for len(s) < digitCount {
		s = "0" + s
	}
	byteCount := (totalNibbles + 1) / 2
	out := make([]byte, byteCount)
	for i := 0; i < len(s); i += 2 {
		hi := s[i] - '0'
		lo := byte(0)
		if i+1 < len(s) {
			lo = s[i+1] - '0'
		}
		out[i/2] = (hi << 4) | lo
	}
	if negative {
		out[byteCount-1] = (out[byteCount-1] & 0xf0) | 0x0c
	}
	return out, nil
}
