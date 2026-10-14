// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// End-to-end tests for the server side of Diagnostics (FC08), Report Server ID
// (FC17), Read/Write File Record (FC20/FC21), Read FIFO Queue (FC24) and the
// raw request handler, plus the guard that keeps client and server complete.

// e2eNonRequestMethods are the exported Client methods that put nothing on the
// wire.
var e2eNonRequestMethods = map[string]bool{
	"Open": true, "Close": true, "Info": true, "LastObservedTransactionID": true,
}

// The guard: every exported Client method that sends a request is in
// e2eAllOps, and every one of them is answered by our own server, over both
// transports, by the handler for its function code. A new client function
// without server support fails here.
func TestE2E_EveryClientFunctionIsServed(t *testing.T) {
	listed := map[string]bool{}
	for _, op := range e2eAllOps() {
		name, _, _ := strings.Cut(op.name, "/")
		listed[name] = true
	}
	ct := reflect.TypeOf(&Client{})
	for i := 0; i < ct.NumMethod(); i++ {
		m := ct.Method(i).Name
		if !e2eNonRequestMethods[m] && !listed[m] {
			t.Errorf("Client.%s is not in e2eAllOps: add it, and serve its function code", m)
		}
	}

	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		ctx := context.Background()
		fcs := map[FunctionCode]bool{}
		for _, op := range e2eAllOps() {
			if err := op.call(ctx, p.client); err != nil {
				t.Errorf("%s (FC 0x%02X) is not served by our server: %v", op.name, uint8(op.fc), err)
				continue
			}
			calls := dev.takeCalls()
			if len(calls) == 0 || calls[0].FC != op.fc {
				t.Errorf("%s: handler calls %+v, want the first for FC 0x%02X", op.name, calls, uint8(op.fc))
			}
			fcs[op.fc] = true
		}
		// Every public function code the client implements, by number.
		for _, fc := range []FunctionCode{
			FCReadCoils, FCReadDiscreteInputs, FCReadHoldingRegisters, FCReadInputRegisters,
			FCWriteSingleCoil, FCWriteSingleRegister, FCReadExceptionStatus, FCDiagnostics,
			FCGetCommEventCounters, FCGetCommEventLog, FCWriteMultipleCoils, FCWriteMultipleRegisters,
			FCReportServerID, FCReadFileRecord, FCWriteFileRecord, FCMaskWriteRegister,
			FCReadWriteMultipleRegs, FCReadFIFOQueue, FCEncapsulatedInterface,
		} {
			if !fcs[fc] {
				t.Errorf("FC 0x%02X was not served end to end", uint8(fc))
			}
		}
	})
}

func TestE2E_Diagnostics(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		c := p.client
		ctx := context.Background()

		dr, err := c.Diagnostics(ctx, e2eUnit, DiagReturnQueryData, []byte{0xCA, 0xFE, 0x01})
		if err != nil || dr.SubFunction != DiagReturnQueryData || !reflect.DeepEqual(dr.Data, []byte{0xCA, 0xFE, 0x01}) {
			t.Errorf("Return Query Data = %+v, %v", dr, err)
		}
		if call := e2eOneCall(t, "Return Query Data", dev); call.Kind != "diag" || call.SubFunc != DiagReturnQueryData ||
			!reflect.DeepEqual(call.Data, []byte{0xCA, 0xFE, 0x01}) || call.UnitID != e2eUnit {
			t.Errorf("handler saw %+v", call)
		}
		// The largest echo that fits in one response.
		big := make([]byte, 250)
		for i := range big {
			big[i] = byte(i)
		}
		if dr, err := c.Diagnostics(ctx, e2eUnit, DiagReturnQueryData, big); err != nil || !reflect.DeepEqual(dr.Data, big) {
			t.Errorf("250-byte Return Query Data: %v", err)
		}
		if v, err := c.DiagnosticLoopback(ctx, e2eUnit, 0x1234); err != nil || v != 0x1234 {
			t.Errorf("DiagnosticLoopback = 0x%04X, %v", v, err)
		}
		if dr, err := c.Diagnostics(ctx, e2eUnit, DiagRestartCommunications, []byte{0xFF, 0x00}); err != nil ||
			dr.SubFunction != DiagRestartCommunications || !reflect.DeepEqual(dr.Data, []byte{0xFF, 0x00}) {
			t.Errorf("Restart Communications = %+v, %v", dr, err)
		}
		if v, err := c.DiagnosticRegister(ctx, e2eUnit); err != nil || v != 0xA55A {
			t.Errorf("DiagnosticRegister = 0x%04X, %v", v, err)
		}
		if err := c.DiagnosticForceListenOnlyMode(ctx, e2eUnit); err != nil {
			t.Errorf("DiagnosticForceListenOnlyMode: %v", err)
		}

		counters := []struct {
			name string
			read func(context.Context, uint8) (uint16, error)
			want uint16
		}{
			{"BusMessageCount", c.BusMessageCount, 11},
			{"DiagnosticBusCommunicationErrorCount", c.DiagnosticBusCommunicationErrorCount, 12},
			{"DiagnosticBusExceptionErrorCount", c.DiagnosticBusExceptionErrorCount, 13},
			{"DiagnosticServerMessageCount", c.DiagnosticServerMessageCount, 14},
			{"DiagnosticServerNoResponseCount", c.DiagnosticServerNoResponseCount, 15},
			{"DiagnosticServerNAKCount", c.DiagnosticServerNAKCount, 16},
			{"DiagnosticServerBusyCount", c.DiagnosticServerBusyCount, 17},
			{"DiagnosticBusCharacterOverrunCount", c.DiagnosticBusCharacterOverrunCount, 18},
		}
		readAll := func(what string, want func(i int) uint16) {
			t.Helper()
			for i, ctr := range counters {
				if v, err := ctr.read(ctx, e2eUnit); err != nil || v != want(i) {
					t.Errorf("%s: %s = %d, %v; want %d", what, ctr.name, v, err, want(i))
				}
			}
		}
		readAll("initially", func(i int) uint16 { return counters[i].want })
		if err := c.DiagnosticClearOverrunCounterAndFlag(ctx, e2eUnit); err != nil {
			t.Fatalf("DiagnosticClearOverrunCounterAndFlag: %v", err)
		}
		readAll("after clearing the overrun counter", func(i int) uint16 {
			if counters[i].name == "DiagnosticBusCharacterOverrunCount" {
				return 0
			}
			return counters[i].want
		})
		if err := c.DiagnosticClearCounters(ctx, e2eUnit); err != nil {
			t.Fatalf("DiagnosticClearCounters: %v", err)
		}
		readAll("after clearing all counters", func(int) uint16 { return 0 })
		if v, err := c.DiagnosticRegister(ctx, e2eUnit); err != nil || v != 0 {
			t.Errorf("DiagnosticRegister after clearing = 0x%04X, %v", v, err)
		}

		dev.takeCalls()
		// Sub-functions the device does not implement, standard and vendor.
		for _, sf := range []DiagnosticSubFunction{DiagChangeASCIIInputDelimiter, 0x1234, 0xFFFF} {
			_, err := c.Diagnostics(ctx, e2eUnit, sf, []byte{0, 0})
			e2eWantException(t, fmt.Sprintf("sub-function 0x%04X", uint16(sf)), err, FCDiagnostics, exIllegalFunction)
			if call := e2eOneCall(t, "unknown sub-function", dev); call.SubFunc != sf {
				t.Errorf("handler saw sub-function 0x%04X, want 0x%04X", uint16(call.SubFunc), uint16(sf))
			}
		}
		if p.conns() != 1 {
			t.Errorf("server connections = %d, want 1", p.conns())
		}
	})
}

// A device in listen-only mode does not answer Force Listen Only Mode, as on a
// serial line: the client times out and the server keeps serving.
func TestE2E_Diagnostics_ListenOnlyIsSilent(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		dev.setListenOnly(true)
		sm := &e2eMetrics{}
		p := e2eStart(t, kind, dev, e2eOpts{
			client: func(c *Config) { c.Timeout = 60 * time.Millisecond },
			server: func(c *ServerConfig) { c.Metrics = sm },
		})
		ctx := context.Background()
		if err := p.client.DiagnosticForceListenOnlyMode(ctx, e2eUnit); !errors.Is(err, ErrRequestTimedOut) {
			t.Fatalf("DiagnosticForceListenOnlyMode on a silent device: %v, want ErrRequestTimedOut", err)
		}
		if call := e2eOneCall(t, "listen only", dev); call.SubFunc != DiagForceListenOnlyMode {
			t.Errorf("handler saw %+v", call)
		}
		events := sm.take()
		if got := e2eKinds(events); !reflect.DeepEqual(got, []string{"request:08", "error:08"}) || !errors.Is(events[1].Err, ErrNoResponse) {
			t.Errorf("server metrics = %v", events)
		}
		dev.setListenOnly(false)
		if v, err := p.client.DiagnosticLoopback(ctx, e2eUnit, 0x0BAD); err != nil || v != 0x0BAD {
			t.Errorf("DiagnosticLoopback afterwards = 0x%04X, %v", v, err)
		}
	})
}

func TestE2E_ReportServerID(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		ctx := context.Background()

		rs, err := p.client.ReportServerID(ctx, e2eUnit)
		if err != nil || !reflect.DeepEqual(rs.Data, []byte{'G', 'O', 'M', 'B', 0xFF}) ||
			rs.RunIndicatorStatus == nil || !*rs.RunIndicatorStatus {
			t.Fatalf("ReportServerID = %+v, %v", rs, err)
		}
		if call := e2eOneCall(t, "ReportServerID", dev); call.Kind != "serverid" || call.FC != FCReportServerID {
			t.Errorf("handler saw %+v", call)
		}
		// The largest payload that fits, run indicator off.
		big := make([]byte, 251)
		big[0] = 0x42
		dev.mu.Lock()
		dev.serverID = big
		dev.mu.Unlock()
		rs, err = p.client.ReportServerID(ctx, e2eUnit)
		if err != nil || !reflect.DeepEqual(rs.Data, big) || rs.RunIndicatorStatus == nil || *rs.RunIndicatorStatus {
			t.Errorf("251-byte ReportServerID = %+v, %v", rs, err)
		}
		// One byte more does not fit in a response: Server Device Failure.
		dev.mu.Lock()
		dev.serverID = make([]byte, 252)
		dev.mu.Unlock()
		_, err = p.client.ReportServerID(ctx, e2eUnit)
		e2eWantException(t, "252-byte ReportServerID", err, FCReportServerID, exServerDeviceFailure)
	})
}

func TestE2E_FileRecords(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		c := p.client
		ctx := context.Background()

		reqs := []FileRecordRequest{
			{FileNumber: 4, RecordNumber: 1, RecordLength: 2},
			{FileNumber: 9, RecordNumber: 0, RecordLength: 4},
			{FileNumber: 4, RecordNumber: 3, RecordLength: 1},
		}
		got, err := c.ReadFileRecords(ctx, e2eUnit, reqs)
		want := [][]uint16{{0x0DFE, 0x0020}, {1, 2, 3, 4}, {0x33CD}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("ReadFileRecords = %v, %v; want %v", got, err, want)
		}
		if call := e2eOneCall(t, "ReadFileRecords", dev); !reflect.DeepEqual(call.FileReqs, reqs) {
			t.Errorf("handler saw %+v, want %+v", call.FileReqs, reqs)
		}

		recs := []FileRecord{
			{FileNumber: 4, RecordNumber: 7, Data: []uint16{0x06AF, 0x04BE}},
			{FileNumber: 9, RecordNumber: 2, Data: []uint16{0xAAAA}},
		}
		if err := c.WriteFileRecords(ctx, e2eUnit, recs); err != nil {
			t.Fatalf("WriteFileRecords: %v", err)
		}
		if call := e2eOneCall(t, "WriteFileRecords", dev); !reflect.DeepEqual(call.FileRecs, recs) {
			t.Errorf("handler saw %+v, want %+v", call.FileRecs, recs)
		}
		if got := dev.fileRegs(4, 7, 2); !reflect.DeepEqual(got, []uint16{0x06AF, 0x04BE}) {
			t.Errorf("file 4 records 7-8 = %04X", got)
		}
		got, err = c.ReadFileRecords(ctx, e2eUnit, []FileRecordRequest{{FileNumber: 9, RecordNumber: 0, RecordLength: 4}})
		if err != nil || !reflect.DeepEqual(got, [][]uint16{{1, 2, 0xAAAA, 4}}) {
			t.Errorf("read back = %v, %v", got, err)
		}
		dev.takeCalls()

		// Files and records the device does not have.
		_, err = c.ReadFileRecords(ctx, e2eUnit, []FileRecordRequest{{FileNumber: 5, RecordNumber: 0, RecordLength: 1}})
		e2eWantException(t, "read of a missing file", err, FCReadFileRecord, exIllegalDataAddress)
		_, err = c.ReadFileRecords(ctx, e2eUnit, []FileRecordRequest{{FileNumber: 4, RecordNumber: 3, RecordLength: 2}})
		e2eWantException(t, "read past the last record", err, FCReadFileRecord, exIllegalDataAddress)
		// A write with one bad sub-request writes nothing.
		err = c.WriteFileRecords(ctx, e2eUnit, []FileRecord{
			{FileNumber: 4, RecordNumber: 7, Data: []uint16{0x1111}},
			{FileNumber: 5, RecordNumber: 0, Data: []uint16{0x2222}},
		})
		e2eWantException(t, "write to a missing file", err, FCWriteFileRecord, exIllegalDataAddress)
		if got := dev.fileRegs(4, 7, 1)[0]; got != 0x06AF {
			t.Errorf("a failed write changed file 4 record 7 to 0x%04X", got)
		}
		if n := len(dev.takeCalls()); n != 3 {
			t.Errorf("handler received %d requests, want 3", n)
		}
		if p.conns() != 1 {
			t.Errorf("server connections = %d, want 1", p.conns())
		}
	})
}

func TestE2E_ReadFIFOQueue(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		c := p.client
		ctx := context.Background()

		if got, err := c.ReadFIFOQueue(ctx, e2eUnit, 0x04DE); err != nil || !reflect.DeepEqual(got, []uint16{0x01B8, 0x1284}) {
			t.Errorf("ReadFIFOQueue = %04X, %v", got, err)
		}
		if call := e2eOneCall(t, "ReadFIFOQueue", dev); call.Kind != "fifo" || call.Addr != 0x04DE {
			t.Errorf("handler saw %+v", call)
		}
		if got, err := c.ReadFIFOQueue(ctx, e2eUnit, 0x0100); err != nil || len(got) != 0 {
			t.Errorf("empty queue = %v, %v", got, err)
		}
		full := make([]uint16, 31)
		for i := range full {
			full[i] = uint16(0xF000 + i)
		}
		dev.setFIFO(0xFFFF, full...)
		if got, err := c.ReadFIFOQueue(ctx, e2eUnit, 0xFFFF); err != nil || !reflect.DeepEqual(got, full) {
			t.Errorf("31-value queue = %04X, %v", got, err)
		}
		// More than 31 values: Illegal Data Value, as the specification requires.
		dev.setFIFO(0xFFFF, append(full, 0xF01F)...)
		_, err := c.ReadFIFOQueue(ctx, e2eUnit, 0xFFFF)
		e2eWantException(t, "32-value queue", err, FCReadFIFOQueue, exIllegalDataValue)
		_, err = c.ReadFIFOQueue(ctx, e2eUnit, 0x0999)
		e2eWantException(t, "unknown queue", err, FCReadFIFOQueue, exIllegalDataAddress)
	})
}

// e2eRawDevice serves the four mandatory functions through an e2eDevice and
// everything else through raw.
type e2eRawDevice struct {
	e2eBasicOnly
	mu   sync.Mutex
	seen []RawRequest
	raw  func(req *RawRequest) ([]byte, error)
}

func (r *e2eRawDevice) HandleRawRequest(_ context.Context, req *RawRequest) ([]byte, error) {
	r.mu.Lock()
	r.seen = append(r.seen, *req)
	fn := r.raw
	r.mu.Unlock()
	return fn(req)
}

func (r *e2eRawDevice) take() []RawRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.seen
	r.seen = nil
	return out
}

// The raw handler serves vendor function codes and every standard one whose
// optional handler is missing; FC01-04 still go to their handlers.
func TestE2E_RawRequestHandler(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		rd := &e2eRawDevice{e2eBasicOnly: e2eBasicOnly{dev}}
		rd.raw = func(req *RawRequest) ([]byte, error) {
			// A byte, not a FunctionCode: the raw handler sees every code.
			switch byte(req.FunctionCode) {
			case byte(FCDiagnostics), byte(FCWriteFileRecord), byte(FCMaskWriteRegister):
				return req.Payload, nil // echo
			case byte(FCReportServerID):
				return []byte{3, 'R', 'W', 0xFF}, nil
			case byte(FCReadExceptionStatus):
				return []byte{0x5A}, nil
			case byte(FCReadFIFOQueue):
				return []byte{0, 4, 0, 1, 0xBE, 0xEF}, nil
			case 0x41:
				return append([]byte{0xAB}, req.Payload...), nil
			case 0x42:
				return nil, ErrIllegalDataValue
			case 0x43:
				return make([]byte, 253), nil
			case 0x44:
				return make([]byte, 252), nil
			}
			return nil, ErrIllegalFunction
		}
		p := e2eStart(t, kind, rd, e2eOpts{})
		c := p.client
		ctx := context.Background()

		if v, err := c.DiagnosticLoopback(ctx, e2eUnit, 0x7777); err != nil || v != 0x7777 {
			t.Errorf("DiagnosticLoopback through raw = 0x%04X, %v", v, err)
		}
		if rs, err := c.ReportServerID(ctx, e2eUnit); err != nil || !reflect.DeepEqual(rs.Data, []byte{'R', 'W', 0xFF}) {
			t.Errorf("ReportServerID through raw = %+v, %v", rs, err)
		}
		if v, err := c.ReadExceptionStatus(ctx, e2eUnit); err != nil || v != 0x5A {
			t.Errorf("ReadExceptionStatus through raw = 0x%02X, %v", v, err)
		}
		if v, err := c.ReadFIFOQueue(ctx, e2eUnit, 1); err != nil || !reflect.DeepEqual(v, []uint16{0xBEEF}) {
			t.Errorf("ReadFIFOQueue through raw = %04X, %v", v, err)
		}
		if err := c.MaskWriteRegister(ctx, e2eUnit, 3, 0xFF00, 0x0012); err != nil {
			t.Errorf("MaskWriteRegister through raw: %v", err)
		}
		if err := c.WriteFileRecords(ctx, e2eUnit, []FileRecord{{FileNumber: 1, RecordNumber: 2, Data: []uint16{3}}}); err != nil {
			t.Errorf("WriteFileRecords through raw: %v", err)
		}
		seen := rd.take()
		if len(seen) != 6 {
			t.Fatalf("raw handler saw %d requests, want 6: %+v", len(seen), seen)
		}
		if r := seen[0]; r.FunctionCode != FCDiagnostics || r.UnitID != e2eUnit || r.ClientAddr == "" ||
			!reflect.DeepEqual(r.Payload, []byte{0x00, 0x00, 0x77, 0x77}) {
			t.Errorf("raw request = %+v", r)
		}

		// Vendor function codes.
		res := sendRawFC(t, c, e2eUnit, 0x41, []byte{1, 2, 3})
		if res.FunctionCode != 0x41 || !reflect.DeepEqual(res.Payload, []byte{0xAB, 1, 2, 3}) {
			t.Errorf("vendor FC 0x41 = %+v", res)
		}
		res = sendRawFC(t, c, e2eUnit, 0x42, nil)
		if res.FunctionCode != 0xC2 || !reflect.DeepEqual(res.Payload, []byte{byte(exIllegalDataValue)}) {
			t.Errorf("vendor FC 0x42 = %+v, want Illegal Data Value", res)
		}
		res = sendRawFC(t, c, e2eUnit, 0x43, nil)
		if res.FunctionCode != 0xC3 || !reflect.DeepEqual(res.Payload, []byte{byte(exServerDeviceFailure)}) {
			t.Errorf("oversized raw response = %+v, want Server Device Failure", res)
		}
		res = sendRawFC(t, c, e2eUnit, 0x44, nil)
		if res.FunctionCode != 0x44 || len(res.Payload) != 252 {
			t.Errorf("largest raw response: fc 0x%02X, %d bytes", res.FunctionCode, len(res.Payload))
		}
		res = sendRawFC(t, c, e2eUnit, 0x65, nil)
		if res.FunctionCode != 0xE5 || !reflect.DeepEqual(res.Payload, []byte{byte(exIllegalFunction)}) {
			t.Errorf("FC 0x65 = %+v, want Illegal Function", res)
		}
		rd.take()

		// The mandatory functions never reach the raw handler.
		dev.setHolding(5, 0x5555)
		if v, err := c.ReadHoldingRegister(ctx, e2eUnit, 5); err != nil || v != 0x5555 {
			t.Errorf("ReadHoldingRegister = 0x%04X, %v", v, err)
		}
		if err := c.WriteCoil(ctx, e2eUnit, 1, true); err != nil {
			t.Errorf("WriteCoil: %v", err)
		}
		if seen := rd.take(); len(seen) != 0 {
			t.Errorf("raw handler saw %+v", seen)
		}
		if n := len(dev.takeCalls()); n != 2 {
			t.Errorf("device saw %d requests, want 2", n)
		}
		if p.conns() != 1 {
			t.Errorf("server connections = %d, want 1", p.conns())
		}
	})
}

// Many clients use the new functions at once while the device randomly fails
// requests. Each client works on its own file record, so every successful read
// must see that client's last write. Run with -race.
func TestE2E_ServerFunctions_Parallel(t *testing.T) {
	const clients, rounds = 8, 40
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		dev.mu.Lock()
		dev.files[10] = map[uint16]uint16{}
		for i := 0; i < clients; i++ {
			dev.files[10][uint16(i)] = 0
		}
		dev.mu.Unlock()
		var n atomic.Uint64
		dev.setHook(func(_ context.Context, c e2eCall) error {
			// Every seventh request fails; writes fail before they are applied.
			if n.Add(1)%7 == 0 {
				return ErrServerDeviceBusy
			}
			return nil
		})
		p := e2eStart(t, kind, dev, e2eOpts{server: func(c *ServerConfig) { c.MaxClients = clients + 1 }})
		ctx := context.Background()

		busy := func(err error) bool { return errors.Is(err, ErrServerDeviceBusy) }
		var wg sync.WaitGroup
		errs := make(chan error, clients*rounds*5)
		for i := 0; i < clients; i++ {
			c := p.newClient(nil)
			if err := c.Open(); err != nil {
				t.Fatalf("Open: %v", err)
			}
			wg.Add(1)
			go func(i int, c *Client) {
				defer wg.Done()
				rec := uint16(i)
				var last uint16
				for r := 1; r <= rounds; r++ {
					v := uint16(i<<8 | r)
					err := c.WriteFileRecords(ctx, e2eUnit, []FileRecord{{FileNumber: 10, RecordNumber: rec, Data: []uint16{v}}})
					switch {
					case err == nil:
						last = v
					case !busy(err):
						errs <- fmt.Errorf("client %d WriteFileRecords: %w", i, err)
					}
					got, err := c.ReadFileRecords(ctx, e2eUnit, []FileRecordRequest{{FileNumber: 10, RecordNumber: rec, RecordLength: 1}})
					switch {
					case err == nil && got[0][0] != last:
						errs <- fmt.Errorf("client %d: record = 0x%04X, want 0x%04X", i, got[0][0], last)
					case err != nil && !busy(err):
						errs <- fmt.Errorf("client %d ReadFileRecords: %w", i, err)
					}
					if lb, err := c.DiagnosticLoopback(ctx, e2eUnit, v); err == nil && lb != v || err != nil && !busy(err) {
						errs <- fmt.Errorf("client %d DiagnosticLoopback = 0x%04X, %v", i, lb, err)
					}
					if q, err := c.ReadFIFOQueue(ctx, e2eUnit, 0x04DE); err == nil && len(q) != 2 || err != nil && !busy(err) {
						errs <- fmt.Errorf("client %d ReadFIFOQueue = %v, %v", i, q, err)
					}
					if rs, err := c.ReportServerID(ctx, e2eUnit); err == nil && len(rs.Data) != 5 || err != nil && !busy(err) {
						errs <- fmt.Errorf("client %d ReportServerID = %+v, %v", i, rs, err)
					}
				}
				if got := dev.fileRegs(10, rec, 1)[0]; got != last {
					errs <- fmt.Errorf("client %d: device holds 0x%04X, want 0x%04X", i, got, last)
				}
			}(i, c)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
		if got := n.Load(); got != clients*rounds*5 {
			t.Errorf("device saw %d requests, want %d", got, clients*rounds*5)
		}
	})
}
