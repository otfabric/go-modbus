// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/logging"
	"github.com/otfabric/go-modbus/internal/transport"
)

// This file holds Go native fuzz targets that provide adversarial feedback on
// the back-to-back path: arbitrary request PDUs into the real server, and
// arbitrary response bytes into the client's parser. The invariant in both
// directions is robustness: no panic, no hang, and only well-formed frames or
// typed errors.
//
// The seed corpora execute as ordinary unit tests under `go test`; run extended
// exploration with `go test -run=^$ -fuzz=FuzzServerRequest -fuzztime=30s`.

// startFuzzServer starts a TCP server backed by a reference device and returns
// its address plus a stop function.
func startFuzzServer(tb testing.TB) (string, func()) {
	tb.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	server, err := NewServer(&ServerConfig{URL: "tcp://" + addr, MaxClients: 256}, newRefDevice())
	if err != nil {
		tb.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		tb.Fatalf("Start: %v", err)
	}
	return addr, func() { _ = server.Stop() }
}

// FuzzServerRequest feeds arbitrary PDUs (function code + payload) wrapped in a
// valid MBAP frame to a live server and asserts the response invariants: the
// server must never panic, and must reply with either an echoed FC or an
// exception FC carrying exactly one valid exception-code byte, or close the
// connection (protocol error).
func FuzzServerRequest(f *testing.F) {
	seeds := [][]byte{
		// FC01 read coils.
		{0x01, 0x00, 0x00, 0x00, 0x08},
		// FC02 read discrete inputs.
		{0x02, 0x00, 0x00, 0x00, 0x08},
		// FC03 read holding.
		{0x03, 0x00, 0x00, 0x00, 0x02},
		// FC04 read input.
		{0x04, 0x00, 0x00, 0x00, 0x02},
		// FC05 write coil.
		{0x05, 0x00, 0x00, 0xFF, 0x00},
		// FC06 write register.
		{0x06, 0x00, 0x07, 0xBE, 0xEF},
		// FC15 write coils.
		{0x0F, 0x00, 0x00, 0x00, 0x02, 0x01, 0x03},
		// FC16 write registers.
		{0x10, 0x00, 0x00, 0x00, 0x01, 0x02, 0x12, 0x34},
		// FC22 mask write.
		{0x16, 0x00, 0x03, 0x00, 0xF2, 0x00, 0x25},
		// FC23.
		{0x17, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00, 0x02, 0x04, 0xAA, 0xBB, 0xCC, 0xDD},
		// FC43 read device id.
		{0x2B, 0x0E, 0x01, 0x00},
		// Unsupported FC.
		{0x08},
		// Empty.
		{},
	}
	for _, s := range seeds {
		f.Add(s)
	}

	addr, stop := startFuzzServer(f)
	defer stop()

	f.Fuzz(func(t *testing.T, data []byte) {
		var fc byte
		var payload []byte
		if len(data) > 0 {
			fc = data[0]
			payload = data[1:]
		}
		// Oversized payloads cannot be framed; skip (client would reject too).
		if len(payload)+2 > adu.MBAPLengthMax {
			return
		}

		conn, err := net.Dial("tcp", addr)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

		const txnID uint16 = 1
		frame := adu.AssembleMBAP(txnID, refUnitID, fc, payload)
		if _, err := conn.Write(frame); err != nil {
			return
		}

		res, err := readMBAPResponse(conn)
		if err != nil {
			// A closed connection (protocol error) is an acceptable outcome.
			return
		}
		if res.TransactionID != txnID {
			t.Errorf("response txn = 0x%04x, want 0x%04x", res.TransactionID, txnID)
		}
		assertMBAPResponseWellFormed(t, res, refUnitID, fc)
	})
}

// FuzzClientResponseParse feeds arbitrary bytes to the client's response parser
// over an in-memory pipe and asserts the parser never panics or hangs; it must
// return a value or a typed error for any input.
func FuzzClientResponseParse(f *testing.F) {
	seeds := [][]byte{
		{0x00, 0x01, 0x00, 0x00, 0x00, 0x05, 0x01, 0x03, 0x02, 0x00, 0x42}, // valid FC03 response
		{0x00, 0x01, 0x00, 0x00, 0x00, 0x03, 0x01, 0x83, 0x02},             // FC03 exception
		{0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x01},                         // truncated (mbapLen=1)
		{0xFF, 0xFF, 0x00, 0x00, 0xFF, 0xFF, 0x01},                         // huge length
		{0x00, 0x01, 0x00, 0x07, 0x00, 0x05, 0x01, 0x03, 0x02, 0x00, 0x42}, // bad protocol id
		{},
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		clientSide, serverSide := net.Pipe()

		done := make(chan struct{})
		go func() {
			defer close(done)
			buf := make([]byte, 512)
			_ = serverSide.SetReadDeadline(time.Now().Add(time.Second))
			// Drain the outgoing request frame, then reply with the fuzz bytes.
			_, _ = serverSide.Read(buf)
			_, _ = serverSide.Write(data)
			_ = serverSide.Close()
		}()

		tr := transport.NewTCP(clientSide, 500*time.Millisecond, logging.NopLogger())
		ctxTimeout, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		res, err := tr.ExecuteRequest(ctxTimeout, &adu.Request{
			UnitID:       1,
			FunctionCode: byte(FCReadHoldingRegisters),
			Payload:      []byte{0x00, 0x00, 0x00, 0x01},
		})
		cancel()
		_ = clientSide.Close()
		<-done

		// Either an error, or a response that is one whole frame of the input,
		// carries the transaction ID of the request (the first on this
		// connection) and fits a Modbus/TCP ADU.
		if err != nil {
			if res != nil {
				t.Fatalf("a response (%+v) with the error %v", res, err)
			}
			return
		}
		if res == nil {
			t.Fatal("neither a response nor an error")
		}
		if res.TransactionID != 1 {
			t.Fatalf("accepted transaction ID %d for request 1", res.TransactionID)
		}
		if 2+len(res.Payload) > adu.MBAPLengthMax {
			t.Fatalf("accepted a %d-byte payload", len(res.Payload))
		}
		if frame := adu.AssembleMBAP(res.TransactionID, res.UnitID, res.FunctionCode, res.Payload); !bytes.Contains(data, frame) {
			t.Fatalf("accepted % X, which the input % X does not contain", frame, data)
		}
	})
}

// fuzzDecodeOp is one client call whose response decoder FuzzClientResponseDecode
// drives. call makes the request, shaped by arg, and returns the error of the
// call; when the call succeeded it checks the result against the request and
// against payload, the response PDU data the device sent.
type fuzzDecodeOp struct {
	name string
	call func(t *testing.T, c *Client, tr *scriptedTransport, arg uint16, payload []byte) error
}

// fuzzUnit is the unit ID of every request FuzzClientResponseDecode makes.
const fuzzUnit = 0x11

func fuzzDecodeOps() []fuzzDecodeOp {
	ctx := context.Background()
	type call = func(*testing.T, *Client, *scriptedTransport, uint16, []byte) error
	bits := func(read func(*Client, uint16) ([]bool, error)) call {
		return func(t *testing.T, c *Client, _ *scriptedTransport, arg uint16, payload []byte) error {
			qty := 1 + arg%maxReadCoils
			v, err := read(c, qty)
			if err == nil && (len(v) != int(qty) || len(payload) != 1+(int(qty)+7)/8) {
				t.Fatalf("%d bits returned for a quantity of %d and a %d-byte payload", len(v), qty, len(payload))
			}
			return err
		}
	}
	regs := func(read func(*Client, uint16) ([]uint16, error)) call {
		return func(t *testing.T, c *Client, _ *scriptedTransport, arg uint16, payload []byte) error {
			qty := 1 + arg%maxReadRegisters
			v, err := read(c, qty)
			if err == nil && (len(v) != int(qty) || len(payload) != 1+2*int(qty)) {
				t.Fatalf("%d registers returned for a quantity of %d and a %d-byte payload", len(v), qty, len(payload))
			}
			return err
		}
	}
	// A write succeeds only when the response is the echo the request calls
	// for: the first echoLen bytes of the request payload (all of it for 0).
	echo := func(echoLen int, write func(*Client, uint16) error) call {
		return func(t *testing.T, c *Client, tr *scriptedTransport, arg uint16, payload []byte) error {
			err := write(c, arg)
			if err != nil {
				return err
			}
			sent := tr.requests()
			want := sent[len(sent)-1].Payload
			if echoLen > 0 {
				want = want[:echoLen]
			}
			if !bytes.Equal(payload, want) {
				t.Fatalf("a write accepted the response % X, the echo is % X", payload, want)
			}
			return nil
		}
	}
	deviceID := func(t *testing.T, di *DeviceIdentification, individual bool) {
		seen := map[DeviceIDObjectID]bool{}
		for _, o := range di.Objects {
			if seen[o.ID] {
				t.Fatalf("object 0x%02X returned twice", uint8(o.ID))
			}
			seen[o.ID] = true
			if len(o.Value) > 0xFF {
				t.Fatalf("object 0x%02X is %d bytes long", uint8(o.ID), len(o.Value))
			}
		}
		if di.MoreFollows || di.NextObjectID != 0 {
			t.Fatalf("a complete identification with MoreFollows=%v, NextObjectID=0x%02X", di.MoreFollows, uint8(di.NextObjectID))
		}
		if individual && len(di.Objects) != 1 {
			t.Fatalf("individual access returned %d objects", len(di.Objects))
		}
	}
	return []fuzzDecodeOp{
		{"ReadCoils", bits(func(c *Client, q uint16) ([]bool, error) { return c.ReadCoils(ctx, fuzzUnit, 0, q) })},
		{"ReadDiscreteInputs", bits(func(c *Client, q uint16) ([]bool, error) { return c.ReadDiscreteInputs(ctx, fuzzUnit, 0, q) })},
		{"ReadHoldingRegisters", regs(func(c *Client, q uint16) ([]uint16, error) { return c.ReadHoldingRegisters(ctx, fuzzUnit, 0, q) })},
		{"ReadInputRegisters", regs(func(c *Client, q uint16) ([]uint16, error) { return c.ReadInputRegisters(ctx, fuzzUnit, 0, q) })},
		{"ReadWriteMultipleRegisters", regs(func(c *Client, q uint16) ([]uint16, error) {
			return c.ReadWriteMultipleRegisters(ctx, fuzzUnit, 0, q, 0, []uint16{1})
		})},
		{"ReadFIFOQueue", func(t *testing.T, c *Client, _ *scriptedTransport, arg uint16, payload []byte) error {
			v, err := c.ReadFIFOQueue(ctx, fuzzUnit, arg)
			if err == nil && (len(v) > maxFIFOCount || len(payload) != 4+2*len(v)) {
				t.Fatalf("%d FIFO values returned from a %d-byte payload", len(v), len(payload))
			}
			return err
		}},
		{"ReadFileRecords", func(t *testing.T, c *Client, _ *scriptedTransport, arg uint16, payload []byte) error {
			// One to three sub-requests of one to four registers each.
			reqs := make([]FileRecordRequest, 1+arg%3)
			total := 1
			for i := range reqs {
				reqs[i] = FileRecordRequest{FileNumber: 4, RecordNumber: uint16(i), RecordLength: 1 + (arg>>(2+2*i))%4}
				total += 2 + 2*int(reqs[i].RecordLength)
			}
			recs, err := c.ReadFileRecords(ctx, fuzzUnit, reqs)
			if err != nil {
				return err
			}
			if len(recs) != len(reqs) || len(payload) != total {
				t.Fatalf("%d records returned for %d sub-requests from a %d-byte payload, want %d bytes", len(recs), len(reqs), len(payload), total)
			}
			for i := range recs {
				if len(recs[i]) != int(reqs[i].RecordLength) {
					t.Fatalf("record %d has %d registers, %d were asked for", i, len(recs[i]), reqs[i].RecordLength)
				}
			}
			return nil
		}},
		{"WriteFileRecords", echo(0, func(c *Client, arg uint16) error {
			return c.WriteFileRecords(ctx, fuzzUnit, []FileRecord{{FileNumber: 4, RecordNumber: arg % 10000, Data: []uint16{arg, ^arg}}})
		})},
		{"ReadDeviceIdentification", func(t *testing.T, c *Client, _ *scriptedTransport, arg uint16, _ []byte) error {
			category := DeviceIDCategory(1 + arg%4)
			di, err := c.ReadDeviceIdentification(ctx, fuzzUnit, category, DeviceIDObjectID(arg>>8))
			if err == nil {
				deviceID(t, di, category == DeviceIDIndividual)
			}
			return err
		}},
		{"ReadAllDeviceIdentification", func(t *testing.T, c *Client, _ *scriptedTransport, _ uint16, _ []byte) error {
			di, err := c.ReadAllDeviceIdentification(ctx, fuzzUnit)
			if err == nil {
				deviceID(t, di, false)
			}
			return err
		}},
		{"GetCommEventLog", func(t *testing.T, c *Client, _ *scriptedTransport, _ uint16, payload []byte) error {
			cl, err := c.GetCommEventLog(ctx, fuzzUnit)
			if err == nil && (len(payload) < 7 || !bytes.Equal(cl.Events, payload[7:]) || int(payload[0]) != len(payload)-1) {
				t.Fatalf("event log %+v from the payload % X", cl, payload)
			}
			return err
		}},
		{"GetCommEventCounter", func(t *testing.T, c *Client, _ *scriptedTransport, _ uint16, payload []byte) error {
			cr, err := c.GetCommEventCounter(ctx, fuzzUnit)
			if err == nil && (len(payload) != 4 || cr.Status != bytesToUint16(BigEndian, payload[0:2]) || cr.EventCount != bytesToUint16(BigEndian, payload[2:4])) {
				t.Fatalf("event counter %+v from the payload % X", cr, payload)
			}
			return err
		}},
		{"ReadExceptionStatus", func(t *testing.T, c *Client, _ *scriptedTransport, _ uint16, payload []byte) error {
			st, err := c.ReadExceptionStatus(ctx, fuzzUnit)
			if err == nil && (len(payload) != 1 || st != payload[0]) {
				t.Fatalf("exception status 0x%02X from the payload % X", st, payload)
			}
			return err
		}},
		{"ReportServerID", func(t *testing.T, c *Client, _ *scriptedTransport, _ uint16, payload []byte) error {
			rs, err := c.ReportServerID(ctx, fuzzUnit)
			if err == nil && (len(payload) < 1 || int(payload[0]) != len(payload)-1 || !bytes.Equal(rs.Data, payload[1:])) {
				t.Fatalf("server ID % X from the payload % X", rs.Data, payload)
			}
			return err
		}},
		{"Diagnostics", func(t *testing.T, c *Client, _ *scriptedTransport, arg uint16, payload []byte) error {
			sub := DiagnosticSubFunction(arg)
			dr, err := c.Diagnostics(ctx, fuzzUnit, sub, []byte{0xA5, 0x37})
			if err == nil && (len(payload) < 2 || dr.SubFunction != sub || bytesToUint16(BigEndian, payload[0:2]) != arg || !bytes.Equal(dr.Data, payload[2:])) {
				t.Fatalf("diagnostics %+v for sub-function 0x%04X from the payload % X", dr, arg, payload)
			}
			return err
		}},
		{"WriteCoil", echo(0, func(c *Client, arg uint16) error { return c.WriteCoil(ctx, fuzzUnit, arg, arg&1 == 1) })},
		{"WriteRegister", echo(0, func(c *Client, arg uint16) error { return c.WriteRegister(ctx, fuzzUnit, arg, ^arg) })},
		{"WriteCoils", echo(4, func(c *Client, arg uint16) error {
			return c.WriteCoils(ctx, fuzzUnit, 0, make([]bool, 1+arg%maxWriteCoils))
		})},
		{"WriteRegisters", echo(4, func(c *Client, arg uint16) error {
			return c.WriteRegisters(ctx, fuzzUnit, 0, make([]uint16, 1+arg%maxWriteRegisters))
		})},
		{"MaskWriteRegister", echo(0, func(c *Client, arg uint16) error { return c.MaskWriteRegister(ctx, fuzzUnit, arg, arg, ^arg) })},
	}
}

// fuzzDecodeSeed is one seed of FuzzClientResponseDecode. ok marks the
// well-formed answers, on which the call succeeds.
type fuzzDecodeSeed struct {
	op      string
	fc      uint8
	arg     uint16
	page    bool
	payload []byte
	ok      bool
}

func fuzzDecodeSeeds() []fuzzDecodeSeed {
	obj := func(id DeviceIDObjectID, v string) DeviceIdentificationObject {
		return DeviceIdentificationObject{ID: id, Value: v}
	}
	return []fuzzDecodeSeed{
		// Well-formed answers: each of these calls succeeds.
		{"ReadCoils", 0, 8, false, []byte{0x02, 0xFF, 0x01}, true},
		{"ReadDiscreteInputs", 0, 0, false, []byte{0x01, 0x01}, true},
		{"ReadHoldingRegisters", 0, 1, false, []byte{0x04, 0x12, 0x34, 0x56, 0x78}, true},
		{"ReadInputRegisters", 0, 0, false, []byte{0x02, 0xAB, 0xCD}, true},
		{"ReadWriteMultipleRegisters", 0, 0, false, []byte{0x02, 0x00, 0x01}, true},
		{"ReadFIFOQueue", 0, 0x04DE, false, []byte{0x00, 0x06, 0x00, 0x02, 0x01, 0xB8, 0x12, 0x84}, true},
		{"ReadFileRecords", 0, 0, false, []byte{0x04, 0x03, 0x06, 0xAA, 0xBB}, true},
		{"ReadFileRecords", 0, 1, false, []byte{0x08, 0x03, 0x06, 0xAA, 0xBB, 0x03, 0x06, 0xCC, 0xDD}, true},
		{"WriteFileRecords", 0, 7, false, []byte{0x0B, 0x06, 0x00, 0x04, 0x00, 0x07, 0x00, 0x02, 0x00, 0x07, 0xFF, 0xF8}, true},
		{"ReadDeviceIdentification", 0, 0, false, devIDPage(DeviceIDBasic, 0x01, 0x00, 0x00, obj(0, "vendor"), obj(1, "code"), obj(2, "1.0")), true},
		{"ReadDeviceIdentification", 0, 3, false, devIDPage(DeviceIDIndividual, 0x81, 0x00, 0x00, obj(0, "vendor")), true},
		{"ReadAllDeviceIdentification", 0, 0, false, devIDPage(DeviceIDExtended, 0x83, 0x00, 0x00, obj(0, "vendor"), obj(0x80, "private")), true},
		{"GetCommEventLog", 0, 0, false, []byte{0x08, 0x00, 0x00, 0x01, 0x08, 0x01, 0x21, 0x20, 0x00}, true},
		{"GetCommEventCounter", 0, 0, false, []byte{0xFF, 0xFF, 0x01, 0x08}, true},
		{"ReadExceptionStatus", 0, 0, false, []byte{0x6D}, true},
		{"ReportServerID", 0, 0, false, []byte{0x05, 'G', 'O', 'M', 'B', 0xFF}, true},
		{"ReportServerID", 0, 0, false, append([]byte{0xFF}, make([]byte, 255)...), true},
		{"Diagnostics", 0, 0, false, []byte{0x00, 0x00, 0xA5, 0x37}, true},
		{"WriteCoil", 0, 1, false, []byte{0x00, 0x01, 0xFF, 0x00}, true},
		{"WriteRegister", 0, 1, false, []byte{0x00, 0x01, 0xFF, 0xFE}, true},
		{"WriteCoils", 0, 9, false, []byte{0x00, 0x00, 0x00, 0x0A}, true},
		{"WriteRegisters", 0, 2, false, []byte{0x00, 0x00, 0x00, 0x03}, true},
		{"MaskWriteRegister", 0, 4, false, []byte{0x00, 0x04, 0x00, 0x04, 0xFF, 0xFB}, true},
		// The malformed ones of client_malformed_test.go and hostile_client_test.go.
		// A device that always has more to send: one page per object ID, then
		// the paging is refused; and one that names the same object again.
		{"ReadAllDeviceIdentification", 0, 0, true, devIDPage(DeviceIDExtended, 0x83, 0xFF, 0x01, obj(0x80, "private")), false},
		{"ReadAllDeviceIdentification", 0, 0, false, devIDPage(DeviceIDExtended, 0x83, 0xFF, 0x05, obj(0, "vendor")), false},
		{"ReadCoils", 0, 8, false, []byte{0x01, 0xFF}, false},
		{"ReadCoils", 0, 7, false, []byte{0x02, 0xFF, 0xFF}, false},
		{"ReadHoldingRegisters", 0, 1, false, []byte{0x02, 0xAA, 0xBB}, false},
		{"ReadHoldingRegisters", 0, 124, false, []byte{0xFA}, false},
		{"ReadFileRecords", 0, 1, false, []byte{0x04, 0x03, 0x06, 0xAA, 0xBB}, false},
		{"ReadFileRecords", 0, 0, false, []byte{0x01, 0x00}, false},
		{"ReadFileRecords", 0, 0, false, []byte{0x01, 0x03}, false},
		{"ReadFileRecords", 0, 0, false, []byte{0x03, 0x02, 0x06, 0xAA}, false},
		{"ReadFileRecords", 0, 0, false, []byte{0x06, 0x05, 0x06, 0xAA, 0xBB, 0xCC, 0xDD}, false},
		{"ReadFileRecords", 0, 0, false, []byte{0x02, 0x03, 0x06}, false},
		{"ReadFileRecords", 0, 0, false, []byte{0x06, 0x03, 0x06, 0xAA, 0xBB, 0x00, 0x00}, false},
		{"ReadFileRecords", 0, 0, false, []byte{0x09, 0x03, 0x06, 0xAA, 0xBB}, false},
		{"ReadFIFOQueue", 0, 0, false, []byte{0x00, 0x42, 0x00, 0x20}, false},
		{"ReadFIFOQueue", 0, 0, false, []byte{0xFF, 0xFF, 0xFF, 0xFF}, false},
		{"ReadDeviceIdentification", 0, 0, false, []byte{0x0E, 0x01, 0x01, 0x00, 0x00, 0x02, 0x00, 0xFF, 'a'}, false},
		{"ReadDeviceIdentification", 0, 0, false, []byte{0x0E, 0x01, 0x01, 0xFF, 0x00, 0x00}, false},
		{"ReadDeviceIdentification", 0, 0, false, []byte{0x0D, 0x01, 0x01, 0x00, 0x00, 0x00}, false},
		{"ReadDeviceIdentification", 0, 0, false, []byte{0x0E, 0x01, 0x04, 0x00, 0x00, 0x00}, false},
		{"ReadDeviceIdentification", 0, 0, false, []byte{0x0E, 0x01, 0x01, 0x7F, 0x00, 0x00}, false},
		{"GetCommEventLog", 0, 0, false, []byte{0x05, 0x00, 0x00, 0x00, 0x00, 0x00}, false},
		{"GetCommEventLog", 0, 0, false, []byte{0xFF}, false},
		{"ReportServerID", 0, 0, false, []byte{0xFF, 0x01}, false},
		{"Diagnostics", 0, 1, false, []byte{0x00, 0x00}, false},
		{"Diagnostics", 0, 0, false, []byte{0x00}, false},
		{"WriteRegisters", 0, 2, false, []byte{0x00, 0x00, 0x00, 0x04}, false},
		{"WriteCoil", 0, 1, false, []byte{0x00, 0x01, 0xFF}, false},
		// Exception responses, well-formed and not, and a foreign function code.
		{"ReadHoldingRegisters", 0x80, 0, false, []byte{0x02}, false},
		{"ReadDeviceIdentification", 0x80, 0, false, []byte{0x01}, false},
		{"ReadFileRecords", 0x80, 0, false, []byte{}, false},
		{"ReadCoils", 0x80, 0, false, []byte{0x02, 0x00}, false},
		{"ReadHoldingRegisters", 0x07, 0, false, []byte{0x02, 0x00, 0x01}, false},
		{"ReadCoils", 0, 0, false, nil, false},
	}
}

// fuzzDecodeIndex returns the index of the op called name.
func fuzzDecodeIndex(tb testing.TB, ops []fuzzDecodeOp, name string) uint8 {
	tb.Helper()
	for i, op := range ops {
		if op.name == name {
			return uint8(i)
		}
	}
	tb.Fatalf("no fuzz op %q", name)
	return 0
}

// fuzzDecodeRun is the body of FuzzClientResponseDecode: one call against a
// scripted device, and the checks that hold for every input. It returns the
// error of the call.
func fuzzDecodeRun(t *testing.T, ops []fuzzDecodeOp, opIndex, fc uint8, arg uint16, page bool, payload []byte) error {
	op := ops[int(opIndex)%len(ops)]
	var reqFC byte
	n := 0
	c, tr, _ := newScriptedClient(t, func(req *adu.Request) (*adu.Response, error) {
		reqFC = req.FunctionCode
		n++
		data := bytes.Clone(payload)
		if page && len(data) > 4 {
			data[4] = byte(n)
		}
		return &adu.Response{UnitID: req.UnitID, FunctionCode: req.FunctionCode ^ fc, Payload: data}, nil
	})

	// What a call that makes one request is answered with.
	first := bytes.Clone(payload)
	if page && len(first) > 4 {
		first[4] = 1
	}
	err := op.call(t, c, tr, arg, first)

	sent := len(tr.requests())
	if sent < 1 || sent > 256 {
		t.Fatalf("%s made %d requests, want 1 to 256", op.name, sent)
	}
	if err == nil && fc != 0 {
		t.Fatalf("%s succeeded on function code 0x%02X in answer to 0x%02X", op.name, reqFC^fc, reqFC)
	}
	if fc == 0x80 && len(payload) == 1 {
		// A well-formed exception response is reported as one.
		if _, ok := errors.AsType[*ExceptionError](err); !ok {
			t.Fatalf("%s: exception response 0x%02X reported as %T: %v", op.name, payload[0], err, err)
		}
	}
	return err
}

// FuzzClientResponseDecode feeds an arbitrary response PDU to the client's
// function-level decoders (the byte counts of FC01 to FC04 and FC23, the
// sub-responses of FC20, the echoes of the writes and of FC21, the FIFO of
// FC24, the event log of FC12, FC07, FC11, FC17, FC08 and the object list and
// paging of FC43), through the scripted transport, so that no framing stands
// between the input and the decoder.
//
// op selects the call and arg shapes its request. The device answers every
// request with the function code of the request xor fc (0: a normal response,
// 0x80: an exception response) and the data payload; with page set it writes
// the number of the request into the fifth byte, which is where an FC43
// response names the object to continue from, so that paging can advance.
//
// A call returns an error or a result that agrees with its request and with
// the payload: the lengths are the ones asked for, an echo is the echo, a
// device identification has no object twice. No call makes more than 256
// requests, and none succeeds on a function code that is not its own.
func FuzzClientResponseDecode(f *testing.F) {
	ops := fuzzDecodeOps()
	for _, s := range fuzzDecodeSeeds() {
		f.Add(fuzzDecodeIndex(f, ops, s.op), s.fc, s.arg, s.page, s.payload)
	}
	f.Fuzz(func(t *testing.T, opIndex, fc uint8, arg uint16, page bool, payload []byte) {
		_ = fuzzDecodeRun(t, ops, opIndex, fc, arg, page, payload)
	})
}

// The seeds reach both sides of every decoder: each call has a seed on which it
// succeeds, and the seeds marked well-formed are the ones that succeed.
func TestFuzzClientResponseDecode_Seeds(t *testing.T) {
	ops := fuzzDecodeOps()
	succeeded := map[string]bool{}
	for i, s := range fuzzDecodeSeeds() {
		err := fuzzDecodeRun(t, ops, fuzzDecodeIndex(t, ops, s.op), s.fc, s.arg, s.page, s.payload)
		if (err == nil) != s.ok {
			t.Errorf("seed %d (%s, payload % X): error %v, want success = %v", i, s.op, s.payload, err, s.ok)
		}
		if err == nil {
			succeeded[s.op] = true
		}
	}
	for _, op := range ops {
		if !succeeded[op.name] {
			t.Errorf("no seed on which %s succeeds", op.name)
		}
	}
}
