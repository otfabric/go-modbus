// SPDX-License-Identifier: MIT

package codec

import (
	"encoding/binary"
	"fmt"
	"testing"
)

// FuzzHostileRuntimeDecode feeds register contents a hostile device chose to
// every registered codec of the matching width. Decoding must not panic, and
// when a decoded value encodes again, decoding that encoding must give the
// same value (decoding is canonical).
func FuzzHostileRuntimeDecode(f *testing.F) {
	f.Add(byte(1), []byte{0x80, 0x00})
	f.Add(byte(2), []byte{0x7F, 0xC0, 0x00, 0x01})
	f.Add(byte(2), []byte{0xFF, 0xFF, 0xFF, 0xFF})
	f.Add(byte(4), []byte{0x7F, 0xF0, 0, 0, 0, 0, 0, 1})
	f.Add(byte(4), []byte{0x27, 0x10, 0x27, 0x10, 0x27, 0x10, 0x27, 0x10})
	f.Add(byte(8), []byte("hostile\x00\xff\xfe device"))
	f.Add(byte(3), []byte{0x99, 0x99, 0xFA, 0xFB, 0x12, 0x34})
	f.Fuzz(func(t *testing.T, width byte, data []byte) {
		count := uint16(width%16) + 1
		regs := make([]uint16, count)
		for i := range regs {
			if 2*i+1 < len(data) {
				regs[i] = binary.BigEndian.Uint16(data[2*i:])
			}
		}
		codecs, err := RuntimeCodecsForRegisterCount(count)
		if err != nil {
			t.Fatalf("RuntimeCodecsForRegisterCount(%d): %v", count, err)
		}
		for _, c := range codecs {
			v, err := c.DecodeRegistersAny(append([]uint16(nil), regs...))
			if err != nil {
				continue
			}
			enc, err := c.EncodeRegistersAny(v)
			if err != nil {
				// Decoding is more lenient than encoding for some codecs (the
				// ASCII codecs pass bytes 0x80-0xFF through but refuse to
				// encode them): not checked here.
				continue
			}
			v2, err := c.DecodeRegistersAny(enc)
			if err != nil {
				t.Errorf("%s: %04X -> %v -> %04X does not decode: %v", c.ID(), regs, v, enc, err)
				continue
			}
			if a, b := fmt.Sprintf("%#v", v), fmt.Sprintf("%#v", v2); a != b {
				t.Errorf("%s: %04X decodes to %s, its encoding %04X to %s", c.ID(), regs, a, enc, b)
			}
		}
	})
}
