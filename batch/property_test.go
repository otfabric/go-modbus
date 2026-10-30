// SPDX-License-Identifier: MIT

package batch_test

import (
	"encoding/binary"
	"errors"
	"math/rand"
	"reflect"
	"slices"
	"testing"

	"github.com/otfabric/go-modbus"
	"github.com/otfabric/go-modbus/batch"
)

var readFCs = []modbus.FunctionCode{fc01, fc02, fc03, fc04}

func limitOf(fc modbus.FunctionCode, opts batch.Options) int {
	if fc == fc01 || fc == fc02 {
		if opts.MaxBits != 0 {
			return int(opts.MaxBits)
		}
		return modbus.MaxReadCoils
	}
	if opts.MaxRegisters != 0 {
		return int(opts.MaxRegisters)
	}
	return modbus.MaxReadRegisters
}

// checkInvariants checks what every compiled plan must satisfy, whatever
// its items.
func checkInvariants(t *testing.T, given []batch.Item, opts batch.Options, p *batch.Plan) {
	t.Helper()
	items := p.Items()
	if len(items) != len(given) {
		t.Fatalf("%d items in the plan, %d given", len(items), len(given))
	}
	windows := p.Windows()
	served := make([][]int, len(items))
	read := 0
	for wi, w := range windows {
		first, last := int(w.Addr), int(w.Addr)+int(w.Count)-1
		read += int(w.Count)
		if w.Count == 0 || int(w.Count) > limitOf(w.FC, opts) || last > 0xFFFF {
			t.Fatalf("window %d %+v: count outside 1..%d or past 0xFFFF", wi, w, limitOf(w.FC, opts))
		}
		for _, b := range opts.Boundaries {
			if b.UnitID == w.UnitID && b.FC == w.FC && int(b.Addr) > first && int(b.Addr) <= last {
				t.Fatalf("window %d %+v spans the boundary at %d", wi, w, b.Addr)
			}
		}
		if len(w.Items) == 0 {
			t.Fatalf("window %d serves no item", wi)
		}
		// Coverage of the window by its items, to check Gap and MaxGap.
		covered := make([]bool, w.Count)
		for _, i := range w.Items {
			it := items[i]
			if it.UnitID != w.UnitID || it.FC != w.FC {
				t.Fatalf("window %d %+v serves item %+v of another unit or function code", wi, w, it)
			}
			from, to := max(int(it.Addr), first), min(int(it.Addr)+int(it.Count)-1, last)
			if from > to {
				t.Fatalf("window %d %+v serves item %+v outside it", wi, w, it)
			}
			for a := from; a <= to; a++ {
				covered[a-first] = true
			}
			served[i] = append(served[i], wi)
		}
		gap, run := 0, 0
		for _, c := range covered {
			if c {
				run = 0
				continue
			}
			gap++
			run++
			if run > int(opts.MaxGap) {
				t.Fatalf("window %d %+v has a gap of %d, MaxGap %d", wi, w, run, opts.MaxGap)
			}
		}
		if gap != int(w.Gap) {
			t.Fatalf("window %d %+v: Gap %d, counted %d", wi, w, w.Gap, gap)
		}
	}
	for i, it := range items {
		if given[i].Codec == nil && it.Count != given[i].Count {
			t.Fatalf("item %d count %d, given %d", i, it.Count, given[i].Count)
		}
		ws := served[i]
		limit := limitOf(it.FC, opts)
		if int(it.Count) <= limit {
			if len(ws) != 1 {
				t.Fatalf("item %d %+v within the limit is served by %d windows", i, it, len(ws))
			}
			w := windows[ws[0]]
			if it.Addr < w.Addr || int(it.Addr)+int(it.Count) > int(w.Addr)+int(w.Count) {
				t.Fatalf("item %d %+v is not inside its window %+v", i, it, w)
			}
			continue
		}
		// An oversized item: consecutive windows of its own covering it exactly.
		next := int(it.Addr)
		for _, wi := range ws {
			w := windows[wi]
			if int(w.Addr) != next || len(w.Items) != 1 {
				t.Fatalf("oversized item %d %+v: window %+v", i, it, w)
			}
			next += int(w.Count)
		}
		if next != int(it.Addr)+int(it.Count) {
			t.Fatalf("oversized item %d %+v covered up to %d", i, it, next)
		}
	}
	s := p.Stats()
	if s.Requests != len(windows) || s.Read != read || s.RequestBytes != 5*len(windows) {
		t.Fatalf("Stats %+v do not match %d windows reading %d", s, len(windows), read)
	}
}

// randomBatch returns random items and options; they may be invalid.
func randomBatch(r *rand.Rand, n int) ([]batch.Item, batch.Options) {
	var opts batch.Options
	if r.Intn(2) == 0 {
		opts.MaxGap = uint16(r.Intn(6))
	}
	if r.Intn(3) == 0 {
		opts.MaxRegisters = uint16(1 + r.Intn(125))
	}
	if r.Intn(3) == 0 {
		opts.MaxBits = uint16(1 + r.Intn(64))
	}
	for i := r.Intn(3); i > 0; i-- {
		opts.Boundaries = append(opts.Boundaries, batch.Boundary{
			UnitID: uint8(1 + r.Intn(2)), FC: readFCs[r.Intn(4)], Addr: uint16(r.Intn(300)),
		})
	}
	base := 0
	if r.Intn(8) == 0 {
		base = 0xFFFF - 300
	}
	items := make([]batch.Item, n)
	for i := range items {
		count := 1 + r.Intn(8)
		if r.Intn(10) == 0 {
			count = 1 + r.Intn(300)
		}
		items[i] = batch.Item{
			Name:   "i" + itoa(uint16(i)),
			UnitID: uint8(1 + r.Intn(2)),
			FC:     readFCs[r.Intn(4)],
			Addr:   uint16(base + r.Intn(300)),
			Count:  uint16(count),
		}
	}
	return items, opts
}

// TestCompile_Properties checks the invariants and order independence on
// random batches.
func TestCompile_Properties(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	valid := 0
	for iter := 0; iter < 5000; iter++ {
		items, opts := randomBatch(r, 1+r.Intn(16))
		p, err := batch.Compile(items, opts)
		if err != nil {
			var ve *batch.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("Compile error %v is not a *ValidationError", err)
			}
			continue
		}
		valid++
		checkInvariants(t, items, opts, p)

		shuffled := append([]batch.Item(nil), items...)
		r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		q, err := batch.Compile(shuffled, opts)
		if err != nil {
			t.Fatalf("shuffled items rejected: %v", err)
		}
		if a, b := windowSets(p), windowSets(q); !reflect.DeepEqual(a, b) {
			t.Fatalf("item order changed the windows:\n%+v\n%+v", a, b)
		}
	}
	if valid < 1000 {
		t.Fatalf("only %d of 5000 random batches were valid", valid)
	}
}

// windowSets describes the windows independently of item order: the items
// of each window as a set of names.
func windowSets(p *batch.Plan) []win {
	ws := gotWindows(p)
	for i := range ws {
		slices.Sort(ws[i].items)
	}
	return ws
}

// TestCompile_Optimal compares the number of windows with a brute-force
// minimum on small batches of one unit and function code, items within the
// limit.
func TestCompile_Optimal(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	for iter := 0; iter < 2000; iter++ {
		n := 1 + r.Intn(7)
		opts := batch.Options{MaxGap: uint16(r.Intn(4)), MaxRegisters: uint16(4 + r.Intn(12))}
		if r.Intn(2) == 0 {
			opts.Boundaries = []batch.Boundary{{UnitID: 1, FC: fc03, Addr: uint16(r.Intn(40))}}
		}
		var items []batch.Item
		for i := 0; i < n; i++ {
			it := item("i"+itoa(uint16(i)), 1, fc03, uint16(r.Intn(40)), uint16(1+r.Intn(int(opts.MaxRegisters))))
			items = append(items, it)
		}
		p, err := batch.Compile(items, opts)
		if err != nil {
			continue // an item across the boundary
		}
		if got, want := len(p.Windows()), minWindows(items, opts); got != want {
			t.Fatalf("items %+v, options %+v: %d windows, minimum %d", items, opts, got, want)
		}
	}
}

// minWindows returns the fewest windows that can serve items, by trying
// every partition of the items into groups.
func minWindows(items []batch.Item, opts batch.Options) int {
	best := len(items)
	groups := make([][]batch.Item, 0, len(items))
	var assign func(i int)
	assign = func(i int) {
		if len(groups) >= best {
			return
		}
		if i == len(items) {
			for _, g := range groups {
				if !groupFits(g, opts) {
					return
				}
			}
			best = len(groups)
			return
		}
		for g := range groups {
			groups[g] = append(groups[g], items[i])
			assign(i + 1)
			groups[g] = groups[g][:len(groups[g])-1]
		}
		groups = append(groups, []batch.Item{items[i]})
		assign(i + 1)
		groups = groups[:len(groups)-1]
	}
	assign(0)
	return best
}

// groupFits reports whether one window can serve the items of g.
func groupFits(g []batch.Item, opts batch.Options) bool {
	first, last := 0x10000, -1
	for _, it := range g {
		first = min(first, int(it.Addr))
		last = max(last, int(it.Addr)+int(it.Count)-1)
	}
	if last-first+1 > int(opts.MaxRegisters) {
		return false
	}
	for _, b := range opts.Boundaries {
		if int(b.Addr) > first && int(b.Addr) <= last {
			return false
		}
	}
	covered := make([]bool, last-first+1)
	for _, it := range g {
		for a := int(it.Addr); a < int(it.Addr)+int(it.Count); a++ {
			covered[a-first] = true
		}
	}
	run := 0
	for _, c := range covered {
		if c {
			run = 0
		} else if run++; run > int(opts.MaxGap) {
			return false
		}
	}
	return true
}

// FuzzCompile checks the invariants on batches decoded from fuzz input.
func FuzzCompile(f *testing.F) {
	f.Add([]byte{0, 1, 3, 0, 100, 0, 2, 1, 1, 3, 0, 102, 0, 2})
	f.Add([]byte{2, 1, 1, 0xFF, 0xF0, 0, 16, 1, 2, 4, 0, 0, 1, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 1 {
			return
		}
		opts := batch.Options{MaxGap: uint16(data[0] % 8)}
		data = data[1:]
		var items []batch.Item
		// Each item: unit, function code, address (2 bytes), count (2 bytes).
		for i := 0; len(data) >= 6 && i < 64; i++ {
			items = append(items, batch.Item{
				Name:   "i" + itoa(uint16(i)),
				UnitID: data[0] % 3,
				FC:     modbus.FunctionCode(data[1] % 6),
				Addr:   binary.BigEndian.Uint16(data[2:4]),
				Count:  binary.BigEndian.Uint16(data[4:6]) % 400,
			})
			data = data[6:]
		}
		p, err := batch.Compile(items, opts)
		if err != nil {
			var ve *batch.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("Compile error %v is not a *ValidationError", err)
			}
			return
		}
		checkInvariants(t, items, opts, p)
	})
}
