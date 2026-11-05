// SPDX-License-Identifier: MIT

package sunspec

import (
	"context"
	"encoding/binary"
	"testing"

	"github.com/otfabric/go-modbus/internal/protocol"
)

// FuzzHostileDiscover walks a model chain a hostile device makes up: the fuzz
// input is the register image from address 40000 on (registers past its end
// read as 0xFFFF), and its first byte decides whether reads come back short.
// Discover must end, report at most MaxModels models, and never visit a model
// address twice or go backwards (a chain that loops would do both).
func FuzzHostileDiscover(f *testing.F) {
	marker := []byte{0x53, 0x75, 0x6E, 0x53}
	chain := func(hdrs ...uint16) []byte {
		b := append([]byte{0}, marker...)
		for _, h := range hdrs {
			b = binary.BigEndian.AppendUint16(b, h)
		}
		return b
	}
	f.Add(chain(1, 2, 0, 0, 0xFFFF, 0))
	f.Add(chain(1, 0))
	f.Add(chain(1, 0xFFFF))
	f.Add(chain(103, 50))
	f.Add(chain(1, 0xFFFD))
	f.Add(append([]byte{1}, chain(1, 2, 0, 0, 0xFFFF, 0)[1:]...))
	f.Add([]byte{0})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 {
			return
		}
		short := data[0]&1 == 1
		image := data[1:]
		const base = 40000
		reads := 0
		r := funcReader(func(_ context.Context, _ uint8, addr, byteCount uint16, _ RegType) ([]byte, error) {
			reads++
			if reads > 10000 {
				t.Fatalf("more than 10000 reads")
			}
			out := make([]byte, 0, byteCount)
			for i := 0; i < int(byteCount); i++ {
				off := (int(addr)-base)*2 + i
				if off >= 0 && off < len(image) {
					out = append(out, image[off])
				} else {
					out = append(out, 0xFF)
				}
			}
			if short && reads > 2 && len(out) > 1 {
				out = out[:len(out)-1]
			}
			return out, nil
		})
		const maxModels = 64
		res, _ := Discover(context.Background(), r, &Options{
			RegType: protocol.HoldingRegister, BaseAddresses: []uint16{base}, MaxModels: maxModels,
		})
		if res == nil {
			return
		}
		if len(res.Models) > maxModels {
			t.Fatalf("%d models, MaxModels %d", len(res.Models), maxModels)
		}
		prev := -1
		for i, m := range res.Models {
			if int(m.StartAddress) <= prev {
				t.Fatalf("model %d starts at %d, after one at %d: the chain went back", i, m.StartAddress, prev)
			}
			prev = int(m.StartAddress)
			if m.NextAddress != uint16(uint32(m.StartAddress)+2+uint32(m.Length)) {
				t.Fatalf("model %d: next address %d, start %d length %d", i, m.NextAddress, m.StartAddress, m.Length)
			}
		}
	})
}
