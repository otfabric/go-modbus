// SPDX-License-Identifier: MIT

package codec

import "fmt"

// PermuteBytesDecode converts raw register bytes (wire order) into canonical
// big-endian byte order using the layout permutation. Positions are 1-based:
// position 1 = LSB, position byteCount = MSB. len(raw) must equal
// layout.RegisterCount()*2.
func PermuteBytesDecode(raw []byte, layout RegisterLayout) ([]byte, error) {
	byteCount := layout.RegisterCount() * 2
	if uint16(len(raw)) != byteCount {
		return nil, fmt.Errorf("%w: PermuteBytesDecode: expected %d bytes, got %d", ErrEncodingError, byteCount, len(raw))
	}
	pos := layout.BytePositions()
	canonical := make([]byte, byteCount)
	for j := uint16(0); j < byteCount; j++ {
		logicalPos := byteCount - j // j=0 -> MSB (position byteCount), j=byteCount-1 -> LSB (position 1)
		for i := 0; i < int(byteCount); i++ {
			if pos[i] == uint8(logicalPos) {
				canonical[j] = raw[i]
				break
			}
		}
	}
	return canonical, nil
}

// PermuteBytesEncode converts canonical big-endian bytes into raw register bytes
// (wire order) using the layout permutation. len(canonical) must equal
// layout.RegisterCount()*2.
func PermuteBytesEncode(canonical []byte, layout RegisterLayout) ([]byte, error) {
	byteCount := layout.RegisterCount() * 2
	if uint16(len(canonical)) != byteCount {
		return nil, fmt.Errorf("%w: PermuteBytesEncode: expected %d bytes, got %d", ErrEncodingError, byteCount, len(canonical))
	}
	pos := layout.BytePositions()
	raw := make([]byte, byteCount)
	for i := 0; i < int(byteCount); i++ {
		p := pos[i]
		j := byteCount - uint16(p)
		raw[i] = canonical[j]
	}
	return raw, nil
}
