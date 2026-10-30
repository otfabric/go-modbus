// SPDX-License-Identifier: MIT

package batch

import (
	"fmt"
	"slices"

	"github.com/otfabric/go-modbus"
)

// Window is one read request of a plan.
type Window struct {
	UnitID uint8
	FC     modbus.FunctionCode
	// Addr and Count are the first address and the number of registers or
	// bits the request reads.
	Addr  uint16
	Count uint16
	// Items holds the indexes, in Plan.Items, of the items the window
	// serves, in address order. An item larger than one request is served
	// by several consecutive windows.
	Items []int
	// Gap is the number of registers or bits read that no item asked for.
	Gap uint16
}

// Stats summarizes the requests a plan makes.
type Stats struct {
	// Requests is the number of windows.
	Requests int
	// Requested is the number of registers and bits the windows read that
	// some item asked for. An address read by several windows counts once
	// per window.
	Requested int
	// Read is the number of registers and bits the windows read, gaps
	// included.
	Read int
	// RequestBytes and ResponseBytes are the protocol data unit (PDU) bytes
	// of all requests and of all successful responses, transport framing
	// excluded.
	RequestBytes  int
	ResponseBytes int
}

// Plan is a compiled batch. It is immutable and safe for concurrent use.
type Plan struct {
	items   []Item
	windows []Window
	// itemWindows holds, per item, the indexes of the windows serving it,
	// in address order.
	itemWindows [][]int
	// continueOnTransportError is Options.ContinueOnTransportError.
	continueOnTransportError bool
}

// Compile validates items and groups them into windows. It performs no I/O.
//
// Items are grouped by unit ID and function code, and each group is swept
// in address order: an item joins the current window when it starts at most
// opts.MaxGap addresses after the window's end, the merged window stays
// within the limit and no boundary falls inside it. An item larger than the
// limit gets consecutive windows of its own. Windows are ordered by unit ID,
// function code and address, so the result does not depend on the order of
// items.
//
// Compile returns a *ValidationError for invalid options or items: an empty
// or duplicate name, a function code other than FC01-FC04, a codec on a bit
// item or with a count that disagrees with Count, a zero count, a range past
// address 0xFFFF, or an item that spans a boundary.
func Compile(items []Item, opts Options) (*Plan, error) {
	maxRegs, maxBits, err := limits(opts)
	if err != nil {
		return nil, err
	}
	for i, b := range opts.Boundaries {
		if !isReadFC(b.FC) {
			return nil, &ValidationError{Index: -1, Reason: fmt.Sprintf("boundary %d: function code %s is not a read of coils, discrete inputs or registers", i, b.FC)}
		}
	}

	p := &Plan{items: make([]Item, len(items)), itemWindows: make([][]int, len(items)), continueOnTransportError: opts.ContinueOnTransportError}
	names := make(map[string]bool, len(items))
	for i, it := range items {
		if err := validateItem(i, &it, names, opts.Boundaries); err != nil {
			return nil, err
		}
		p.items[i] = it
	}

	type groupKey struct {
		unit uint8
		fc   modbus.FunctionCode
	}
	groups := make(map[groupKey][]int)
	var keys []groupKey
	for i, it := range p.items {
		k := groupKey{it.UnitID, it.FC}
		if _, ok := groups[k]; !ok {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], i)
	}
	slices.SortFunc(keys, func(a, b groupKey) int {
		if a.unit != b.unit {
			return int(a.unit) - int(b.unit)
		}
		return int(a.fc) - int(b.fc)
	})

	for _, k := range keys {
		limit := maxRegs
		if isBitFC(k.fc) {
			limit = maxBits
		}
		var bounds []uint16
		for _, b := range opts.Boundaries {
			if b.UnitID == k.unit && b.FC == k.fc {
				bounds = append(bounds, b.Addr)
			}
		}
		p.sweep(k.unit, k.fc, groups[k], limit, opts.MaxGap, bounds)
	}
	return p, nil
}

// limits returns the register and bit window limits opts selects.
func limits(opts Options) (maxRegs, maxBits int, err error) {
	maxRegs, maxBits = modbus.MaxReadRegisters, modbus.MaxReadCoils
	if opts.MaxRegisters != 0 {
		if opts.MaxRegisters > modbus.MaxReadRegisters {
			return 0, 0, &ValidationError{Index: -1, Reason: fmt.Sprintf("MaxRegisters %d exceeds %d", opts.MaxRegisters, modbus.MaxReadRegisters)}
		}
		maxRegs = int(opts.MaxRegisters)
	}
	if opts.MaxBits != 0 {
		if opts.MaxBits > modbus.MaxReadCoils {
			return 0, 0, &ValidationError{Index: -1, Reason: fmt.Sprintf("MaxBits %d exceeds %d", opts.MaxBits, modbus.MaxReadCoils)}
		}
		maxBits = int(opts.MaxBits)
	}
	return maxRegs, maxBits, nil
}

// validateItem checks item i, fills in its count from the codec and records
// its name.
func validateItem(i int, it *Item, names map[string]bool, bounds []Boundary) error {
	bad := func(format string, args ...any) error {
		return &ValidationError{Item: it.Name, Index: i, Reason: fmt.Sprintf(format, args...)}
	}
	if it.Name == "" {
		return bad("name is empty")
	}
	if names[it.Name] {
		return bad("duplicate name")
	}
	names[it.Name] = true
	if !isReadFC(it.FC) {
		return bad("function code %s is not a read of coils, discrete inputs or registers", it.FC)
	}
	if it.Codec != nil {
		if isBitFC(it.FC) {
			return bad("a codec is only allowed on register reads, not on %s", it.FC)
		}
		n := it.Codec.RegisterSpec().Count
		switch {
		case n == 0:
			return bad("codec %s has a register count of zero", it.Codec.ID())
		case it.Count == 0:
			it.Count = n
		case it.Count != n:
			return bad("count %d differs from the %d registers of codec %s", it.Count, n, it.Codec.ID())
		}
	}
	if it.Count == 0 {
		return bad("count is zero")
	}
	last := int(it.Addr) + int(it.Count) - 1
	if last > 0xFFFF {
		return bad("address %d + count %d - 1 exceeds 0xFFFF", it.Addr, it.Count)
	}
	for _, b := range bounds {
		if b.UnitID == it.UnitID && b.FC == it.FC && int(b.Addr) > int(it.Addr) && int(b.Addr) <= last {
			return bad("spans the boundary at address %d", b.Addr)
		}
	}
	return nil
}

// sweep groups the items idx of one unit ID and function code into windows.
func (p *Plan) sweep(unit uint8, fc modbus.FunctionCode, idx []int, limit int, maxGap uint16, bounds []uint16) {
	end := func(i int) int { return int(p.items[i].Addr) + int(p.items[i].Count) - 1 }
	idx = slices.Clone(idx)
	slices.SortFunc(idx, func(a, b int) int {
		if p.items[a].Addr != p.items[b].Addr {
			return int(p.items[a].Addr) - int(p.items[b].Addr)
		}
		if end(a) != end(b) {
			return end(b) - end(a) // the longer item first
		}
		return a - b
	})
	crosses := func(first, last int) bool {
		for _, b := range bounds {
			if int(b) > first && int(b) <= last {
				return true
			}
		}
		return false
	}

	var (
		open        bool
		first, last int   // the current window's address range
		members     []int // its items, in address order
	)
	flush := func() {
		if !open {
			return
		}
		p.addWindow(unit, fc, first, last, members)
		open, members = false, nil
	}
	for _, i := range idx {
		it := p.items[i]
		if int(it.Count) > limit {
			// An item larger than one request is read alone, in
			// consecutive windows.
			flush()
			for off := 0; off < int(it.Count); off += limit {
				n := min(limit, int(it.Count)-off)
				p.addWindow(unit, fc, int(it.Addr)+off, int(it.Addr)+off+n-1, []int{i})
			}
			continue
		}
		if open && int(it.Addr) <= last+1+int(maxGap) {
			newLast := max(last, end(i))
			if newLast-first+1 <= limit && !crosses(first, newLast) {
				last = newLast
				members = append(members, i)
				continue
			}
		}
		flush()
		open, first, last, members = true, int(it.Addr), end(i), []int{i}
	}
	flush()
}

// addWindow appends the window first..last serving members, which are in
// address order.
func (p *Plan) addWindow(unit uint8, fc modbus.FunctionCode, first, last int, members []int) {
	covered, coveredTo := 0, first-1
	for _, i := range members {
		from := max(int(p.items[i].Addr), coveredTo+1)
		to := min(int(p.items[i].Addr)+int(p.items[i].Count)-1, last)
		if to >= from {
			covered += to - from + 1
			coveredTo = to
		}
	}
	w := len(p.windows)
	p.windows = append(p.windows, Window{
		UnitID: unit,
		FC:     fc,
		Addr:   uint16(first),
		Count:  uint16(last - first + 1),
		Items:  members,
		Gap:    uint16(last - first + 1 - covered),
	})
	for _, i := range members {
		p.itemWindows[i] = append(p.itemWindows[i], w)
	}
}

// Items returns the plan's items in the order given to Compile, with every
// Count filled in.
func (p *Plan) Items() []Item { return slices.Clone(p.items) }

// Windows returns the plan's read requests, in the order they are made.
func (p *Plan) Windows() []Window {
	out := make([]Window, len(p.windows))
	for i, w := range p.windows {
		w.Items = slices.Clone(w.Items)
		out[i] = w
	}
	return out
}

// FunctionCodes returns the distinct function codes of the plan's windows,
// in ascending order: the codes a client must allow, for example in
// modbus.Config.AllowedFunctionCodes.
func (p *Plan) FunctionCodes() []modbus.FunctionCode {
	var out []modbus.FunctionCode
	for _, w := range p.windows {
		if !slices.Contains(out, w.FC) {
			out = append(out, w.FC)
		}
	}
	slices.Sort(out)
	return out
}

// Stats returns the plan's request and byte counts.
func (p *Plan) Stats() Stats {
	var s Stats
	for _, w := range p.windows {
		n := int(w.Count)
		s.Requests++
		s.Read += n
		s.Requested += n - int(w.Gap)
		// Request: function code, address, quantity. Response: function
		// code, byte count, data.
		s.RequestBytes += 5
		if isBitFC(w.FC) {
			s.ResponseBytes += 2 + (n+7)/8
		} else {
			s.ResponseBytes += 2 + 2*n
		}
	}
	return s
}
