// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/otfabric/go-modbus/codec"
)

func TestValidateRuntimeDecodePlan_Rejections(t *testing.T) {
	u16c := codec.MustRuntimeCodecByID("uint16/layout:21")
	cases := []struct {
		name     string
		plan     RuntimeDecodePlan
		wantItem string
		wantText string
	}{
		{
			name: "window runs past the address space",
			plan: RuntimeDecodePlan{
				Window: ReadWindow{Addr: 0xFFFF, Quantity: 2, RegType: HoldingRegister},
				Items:  []RuntimeDecodeItem{{Name: "a", Codec: u16c}},
			},
			wantText: "exceeds 0xFFFF",
		},
		{
			name: "empty item name",
			plan: RuntimeDecodePlan{
				Window: ReadWindow{Addr: 0, Quantity: 2, RegType: HoldingRegister},
				Items:  []RuntimeDecodeItem{{Name: "a", Codec: u16c}, {Name: "", Offset: 1, Codec: u16c}},
			},
			wantText: "item name is empty",
		},
		{
			name: "offset beyond the window",
			plan: RuntimeDecodePlan{
				Window: ReadWindow{Addr: 0, Quantity: 2, RegType: HoldingRegister},
				Items:  []RuntimeDecodeItem{{Name: "late", Offset: 2, Codec: u16c}},
			},
			wantItem: "late",
			wantText: "offset 2 >= window quantity 2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRuntimeDecodePlan(tc.plan)
			var ve *RuntimePlanValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want *RuntimePlanValidationError, got %T: %v", err, err)
			}
			if ve.ItemName != tc.wantItem || !strings.Contains(ve.Reason, tc.wantText) {
				t.Fatalf("got item %q reason %q, want item %q reason containing %q", ve.ItemName, ve.Reason, tc.wantItem, tc.wantText)
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("Error() = %q does not mention %q", err.Error(), tc.wantText)
			}

			// An invalid plan is rejected before the device is contacted.
			c, tr, m := newScriptedClient(t, goodDevice)
			res, err := ExecuteRuntimeDecodePlan(c, context.Background(), 1, tc.plan)
			if !errors.As(err, &ve) || res != nil {
				t.Fatalf("ExecuteRuntimeDecodePlan = (%v, %v), want (nil, validation error)", res, err)
			}
			if len(tr.requests()) != 0 || len(m.snapshot()) != 0 {
				t.Fatal("device was contacted for an invalid plan")
			}
		})
	}
}

// The window is read with one request at the edge of the address space and
// each item is decoded from its own offset.
func TestExecuteRuntimeDecodePlan_WindowAtEndOfAddressSpace(t *testing.T) {
	u16c := codec.MustRuntimeCodecByID("uint16/layout:21")
	plan := RuntimeDecodePlan{
		Window: ReadWindow{Addr: 0xFFFE, Quantity: 2, RegType: InputRegister},
		Items: []RuntimeDecodeItem{
			{Name: "first", Offset: 0, Codec: u16c},
			{Name: "last", Offset: 1, Codec: u16c},
		},
	}
	c, tr, _ := newScriptedClient(t, goodDevice)
	res, err := ExecuteRuntimeDecodePlan(c, context.Background(), 9, plan)
	if err != nil {
		t.Fatalf("ExecuteRuntimeDecodePlan: %v", err)
	}
	reqs := tr.requests()
	if len(reqs) != 1 || reqs[0].UnitID != 9 || reqs[0].FunctionCode != byte(FCReadInputRegisters) ||
		string(reqs[0].Payload) != string([]byte{0xFF, 0xFE, 0x00, 0x02}) {
		t.Fatalf("requests = %+v, want one FC04 read of 2 registers at 0xFFFE", reqs)
	}
	if res.Addr != 0xFFFE || res.Quantity != 2 || res.RegType != InputRegister || len(res.Values) != 2 {
		t.Fatalf("unexpected result %+v", res)
	}
	for i, name := range []string{"first", "last"} {
		v := res.Values[i]
		if v.Name != name || v.Error != nil || v.Value != uint16(scriptedRegValue) || v.Offset != uint16(i) {
			t.Errorf("value %d = %+v", i, v)
		}
	}
}
