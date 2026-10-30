// SPDX-License-Identifier: MIT

// Package batch plans, executes and decodes sets of Modbus reads.
//
// A batch is a list of named read items. Compile validates the items and
// groups them into windows: the read requests that cover them, each within
// the protocol and device limits, never mixing unit IDs or address spaces.
// Items that are contiguous or overlap share a window; merging across
// unrequested addresses happens only when Options.MaxGap allows it.
//
// A Plan is immutable and safe for concurrent use. It can be inspected
// without I/O (Windows, Stats, FunctionCodes). Execute reads its windows
// one after another through a Reader such as *modbus.Client, and Decode
// turns captured window data into the same result: per-item results, with
// an explicit State for every window and item, kept when a batch fails
// part-way. The package does not schedule, cache, retry or start
// goroutines: a batch is planned once and run when the caller decides.
package batch

import (
	"errors"
	"fmt"

	"github.com/otfabric/go-modbus"
	"github.com/otfabric/go-modbus/codec"
)

// Item is one logical read.
type Item struct {
	// Name identifies the item in results. Names are unique within a batch.
	Name string
	// UnitID is the Modbus unit ID the item is read from.
	UnitID uint8
	// FC is the read function code: modbus.FCReadCoils,
	// modbus.FCReadDiscreteInputs, modbus.FCReadHoldingRegisters or
	// modbus.FCReadInputRegisters. Any other function code is rejected.
	FC modbus.FunctionCode
	// Addr is the address of the first register or bit.
	Addr uint16
	// Count is the number of registers (FC03, FC04) or bits (FC01, FC02).
	// Zero takes the count from Codec.
	Count uint16
	// Codec decodes the item's registers into ItemResult.Value. It is only
	// allowed on register items (FC03, FC04); when Count is set too, it must
	// equal Codec.RegisterSpec().Count. Nil leaves the item raw.
	Codec codec.RuntimeDecoder
}

// Options configures how Compile groups items into windows. The zero value
// merges contiguous and overlapping items only, up to the protocol limits.
type Options struct {
	// MaxGap is the number of unrequested registers or bits a window may
	// span between two items. Zero merges contiguous and overlapping items
	// only. Gaps are read, so a device must allow reads of those addresses.
	MaxGap uint16
	// MaxRegisters is the largest register window, 1..modbus.MaxReadRegisters.
	// Zero selects modbus.MaxReadRegisters.
	MaxRegisters uint16
	// MaxBits is the largest coil or discrete input window,
	// 1..modbus.MaxReadCoils. Zero selects modbus.MaxReadCoils.
	MaxBits uint16
	// Boundaries are addresses no window may span, for devices that reject
	// reads across internal blocks.
	Boundaries []Boundary
	// ContinueOnTransportError makes Execute go on with the next window
	// after a transport error (a timeout, a broken connection, a protocol
	// error) instead of ending the batch. A cancelled or expired context
	// always ends it. The planner ignores it.
	ContinueOnTransportError bool
}

// Boundary separates two address blocks of one unit and address space: no
// window holds both Addr-1 and Addr. An item that holds both is rejected.
type Boundary struct {
	UnitID uint8
	FC     modbus.FunctionCode
	Addr   uint16
}

// ValidationError reports a batch that Compile or Decode rejects.
type ValidationError struct {
	// Item is the name of the offending item, empty when the error is not
	// about an item.
	Item string
	// Index is the offending item's index in the items given to Compile, or
	// -1 when the error is not about an item.
	Index int
	// Reason describes the problem.
	Reason string
}

func (e *ValidationError) Error() string {
	if e.Index >= 0 {
		return fmt.Sprintf("batch: item %d (%q): %s", e.Index, e.Item, e.Reason)
	}
	return "batch: " + e.Reason
}

// ErrNotAttempted marks a window that was not read because the batch stopped
// before it. WindowResult.Err and ItemResult.Err wrap it for such windows.
var ErrNotAttempted = errors.New("batch: window not attempted")

// WindowError is the ItemResult.Err of an item whose window was not read.
type WindowError struct {
	// Window is the index of the window in Plan.Windows.
	Window int
	// Err is the window's error.
	Err error
}

func (e *WindowError) Error() string {
	return fmt.Sprintf("batch: window %d: %v", e.Window, e.Err)
}

func (e *WindowError) Unwrap() error { return e.Err }

// isBitFC reports whether fc reads bits (FC01, FC02).
func isBitFC(fc modbus.FunctionCode) bool {
	return fc == modbus.FCReadCoils || fc == modbus.FCReadDiscreteInputs
}

// isReadFC reports whether fc is one of the four read function codes.
func isReadFC(fc modbus.FunctionCode) bool {
	return isBitFC(fc) || fc == modbus.FCReadHoldingRegisters || fc == modbus.FCReadInputRegisters
}
