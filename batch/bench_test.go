// SPDX-License-Identifier: MIT

package batch_test

import (
	"testing"

	"github.com/otfabric/go-modbus"
	"github.com/otfabric/go-modbus/batch"
	"github.com/otfabric/go-modbus/internal/benchserver"
)

// CPU benchmarks of planning and offline decoding, on the read sets of
// benchserver.ReadSets: the same reads the end-to-end benchmarks in the root
// package make. Each reports the plan's requests and PDU bytes; compare them
// with BenchmarkReadEach.

// strategies are the planning options compared on every read set.
var strategies = []struct {
	name string
	opts batch.Options
}{
	{"contiguous", batch.Options{}},
	{"gap=2", batch.Options{MaxGap: 2}},
}

func setItems(set benchserver.ReadSet) []batch.Item {
	items := make([]batch.Item, len(set.Reads))
	for i, r := range set.Reads {
		items[i] = batch.Item{Name: set.Name + itoa(uint16(i)), UnitID: r.UnitID, FC: r.FC, Addr: r.Addr, Count: r.Count}
	}
	return items
}

func reportPlan(b *testing.B, p *batch.Plan) {
	s := p.Stats()
	b.ReportMetric(float64(s.Requests), "requests/op")
	b.ReportMetric(float64(s.RequestBytes+s.ResponseBytes), "pdu-bytes/op")
}

func BenchmarkCompile(b *testing.B) {
	for _, set := range benchserver.ReadSets() {
		items := setItems(set)
		for _, st := range strategies {
			b.Run(set.Name+"/"+st.name, func(b *testing.B) {
				var p *batch.Plan
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					var err error
					if p, err = batch.Compile(items, st.opts); err != nil {
						b.Fatal(err)
					}
				}
				reportPlan(b, p)
			})
		}
	}
}

// windowData returns the data benchserver serves for every window of p.
func windowData(p *batch.Plan) []batch.WindowData {
	var data []batch.WindowData
	for _, w := range p.Windows() {
		var d batch.WindowData
		for a := int(w.Addr); a < int(w.Addr)+int(w.Count); a++ {
			if w.FC == modbus.FCReadCoils || w.FC == modbus.FCReadDiscreteInputs {
				d.Bits = append(d.Bits, benchserver.BitValue(uint16(a)))
			} else {
				d.Registers = append(d.Registers, benchserver.RegisterValue(uint16(a)))
			}
		}
		data = append(data, d)
	}
	return data
}

func BenchmarkDecode(b *testing.B) {
	for _, set := range benchserver.ReadSets() {
		items := setItems(set)
		for _, st := range strategies {
			b.Run(set.Name+"/"+st.name, func(b *testing.B) {
				p, err := batch.Compile(items, st.opts)
				if err != nil {
					b.Fatal(err)
				}
				data := windowData(p)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := p.Decode(data); err != nil {
						b.Fatal(err)
					}
				}
				b.StopTimer()
				reportPlan(b, p)
			})
		}
	}
}
