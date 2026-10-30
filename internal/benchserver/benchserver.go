// SPDX-License-Identifier: MIT

// Package benchserver serves deterministic Modbus data with a configurable
// delay per request, for the benchmarks of this module.
//
// The handler counts requests and protocol data unit (PDU) bytes, so a
// benchmark can report them next to time and allocations. Read sets define
// the same reads for every read strategy, so strategies are compared on
// identical work.
package benchserver

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-modbus"
)

// RegisterValue is the value the handler serves for a register address.
func RegisterValue(addr uint16) uint16 { return addr ^ 0x5A5A }

// BitValue is the value the handler serves for a coil or discrete input address.
func BitValue(addr uint16) bool { return addr%3 == 0 }

// Handler is a modbus.RequestHandler that serves RegisterValue and BitValue
// for every unit ID and address, after Delay per request. It refuses writes.
type Handler struct {
	// Delay is the time each request takes; zero answers at once. It is
	// read on every request and must not change while the server runs.
	Delay time.Duration

	requests atomic.Int64
	pduBytes atomic.Int64
}

// Requests returns the number of requests served since the last Reset.
func (h *Handler) Requests() int64 { return h.requests.Load() }

// PDUBytes returns the request and response PDU bytes of the requests
// served since the last Reset, transport framing excluded.
func (h *Handler) PDUBytes() int64 { return h.pduBytes.Load() }

// Reset sets the request and byte counters to zero.
func (h *Handler) Reset() {
	h.requests.Store(0)
	h.pduBytes.Store(0)
}

// ReportMetrics reports requests/op and pdu-bytes/op for the b.N operations
// run since the last Reset.
func (h *Handler) ReportMetrics(b *testing.B) {
	b.ReportMetric(float64(h.Requests())/float64(b.N), "requests/op")
	b.ReportMetric(float64(h.PDUBytes())/float64(b.N), "pdu-bytes/op")
}

// serve counts one read of quantity registers (or bits) and waits Delay.
func (h *Handler) serve(ctx context.Context, quantity uint16, bits bool) error {
	data := 2 * int64(quantity)
	if bits {
		data = (int64(quantity) + 7) / 8
	}
	h.requests.Add(1)
	// Request: function code, address, quantity. Response: function code,
	// byte count, data.
	h.pduBytes.Add(5 + 2 + data)
	if h.Delay <= 0 {
		return nil
	}
	t := time.NewTimer(h.Delay)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return modbus.ErrServerDeviceFailure
	}
}

func bitsFrom(addr, quantity uint16) []bool {
	out := make([]bool, quantity)
	for i := range out {
		out[i] = BitValue(addr + uint16(i))
	}
	return out
}

func registersFrom(addr, quantity uint16) []uint16 {
	out := make([]uint16, quantity)
	for i := range out {
		out[i] = RegisterValue(addr + uint16(i))
	}
	return out
}

// HandleCoils implements modbus.RequestHandler.
func (h *Handler) HandleCoils(ctx context.Context, req *modbus.CoilsRequest) ([]bool, error) {
	if req.IsWrite {
		return nil, modbus.ErrIllegalFunction
	}
	if err := h.serve(ctx, req.Quantity, true); err != nil {
		return nil, err
	}
	return bitsFrom(req.Addr, req.Quantity), nil
}

// HandleDiscreteInputs implements modbus.RequestHandler.
func (h *Handler) HandleDiscreteInputs(ctx context.Context, req *modbus.DiscreteInputsRequest) ([]bool, error) {
	if err := h.serve(ctx, req.Quantity, true); err != nil {
		return nil, err
	}
	return bitsFrom(req.Addr, req.Quantity), nil
}

// HandleHoldingRegisters implements modbus.RequestHandler.
func (h *Handler) HandleHoldingRegisters(ctx context.Context, req *modbus.HoldingRegistersRequest) ([]uint16, error) {
	if req.IsWrite {
		return nil, modbus.ErrIllegalFunction
	}
	if err := h.serve(ctx, req.Quantity, false); err != nil {
		return nil, err
	}
	return registersFrom(req.Addr, req.Quantity), nil
}

// HandleInputRegisters implements modbus.RequestHandler.
func (h *Handler) HandleInputRegisters(ctx context.Context, req *modbus.InputRegistersRequest) ([]uint16, error) {
	if err := h.serve(ctx, req.Quantity, false); err != nil {
		return nil, err
	}
	return registersFrom(req.Addr, req.Quantity), nil
}

// Start serves h over Modbus TCP on a loopback port until tb ends and
// returns an open client for it. The client is closed when tb ends. Each
// configure function may change the client's configuration before it is
// created.
func Start(tb testing.TB, h modbus.RequestHandler, configure ...func(*modbus.Config)) *modbus.Client {
	tb.Helper()
	var (
		srv *modbus.Server
		url string
		err error
	)
	// The port is picked and released before the server binds it: retry
	// when something else took it in between.
	for attempt := 0; attempt < 10; attempt++ {
		url = "tcp://" + freeAddr(tb)
		srv, err = modbus.NewServer(&modbus.ServerConfig{
			URL: url, MaxClients: 64, Timeout: time.Minute, Logger: modbus.NopLogger(),
		}, h)
		if err != nil {
			tb.Fatalf("benchserver: NewServer: %v", err)
		}
		if err = srv.Start(); err == nil {
			break
		}
	}
	if err != nil {
		tb.Fatalf("benchserver: Start: %v", err)
	}
	tb.Cleanup(func() { _ = srv.Stop() })

	cfg := modbus.Config{URL: url, Timeout: 10 * time.Second, Logger: modbus.NopLogger()}
	for _, f := range configure {
		f(&cfg)
	}
	client, err := modbus.New(cfg)
	if err != nil {
		tb.Fatalf("benchserver: New: %v", err)
	}
	if err := client.Open(); err != nil {
		tb.Fatalf("benchserver: Open: %v", err)
	}
	tb.Cleanup(func() { _ = client.Close() })
	return client
}

func freeAddr(tb testing.TB) string {
	tb.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("benchserver: find free port: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		tb.Fatalf("benchserver: release port: %v", err)
	}
	return addr
}

// Delays are the per-request delays the end-to-end benchmarks run at: a
// loopback link, and two delays in the range of serial RTU links.
var Delays = []time.Duration{0, 5 * time.Millisecond, 20 * time.Millisecond}

// Read is one logical read of a read set.
type Read struct {
	UnitID uint8
	FC     modbus.FunctionCode
	Addr   uint16
	Count  uint16
}

// ReadSet is a named list of reads.
type ReadSet struct {
	Name  string
	Reads []Read
}

// ReadSets returns the read sets every read strategy is measured on:
//
//   - contiguous: 32 two-register reads covering holding registers 100-163
//     without gaps;
//   - gapped: 16 two-register reads every 4 registers from holding register
//     1000, two unrequested registers between neighbours;
//   - mixed: holding registers, coils and input registers on two unit IDs.
func ReadSets() []ReadSet {
	var contiguous, gapped, mixed []Read
	for i := uint16(0); i < 32; i++ {
		contiguous = append(contiguous, Read{UnitID: 1, FC: modbus.FCReadHoldingRegisters, Addr: 100 + 2*i, Count: 2})
	}
	for i := uint16(0); i < 16; i++ {
		gapped = append(gapped, Read{UnitID: 1, FC: modbus.FCReadHoldingRegisters, Addr: 1000 + 4*i, Count: 2})
	}
	for i := uint16(0); i < 10; i++ {
		mixed = append(mixed, Read{UnitID: 1, FC: modbus.FCReadHoldingRegisters, Addr: 200 + 2*i, Count: 2})
	}
	for i := uint16(0); i < 4; i++ {
		mixed = append(mixed, Read{UnitID: 1, FC: modbus.FCReadCoils, Addr: 8 * i, Count: 8})
	}
	for i := uint16(0); i < 8; i++ {
		mixed = append(mixed, Read{UnitID: 2, FC: modbus.FCReadInputRegisters, Addr: 300 + i, Count: 1})
	}
	return []ReadSet{
		{Name: "contiguous", Reads: contiguous},
		{Name: "gapped", Reads: gapped},
		{Name: "mixed", Reads: mixed},
	}
}

// ReadEach performs every read of set with its own request, in order: the
// strategy without any merging.
func ReadEach(ctx context.Context, c *modbus.Client, set ReadSet) error {
	for _, r := range set.Reads {
		var err error
		switch r.FC {
		case modbus.FCReadCoils:
			_, err = c.ReadCoils(ctx, r.UnitID, r.Addr, r.Count)
		case modbus.FCReadDiscreteInputs:
			_, err = c.ReadDiscreteInputs(ctx, r.UnitID, r.Addr, r.Count)
		case modbus.FCReadHoldingRegisters:
			_, err = c.ReadRegisters(ctx, r.UnitID, r.Addr, r.Count, modbus.HoldingRegister)
		case modbus.FCReadInputRegisters:
			_, err = c.ReadRegisters(ctx, r.UnitID, r.Addr, r.Count, modbus.InputRegister)
		default:
			err = modbus.ErrUnexpectedParameters
		}
		if err != nil {
			return err
		}
	}
	return nil
}
