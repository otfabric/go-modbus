// SPDX-License-Identifier: MIT

package codec

import (
	"errors"
	"fmt"
)

// RegisterLayout describes how the bytes of a multi-register value are permuted
// across Modbus registers. Positions are 1-based: 1 is the least-significant byte,
// and the highest position (e.g. 8 for 64-bit) is the most-significant byte.
// Layout is immutable; use NewRegisterLayout or MustNewRegisterLayout to construct.
type RegisterLayout struct {
	registerCount uint8
	byteCount     uint8
	positions     [8]uint8
}

const maxRegisterCount = 4

// ErrInvalidLayout is returned when layout parameters are invalid (wrong length,
// duplicate or out-of-range positions).
var ErrInvalidLayout = errors.New("modbus: invalid register layout")

// NewRegisterLayout builds a RegisterLayout from a register count and byte
// positions. Positions are 1-based (1 = least-significant byte). Each position
// 1..(registerCount*2) must appear exactly once. registerCount must be 1..4.
func NewRegisterLayout(registerCount uint16, positions ...uint8) (RegisterLayout, error) {
	if registerCount == 0 || registerCount > maxRegisterCount {
		return RegisterLayout{}, fmt.Errorf("%w: register count %d not in 1..%d", ErrInvalidLayout, registerCount, maxRegisterCount)
	}
	byteCount := registerCount * 2
	if len(positions) != int(byteCount) {
		return RegisterLayout{}, fmt.Errorf("%w: got %d positions, need %d", ErrInvalidLayout, len(positions), byteCount)
	}
	seen := make([]bool, byteCount+1) // 1-based index
	byteCountU8 := uint8(byteCount)
	for _, p := range positions {
		if p == 0 || p > byteCountU8 {
			return RegisterLayout{}, fmt.Errorf("%w: position %d out of range 1..%d", ErrInvalidLayout, p, byteCount)
		}
		if seen[p] {
			return RegisterLayout{}, fmt.Errorf("%w: duplicate position %d", ErrInvalidLayout, p)
		}
		seen[p] = true
	}
	var out RegisterLayout
	out.registerCount = uint8(registerCount)
	out.byteCount = uint8(byteCount)
	copy(out.positions[:byteCount], positions)
	return out, nil
}

// MustNewRegisterLayout is like NewRegisterLayout but panics on error. Use for
// known-good layouts (e.g. package-level vars).
func MustNewRegisterLayout(registerCount uint16, positions ...uint8) RegisterLayout {
	l, err := NewRegisterLayout(registerCount, positions...)
	if err != nil {
		panic(err)
	}
	return l
}

// RegisterCount returns the number of 16-bit Modbus registers (1..4).
func (l RegisterLayout) RegisterCount() uint16 {
	return uint16(l.registerCount)
}

// BytePositions returns a copy of the byte position permutation. Callers must
// not mutate the result. Length equals RegisterCount()*2.
func (l RegisterLayout) BytePositions() []uint8 {
	out := make([]uint8, l.byteCount)
	copy(out, l.positions[:l.byteCount])
	return out
}

// String returns the layout as a compact digit string for IDs and discovery,
// e.g. "21", "4321", "87654321", "21436587".
func (l RegisterLayout) String() string {
	b := make([]byte, l.byteCount)
	for i := uint8(0); i < l.byteCount; i++ {
		b[i] = '0' + l.positions[i]
	}
	return string(b)
}

// Named common layouts (fully explicit byte-position notation).

var (
	Layout16_21 = MustNewRegisterLayout(1, 2, 1)
	Layout16_12 = MustNewRegisterLayout(1, 1, 2)
)

var (
	Layout32_4321 = MustNewRegisterLayout(2, 4, 3, 2, 1)
	Layout32_3412 = MustNewRegisterLayout(2, 3, 4, 1, 2)
	Layout32_2143 = MustNewRegisterLayout(2, 2, 1, 4, 3)
	Layout32_1234 = MustNewRegisterLayout(2, 1, 2, 3, 4)
)

var (
	Layout48_654321 = MustNewRegisterLayout(3, 6, 5, 4, 3, 2, 1)
	Layout48_563412 = MustNewRegisterLayout(3, 5, 6, 3, 4, 1, 2)
	Layout48_214365 = MustNewRegisterLayout(3, 2, 1, 4, 3, 6, 5)
	Layout48_123456 = MustNewRegisterLayout(3, 1, 2, 3, 4, 5, 6)
)

var (
	Layout64_87654321 = MustNewRegisterLayout(4, 8, 7, 6, 5, 4, 3, 2, 1)
	Layout64_78563412 = MustNewRegisterLayout(4, 7, 8, 5, 6, 3, 4, 1, 2)
	Layout64_21436587 = MustNewRegisterLayout(4, 2, 1, 4, 3, 6, 5, 8, 7)
	Layout64_12345678 = MustNewRegisterLayout(4, 1, 2, 3, 4, 5, 6, 7, 8)
)
