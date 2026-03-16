package modbus

import (
	"context"
	"fmt"

	"github.com/otfabric/go-modbus/codec"
)

//
// Batch decode plan (single-window): read one register window, decode multiple items.
// MIGRATE_PLAN Phase 5.
//

// ReadWindow defines the Modbus register range to read in one request.
type ReadWindow struct {
	Addr     uint16
	Quantity uint16
	RegType  RegType
}

// RuntimeDecodeItem describes one field to decode from the window at a given offset.
// Plan execution is decode-only; the codec is a codec.RuntimeDecoder (not RuntimeCodec).
type RuntimeDecodeItem struct {
	Name     string
	Offset   uint16 // register offset within the window (in 16-bit register units)
	Codec    codec.RuntimeDecoder
	Metadata map[string]any
}

// RuntimeDecodePlan is a single-window read plus multiple decode items.
type RuntimeDecodePlan struct {
	Window ReadWindow
	Items  []RuntimeDecodeItem
}

// RuntimeDecodedValue holds the result of decoding one item (value or error).
type RuntimeDecodedValue struct {
	Name          string
	CodecID       string
	ValueKind     codec.CodecValueKind
	Offset        uint16
	RegisterCount uint16
	Value         any
	Error         error
}

// RuntimeDecodeResult holds the raw registers and per-item decoded values.
type RuntimeDecodeResult struct {
	Addr      uint16
	Quantity  uint16
	RegType   RegType
	Registers []uint16
	Values    []RuntimeDecodedValue
}

// RuntimePlanValidationError is returned when a plan fails validation.
type RuntimePlanValidationError struct {
	ItemName string
	Reason   string
}

func (e *RuntimePlanValidationError) Error() string {
	if e.ItemName != "" {
		return "modbus: runtime decode plan validation: item " + e.ItemName + ": " + e.Reason
	}
	return "modbus: runtime decode plan validation: " + e.Reason
}

// ValidateRuntimeDecodePlan checks the plan before execution. Returns
// *RuntimePlanValidationError on invalid window, duplicate names, nil codec,
// or item offset/count that would spill past the window.
func ValidateRuntimeDecodePlan(plan RuntimeDecodePlan) error {
	w := plan.Window
	if w.Quantity < 1 || w.Quantity > 125 {
		return &RuntimePlanValidationError{Reason: fmt.Sprintf("window quantity %d not in 1..125", w.Quantity)}
	}
	if uint32(w.Addr)+uint32(w.Quantity)-1 > 0xFFFF {
		return &RuntimePlanValidationError{Reason: "window addr + quantity - 1 exceeds 0xFFFF"}
	}
	if len(plan.Items) == 0 {
		return &RuntimePlanValidationError{Reason: "plan has no items"}
	}
	seen := make(map[string]bool)
	for i := range plan.Items {
		item := &plan.Items[i]
		if item.Name == "" {
			return &RuntimePlanValidationError{ItemName: item.Name, Reason: "item name is empty"}
		}
		if seen[item.Name] {
			return &RuntimePlanValidationError{ItemName: item.Name, Reason: "duplicate item name"}
		}
		seen[item.Name] = true
		if item.Codec == nil {
			return &RuntimePlanValidationError{ItemName: item.Name, Reason: "codec is nil"}
		}
		count := item.Codec.RegisterSpec().Count
		if item.Offset >= w.Quantity {
			return &RuntimePlanValidationError{ItemName: item.Name, Reason: fmt.Sprintf("offset %d >= window quantity %d", item.Offset, w.Quantity)}
		}
		if uint32(item.Offset)+uint32(count) > uint32(w.Quantity) {
			return &RuntimePlanValidationError{ItemName: item.Name, Reason: fmt.Sprintf("item offset %d + count %d exceeds window quantity %d", item.Offset, count, w.Quantity)}
		}
	}
	return nil
}

// ExecuteRuntimeDecodePlan validates the plan, performs one register read for the
// window, then decodes each item. Transport/read failure returns a top-level error.
// Per-item decode failures are stored in the corresponding RuntimeDecodedValue.Error;
// sibling items are still decoded.
func ExecuteRuntimeDecodePlan(
	mc *Client,
	ctx context.Context,
	unitID uint8,
	plan RuntimeDecodePlan,
) (*RuntimeDecodeResult, error) {
	if err := ValidateRuntimeDecodePlan(plan); err != nil {
		return nil, err
	}
	regs, err := readWindowFromClient(mc, ctx, unitID, plan.Window)
	if err != nil {
		return nil, err
	}
	return executePlanOffline(regs, plan)
}

// ExecuteRuntimeDecodePlanOffline runs the same decode logic as ExecuteRuntimeDecodePlan
// but uses the provided registers instead of reading from the client. The plan is
// validated; regs must have length exactly plan.Window.Quantity.
func ExecuteRuntimeDecodePlanOffline(regs []uint16, plan RuntimeDecodePlan) (*RuntimeDecodeResult, error) {
	if err := ValidateRuntimeDecodePlan(plan); err != nil {
		return nil, err
	}
	if uint16(len(regs)) != plan.Window.Quantity {
		return nil, &RuntimePlanValidationError{Reason: fmt.Sprintf("regs length %d != window quantity %d", len(regs), plan.Window.Quantity)}
	}
	return executePlanOffline(regs, plan)
}

func readWindowFromClient(mc *Client, ctx context.Context, unitID uint8, w ReadWindow) ([]uint16, error) {
	mbPayload, err := mc.readRegisterPayload(ctx, unitID, w.Addr, w.Quantity, w.RegType)
	if err != nil {
		return nil, err
	}
	return bytesToUint16s(BigEndian, mbPayload), nil
}

func executePlanOffline(regs []uint16, plan RuntimeDecodePlan) (*RuntimeDecodeResult, error) {
	w := plan.Window
	windowRegs := append([]uint16(nil), regs[:w.Quantity]...)
	result := &RuntimeDecodeResult{
		Addr:      w.Addr,
		Quantity:  w.Quantity,
		RegType:   w.RegType,
		Registers: windowRegs,
		Values:    make([]RuntimeDecodedValue, len(plan.Items)),
	}
	for i := range plan.Items {
		item := &plan.Items[i]
		count := item.Codec.RegisterSpec().Count
		result.Values[i] = RuntimeDecodedValue{
			Name:          item.Name,
			CodecID:       item.Codec.ID(),
			ValueKind:     item.Codec.ValueKind(),
			Offset:        item.Offset,
			RegisterCount: count,
		}
		end := item.Offset + count
		if uint16(len(regs)) < end {
			result.Values[i].Error = fmt.Errorf("%w: regs length %d < offset+count %d", codec.ErrCodecRegisterCount, len(regs), end)
			continue
		}
		slice := regs[item.Offset:end]
		val, err := codec.DecodeRegistersAny(slice, item.Codec)
		if err != nil {
			result.Values[i].Error = err
			continue
		}
		result.Values[i].Value = val
	}
	return result, nil
}
