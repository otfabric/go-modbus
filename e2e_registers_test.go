// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"reflect"
	"testing"
)

// e2eWordPattern returns n reproducible register values.
func e2eWordPattern(n int, seed uint16) []uint16 {
	out := make([]uint16, n)
	for i := range out {
		out[i] = seed + uint16(i)*0x0101 + uint16(i>>3)
	}
	return out
}

func TestE2E_Registers_Read(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		c := p.client
		ctx := context.Background()

		const base = 2000
		holding := e2eWordPattern(125, 0x1000)
		input := e2eWordPattern(125, 0x8000)
		dev.setHolding(base, holding...)
		dev.setInput(base, input...)

		tables := []struct {
			regType RegType
			fc      FunctionCode
			kind    string
			vals    []uint16
		}{
			{HoldingRegister, FCReadHoldingRegisters, "holding", holding},
			{InputRegister, FCReadInputRegisters, "input", input},
		}
		for _, tb := range tables {
			for _, qty := range []uint16{1, 2, 3, 124, 125} {
				got, err := c.ReadRegisters(ctx, e2eUnit, base, qty, tb.regType)
				if err != nil {
					t.Fatalf("ReadRegisters(%s, qty=%d): %v", tb.kind, qty, err)
				}
				if !reflect.DeepEqual(got, tb.vals[:qty]) {
					t.Errorf("ReadRegisters(%s, qty=%d) returned wrong values", tb.kind, qty)
				}
				call := e2eOneCall(t, "ReadRegisters", dev)
				call.ClientAddr, call.ClientRole, call.Words = "", "", nil
				want := e2eCall{Kind: tb.kind, FC: tb.fc, UnitID: e2eUnit, Addr: base, Quantity: qty}
				if !reflect.DeepEqual(call, want) {
					t.Errorf("ReadRegisters(%s, qty=%d): server saw %+v, want %+v", tb.kind, qty, call, want)
				}
			}

			if got, err := c.ReadRegister(ctx, e2eUnit, base+7, tb.regType); err != nil || got != tb.vals[7] {
				t.Errorf("ReadRegister(%s) = 0x%04X, %v; want 0x%04X", tb.kind, got, err, tb.vals[7])
			}
			if call := e2eOneCall(t, "ReadRegister", dev); call.FC != tb.fc || call.Addr != base+7 || call.Quantity != 1 {
				t.Errorf("ReadRegister(%s): server saw %+v", tb.kind, call)
			}

			// ReadRegisterBytes: wire order, odd counts trimmed.
			wire := uint16sToBytes(BigEndian, tb.vals)
			for _, n := range []uint16{1, 2, 3, 4, 249, 250} {
				got, err := c.ReadRegisterBytes(ctx, e2eUnit, base, n, tb.regType)
				if err != nil {
					t.Fatalf("ReadRegisterBytes(%s, %d): %v", tb.kind, n, err)
				}
				if !bytes.Equal(got, wire[:n]) {
					t.Errorf("ReadRegisterBytes(%s, %d) = % X, want % X", tb.kind, n, got, wire[:n])
				}
				call := e2eOneCall(t, "ReadRegisterBytes", dev)
				if call.FC != tb.fc || call.Quantity != (n+1)/2 {
					t.Errorf("ReadRegisterBytes(%s, %d): server saw fc=0x%02X qty=%d", tb.kind, n, uint8(call.FC), call.Quantity)
				}
			}
			_, err := c.ReadRegisterBytes(ctx, e2eUnit, base, 0, tb.regType)
			e2eWantParamError(t, "ReadRegisterBytes(0)", err, dev)
			_, err = c.ReadRegisterBytes(ctx, e2eUnit, base, 251, tb.regType)
			e2eWantParamError(t, "ReadRegisterBytes(251)", err, dev)

			// Quantity limits.
			_, err = c.ReadRegisters(ctx, e2eUnit, base, 0, tb.regType)
			e2eWantParamError(t, "ReadRegisters(qty=0)", err, dev)
			_, err = c.ReadRegisters(ctx, e2eUnit, base, 126, tb.regType)
			e2eWantParamError(t, "ReadRegisters(qty=126)", err, dev)

			// Server-side enforcement of the same limits.
			assertExceptionResponse(t, sendRawFC(t, c, e2eUnit, byte(tb.fc), u16(0, 126)), exIllegalDataValue)
			assertExceptionResponse(t, sendRawFC(t, c, e2eUnit, byte(tb.fc), u16(0, 0)), exIllegalDataValue)
			assertExceptionResponse(t, sendRawFC(t, c, e2eUnit, byte(tb.fc), u16(0xFFFF, 2)), exIllegalDataAddress)
			if calls := dev.takeCalls(); len(calls) != 0 {
				t.Errorf("out-of-limit raw requests reached the handler: %+v", calls)
			}
		}

		// Convenience wrappers.
		if got, err := c.ReadHoldingRegister(ctx, e2eUnit, base+1); err != nil || got != holding[1] {
			t.Errorf("ReadHoldingRegister = 0x%04X, %v", got, err)
		}
		if got, err := c.ReadHoldingRegisters(ctx, e2eUnit, base, 125); err != nil || !reflect.DeepEqual(got, holding) {
			t.Errorf("ReadHoldingRegisters(125): err=%v", err)
		}
		if got, err := c.ReadInputRegister(ctx, e2eUnit, base+1); err != nil || got != input[1] {
			t.Errorf("ReadInputRegister = 0x%04X, %v", got, err)
		}
		if got, err := c.ReadInputRegisters(ctx, e2eUnit, base, 125); err != nil || !reflect.DeepEqual(got, input) {
			t.Errorf("ReadInputRegisters(125): err=%v", err)
		}
		calls := dev.takeCalls()
		wantFCs := []FunctionCode{FCReadHoldingRegisters, FCReadHoldingRegisters, FCReadInputRegisters, FCReadInputRegisters}
		if len(calls) != len(wantFCs) {
			t.Fatalf("wrappers: server received %d requests, want %d", len(calls), len(wantFCs))
		}
		for i := range calls {
			if calls[i].FC != wantFCs[i] {
				t.Errorf("wrapper %d: server saw fc=0x%02X, want 0x%02X", i, uint8(calls[i].FC), uint8(wantFCs[i]))
			}
		}
		_, err := c.ReadHoldingRegisters(ctx, e2eUnit, 0, 126)
		e2eWantParamError(t, "ReadHoldingRegisters(126)", err, dev)
		_, err = c.ReadInputRegisters(ctx, e2eUnit, 0, 126)
		e2eWantParamError(t, "ReadInputRegisters(126)", err, dev)
		_, err = c.ReadRegisters(ctx, e2eUnit, 0, 1, RegType(99))
		e2eWantParamError(t, "ReadRegisters(bad regType)", err, dev)

		// End of the address range.
		dev.setHolding(0xFFFF, 0xCAFE)
		dev.setInput(0xFFFF, 0xF00D)
		if got, err := c.ReadHoldingRegister(ctx, e2eUnit, 0xFFFF); err != nil || got != 0xCAFE {
			t.Errorf("ReadHoldingRegister(65535) = 0x%04X, %v", got, err)
		}
		if got, err := c.ReadInputRegisters(ctx, e2eUnit, 0xFFFF-124, 125); err != nil || len(got) != 125 || got[124] != 0xF00D {
			t.Errorf("ReadInputRegisters(65411, 125): err=%v", err)
		}
		dev.takeCalls()
		_, err = c.ReadHoldingRegisters(ctx, e2eUnit, 0xFFFF, 2)
		e2eWantParamError(t, "ReadHoldingRegisters(65535, 2)", err, dev)
		_, err = c.ReadInputRegisters(ctx, e2eUnit, 0xFFFF-123, 125)
		e2eWantParamError(t, "ReadInputRegisters(65412, 125)", err, dev)
	})
}

func TestE2E_Registers_Write(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		c := p.client
		ctx := context.Background()

		// WriteRegister (FC06).
		for _, v := range []uint16{0x0000, 0x0001, 0x8000, 0xFFFF, 0xBEEF} {
			if err := c.WriteRegister(ctx, e2eUnit, 50, v); err != nil {
				t.Fatalf("WriteRegister(0x%04X): %v", v, err)
			}
			call := e2eOneCall(t, "WriteRegister", dev)
			if call.FC != FCWriteSingleRegister || call.Addr != 50 || call.Quantity != 1 || !call.IsWrite ||
				!reflect.DeepEqual(call.Words, []uint16{v}) {
				t.Errorf("WriteRegister(0x%04X): server saw %+v", v, call)
			}
			if got := dev.holdingAt(50, 1)[0]; got != v {
				t.Errorf("WriteRegister(0x%04X): device holds 0x%04X", v, got)
			}
		}

		// WriteRegisters (FC16), including the 123-register maximum. A single
		// value still goes out as FC16.
		for _, qty := range []int{1, 2, 122, 123} {
			const base = 300
			dev.setHolding(base-1, make([]uint16, 130)...)
			dev.setHolding(base-1, 0x7777)
			dev.setHolding(base+qty, 0x7777)
			vals := e2eWordPattern(qty, uint16(0x4000+qty))
			if err := c.WriteRegisters(ctx, e2eUnit, base, vals); err != nil {
				t.Fatalf("WriteRegisters(qty=%d): %v", qty, err)
			}
			call := e2eOneCall(t, "WriteRegisters", dev)
			if call.FC != FCWriteMultipleRegisters || call.Addr != base || int(call.Quantity) != qty || !call.IsWrite ||
				!reflect.DeepEqual(call.Words, vals) {
				t.Errorf("WriteRegisters(qty=%d): server saw fc=0x%02X addr=%d qty=%d", qty, uint8(call.FC), call.Addr, call.Quantity)
			}
			if got := dev.holdingAt(base, qty); !reflect.DeepEqual(got, vals) {
				t.Errorf("WriteRegisters(qty=%d): device holds wrong values", qty)
			}
			if dev.holdingAt(base-1, 1)[0] != 0x7777 || dev.holdingAt(base+qty, 1)[0] != 0x7777 {
				t.Errorf("WriteRegisters(qty=%d) touched neighbouring registers", qty)
			}
		}
		e2eWantParamError(t, "WriteRegisters(empty)", c.WriteRegisters(ctx, e2eUnit, 0, nil), dev)
		e2eWantParamError(t, "WriteRegisters(124)", c.WriteRegisters(ctx, e2eUnit, 0, make([]uint16, 124)), dev)

		// WriteRegisterBytes: wire order, odd length zero-padded.
		dev.setHolding(600, 0xFFFF, 0xFFFF, 0xFFFF)
		in := []byte{0x11, 0x22, 0x33}
		if err := c.WriteRegisterBytes(ctx, e2eUnit, 600, in); err != nil {
			t.Fatalf("WriteRegisterBytes(odd): %v", err)
		}
		if call := e2eOneCall(t, "WriteRegisterBytes", dev); call.FC != FCWriteMultipleRegisters || call.Quantity != 2 {
			t.Errorf("WriteRegisterBytes(odd): server saw %+v", call)
		}
		if got := dev.holdingAt(600, 3); !reflect.DeepEqual(got, []uint16{0x1122, 0x3300, 0xFFFF}) {
			t.Errorf("WriteRegisterBytes(odd): device holds %04X", got)
		}
		if !bytes.Equal(in, []byte{0x11, 0x22, 0x33}) {
			t.Error("WriteRegisterBytes modified the caller's slice")
		}
		full := make([]byte, 246)
		for i := range full {
			full[i] = byte(i + 1)
		}
		if err := c.WriteRegisterBytes(ctx, e2eUnit, 700, full); err != nil {
			t.Fatalf("WriteRegisterBytes(246): %v", err)
		}
		if got := dev.holdingAt(700, 123); !reflect.DeepEqual(got, bytesToUint16s(BigEndian, full)) {
			t.Error("WriteRegisterBytes(246): device holds wrong values")
		}
		back, err := c.ReadRegisterBytes(ctx, e2eUnit, 700, 246, HoldingRegister)
		if err != nil || !bytes.Equal(back, full) {
			t.Errorf("ReadRegisterBytes after WriteRegisterBytes: err=%v", err)
		}
		dev.takeCalls()
		e2eWantParamError(t, "WriteRegisterBytes(empty)", c.WriteRegisterBytes(ctx, e2eUnit, 0, nil), dev)
		e2eWantParamError(t, "WriteRegisterBytes(247)", c.WriteRegisterBytes(ctx, e2eUnit, 0, make([]byte, 247)), dev)

		// End of the address range.
		if err := c.WriteRegister(ctx, e2eUnit, 0xFFFF, 0xABCD); err != nil {
			t.Errorf("WriteRegister(65535): %v", err)
		}
		if err := c.WriteRegisters(ctx, e2eUnit, 0xFFFF-122, e2eWordPattern(123, 1)); err != nil {
			t.Errorf("WriteRegisters(65413, 123): %v", err)
		}
		if got := dev.holdingAt(0xFFFF, 1)[0]; got != e2eWordPattern(123, 1)[122] {
			t.Errorf("register 65535 = 0x%04X", got)
		}
		dev.takeCalls()
		e2eWantParamError(t, "WriteRegisters(65535, 2)", c.WriteRegisters(ctx, e2eUnit, 0xFFFF, []uint16{1, 2}), dev)

		// Server-side enforcement for FC16.
		zero := append(u16(0, 0), 0, 0)
		assertExceptionResponse(t, sendRawFC(t, c, e2eUnit, byte(FCWriteMultipleRegisters), zero), exIllegalDataValue)
		wrap := append(u16(0xFFFF, 2), 4, 0, 1, 0, 2)
		assertExceptionResponse(t, sendRawFC(t, c, e2eUnit, byte(FCWriteMultipleRegisters), wrap), exIllegalDataAddress)
		if calls := dev.takeCalls(); len(calls) != 0 {
			t.Errorf("out-of-limit raw FC16 requests reached the handler: %+v", calls)
		}
	})
}

// A request the client can never frame must be refused as a parameter error,
// like every other out-of-range quantity.
func TestE2E_Registers_HugeWriteIsParameterError(t *testing.T) {
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp", dev, e2eOpts{})
	ctx := context.Background()

	cases := []struct {
		name string
		call func() error
	}{
		{"WriteRegisters(32769 values)", func() error {
			return p.client.WriteRegisters(ctx, e2eUnit, 0, make([]uint16, 32769))
		}},
		{"WriteCoils(65537 values)", func() error {
			return p.client.WriteCoils(ctx, e2eUnit, 0, make([]bool, 65537))
		}},
		{"ReadWriteMultipleRegisters(65537 write values)", func() error {
			_, err := p.client.ReadWriteMultipleRegisters(ctx, e2eUnit, 0, 1, 0, make([]uint16, 65537))
			return err
		}},
	}
	for _, tc := range cases {
		err := tc.call()
		if err == nil {
			t.Fatalf("%s succeeded", tc.name)
		}
		if n := dev.callCount(); n != 0 {
			t.Fatalf("%s reached the server (%d requests)", tc.name, n)
		}
		if !e2eIsParamError(err) {
			t.Fatalf("%s: the quantity is truncated to uint16 before validation, so the call "+
				"passes the range check and fails in the transport instead: got %v, want ErrUnexpectedParameters", tc.name, err)
		}
	}
}

func TestE2E_Registers_BitHelpers(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		c := p.client
		ctx := context.Background()

		dev.setHolding(10, 0x8001)
		dev.setInput(10, 0x4002)
		for _, tb := range []struct {
			regType RegType
			fc      FunctionCode
			val     uint16
		}{{HoldingRegister, FCReadHoldingRegisters, 0x8001}, {InputRegister, FCReadInputRegisters, 0x4002}} {
			for bit := uint8(0); bit < 16; bit++ {
				got, err := c.ReadRegisterBit(ctx, e2eUnit, 10, bit, tb.regType)
				want := tb.val>>bit&1 == 1
				if err != nil || got != want {
					t.Errorf("ReadRegisterBit(fc=0x%02X, bit=%d) = %v, %v; want %v", uint8(tb.fc), bit, got, err, want)
				}
				if call := e2eOneCall(t, "ReadRegisterBit", dev); call.FC != tb.fc || call.Addr != 10 || call.Quantity != 1 {
					t.Errorf("ReadRegisterBit: server saw %+v", call)
				}
			}
			all, err := c.ReadRegisterBits(ctx, e2eUnit, 10, 0, 16, tb.regType)
			if err != nil || len(all) != 16 {
				t.Fatalf("ReadRegisterBits(0, 16): %v", err)
			}
			for bit := range all {
				if all[bit] != (tb.val>>uint(bit)&1 == 1) {
					t.Errorf("ReadRegisterBits(0, 16)[%d] = %v", bit, all[bit])
				}
			}
			top, err := c.ReadRegisterBits(ctx, e2eUnit, 10, 13, 3, tb.regType)
			wantTop := []bool{tb.val>>13&1 == 1, tb.val>>14&1 == 1, tb.val>>15&1 == 1}
			if err != nil || !reflect.DeepEqual(top, wantTop) {
				t.Errorf("ReadRegisterBits(13, 3) = %v, %v; want %v", top, err, wantTop)
			}
			dev.takeCalls()

			_, err = c.ReadRegisterBit(ctx, e2eUnit, 10, 16, tb.regType)
			e2eWantParamError(t, "ReadRegisterBit(bit=16)", err, dev)
			_, err = c.ReadRegisterBits(ctx, e2eUnit, 10, 0, 0, tb.regType)
			e2eWantParamError(t, "ReadRegisterBits(count=0)", err, dev)
			_, err = c.ReadRegisterBits(ctx, e2eUnit, 10, 0, 17, tb.regType)
			e2eWantParamError(t, "ReadRegisterBits(count=17)", err, dev)
			_, err = c.ReadRegisterBits(ctx, e2eUnit, 10, 14, 3, tb.regType)
			e2eWantParamError(t, "ReadRegisterBits(14, 3)", err, dev)
		}

		// WriteRegisterBit: FC03 read, FC16 write-back, other bits preserved.
		dev.setHolding(20, 0x00F0)
		steps := []struct {
			bit  uint8
			val  bool
			want uint16
		}{{0, true, 0x00F1}, {15, true, 0x80F1}, {4, false, 0x80E1}, {4, false, 0x80E1}, {15, false, 0x00E1}}
		for _, s := range steps {
			if err := c.WriteRegisterBit(ctx, e2eUnit, 20, s.bit, s.val); err != nil {
				t.Fatalf("WriteRegisterBit(bit=%d, %v): %v", s.bit, s.val, err)
			}
			calls := dev.takeCalls()
			if len(calls) != 2 || calls[0].FC != FCReadHoldingRegisters || calls[0].IsWrite ||
				calls[1].FC != FCWriteMultipleRegisters || !reflect.DeepEqual(calls[1].Words, []uint16{s.want}) {
				t.Errorf("WriteRegisterBit(bit=%d, %v): server saw %+v", s.bit, s.val, calls)
			}
			if got := dev.holdingAt(20, 1)[0]; got != s.want {
				t.Errorf("WriteRegisterBit(bit=%d, %v): register = 0x%04X, want 0x%04X", s.bit, s.val, got, s.want)
			}
		}
		e2eWantParamError(t, "WriteRegisterBit(bit=16)", c.WriteRegisterBit(ctx, e2eUnit, 20, 16, true), dev)

		// UpdateRegisterMask: newVal = (old &^ mask) | (value & mask), FC03 + FC16.
		dev.setHolding(21, 0x1234)
		if err := c.UpdateRegisterMask(ctx, e2eUnit, 21, 0x0FF0, 0xABCD); err != nil {
			t.Fatalf("UpdateRegisterMask: %v", err)
		}
		calls := dev.takeCalls()
		if len(calls) != 2 || calls[0].FC != FCReadHoldingRegisters || calls[1].FC != FCWriteMultipleRegisters ||
			!reflect.DeepEqual(calls[1].Words, []uint16{0x1BC4}) {
			t.Errorf("UpdateRegisterMask: server saw %+v", calls)
		}
		if got := dev.holdingAt(21, 1)[0]; got != 0x1BC4 {
			t.Errorf("UpdateRegisterMask: register = 0x%04X, want 0x1BC4", got)
		}

		// When the read half fails, nothing is written.
		dev.setHook(func(_ context.Context, c e2eCall) error {
			if !c.IsWrite {
				return ErrServerDeviceBusy
			}
			return nil
		})
		err := c.WriteRegisterBit(ctx, e2eUnit, 20, 1, true)
		e2eWantException(t, "WriteRegisterBit(read fails)", err, FCReadHoldingRegisters, exServerDeviceBusy)
		err = c.UpdateRegisterMask(ctx, e2eUnit, 21, 0xFFFF, 0)
		e2eWantException(t, "UpdateRegisterMask(read fails)", err, FCReadHoldingRegisters, exServerDeviceBusy)
		if calls := dev.takeCalls(); len(calls) != 2 {
			t.Errorf("failed read-modify-write: server received %d requests, want 2 reads", len(calls))
		}
		// When the write half fails, the register keeps its value.
		dev.setHook(func(_ context.Context, c e2eCall) error {
			if c.IsWrite {
				return ErrIllegalDataValue
			}
			return nil
		})
		err = c.WriteRegisterBit(ctx, e2eUnit, 20, 1, true)
		e2eWantException(t, "WriteRegisterBit(write fails)", err, FCWriteMultipleRegisters, exIllegalDataValue)
		err = c.UpdateRegisterMask(ctx, e2eUnit, 21, 0xFFFF, 0)
		e2eWantException(t, "UpdateRegisterMask(write fails)", err, FCWriteMultipleRegisters, exIllegalDataValue)
		if got := dev.holdingAt(20, 2); !reflect.DeepEqual(got, []uint16{0x00E1, 0x1BC4}) {
			t.Errorf("failed read-modify-write changed the device: %04X", got)
		}
	})
}

func TestE2E_Registers_MaskWrite(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		c := p.client
		ctx := context.Background()

		cases := []struct {
			addr          uint16
			cur, and, or  uint16
			wantRegister  uint16
			wantNeighbour uint16
		}{
			{30, 0x0012, 0x00F2, 0x0025, 0x0017, 0x5555},
			{31, 0xFFFF, 0x0000, 0x0000, 0x0000, 0x5555},
			{32, 0x1234, 0xFFFF, 0xFFFF, 0x1234, 0x5555},
			{33, 0x1234, 0x0000, 0xFFFF, 0xFFFF, 0x5555},
			{0xFFFF, 0xA5A5, 0xFF00, 0x00FF, 0xA5FF, 0},
		}
		for _, tc := range cases {
			dev.setHolding(int(tc.addr), tc.cur)
			if tc.addr != 0xFFFF {
				dev.setHolding(int(tc.addr)+1, 0x5555)
			}
			if err := c.MaskWriteRegister(ctx, e2eUnit, tc.addr, tc.and, tc.or); err != nil {
				t.Fatalf("MaskWriteRegister(%d): %v", tc.addr, err)
			}
			call := e2eOneCall(t, "MaskWriteRegister", dev)
			if call.Kind != "mask" || call.FC != FCMaskWriteRegister || call.Addr != tc.addr ||
				call.AndMask != tc.and || call.OrMask != tc.or || call.UnitID != e2eUnit {
				t.Errorf("MaskWriteRegister(%d): server saw %+v", tc.addr, call)
			}
			if got := dev.holdingAt(int(tc.addr), 1)[0]; got != tc.wantRegister {
				t.Errorf("MaskWriteRegister(%d): register = 0x%04X, want 0x%04X", tc.addr, got, tc.wantRegister)
			}
			if tc.addr != 0xFFFF && dev.holdingAt(int(tc.addr)+1, 1)[0] != tc.wantNeighbour {
				t.Errorf("MaskWriteRegister(%d) touched the next register", tc.addr)
			}
		}

		// Exceptions from the handler.
		dev.setHook(func(context.Context, e2eCall) error { return ErrIllegalDataAddress })
		err := c.MaskWriteRegister(ctx, e2eUnit, 30, 0, 0)
		e2eWantException(t, "MaskWriteRegister", err, FCMaskWriteRegister, exIllegalDataAddress)
		if got := dev.holdingAt(30, 1)[0]; got != 0x0017 {
			t.Errorf("rejected MaskWriteRegister changed the register to 0x%04X", got)
		}
	})
}

func TestE2E_Registers_ReadWriteMultiple(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		c := p.client
		ctx := context.Background()

		// Disjoint read and write windows.
		dev.setHolding(100, 0x0A0A, 0x0B0B, 0x0C0C)
		got, err := c.ReadWriteMultipleRegisters(ctx, e2eUnit, 100, 3, 200, []uint16{0x1111, 0x2222})
		if err != nil || !reflect.DeepEqual(got, []uint16{0x0A0A, 0x0B0B, 0x0C0C}) {
			t.Fatalf("ReadWriteMultipleRegisters = %04X, %v", got, err)
		}
		call := e2eOneCall(t, "ReadWriteMultipleRegisters", dev)
		call.ClientAddr, call.ClientRole = "", ""
		want := e2eCall{Kind: "rw", FC: FCReadWriteMultipleRegs, UnitID: e2eUnit, Addr: 100, Quantity: 3,
			WriteAddr: 200, Words: []uint16{0x1111, 0x2222}}
		if !reflect.DeepEqual(call, want) {
			t.Errorf("server saw %+v, want %+v", call, want)
		}
		if got := dev.holdingAt(200, 2); !reflect.DeepEqual(got, []uint16{0x1111, 0x2222}) {
			t.Errorf("device holds %04X at the write window", got)
		}

		// Partially overlapping windows: the write is applied before the read.
		dev.setHolding(300, 1, 2, 3, 4, 5, 6)
		got, err = c.ReadWriteMultipleRegisters(ctx, e2eUnit, 300, 6, 302, []uint16{0xAAAA, 0xBBBB})
		if err != nil || !reflect.DeepEqual(got, []uint16{1, 2, 0xAAAA, 0xBBBB, 5, 6}) {
			t.Errorf("overlapping read/write = %04X, %v", got, err)
		}
		dev.takeCalls()

		// Limits: 125 read and 121 written registers in one transaction.
		wvals := e2eWordPattern(121, 0x6000)
		dev.setHolding(1000, e2eWordPattern(125, 0x2000)...)
		got, err = c.ReadWriteMultipleRegisters(ctx, e2eUnit, 1000, 125, 1100, wvals)
		if err != nil {
			t.Fatalf("ReadWriteMultipleRegisters(125/121): %v", err)
		}
		wantRead := e2eWordPattern(125, 0x2000)
		copy(wantRead[100:], wvals)
		if !reflect.DeepEqual(got, wantRead) {
			t.Error("ReadWriteMultipleRegisters(125/121) returned wrong values")
		}
		if call := e2eOneCall(t, "ReadWriteMultipleRegisters(125/121)", dev); call.Quantity != 125 || len(call.Words) != 121 {
			t.Errorf("server saw readQty=%d writeQty=%d", call.Quantity, len(call.Words))
		}
		if got := dev.holdingAt(1100, 121); !reflect.DeepEqual(got, wvals) {
			t.Error("device holds wrong values after the 121-register write")
		}
		dev.takeCalls()

		_, err = c.ReadWriteMultipleRegisters(ctx, e2eUnit, 0, 126, 0, []uint16{1})
		e2eWantParamError(t, "readQty=126", err, dev)
		_, err = c.ReadWriteMultipleRegisters(ctx, e2eUnit, 0, 0, 0, []uint16{1})
		e2eWantParamError(t, "readQty=0", err, dev)
		_, err = c.ReadWriteMultipleRegisters(ctx, e2eUnit, 0, 1, 0, make([]uint16, 122))
		e2eWantParamError(t, "writeQty=122", err, dev)
		_, err = c.ReadWriteMultipleRegisters(ctx, e2eUnit, 0, 1, 0, nil)
		e2eWantParamError(t, "writeQty=0", err, dev)

		// End of the address range, for both windows.
		got, err = c.ReadWriteMultipleRegisters(ctx, e2eUnit, 0xFFFF, 1, 0xFFFE, []uint16{0x0E0E, 0x0F0F})
		if err != nil || !reflect.DeepEqual(got, []uint16{0x0F0F}) {
			t.Errorf("read/write at end of range = %04X, %v", got, err)
		}
		dev.takeCalls()
		_, err = c.ReadWriteMultipleRegisters(ctx, e2eUnit, 0xFFFF, 2, 0, []uint16{1})
		e2eWantParamError(t, "read past 65535", err, dev)
		_, err = c.ReadWriteMultipleRegisters(ctx, e2eUnit, 0, 1, 0xFFFF, []uint16{1, 2})
		e2eWantParamError(t, "write past 65535", err, dev)

		// Server-side enforcement for requests a client would not send.
		raw := func(readAddr, readQty, writeAddr uint16, vals ...uint16) []byte {
			b := u16(readAddr, readQty, writeAddr, uint16(len(vals)))
			b = append(b, byte(2*len(vals)))
			return append(b, u16(vals...)...)
		}
		fc := byte(FCReadWriteMultipleRegs)
		assertExceptionResponse(t, sendRawFC(t, c, e2eUnit, fc, raw(0, 126, 0, 1)), exIllegalDataValue)
		assertExceptionResponse(t, sendRawFC(t, c, e2eUnit, fc, raw(0, 0, 0, 1)), exIllegalDataValue)
		assertExceptionResponse(t, sendRawFC(t, c, e2eUnit, fc, raw(0xFFFF, 2, 0, 1)), exIllegalDataAddress)
		assertExceptionResponse(t, sendRawFC(t, c, e2eUnit, fc, raw(0, 1, 0xFFFF, 1, 2)), exIllegalDataAddress)
		if calls := dev.takeCalls(); len(calls) != 0 {
			t.Errorf("out-of-limit raw FC23 requests reached the handler: %+v", calls)
		}

		// A handler exception leaves the device untouched.
		dev.setHook(func(context.Context, e2eCall) error { return ErrIllegalDataAddress })
		_, err = c.ReadWriteMultipleRegisters(ctx, e2eUnit, 100, 1, 200, []uint16{0xDEAD})
		e2eWantException(t, "ReadWriteMultipleRegisters", err, FCReadWriteMultipleRegs, exIllegalDataAddress)
		if got := dev.holdingAt(200, 1)[0]; got != 0x1111 {
			t.Errorf("rejected read/write changed the device: 0x%04X", got)
		}
	})
}

// A device that implements none of the optional handler interfaces answers
// Illegal Function to FC22 and FC23, while the client-side read-modify-write
// helpers keep working because they only need FC03 and FC16.
func TestE2E_Registers_OptionalHandlersMissing(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, e2eBasicOnly{dev}, e2eOpts{})
		c := p.client
		ctx := context.Background()

		dev.setHolding(5, 0x00FF)
		err := c.MaskWriteRegister(ctx, e2eUnit, 5, 0, 0)
		e2eWantException(t, "MaskWriteRegister", err, FCMaskWriteRegister, exIllegalFunction)
		_, err = c.ReadWriteMultipleRegisters(ctx, e2eUnit, 5, 1, 5, []uint16{0})
		e2eWantException(t, "ReadWriteMultipleRegisters", err, FCReadWriteMultipleRegs, exIllegalFunction)
		if n := dev.callCount(); n != 0 {
			t.Errorf("handler received %d requests for unimplemented FCs", n)
		}
		if got := dev.holdingAt(5, 1)[0]; got != 0x00FF {
			t.Errorf("register changed to 0x%04X", got)
		}

		if err := c.UpdateRegisterMask(ctx, e2eUnit, 5, 0x0F0F, 0); err != nil {
			t.Fatalf("UpdateRegisterMask: %v", err)
		}
		if err := c.WriteRegisterBit(ctx, e2eUnit, 5, 15, true); err != nil {
			t.Fatalf("WriteRegisterBit: %v", err)
		}
		if got := dev.holdingAt(5, 1)[0]; got != 0x80F0 {
			t.Errorf("register = 0x%04X, want 0x80F0", got)
		}
		if p.conns() != 1 {
			t.Errorf("server connections = %d, want 1", p.conns())
		}
	})
}
