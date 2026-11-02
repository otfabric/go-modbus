// SPDX-License-Identifier: MIT

package codec

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/otfabric/go-modbus/internal/adu"
)

type Endianness = adu.Endianness

const (
	BigEndian    = adu.BigEndian
	LittleEndian = adu.LittleEndian
)

func uint16sToBytes(endianness Endianness, in []uint16) []byte {
	return adu.Uint16sToBytes(endianness, in)
}

func bytesToUint16s(endianness Endianness, in []byte) []uint16 {
	return adu.BytesToUint16s(endianness, in)
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
		if wordOrder == LowWordFirst {
			out[0], out[1], out[2], out[3] = out[2], out[3], out[0], out[1]
		}
	case LittleEndian:
		binary.LittleEndian.PutUint32(out, in)
		if wordOrder == HighWordFirst {
			out[0], out[1], out[2], out[3] = out[2], out[3], out[0], out[1]
		}
	}
	return
}

func bytesToFloat32s(endianness Endianness, wordOrder WordOrder, in []byte) (out []float32) {
	for _, u32 := range bytesToUint32s(endianness, wordOrder, in) {
		out = append(out, math.Float32frombits(u32))
	}
	return
}

func float32ToBytes(endianness Endianness, wordOrder WordOrder, in float32) []byte {
	return uint32ToBytes(endianness, wordOrder, math.Float32bits(in))
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
		if wordOrder == LowWordFirst {
			out[0], out[1], out[2], out[3], out[4], out[5], out[6], out[7] =
				out[6], out[7], out[4], out[5], out[2], out[3], out[0], out[1]
		}
	case LittleEndian:
		binary.LittleEndian.PutUint64(out, in)
		if wordOrder == HighWordFirst {
			out[0], out[1], out[2], out[3], out[4], out[5], out[6], out[7] =
				out[6], out[7], out[4], out[5], out[2], out[3], out[0], out[1]
		}
	}
	return
}

func bytesToAscii(in []byte) string {
	return strings.TrimRight(string(in), " ")
}

func bytesToAsciiReverse(in []byte) string {
	swapped := make([]byte, len(in))
	for i := 0; i+1 < len(in); i += 2 {
		swapped[i], swapped[i+1] = in[i+1], in[i]
	}
	return strings.TrimRight(string(swapped), " ")
}

func bytesToBCD(in []byte) string {
	var sb strings.Builder
	for _, b := range in {
		sb.WriteByte('0' + b%10)
	}
	return sb.String()
}

func bytesToPackedBCD(in []byte) string {
	var sb strings.Builder
	for _, b := range in {
		sb.WriteByte('0' + (b>>4)%10)
		sb.WriteByte('0' + (b&0x0f)%10)
	}
	return sb.String()
}

func asciiToBytes(s string) []byte {
	b := []byte(s)
	if len(b)%2 == 1 {
		b = append(b, 0)
	}
	return b
}

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
