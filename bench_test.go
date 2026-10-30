// SPDX-License-Identifier: MIT

package modbus_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/otfabric/go-modbus"
	"github.com/otfabric/go-modbus/codec"
	"github.com/otfabric/go-modbus/internal/benchserver"
)

// Baseline benchmarks for read strategies (run with make bench). Every
// end-to-end benchmark reports requests/op and pdu-bytes/op next to time and
// allocations, at each of benchserver.Delays, on the read sets of
// benchserver.ReadSets.

func delayName(d time.Duration) string {
	return fmt.Sprintf("delay=%s", d)
}

// BenchmarkReadEach reads every item of a read set with its own request.
func BenchmarkReadEach(b *testing.B) {
	for _, set := range benchserver.ReadSets() {
		for _, delay := range benchserver.Delays {
			b.Run(set.Name+"/"+delayName(delay), func(b *testing.B) {
				h := &benchserver.Handler{Delay: delay}
				c := benchserver.Start(b, h)
				ctx := context.Background()
				h.Reset()
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := benchserver.ReadEach(ctx, c, set); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				h.ReportMetrics(b)
			})
		}
	}
}

// contiguousDecodePlan covers the contiguous read set with one window and a
// uint32 item per read.
func contiguousDecodePlan(tb testing.TB) modbus.RuntimeDecodePlan {
	tb.Helper()
	dec := codec.MustRuntimeCodecByID("uint32/layout:4321")
	set := benchserver.ReadSets()[0]
	first := set.Reads[0].Addr
	plan := modbus.RuntimeDecodePlan{
		Window: modbus.ReadWindow{Addr: first, Quantity: uint16(2 * len(set.Reads)), RegType: modbus.HoldingRegister},
	}
	for i, r := range set.Reads {
		plan.Items = append(plan.Items, modbus.RuntimeDecodeItem{
			Name: fmt.Sprintf("item%d", i), Offset: r.Addr - first, Codec: dec,
		})
	}
	if err := modbus.ValidateRuntimeDecodePlan(plan); err != nil {
		tb.Fatal(err)
	}
	return plan
}

// BenchmarkRuntimeDecodePlan reads the contiguous read set in one request
// and decodes every item: the hand-built single-window strategy.
func BenchmarkRuntimeDecodePlan(b *testing.B) {
	plan := contiguousDecodePlan(b)
	for _, delay := range benchserver.Delays {
		b.Run("contiguous/"+delayName(delay), func(b *testing.B) {
			h := &benchserver.Handler{Delay: delay}
			c := benchserver.Start(b, h)
			ctx := context.Background()
			h.Reset()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := modbus.ExecuteRuntimeDecodePlan(c, ctx, 1, plan); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			h.ReportMetrics(b)
		})
	}
}

// BenchmarkRuntimeDecodePlanOffline measures decoding alone (no I/O).
func BenchmarkRuntimeDecodePlanOffline(b *testing.B) {
	plan := contiguousDecodePlan(b)
	regs := make([]uint16, plan.Window.Quantity)
	for i := range regs {
		regs[i] = benchserver.RegisterValue(plan.Window.Addr + uint16(i))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := modbus.ExecuteRuntimeDecodePlanOffline(regs, plan); err != nil {
			b.Fatal(err)
		}
	}
}
