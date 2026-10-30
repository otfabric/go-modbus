// SPDX-License-Identifier: MIT

package batch_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/otfabric/go-modbus"
	"github.com/otfabric/go-modbus/batch"
	"github.com/otfabric/go-modbus/codec"
)

// seq returns n registers counting up from start.
func seq(start uint16, n int) []uint16 {
	out := make([]uint16, n)
	for i := range out {
		out[i] = start + uint16(i)
	}
	return out
}

// failCodec decodes like float32/layout:4321, but fails on a first register
// of 0xFFFF.
type failCodec struct{ codec.RuntimeDecoder }

func (c failCodec) DecodeRegistersAny(regs []uint16) (any, error) {
	if len(regs) > 0 && regs[0] == 0xFFFF {
		return nil, errors.New("failCodec: rejected")
	}
	return c.RuntimeDecoder.DecodeRegistersAny(regs)
}

func TestDecode_States(t *testing.T) {
	f32 := codec.MustRuntimeCodecByID("float32/layout:4321")
	fail := failCodec{f32}
	p, err := batch.Compile([]batch.Item{
		{Name: "value", UnitID: 1, FC: fc03, Addr: 0, Codec: f32},
		{Name: "rejected", UnitID: 1, FC: fc03, Addr: 2, Codec: fail},
		{Name: "raw", UnitID: 1, FC: fc03, Addr: 4, Count: 1},
		{Name: "unit2", UnitID: 2, FC: fc03, Addr: 0, Count: 2},
		{Name: "unit3", UnitID: 3, FC: fc03, Addr: 0, Count: 2},
	}, batch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(p.Windows()); n != 3 {
		t.Fatalf("%d windows, want 3", n)
	}
	exc := &modbus.ExceptionError{FunctionCode: fc03, ExceptionCode: 0x02, Sentinel: modbus.ErrIllegalDataAddress}
	notAttempted := fmt.Errorf("%w: %w", batch.ErrNotAttempted, context.Canceled)
	regs := []uint16{0x4048, 0xF5C3, 0xFFFF, 0x0000, 7}
	res, err := p.Decode([]batch.WindowData{
		{Registers: regs},
		{Err: exc},
		{Err: notAttempted},
	})
	if err != nil {
		t.Fatal(err)
	}

	wantWindows := []batch.State{batch.OK, batch.Failed, batch.NotAttempted}
	for i, w := range res.Windows {
		if w.State != wantWindows[i] {
			t.Errorf("window %d state = %v, want %v", i, w.State, wantWindows[i])
		}
	}
	if !reflect.DeepEqual(res.Windows[0].Registers, regs) || res.Windows[0].Err != nil {
		t.Errorf("window 0 = %+v", res.Windows[0])
	}
	if res.Windows[1].Registers != nil || !errors.Is(res.Windows[1].Err, modbus.ErrIllegalDataAddress) {
		t.Errorf("window 1 = %+v", res.Windows[1])
	}

	byName := map[string]batch.ItemResult{}
	for i, ir := range res.Items {
		if ir.Name != p.Items()[i].Name {
			t.Fatalf("item %d is %q, want %q: results must follow item order", i, ir.Name, p.Items()[i].Name)
		}
		byName[ir.Name] = ir
	}

	want, _ := f32.DecodeRegistersAny([]uint16{0x4048, 0xF5C3})
	if v := byName["value"]; v.State != batch.OK || v.Value != want || v.Err != nil {
		t.Errorf("value = %+v, want OK %v", v, want)
	}
	if v := byName["rejected"]; v.State != batch.DecodeFailed || v.Value != nil || v.Err == nil || !reflect.DeepEqual(v.Registers, []uint16{0xFFFF, 0}) {
		t.Errorf("rejected = %+v, want DecodeFailed with its registers", v)
	}
	if v := byName["raw"]; v.State != batch.OK || v.Value != nil || !reflect.DeepEqual(v.Registers, []uint16{7}) {
		t.Errorf("raw = %+v", v)
	}

	u2 := byName["unit2"]
	var we *batch.WindowError
	if u2.State != batch.Failed || !errors.As(u2.Err, &we) || we.Window != 1 || !errors.Is(u2.Err, modbus.ErrIllegalDataAddress) || u2.Registers != nil {
		t.Errorf("unit2 = %+v, want Failed with a *WindowError for window 1", u2)
	}
	u3 := byName["unit3"]
	if u3.State != batch.NotAttempted || !errors.Is(u3.Err, batch.ErrNotAttempted) || !errors.Is(u3.Err, context.Canceled) {
		t.Errorf("unit3 = %+v, want NotAttempted wrapping the cancellation", u3)
	}
}

func TestDecode_SplitItemKeepsPrefix(t *testing.T) {
	p, err := batch.Compile([]batch.Item{item("big", 1, fc04, 10, 300)}, batch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	all := seq(10, 300)

	res, err := p.Decode([]batch.WindowData{{Registers: all[:125]}, {Registers: all[125:250]}, {Registers: all[250:]}})
	if err != nil {
		t.Fatal(err)
	}
	if ir := res.Items[0]; ir.State != batch.OK || !reflect.DeepEqual(ir.Registers, all) {
		t.Errorf("whole item: state %v, %d registers", ir.State, len(ir.Registers))
	}

	res, err = p.Decode([]batch.WindowData{
		{Registers: all[:125]},
		{Err: modbus.ErrRequestTimedOut},
		{Err: batch.ErrNotAttempted},
	})
	if err != nil {
		t.Fatal(err)
	}
	ir := res.Items[0]
	var we *batch.WindowError
	if ir.State != batch.Failed || !reflect.DeepEqual(ir.Registers, all[:125]) || !errors.As(ir.Err, &we) || we.Window != 1 || !errors.Is(ir.Err, modbus.ErrRequestTimedOut) {
		t.Errorf("failed item = state %v, %d registers, err %v; want Failed with the 125-register prefix", ir.State, len(ir.Registers), ir.Err)
	}

	res, err = p.Decode([]batch.WindowData{{Err: batch.ErrNotAttempted}, {Err: batch.ErrNotAttempted}, {Err: batch.ErrNotAttempted}})
	if err != nil {
		t.Fatal(err)
	}
	if ir := res.Items[0]; ir.State != batch.NotAttempted || ir.Registers != nil {
		t.Errorf("not attempted item = %+v", ir)
	}
}

func TestDecode_Bits(t *testing.T) {
	p, err := batch.Compile([]batch.Item{
		item("a", 1, fc01, 0, 3),
		item("b", 1, fc01, 3, 2),
		item("di", 1, fc02, 100, 4),
	}, batch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	coils := []bool{true, false, true, true, false}
	inputs := []bool{false, true, false, true}
	res, err := p.Decode([]batch.WindowData{{Bits: coils}, {Bits: inputs}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]bool{"a": coils[:3], "b": coils[3:], "di": inputs}
	for _, ir := range res.Items {
		if ir.State != batch.OK || !reflect.DeepEqual(ir.Bits, want[ir.Name]) || ir.Registers != nil {
			t.Errorf("%s = %+v, want bits %v", ir.Name, ir, want[ir.Name])
		}
	}
}

func TestDecode_SplitBitItem(t *testing.T) {
	p, err := batch.Compile([]batch.Item{item("coils", 1, fc01, 0, 5)}, batch.Options{MaxBits: 2})
	if err != nil {
		t.Fatal(err)
	}
	bits := []bool{true, false, false, true, true}
	res, err := p.Decode([]batch.WindowData{{Bits: bits[:2]}, {Bits: bits[2:4]}, {Bits: bits[4:]}})
	if err != nil {
		t.Fatal(err)
	}
	if ir := res.Items[0]; ir.State != batch.OK || !reflect.DeepEqual(ir.Bits, bits) {
		t.Errorf("item = %+v, want bits %v", ir, bits)
	}
	res, err = p.Decode([]batch.WindowData{{Bits: bits[:2]}, {Err: modbus.ErrRequestTimedOut}, {Err: batch.ErrNotAttempted}})
	if err != nil {
		t.Fatal(err)
	}
	if ir := res.Items[0]; ir.State != batch.Failed || !reflect.DeepEqual(ir.Bits, bits[:2]) {
		t.Errorf("item = %+v, want Failed with the first two bits", ir)
	}
}

func TestDecode_OverlapAndGap(t *testing.T) {
	p, err := batch.Compile([]batch.Item{
		item("a", 1, fc03, 100, 4),
		item("b", 1, fc03, 102, 4),
		item("c", 1, fc03, 110, 2),
	}, batch.Options{MaxGap: 4})
	if err != nil {
		t.Fatal(err)
	}
	regs := seq(100, 12)
	res, err := p.Decode([]batch.WindowData{{Registers: regs}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]uint16{"a": seq(100, 4), "b": seq(102, 4), "c": seq(110, 2)}
	for _, ir := range res.Items {
		if !reflect.DeepEqual(ir.Registers, want[ir.Name]) {
			t.Errorf("%s = %v, want %v", ir.Name, ir.Registers, want[ir.Name])
		}
	}
	// An item's slice must not reach into the registers after it.
	a := res.Items[0].Registers
	if cap(a) != len(a) {
		t.Errorf("item a: cap %d, len %d; appending would overwrite b's registers", cap(a), len(a))
	}
}

func TestDecode_CopiesInput(t *testing.T) {
	p, err := batch.Compile([]batch.Item{item("a", 1, fc03, 0, 2)}, batch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	regs := []uint16{1, 2}
	res, err := p.Decode([]batch.WindowData{{Registers: regs}})
	if err != nil {
		t.Fatal(err)
	}
	regs[0] = 99
	if res.Windows[0].Registers[0] != 1 || res.Items[0].Registers[0] != 1 {
		t.Errorf("result changed with its input")
	}
}

func TestDecode_RejectsMalformedData(t *testing.T) {
	p, err := batch.Compile([]batch.Item{item("r", 1, fc03, 0, 2), item("c", 1, fc01, 0, 3)}, batch.Options{})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data []batch.WindowData
	}{
		{"too few windows", []batch.WindowData{{Bits: []bool{true, true, true}}}},
		{"too many windows", []batch.WindowData{{}, {}, {}}},
		{"short registers", []batch.WindowData{{Bits: []bool{true, true, true}}, {Registers: []uint16{1}}}},
		{"long bits", []batch.WindowData{{Bits: []bool{true, true, true, true}}, {Registers: []uint16{1, 2}}}},
		{"registers for a bit window", []batch.WindowData{{Registers: []uint16{1, 2, 3}}, {Registers: []uint16{1, 2}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := p.Decode(tc.data)
			var ve *batch.ValidationError
			if !errors.As(err, &ve) || res != nil {
				t.Errorf("Decode = (%v, %v), want a *ValidationError", res, err)
			}
		})
	}
}

func TestState_String(t *testing.T) {
	for s, want := range map[batch.State]string{
		batch.NotAttempted: "not attempted",
		batch.Failed:       "failed",
		batch.DecodeFailed: "decode failed",
		batch.OK:           "ok",
		batch.State(9):     "State(9)",
	} {
		if got := s.String(); got != want {
			t.Errorf("State(%d).String() = %q, want %q", uint8(s), got, want)
		}
	}
	var zero batch.State
	if zero != batch.NotAttempted {
		t.Error("the zero State must be NotAttempted")
	}
}

func TestWindowError(t *testing.T) {
	err := &batch.WindowError{Window: 3, Err: modbus.ErrIllegalDataAddress}
	if got := err.Error(); got != "batch: window 3: "+modbus.ErrIllegalDataAddress.Error() {
		t.Errorf("Error() = %q", got)
	}
	if !errors.Is(err, modbus.ErrIllegalDataAddress) {
		t.Error("WindowError does not unwrap to its error")
	}
}
