package modbus

import (
	"context"
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
		{0x01, 0x00, 0x00, 0x00, 0x08},                                                       // FC01 read coils
		{0x02, 0x00, 0x00, 0x00, 0x08},                                                       // FC02 read discrete inputs
		{0x03, 0x00, 0x00, 0x00, 0x02},                                                       // FC03 read holding
		{0x04, 0x00, 0x00, 0x00, 0x02},                                                       // FC04 read input
		{0x05, 0x00, 0x00, 0xFF, 0x00},                                                       // FC05 write coil
		{0x06, 0x00, 0x07, 0xBE, 0xEF},                                                       // FC06 write register
		{0x0F, 0x00, 0x00, 0x00, 0x02, 0x01, 0x03},                                           // FC15 write coils
		{0x10, 0x00, 0x00, 0x00, 0x01, 0x02, 0x12, 0x34},                                     // FC16 write registers
		{0x16, 0x00, 0x03, 0x00, 0xF2, 0x00, 0x25},                                           // FC22 mask write
		{0x17, 0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00, 0x02, 0x04, 0xAA, 0xBB, 0xCC, 0xDD}, // FC23
		{0x2B, 0x0E, 0x01, 0x00},                                                             // FC43 read device id
		{0x08},                                                                               // unsupported FC
		{},                                                                                   // empty
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
		// The invariant is simply that this returns without panicking.
		_, _ = tr.ExecuteRequest(ctxTimeout, &adu.Request{
			UnitID:       1,
			FunctionCode: byte(FCReadHoldingRegisters),
			Payload:      []byte{0x00, 0x00, 0x00, 0x01},
		})
		cancel()
		_ = clientSide.Close()
		<-done
	})
}
