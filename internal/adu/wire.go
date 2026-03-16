package adu

import "encoding/binary"

// Endianness selects byte order for 16-bit wire encoding.
type Endianness uint

const (
	BigEndian    Endianness = 1
	LittleEndian Endianness = 2
)

// Uint16ToBytes encodes a uint16 in the given byte order.
func Uint16ToBytes(endianness Endianness, in uint16) (out []byte) {
	out = make([]byte, 2)
	switch endianness {
	case BigEndian:
		binary.BigEndian.PutUint16(out, in)
	case LittleEndian:
		binary.LittleEndian.PutUint16(out, in)
	}
	return out
}

// BytesToUint16 decodes a uint16 from the given byte order.
func BytesToUint16(endianness Endianness, in []byte) (out uint16) {
	switch endianness {
	case BigEndian:
		out = binary.BigEndian.Uint16(in)
	case LittleEndian:
		out = binary.LittleEndian.Uint16(in)
	}
	return out
}

// Uint16sToBytes encodes a slice of uint16 in the given byte order.
func Uint16sToBytes(endianness Endianness, in []uint16) []byte {
	out := make([]byte, len(in)*2)
	for i, v := range in {
		switch endianness {
		case BigEndian:
			binary.BigEndian.PutUint16(out[i*2:], v)
		case LittleEndian:
			binary.LittleEndian.PutUint16(out[i*2:], v)
		}
	}
	return out
}

// BytesToUint16s converts bytes to uint16s in the given byte order.
// len(in) must be even; callers must ensure aligned input.
func BytesToUint16s(endianness Endianness, in []byte) []uint16 {
	out := make([]uint16, len(in)/2)
	for i := range out {
		out[i] = BytesToUint16(endianness, in[i*2:i*2+2])
	}
	return out
}
