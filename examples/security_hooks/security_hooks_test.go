// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"fmt"
	"io"
	"strings"
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

// The demo produces one coherent trail: every connection is accepted or
// refused with its verified subject, every decision has its PDU record under
// the same ConnectionID and RequestSeq, and nothing is dropped.
func TestRun(t *testing.T) {
	trail, dropped, err := run(io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 0 {
		t.Errorf("%d events dropped", dropped)
	}
	count := map[string]int{}
	type key struct {
		id  modbus.ConnectionID
		seq uint64
	}
	pdus := map[key]bool{}
	for _, e := range trail {
		count[e.Kind]++
		if e.Kind == "pdu" {
			pdus[key{e.ConnectionID, e.RequestSeq}] = true
		}
		if e.ConnectionID == 0 || e.Subject == "" {
			t.Errorf("event %v without a connection or a subject", e)
		}
	}
	for kind, want := range map[string]int{
		"connection accepted": 3, "connection refused": 1, "connection closed": 3,
		"request allowed": 3, "request refused": 1, "pdu": 5, "protocol violation": 1,
	} {
		if count[kind] != want {
			t.Errorf("%d %q events, want %d (trail %v)", count[kind], kind, want, trail)
		}
	}
	for _, e := range trail {
		switch e.Kind {
		case "request allowed", "request refused", "protocol violation":
			if !pdus[key{e.ConnectionID, e.RequestSeq}] {
				t.Errorf("%v has no PDU record", e)
			}
		case "connection refused":
			if e.Subject != "revoked client" || !strings.Contains(e.Detail, "not authorized") {
				t.Errorf("refusal %v, want the revoked client, not authorized", e)
			}
		}
		if e.Kind == "request refused" && (e.Subject != "viewer client" || e.Role != "viewer") {
			t.Errorf("refusal %v, want the viewer's write", e)
		}
	}
}
