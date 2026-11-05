// SPDX-License-Identifier: MIT

package adu

import (
	"fmt"
	"math"
)

// Uint8 returns n for a one-byte count or length field. Callers bound n
// before (a quantity limit or the frame size), so a value outside 0..255 is
// a bug in this module: it panics rather than going out truncated.
func Uint8(n int) uint8 {
	if n < 0 || n > math.MaxUint8 {
		panic(fmt.Sprintf("modbus: %d does not fit in a byte field", n))
	}
	return uint8(n)
}

// Uint16 returns n for a two-byte count, length or address field. Callers
// bound n before, so a value outside 0..0xFFFF is a bug in this module: it
// panics rather than going out truncated.
func Uint16(n int) uint16 {
	if n < 0 || n > math.MaxUint16 {
		panic(fmt.Sprintf("modbus: %d does not fit in a 16-bit field", n))
	}
	return uint16(n)
}
