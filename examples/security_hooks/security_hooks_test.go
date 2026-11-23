// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/otfabric/go-modbus"
)

// The example's policy: reads and reporting diagnostics for everyone;
// writes and state-changing FC08 sub-functions for operators only, never
// broadcast.
func TestAuthorize(t *testing.T) {
	fc08 := func(sub uint16) modbus.AuthorizationRequest {
		return modbus.AuthorizationRequest{UnitID: 1, FunctionCode: modbus.FCDiagnostics,
			Access: modbus.AccessDiagnostic, SubFunction: sub, HasSubFunction: true}
	}
	write := modbus.AuthorizationRequest{UnitID: 1, FunctionCode: modbus.FCWriteSingleRegister, Access: modbus.AccessWrite}
	for _, tc := range []struct {
		name  string
		req   modbus.AuthorizationRequest
		role  string
		allow bool
	}{
		{"read", modbus.AuthorizationRequest{UnitID: 1, FunctionCode: modbus.FCReadHoldingRegisters, Access: modbus.AccessRead}, "", true},
		{"report server id", modbus.AuthorizationRequest{UnitID: 1, FunctionCode: modbus.FCReportServerID, Access: modbus.AccessDiagnostic}, "", true},
		{"fc08 return query data", fc08(0x00), "", true},
		{"fc08 return diagnostic register", fc08(0x02), "", true},
		{"fc08 return bus message count", fc08(0x0B), "", true},
		{"fc08 return bus character overrun count", fc08(0x12), "", true},
		{"fc08 restart communications", fc08(0x01), "", false},
		{"fc08 change ascii delimiter", fc08(0x03), "", false},
		{"fc08 force listen only", fc08(0x04), "", false},
		{"fc08 clear counters", fc08(0x0A), "", false},
		{"fc08 clear overrun counter", fc08(0x14), "", false},
		{"fc08 reserved", fc08(0x13), "", false},
		{"fc08 without a sub-function", modbus.AuthorizationRequest{UnitID: 1, FunctionCode: modbus.FCDiagnostics, Access: modbus.AccessDiagnostic}, "", false},
		{"fc08 restart by an operator", fc08(0x01), "operator", true},
		{"write", write, "", false},
		{"write by an operator", write, "operator", true},
		{"read with an unusable role", modbus.AuthorizationRequest{UnitID: 1, FunctionCode: modbus.FCReadHoldingRegisters, Access: modbus.AccessRead,
			ClientRoleErr: fmt.Errorf("%w: not a UTF8String", modbus.ErrInvalidRole)}, "", false},
		{"broadcast write by an operator", modbus.AuthorizationRequest{UnitID: 0, FunctionCode: modbus.FCWriteSingleRegister, Access: modbus.AccessWrite}, "operator", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			req.ClientRole = tc.role
			if err := authorize(context.Background(), &req); (err == nil) != tc.allow {
				t.Errorf("authorize = %v, want allowed %v", err, tc.allow)
			}
		})
	}
}
