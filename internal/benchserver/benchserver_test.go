// SPDX-License-Identifier: MIT

package benchserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/otfabric/go-modbus"
)

func TestHandler_Serves(t *testing.T) {
	h := &Handler{}
	c := Start(t, h)
	ctx := context.Background()

	regs, err := c.ReadRegisters(ctx, 1, 100, 3, modbus.HoldingRegister)
	if err != nil || len(regs) != 3 || regs[0] != RegisterValue(100) || regs[2] != RegisterValue(102) {
		t.Errorf("holding registers = %v, %v", regs, err)
	}
	regs, err = c.ReadRegisters(ctx, 2, 7, 1, modbus.InputRegister)
	if err != nil || len(regs) != 1 || regs[0] != RegisterValue(7) {
		t.Errorf("input registers = %v, %v", regs, err)
	}
	bits, err := c.ReadCoils(ctx, 1, 0, 4)
	if err != nil || len(bits) != 4 || bits[0] != BitValue(0) || bits[1] != BitValue(1) || bits[3] != BitValue(3) {
		t.Errorf("coils = %v, %v", bits, err)
	}
	bits, err = c.ReadDiscreteInputs(ctx, 1, 9, 2)
	if err != nil || len(bits) != 2 || bits[0] != BitValue(9) || bits[1] != BitValue(10) {
		t.Errorf("discrete inputs = %v, %v", bits, err)
	}
	if n := h.Requests(); n != 4 {
		t.Errorf("%d requests, want 4", n)
	}
	// 5 request bytes each; responses 2+2*3, 2+2*1, 2+1, 2+1.
	if b := h.PDUBytes(); b != 4*5+8+4+3+3 {
		t.Errorf("%d PDU bytes", b)
	}

	if err := c.WriteRegister(ctx, 1, 0, 1); !errors.Is(err, modbus.ErrIllegalFunction) {
		t.Errorf("WriteRegister = %v, want ErrIllegalFunction", err)
	}
	if err := c.WriteCoil(ctx, 1, 0, true); !errors.Is(err, modbus.ErrIllegalFunction) {
		t.Errorf("WriteCoil = %v, want ErrIllegalFunction", err)
	}
	h.Reset()
	if h.Requests() != 0 || h.PDUBytes() != 0 {
		t.Error("Reset left counts")
	}
}

func TestHandler_Delay(t *testing.T) {
	h := &Handler{Delay: 50 * time.Millisecond}
	c := Start(t, h, func(cfg *modbus.Config) { cfg.Timeout = 5 * time.Second })
	start := time.Now()
	if _, err := c.ReadRegisters(context.Background(), 1, 0, 1, modbus.HoldingRegister); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d < h.Delay {
		t.Errorf("request took %v, delay %v", d, h.Delay)
	}

	// A request whose client gives up before the delay ends.
	h2 := &Handler{Delay: time.Minute}
	c2 := Start(t, h2, func(cfg *modbus.Config) { cfg.Timeout = 50 * time.Millisecond })
	if _, err := c2.ReadCoils(context.Background(), 1, 0, 1); err == nil {
		t.Error("read during a one-minute delay succeeded")
	}
}

func TestReadEach(t *testing.T) {
	h := &Handler{}
	c := Start(t, h)
	for _, set := range ReadSets() {
		h.Reset()
		if err := ReadEach(context.Background(), c, set); err != nil {
			t.Fatalf("%s: %v", set.Name, err)
		}
		if n := h.Requests(); n != int64(len(set.Reads)) {
			t.Errorf("%s: %d requests for %d reads", set.Name, n, len(set.Reads))
		}
	}
	bad := ReadSet{Name: "bad", Reads: []Read{
		{UnitID: 1, FC: modbus.FCReadDiscreteInputs, Addr: 0, Count: 1},
		{UnitID: 1, FC: modbus.FCWriteSingleRegister, Addr: 0, Count: 1},
	}}
	if err := ReadEach(context.Background(), c, bad); !errors.Is(err, modbus.ErrUnexpectedParameters) {
		t.Errorf("ReadEach with a write = %v", err)
	}
	if err := ReadEach(context.Background(), c, ReadSet{Reads: []Read{{UnitID: 1, FC: modbus.FCReadInputRegisters, Addr: 0, Count: 0}}}); err == nil {
		t.Error("ReadEach of zero registers succeeded")
	}
}

func TestReportMetrics(t *testing.T) {
	h := &Handler{}
	c := Start(t, h)
	r := testing.Benchmark(func(b *testing.B) {
		h.Reset()
		for i := 0; i < b.N; i++ {
			if _, err := c.ReadRegisters(context.Background(), 1, 0, 2, modbus.HoldingRegister); err != nil {
				b.Fatal(err)
			}
		}
		h.ReportMetrics(b)
	})
	if got := r.Extra["requests/op"]; got != 1 {
		t.Errorf("requests/op = %v, want 1", got)
	}
	if got := r.Extra["pdu-bytes/op"]; got != 11 {
		t.Errorf("pdu-bytes/op = %v, want 11", got)
	}
}
