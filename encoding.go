package modbus

import (
	"github.com/otfabric/go-modbus/internal/adu"
)

func uint16ToBytes(endianness Endianness, in uint16) (out []byte) {
	return adu.Uint16ToBytes(endianness, in)
}

func uint16sToBytes(endianness Endianness, in []uint16) (out []byte) {
	return adu.Uint16sToBytes(endianness, in)
}

func bytesToUint16(endianness Endianness, in []byte) (out uint16) {
	return adu.BytesToUint16(endianness, in)
}

func bytesToUint16s(endianness Endianness, in []byte) (out []uint16) {
	return adu.BytesToUint16s(endianness, in)
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

func decodeBools(quantity uint16, in []byte) []bool {
	out := make([]bool, quantity)
	for i := uint(0); i < uint(quantity); i++ {
		out[i] = (in[i/8]>>(i%8))&0x01 == 0x01
	}
	return out
}
