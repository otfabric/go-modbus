//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	modbus "github.com/otfabric/go-modbus"
)

// TestClientConformanceChecks runs the go-modbus client's stricter response
// checks of v1.20.0 against the reference servers that serve each function,
// so that a third-party answer they reject is noticed: the FC08 wrappers'
// data fields and echo checks (V1.1b3 section 6.8.1), the FC17 run
// indicator (6.13), the FC43 Read Device ID code and object id checks
// (6.21) and the FC20 response length limit (6.14).
func TestClientConformanceChecks(t *testing.T) {
	forEachAdapter(t, func(t *testing.T, a adapter) {
		fx := loadFixture(t, a)
		ctx := context.Background()
		u := fx.unit()

		t.Run("diagnostics-wrappers", func(t *testing.T) {
			a.need(t, featServerDiagnostics)
			c := newClient(t, startServer(t, a), nil)
			// Return Query Data must come back as the echo, for every value.
			for _, v := range []uint16{0x0000, 0xFFFF, 0x0102} {
				if got, err := c.DiagnosticLoopback(ctx, u, v); err != nil || got != v {
					t.Errorf("DiagnosticLoopback(0x%04X) = 0x%04X, %v", v, got, err)
				}
			}
			// The fixture serves sub-function 0 only and answers every other
			// one with exception 1 (FIXTURES.md rule 8). The wrappers now send
			// the data field 00 00 (08 00 02 00 00, 08 00 0B 00 00, ...): the
			// server must decode the request and answer, not close the link
			// or answer exception 3.
			for _, w := range []struct {
				name string
				call func() error
			}{
				{"DiagnosticRegister", func() error { _, err := c.DiagnosticRegister(ctx, u); return err }},
				{"BusMessageCount", func() error { _, err := c.BusMessageCount(ctx, u); return err }},
				{"DiagnosticClearCounters", func() error { return c.DiagnosticClearCounters(ctx, u) }},
				{"DiagnosticClearOverrunCounterAndFlag", func() error { return c.DiagnosticClearOverrunCounterAndFlag(ctx, u) }},
			} {
				wantException(t, w.name, w.call(), modbus.FCDiagnostics, excIllegalFunction)
			}
			// The link is still usable.
			if got, err := c.DiagnosticLoopback(ctx, u, 0x5AA5); err != nil || got != 0x5AA5 {
				t.Errorf("DiagnosticLoopback after the exceptions = 0x%04X, %v", got, err)
			}
		})

		t.Run("report-server-id-run-indicator", func(t *testing.T) {
			a.need(t, featServerReportServerID)
			c := newClient(t, startServer(t, a), nil)
			rs, err := c.ReportServerID(ctx, u)
			if err != nil {
				t.Fatalf("ReportServerID: %v", err)
			}
			// The fixture's Server ID is one byte, followed by the run
			// indicator and no Additional Data.
			if on, ok := rs.RunIndicator(1); !ok || on != fx.ServerID.RunIndicator {
				t.Errorf("RunIndicator(1) = %v, %v; want %v, true (data % x)", on, ok, fx.ServerID.RunIndicator, rs.Data)
			}
			if _, ok := rs.RunIndicator(2); ok {
				t.Errorf("RunIndicator(2) reports ok on a %d-byte response", len(rs.Data))
			}
			if rs.RunIndicatorStatus == nil || *rs.RunIndicatorStatus != fx.ServerID.RunIndicator {
				t.Errorf("RunIndicatorStatus = %v, want %v", rs.RunIndicatorStatus, fx.ServerID.RunIndicator)
			}
		})

		t.Run("device-identification-codes", func(t *testing.T) {
			a.need(t, featServerDeviceIdentification)
			c := newClient(t, startServer(t, a), nil)
			// Every stream category: the response's Read Device ID code must
			// be the request's, which ReadDeviceIdentification checks.
			for _, cat := range []modbus.DeviceIDCategory{modbus.DeviceIDBasic, modbus.DeviceIDRegular, modbus.DeviceIDExtended} {
				if _, err := c.ReadDeviceIdentification(ctx, u, cat, 0); err != nil {
					t.Errorf("ReadDeviceIdentification(category %d): %v", cat, err)
				}
			}
			di, err := c.ReadDeviceIdentification(ctx, u, modbus.DeviceIDBasic, 0)
			if err != nil {
				t.Fatalf("ReadDeviceIdentification(basic): %v", err)
			}
			if !di.SupportsIndividualAccess() {
				t.Skipf("%s does not offer individual access", a.name)
			}
			// Individual access: code 04 and the object id of the request.
			for _, want := range fx.objects(modbus.DeviceIDExtended) {
				di, err := c.ReadDeviceIdentification(ctx, u, modbus.DeviceIDIndividual, modbus.DeviceIDObjectID(want.ID))
				if err != nil {
					t.Errorf("individual access to object 0x%02X: %v", want.ID, err)
					continue
				}
				if len(di.Objects) != 1 || uint8(di.Objects[0].ID) != want.ID {
					t.Errorf("individual access to object 0x%02X: got %v", want.ID, di.Objects)
				}
			}
			// An object the device does not have: exception 2 (6.21).
			missing := missingObject(fx)
			_, err = c.ReadDeviceIdentification(ctx, u, modbus.DeviceIDIndividual, missing)
			wantException(t, fmt.Sprintf("individual access to the missing object 0x%02X", uint8(missing)), err,
				modbus.FCEncapsulatedInterface, excIllegalDataAddress)
		})

		t.Run("file-records-response-limit", func(t *testing.T) {
			a.need(t, featServerFileRecord)
			c := newClient(t, startServer(t, a), nil)
			f1 := fx.file(1)
			// Response data lengths at and near the 0xF5 limit, even record
			// lengths only (the PyModbus server cannot decode odd ones).
			for _, tc := range []struct {
				name string
				reqs []modbus.FileRecordRequest
				want [][]uint16
			}{
				// 2 + 240 = 242 bytes.
				{"one sub-request of 120", []modbus.FileRecordRequest{{FileNumber: 1, RecordNumber: 0, RecordLength: 120}},
					[][]uint16{f1[:120]}},
				// (2 + 200) + (2 + 40) = 244 bytes.
				{"two sub-requests of 120 in all", []modbus.FileRecordRequest{
					{FileNumber: 1, RecordNumber: 0, RecordLength: 100},
					{FileNumber: 1, RecordNumber: 100, RecordLength: 20},
				}, [][]uint16{f1[:100], f1[100:120]}},
				// (2 + 120) + (2 + 108) + (2 + 8) = 242 bytes.
				{"three sub-requests of 118 in all", []modbus.FileRecordRequest{
					{FileNumber: 1, RecordNumber: 0, RecordLength: 60},
					{FileNumber: 1, RecordNumber: 140, RecordLength: 54},
					{FileNumber: 2, RecordNumber: 0, RecordLength: 4},
				}, [][]uint16{f1[:60], f1[140:194], fx.file(2)}},
				// 35 sub-requests: a 245-byte request, 35 * 6 = 210 response bytes.
				{"35 sub-requests", manyFileSubRequests(35), manyFileRecords(f1, 35)},
			} {
				got, err := c.ReadFileRecords(ctx, u, tc.reqs)
				if err != nil || !reflect.DeepEqual(got, tc.want) {
					t.Errorf("ReadFileRecords, %s: %v, %v", tc.name, got, err)
				}
			}
			// Over the limit: refused locally, nothing sent.
			for _, tc := range []struct {
				name string
				reqs []modbus.FileRecordRequest
			}{
				{"one sub-request of 122 (246 bytes)", []modbus.FileRecordRequest{{FileNumber: 1, RecordNumber: 0, RecordLength: 122}}},
				{"two sub-requests of 122 in all (248 bytes)", []modbus.FileRecordRequest{
					{FileNumber: 1, RecordNumber: 0, RecordLength: 100},
					{FileNumber: 1, RecordNumber: 100, RecordLength: 22},
				}},
				{"36 sub-requests (a 252-byte request)", manyFileSubRequests(36)},
			} {
				_, err := c.ReadFileRecords(ctx, u, tc.reqs)
				if _, ok := errors.AsType[*modbus.ParameterError](err); !ok {
					t.Errorf("ReadFileRecords, %s: got %v, want a *ParameterError", tc.name, err)
				}
			}
			// The connection is unaffected.
			if got, err := c.ReadFileRecords(ctx, u, []modbus.FileRecordRequest{{FileNumber: 1, RecordNumber: 0, RecordLength: 2}}); err != nil ||
				!reflect.DeepEqual(got, [][]uint16{f1[:2]}) {
				t.Errorf("ReadFileRecords after the refused calls: %v, %v", got, err)
			}
		})
	})
}

// manyFileSubRequests is n sub-requests of two records each, from file 1.
func manyFileSubRequests(n int) []modbus.FileRecordRequest {
	out := make([]modbus.FileRecordRequest, n)
	for i := range out {
		out[i] = modbus.FileRecordRequest{FileNumber: 1, RecordNumber: uint16(2 * i), RecordLength: 2}
	}
	return out
}

func manyFileRecords(f []uint16, n int) [][]uint16 {
	out := make([][]uint16, n)
	for i := range out {
		out[i] = f[2*i : 2*i+2]
	}
	return out
}

// missingObject is an object id the fixture does not have.
func missingObject(fx *fixture) modbus.DeviceIDObjectID {
	have := map[uint8]bool{}
	for _, o := range fx.objects(modbus.DeviceIDExtended) {
		have[o.ID] = true
	}
	for id := 0; id < 256; id++ {
		if !have[uint8(id)] && (id < 7 || id >= 0x80) {
			return modbus.DeviceIDObjectID(id)
		}
	}
	return 0xFF
}
