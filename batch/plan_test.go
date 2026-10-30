// SPDX-License-Identifier: MIT

package batch_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/otfabric/go-modbus"
	"github.com/otfabric/go-modbus/batch"
	"github.com/otfabric/go-modbus/codec"
)

const (
	fc01 = modbus.FCReadCoils
	fc02 = modbus.FCReadDiscreteInputs
	fc03 = modbus.FCReadHoldingRegisters
	fc04 = modbus.FCReadInputRegisters
)

func item(name string, unit uint8, fc modbus.FunctionCode, addr, count uint16) batch.Item {
	return batch.Item{Name: name, UnitID: unit, FC: fc, Addr: addr, Count: count}
}

// win describes an expected window by the names of its items.
type win struct {
	unit  uint8
	fc    modbus.FunctionCode
	addr  uint16
	count uint16
	gap   uint16
	items []string
}

func gotWindows(p *batch.Plan) []win {
	items := p.Items()
	var out []win
	for _, w := range p.Windows() {
		g := win{unit: w.UnitID, fc: w.FC, addr: w.Addr, count: w.Count, gap: w.Gap}
		for _, i := range w.Items {
			g.items = append(g.items, items[i].Name)
		}
		out = append(out, g)
	}
	return out
}

func TestCompile_Windows(t *testing.T) {
	var contiguous, gapped, single []batch.Item
	var contiguousNames, gappedNames []string
	for i := uint16(0); i < 32; i++ {
		it := item("c"+itoa(i), 1, fc03, 100+2*i, 2)
		contiguous = append(contiguous, it)
		contiguousNames = append(contiguousNames, it.Name)
	}
	for i := uint16(0); i < 16; i++ {
		it := item("g"+itoa(i), 1, fc03, 1000+4*i, 2)
		gapped = append(gapped, it)
		gappedNames = append(gappedNames, it.Name)
	}
	for i := uint16(0); i < 126; i++ {
		single = append(single, item("s"+itoa(i), 1, fc03, i, 1))
	}
	var singleFirst []string
	for i := uint16(0); i < 125; i++ {
		singleFirst = append(singleFirst, "s"+itoa(i))
	}

	tests := []struct {
		name  string
		items []batch.Item
		opts  batch.Options
		want  []win
	}{
		{name: "no items", items: nil, want: nil},
		{
			name:  "contiguous items share one window",
			items: contiguous,
			want:  []win{{1, fc03, 100, 64, 0, contiguousNames}},
		},
		{
			name:  "gaps are not merged by default",
			items: gapped[:3],
			want: []win{
				{1, fc03, 1000, 2, 0, []string{"g0"}},
				{1, fc03, 1004, 2, 0, []string{"g1"}},
				{1, fc03, 1008, 2, 0, []string{"g2"}},
			},
		},
		{
			name:  "MaxGap merges across gaps",
			items: gapped,
			opts:  batch.Options{MaxGap: 2},
			want:  []win{{1, fc03, 1000, 62, 30, gappedNames}},
		},
		{
			name:  "MaxGap smaller than the gap",
			items: gapped[:2],
			opts:  batch.Options{MaxGap: 1},
			want: []win{
				{1, fc03, 1000, 2, 0, []string{"g0"}},
				{1, fc03, 1004, 2, 0, []string{"g1"}},
			},
		},
		{
			name:  "the register limit splits windows",
			items: single,
			want: []win{
				{1, fc03, 0, 125, 0, singleFirst},
				{1, fc03, 125, 1, 0, []string{"s125"}},
			},
		},
		{
			name:  "MaxRegisters lowers the limit",
			items: contiguous[:4],
			opts:  batch.Options{MaxRegisters: 5},
			want: []win{
				{1, fc03, 100, 4, 0, []string{"c0", "c1"}},
				{1, fc03, 104, 4, 0, []string{"c2", "c3"}},
			},
		},
		{
			name: "overlapping and duplicate items share registers",
			items: []batch.Item{
				item("b", 1, fc03, 12, 4),
				item("a", 1, fc03, 10, 4),
				item("a2", 1, fc03, 10, 4),
				item("inner", 1, fc03, 11, 1),
			},
			want: []win{{1, fc03, 10, 6, 0, []string{"a", "a2", "inner", "b"}}},
		},
		{
			name: "unit IDs and function codes are never merged",
			items: []batch.Item{
				item("u2", 2, fc03, 2, 2),
				item("u1in", 1, fc04, 2, 2),
				item("u1", 1, fc03, 0, 2),
				item("u1b", 1, fc03, 2, 2),
				item("coils", 1, fc01, 0, 8),
				item("inputs", 1, fc02, 8, 8),
			},
			want: []win{
				{1, fc01, 0, 8, 0, []string{"coils"}},
				{1, fc02, 8, 8, 0, []string{"inputs"}},
				{1, fc03, 0, 4, 0, []string{"u1", "u1b"}},
				{1, fc04, 2, 2, 0, []string{"u1in"}},
				{2, fc03, 2, 2, 0, []string{"u2"}},
			},
		},
		{
			name:  "a boundary splits contiguous items",
			items: []batch.Item{item("a", 1, fc03, 0, 2), item("b", 1, fc03, 2, 2)},
			opts:  batch.Options{Boundaries: []batch.Boundary{{UnitID: 1, FC: fc03, Addr: 2}}},
			want: []win{
				{1, fc03, 0, 2, 0, []string{"a"}},
				{1, fc03, 2, 2, 0, []string{"b"}},
			},
		},
		{
			name:  "a boundary of another unit or function code has no effect",
			items: []batch.Item{item("a", 1, fc03, 0, 2), item("b", 1, fc03, 2, 2)},
			opts: batch.Options{Boundaries: []batch.Boundary{
				{UnitID: 2, FC: fc03, Addr: 2},
				{UnitID: 1, FC: fc04, Addr: 2},
			}},
			want: []win{{1, fc03, 0, 4, 0, []string{"a", "b"}}},
		},
		{
			name:  "a boundary stops gap merging",
			items: gapped[:2],
			opts:  batch.Options{MaxGap: 2, Boundaries: []batch.Boundary{{UnitID: 1, FC: fc03, Addr: 1003}}},
			want: []win{
				{1, fc03, 1000, 2, 0, []string{"g0"}},
				{1, fc03, 1004, 2, 0, []string{"g1"}},
			},
		},
		{
			name: "an item larger than the limit is read alone",
			items: []batch.Item{
				item("before", 1, fc03, 0, 2),
				item("big", 1, fc03, 2, 300),
				item("after", 1, fc03, 302, 2),
			},
			want: []win{
				{1, fc03, 0, 2, 0, []string{"before"}},
				{1, fc03, 2, 125, 0, []string{"big"}},
				{1, fc03, 127, 125, 0, []string{"big"}},
				{1, fc03, 252, 50, 0, []string{"big"}},
				{1, fc03, 302, 2, 0, []string{"after"}},
			},
		},
		{
			name:  "bits merge up to 2000",
			items: []batch.Item{item("a", 1, fc01, 0, 1000), item("b", 1, fc01, 1000, 1000), item("c", 1, fc01, 2000, 1)},
			want: []win{
				{1, fc01, 0, 2000, 0, []string{"a", "b"}},
				{1, fc01, 2000, 1, 0, []string{"c"}},
			},
		},
		{
			name:  "MaxBits lowers the bit limit",
			items: []batch.Item{item("a", 1, fc02, 0, 8), item("b", 1, fc02, 8, 8)},
			opts:  batch.Options{MaxBits: 8},
			want: []win{
				{1, fc02, 0, 8, 0, []string{"a"}},
				{1, fc02, 8, 8, 0, []string{"b"}},
			},
		},
		{
			name:  "the last address",
			items: []batch.Item{item("end", 1, fc03, 0xFFFE, 2), item("before", 1, fc03, 0xFFFC, 2)},
			want:  []win{{1, fc03, 0xFFFC, 4, 0, []string{"before", "end"}}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := batch.Compile(tc.items, tc.opts)
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if got := gotWindows(p); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("windows:\n got %+v\nwant %+v", got, tc.want)
			}
			checkInvariants(t, tc.items, tc.opts, p)
		})
	}
}

func TestCompile_Rejects(t *testing.T) {
	f32 := codec.MustRuntimeCodecByID("float32/layout:4321")
	tests := []struct {
		name   string
		items  []batch.Item
		opts   batch.Options
		index  int
		reason string
	}{
		{"empty name", []batch.Item{item("", 1, fc03, 0, 1)}, batch.Options{}, 0, "name is empty"},
		{"duplicate name", []batch.Item{item("a", 1, fc03, 0, 1), item("a", 1, fc03, 5, 1)}, batch.Options{}, 1, "duplicate name"},
		{"write function code", []batch.Item{item("a", 1, modbus.FCWriteSingleRegister, 0, 1)}, batch.Options{}, 0, "not a read"},
		{"zero function code", []batch.Item{item("a", 1, 0, 0, 1)}, batch.Options{}, 0, "not a read"},
		{"zero count", []batch.Item{item("a", 1, fc03, 0, 0)}, batch.Options{}, 0, "count is zero"},
		{"past 0xFFFF", []batch.Item{item("a", 1, fc03, 0xFFFE, 3)}, batch.Options{}, 0, "exceeds 0xFFFF"},
		{"bits past 0xFFFF", []batch.Item{item("a", 1, fc01, 0xFFF0, 17)}, batch.Options{}, 0, "exceeds 0xFFFF"},
		{"codec on coils", []batch.Item{{Name: "a", UnitID: 1, FC: fc01, Codec: f32}}, batch.Options{}, 0, "only allowed on register reads"},
		{"codec on discrete inputs", []batch.Item{{Name: "a", UnitID: 1, FC: fc02, Count: 2, Codec: f32}}, batch.Options{}, 0, "only allowed on register reads"},
		{"codec count mismatch", []batch.Item{{Name: "a", UnitID: 1, FC: fc03, Count: 3, Codec: f32}}, batch.Options{}, 0, "differs from the 2 registers"},
		{"zero-register codec", []batch.Item{{Name: "a", UnitID: 1, FC: fc03, Codec: zeroCodec{}}}, batch.Options{}, 0, "register count of zero"},
		{
			"item spans a boundary",
			[]batch.Item{item("a", 1, fc03, 1, 2)},
			batch.Options{Boundaries: []batch.Boundary{{UnitID: 1, FC: fc03, Addr: 2}}},
			0, "spans the boundary at address 2",
		},
		{"MaxRegisters too large", nil, batch.Options{MaxRegisters: 126}, -1, "MaxRegisters 126 exceeds 125"},
		{"MaxBits too large", nil, batch.Options{MaxBits: 2001}, -1, "MaxBits 2001 exceeds 2000"},
		{"boundary with a write code", nil, batch.Options{Boundaries: []batch.Boundary{{FC: modbus.FCWriteMultipleRegisters}}}, -1, "boundary 0"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := batch.Compile(tc.items, tc.opts)
			var ve *batch.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("Compile = (%v, %v), want a *ValidationError", p, err)
			}
			if p != nil {
				t.Errorf("Compile returned a plan with an error")
			}
			if ve.Index != tc.index || !strings.Contains(ve.Reason, tc.reason) {
				t.Errorf("error = %+v (%q), want index %d and a reason containing %q", ve, err, tc.index, tc.reason)
			}
		})
	}
}

func TestCompile_CodecSetsCount(t *testing.T) {
	f32 := codec.MustRuntimeCodecByID("float32/layout:4321")
	p, err := batch.Compile([]batch.Item{
		{Name: "derived", UnitID: 1, FC: fc03, Addr: 10, Codec: f32},
		{Name: "explicit", UnitID: 1, FC: fc03, Addr: 12, Count: 2, Codec: f32},
	}, batch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Items()[0].Count; got != 2 {
		t.Errorf("derived count = %d, want 2", got)
	}
	if got := gotWindows(p); !reflect.DeepEqual(got, []win{{1, fc03, 10, 4, 0, []string{"derived", "explicit"}}}) {
		t.Errorf("windows = %+v", got)
	}
}

func TestPlan_AccessorsReturnCopies(t *testing.T) {
	items := []batch.Item{item("a", 1, fc03, 0, 2), item("b", 1, fc03, 2, 2)}
	p, err := batch.Compile(items, batch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	items[0].Addr = 99 // the plan keeps its own copy
	w := p.Windows()
	w[0].Items[0] = 7
	w[0].Addr = 50
	it := p.Items()
	it[1].Name = "changed"
	if got := gotWindows(p); !reflect.DeepEqual(got, []win{{1, fc03, 0, 4, 0, []string{"a", "b"}}}) {
		t.Errorf("plan changed through its accessors: %+v", got)
	}
}

func TestPlan_StatsAndFunctionCodes(t *testing.T) {
	p, err := batch.Compile([]batch.Item{
		item("h1", 1, fc03, 0, 2),
		item("h2", 1, fc03, 4, 2),
		item("c", 2, fc01, 0, 10),
		item("i", 1, fc04, 0, 1),
	}, batch.Options{MaxGap: 2})
	if err != nil {
		t.Fatal(err)
	}
	want := batch.Stats{
		Requests:  3,
		Requested: 2 + 2 + 10 + 1,
		Read:      6 + 10 + 1,
		// 5 bytes per request; 2 + data bytes per response.
		RequestBytes:  15,
		ResponseBytes: (2 + 12) + (2 + 2) + (2 + 2),
	}
	if got := p.Stats(); got != want {
		t.Errorf("Stats = %+v, want %+v", got, want)
	}
	if got := p.FunctionCodes(); !reflect.DeepEqual(got, []modbus.FunctionCode{fc01, fc03, fc04}) {
		t.Errorf("FunctionCodes = %v", got)
	}
}

func TestValidationError_Error(t *testing.T) {
	if got := (&batch.ValidationError{Item: "x", Index: 2, Reason: "bad"}).Error(); got != `batch: item 2 ("x"): bad` {
		t.Errorf("item error = %q", got)
	}
	if got := (&batch.ValidationError{Index: -1, Reason: "bad"}).Error(); got != "batch: bad" {
		t.Errorf("plan error = %q", got)
	}
}

// zeroCodec is a decoder that claims zero registers.
type zeroCodec struct{}

func (zeroCodec) ID() string                       { return "zero" }
func (zeroCodec) Name() string                     { return "zero" }
func (zeroCodec) RegisterSpec() codec.RegisterSpec { return codec.RegisterSpec{} }
func (zeroCodec) ByteSpec() codec.ByteSpec         { return codec.ByteSpec{} }
func (zeroCodec) ValueKind() codec.CodecValueKind  { return codec.CodecValueKind(0) }
func (zeroCodec) DecodeRegistersAny([]uint16) (any, error) {
	return nil, errors.New("zero codec")
}

func itoa(n uint16) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
