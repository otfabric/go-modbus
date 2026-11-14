// SPDX-License-Identifier: MIT

package codec

import (
	"testing"
)

// Microbenchmark of one codec, CPU only: the decode of a 32-bit float through
// the generic helper (the register count check included), in the two most
// common register layouts. The result is checked and kept in a sink.

var sinkFloat32 float32

func BenchmarkDecodeFloat32(b *testing.B) {
	for _, layout := range []RegisterLayout{Layout32_4321, Layout32_2143} {
		b.Run("layout="+layout.String(), func(b *testing.B) {
			c, err := NewFloat32Codec(layout)
			if err != nil {
				b.Fatal(err)
			}
			const want = float32(230.5)
			regs, err := c.EncodeRegisters(want)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				v, err := DecodeRegisters(regs, c)
				if err != nil || v != want {
					b.Fatalf("DecodeRegisters: %v, %v; want %v", v, err, want)
				}
				sinkFloat32 = v
			}
		})
	}
}
