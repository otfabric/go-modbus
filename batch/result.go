// SPDX-License-Identifier: MIT

package batch

import (
	"errors"
	"fmt"
	"time"

	"github.com/otfabric/go-modbus/codec"
)

// State is the outcome of one window or one item. The zero value is
// NotAttempted.
type State uint8

const (
	// NotAttempted: the window was not read, because the batch stopped or
	// was cancelled before it.
	NotAttempted State = iota
	// Failed: the request was made and failed.
	Failed
	// DecodeFailed: the item was read, but its codec rejected the
	// registers. Items only.
	DecodeFailed
	// OK: read, and decoded when the item has a codec.
	OK
)

func (s State) String() string {
	switch s {
	case NotAttempted:
		return "not attempted"
	case Failed:
		return "failed"
	case DecodeFailed:
		return "decode failed"
	case OK:
		return "ok"
	default:
		return fmt.Sprintf("State(%d)", uint8(s))
	}
}

// WindowData is what reading one window returned: the registers or bits on
// success, or the error.
type WindowData struct {
	// Registers holds the window's registers (FC03, FC04).
	Registers []uint16
	// Bits holds the window's coils or discrete inputs (FC01, FC02).
	Bits []bool
	// Err is the read error, nil on success. An error wrapping
	// ErrNotAttempted marks a window that was not read.
	Err error
	// Started and Finished are when the request was made and when its
	// response or error came back. Both are zero for offline data.
	Started, Finished time.Time
}

// Result holds the outcome of every window and item of a plan. Slices in a
// Result may share storage with each other.
type Result struct {
	// Started and Finished bound the whole batch. Both are zero for a result
	// decoded offline.
	Started, Finished time.Time
	// Windows holds one result per window, in Plan.Windows order.
	Windows []WindowResult
	// Items holds one result per item, in Plan.Items order.
	Items []ItemResult
}

// WindowResult is the outcome of one window.
type WindowResult struct {
	Window
	// State is OK, Failed or NotAttempted.
	State State
	// Started and Finished are when the request was made and when its
	// response or error came back; zero when the window was not attempted
	// or the data was decoded offline.
	Started, Finished time.Time
	// Registers or Bits hold the data read; nil unless State is OK.
	Registers []uint16
	Bits      []bool
	// Err is the read error; nil when State is OK.
	Err error
}

// ItemResult is the outcome of one item.
type ItemResult struct {
	Name  string
	State State
	// Registers or Bits hold the item's data. For an item read in several
	// windows of which one failed, they hold the part read before the first
	// window that is not OK.
	Registers []uint16
	Bits      []bool
	// Value is the decoded value; set when State is OK and the item has a
	// codec.
	Value any
	// Err is a *WindowError when a window serving the item is not OK, or the
	// codec's error when State is DecodeFailed.
	Err error
}

// Decode builds the result of the plan from the data of its windows, one
// WindowData per window in Plan.Windows order: from a capture, or in tests.
//
// A window whose data has no error must hold exactly its count of registers
// or bits; Decode returns a *ValidationError otherwise, and for a data slice
// of the wrong length. A window error marks the window Failed, or
// NotAttempted when it wraps ErrNotAttempted.
func (p *Plan) Decode(data []WindowData) (*Result, error) {
	if len(data) != len(p.windows) {
		return nil, &ValidationError{Index: -1, Reason: fmt.Sprintf("%d window data for %d windows", len(data), len(p.windows))}
	}
	res := &Result{Windows: make([]WindowResult, len(p.windows))}
	for i, w := range p.windows {
		d := data[i]
		wr := WindowResult{Window: w, Started: d.Started, Finished: d.Finished}
		wr.Items = append([]int(nil), w.Items...)
		switch {
		case errors.Is(d.Err, ErrNotAttempted):
			wr.State, wr.Err = NotAttempted, d.Err
		case d.Err != nil:
			wr.State, wr.Err = Failed, d.Err
		case isBitFC(w.FC):
			if len(d.Bits) != int(w.Count) {
				return nil, &ValidationError{Index: -1, Reason: fmt.Sprintf("window %d: %d bits for a window of %d", i, len(d.Bits), w.Count)}
			}
			wr.State, wr.Bits = OK, append([]bool(nil), d.Bits...)
		default:
			if len(d.Registers) != int(w.Count) {
				return nil, &ValidationError{Index: -1, Reason: fmt.Sprintf("window %d: %d registers for a window of %d", i, len(d.Registers), w.Count)}
			}
			wr.State, wr.Registers = OK, append([]uint16(nil), d.Registers...)
		}
		res.Windows[i] = wr
	}
	res.Items = p.itemResults(res.Windows)
	return res, nil
}

// itemResults derives the result of every item from the window results.
func (p *Plan) itemResults(windows []WindowResult) []ItemResult {
	out := make([]ItemResult, len(p.items))
	for i, it := range p.items {
		ir := ItemResult{Name: it.Name, State: OK}
		bits := isBitFC(it.FC)
		for _, wi := range p.itemWindows[i] {
			wr := &windows[wi]
			if wr.State != OK {
				ir.State, ir.Err = wr.State, &WindowError{Window: wi, Err: wr.Err}
				break
			}
			// The part of the item this window holds.
			from := max(int(it.Addr), int(wr.Addr))
			to := min(int(it.Addr)+int(it.Count), int(wr.Addr)+int(wr.Count))
			lo, hi := from-int(wr.Addr), to-int(wr.Addr)
			if len(p.itemWindows[i]) == 1 {
				// The usual case: the item's data is a slice of the window's.
				if bits {
					ir.Bits = wr.Bits[lo:hi:hi]
				} else {
					ir.Registers = wr.Registers[lo:hi:hi]
				}
				continue
			}
			if bits {
				ir.Bits = append(ir.Bits, wr.Bits[lo:hi]...)
			} else {
				ir.Registers = append(ir.Registers, wr.Registers[lo:hi]...)
			}
		}
		if ir.State == OK && it.Codec != nil {
			v, err := codec.DecodeRegistersAny(ir.Registers, it.Codec)
			if err != nil {
				ir.State, ir.Err = DecodeFailed, err
			} else {
				ir.Value = v
			}
		}
		out[i] = ir
	}
	return out
}
