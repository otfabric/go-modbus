// SPDX-License-Identifier: MIT

package modbus

import (
	"testing"

	"github.com/otfabric/go-modbus/internal/adu"
)

// mbapResponseOutOfSync drops a Modbus/TCP connection whose response disagrees
// with itself. For a function code the library does not implement there is
// nothing to disagree with: any payload, an empty one included, is in step.
func TestMBAPResponseOutOfSync(t *testing.T) {
	req := func(fc byte) *adu.Request { return &adu.Request{UnitID: 1, FunctionCode: fc} }
	res := func(unit, fc byte, payload ...byte) *adu.Response {
		return &adu.Response{UnitID: unit, FunctionCode: fc, Payload: payload}
	}
	cases := []struct {
		name string
		req  *adu.Request
		res  *adu.Response
		want bool
	}{
		{"FC03 consistent", req(0x03), res(1, 0x03, 2, 0x12, 0x34), false},
		{"FC03 byte count disagrees", req(0x03), res(1, 0x03, 4, 0x12, 0x34), true},
		{"FC03 empty", req(0x03), res(1, 0x03), true},
		{"FC07 empty", req(0x07), res(1, 0x07), true},
		{"FC08 any length", req(0x08), res(1, 0x08, 0, 0, 1, 2, 3), false},
		{"exception", req(0x03), res(1, 0x83, 2), false},
		{"exception too long", req(0x03), res(1, 0x83, 2, 0), true},
		{"other unit", req(0x03), res(2, 0x03, 2, 0x12, 0x34), true},
		{"other function code", req(0x03), res(1, 0x04, 2, 0x12, 0x34), true},
		{"vendor", req(0x41), res(1, 0x41, 9, 1), false},
		{"vendor empty", req(0x41), res(1, 0x41), false},
		{"vendor exception", req(0x41), res(1, 0xC1, 1), false},
		{"vendor exception too long", req(0x41), res(1, 0xC1, 1, 1), true},
		{"vendor answered with another code", req(0x41), res(1, 0x42, 1), true},
	}
	for _, tc := range cases {
		if got := mbapResponseOutOfSync(tc.req, tc.res); got != tc.want {
			t.Errorf("%s: out of sync = %v, want %v", tc.name, got, tc.want)
		}
	}
}
