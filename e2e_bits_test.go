// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// e2eBitPattern returns n pseudo-random but reproducible bits.
func e2eBitPattern(n int, seed uint32) []bool {
	out := make([]bool, n)
	x := seed*2654435761 + 1
	for i := range out {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		out[i] = x&1 == 1
	}
	return out
}

// e2eWantParamError asserts that err is a client-side parameter error and that
// nothing reached the server.
func e2eWantParamError(t *testing.T, what string, err error, dev *e2eDevice) {
	t.Helper()
	if !errors.Is(err, ErrUnexpectedParameters) {
		t.Errorf("%s: got %v, want ErrUnexpectedParameters", what, err)
	}
	if calls := dev.takeCalls(); len(calls) != 0 {
		t.Errorf("%s: server received %d request(s), want none: %+v", what, len(calls), calls)
	}
}

// e2eWantException asserts that err is the Modbus exception code for fc.
func e2eWantException(t *testing.T, what string, err error, fc FunctionCode, code ExceptionCode) {
	t.Helper()
	var exc *ExceptionError
	if !errors.As(err, &exc) {
		t.Errorf("%s: got %v, want *ExceptionError(%v)", what, err, code)
		return
	}
	if exc.ExceptionCode != code || exc.FunctionCode != fc {
		t.Errorf("%s: got exception fc=0x%02X code=0x%02X, want fc=0x%02X code=0x%02X",
			what, uint8(exc.FunctionCode), uint8(exc.ExceptionCode), uint8(fc), uint8(code))
	}
	if !errors.Is(err, code.ToError()) {
		t.Errorf("%s: errors.Is(%v, %v) = false", what, err, code.ToError())
	}
}

// e2eOneCall returns the single request the device received since the last
// check, failing the test when there is not exactly one.
func e2eOneCall(t *testing.T, what string, dev *e2eDevice) e2eCall {
	t.Helper()
	calls := dev.takeCalls()
	if len(calls) != 1 {
		t.Fatalf("%s: server received %d request(s), want 1: %+v", what, len(calls), calls)
	}
	return calls[0]
}

func TestE2E_Bits_ReadPackingAndLimits(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		c := p.client
		ctx := context.Background()

		const base = 1000
		coils := e2eBitPattern(2000, 1)
		inputs := e2eBitPattern(2000, 2)
		dev.setCoils(base, coils...)
		dev.setDiscrete(base, inputs...)
		// Neighbours of the read windows are set, so padding bits that leak
		// into a result would show up as true.
		dev.setCoils(base-1, true)
		dev.setDiscrete(base-1, true)

		for _, qty := range []uint16{1, 2, 7, 8, 9, 15, 16, 17, 63, 64, 65, 1999, 2000} {
			got, err := c.ReadCoils(ctx, e2eUnit, base, qty)
			if err != nil {
				t.Fatalf("ReadCoils(qty=%d): %v", qty, err)
			}
			if !reflect.DeepEqual(got, coils[:qty]) {
				t.Errorf("ReadCoils(qty=%d) returned wrong bits", qty)
			}
			call := e2eOneCall(t, "ReadCoils", dev)
			want := e2eCall{Kind: "coils", FC: FCReadCoils, UnitID: e2eUnit, Addr: base, Quantity: qty}
			call.ClientAddr, call.ClientRole, call.Bools = "", "", nil
			if !reflect.DeepEqual(call, want) {
				t.Errorf("ReadCoils(qty=%d): server saw %+v, want %+v", qty, call, want)
			}

			got, err = c.ReadDiscreteInputs(ctx, e2eUnit, base, qty)
			if err != nil {
				t.Fatalf("ReadDiscreteInputs(qty=%d): %v", qty, err)
			}
			if !reflect.DeepEqual(got, inputs[:qty]) {
				t.Errorf("ReadDiscreteInputs(qty=%d) returned wrong bits", qty)
			}
			call = e2eOneCall(t, "ReadDiscreteInputs", dev)
			want = e2eCall{Kind: "discrete", FC: FCReadDiscreteInputs, UnitID: e2eUnit, Addr: base, Quantity: qty}
			call.ClientAddr, call.ClientRole = "", ""
			if !reflect.DeepEqual(call, want) {
				t.Errorf("ReadDiscreteInputs(qty=%d): server saw %+v, want %+v", qty, call, want)
			}
		}

		// Single-bit helpers.
		for i := 0; i < 10; i++ {
			got, err := c.ReadCoil(ctx, e2eUnit, uint16(base+i))
			if err != nil || got != coils[i] {
				t.Errorf("ReadCoil(%d) = %v, %v; want %v", base+i, got, err, coils[i])
			}
			got, err = c.ReadDiscreteInput(ctx, e2eUnit, uint16(base+i))
			if err != nil || got != inputs[i] {
				t.Errorf("ReadDiscreteInput(%d) = %v, %v; want %v", base+i, got, err, inputs[i])
			}
		}
		if n := len(dev.takeCalls()); n != 20 {
			t.Errorf("single-bit reads: server received %d requests, want 20", n)
		}

		// Quantity limits: 0 and limit+1 never reach the server.
		_, err := c.ReadCoils(ctx, e2eUnit, 0, 0)
		e2eWantParamError(t, "ReadCoils(qty=0)", err, dev)
		_, err = c.ReadCoils(ctx, e2eUnit, 0, 2001)
		e2eWantParamError(t, "ReadCoils(qty=2001)", err, dev)
		_, err = c.ReadDiscreteInputs(ctx, e2eUnit, 0, 0)
		e2eWantParamError(t, "ReadDiscreteInputs(qty=0)", err, dev)
		_, err = c.ReadDiscreteInputs(ctx, e2eUnit, 0, 2001)
		e2eWantParamError(t, "ReadDiscreteInputs(qty=2001)", err, dev)

		// End of the address range.
		dev.setCoils(0xFFFF, true)
		dev.setDiscrete(0xFFFF, true)
		if got, err := c.ReadCoil(ctx, e2eUnit, 0xFFFF); err != nil || !got {
			t.Errorf("ReadCoil(65535) = %v, %v; want true", got, err)
		}
		if got, err := c.ReadDiscreteInput(ctx, e2eUnit, 0xFFFF); err != nil || !got {
			t.Errorf("ReadDiscreteInput(65535) = %v, %v; want true", got, err)
		}
		got, err := c.ReadCoils(ctx, e2eUnit, 0xFFFF-1999, 2000)
		if err != nil || len(got) != 2000 || !got[1999] || got[1998] {
			t.Errorf("ReadCoils(63536, 2000): err=%v len=%d", err, len(got))
		}
		got, err = c.ReadDiscreteInputs(ctx, e2eUnit, 0xFFFF-1999, 2000)
		if err != nil || len(got) != 2000 || !got[1999] || got[1998] {
			t.Errorf("ReadDiscreteInputs(63536, 2000): err=%v len=%d", err, len(got))
		}
		dev.takeCalls()
		_, err = c.ReadCoils(ctx, e2eUnit, 0xFFFF, 2)
		e2eWantParamError(t, "ReadCoils(65535, 2)", err, dev)
		_, err = c.ReadDiscreteInputs(ctx, e2eUnit, 0xFFFF-1998, 2000)
		e2eWantParamError(t, "ReadDiscreteInputs(63537, 2000)", err, dev)

		// The server enforces the same limits on requests a client would not send.
		for _, fc := range []FunctionCode{FCReadCoils, FCReadDiscreteInputs} {
			res := sendRawFC(t, c, e2eUnit, byte(fc), u16(0, 2001))
			assertExceptionResponse(t, res, exIllegalDataValue)
			res = sendRawFC(t, c, e2eUnit, byte(fc), u16(0, 0))
			assertExceptionResponse(t, res, exIllegalDataValue)
			res = sendRawFC(t, c, e2eUnit, byte(fc), u16(0xFFFF, 2))
			assertExceptionResponse(t, res, exIllegalDataAddress)
		}
		if calls := dev.takeCalls(); len(calls) != 0 {
			t.Errorf("out-of-limit raw requests reached the handler: %+v", calls)
		}
	})
}

func TestE2E_Bits_Write(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		c := p.client
		ctx := context.Background()

		// WriteCoil (FC05): the handler sees one coil and the device changes.
		for _, v := range []bool{true, false, true} {
			if err := c.WriteCoil(ctx, e2eUnit, 40, v); err != nil {
				t.Fatalf("WriteCoil(%v): %v", v, err)
			}
			call := e2eOneCall(t, "WriteCoil", dev)
			if call.FC != FCWriteSingleCoil || call.Addr != 40 || call.Quantity != 1 || !call.IsWrite ||
				!reflect.DeepEqual(call.Bools, []bool{v}) {
				t.Errorf("WriteCoil(%v): server saw %+v", v, call)
			}
			if got := dev.coilsAt(40, 1)[0]; got != v {
				t.Errorf("WriteCoil(%v): device coil = %v", v, got)
			}
		}

		// WriteCoilRaw with the two standard payloads behaves like WriteCoil.
		if err := c.WriteCoilRaw(ctx, e2eUnit, 41, 0xFF00); err != nil {
			t.Fatalf("WriteCoilRaw(0xFF00): %v", err)
		}
		if call := e2eOneCall(t, "WriteCoilRaw", dev); call.FC != FCWriteSingleCoil || !reflect.DeepEqual(call.Bools, []bool{true}) {
			t.Errorf("WriteCoilRaw(0xFF00): server saw %+v", call)
		}
		if err := c.WriteCoilRaw(ctx, e2eUnit, 41, 0x0000); err != nil {
			t.Fatalf("WriteCoilRaw(0x0000): %v", err)
		}
		if call := e2eOneCall(t, "WriteCoilRaw", dev); !reflect.DeepEqual(call.Bools, []bool{false}) {
			t.Errorf("WriteCoilRaw(0x0000): server saw %+v", call)
		}
		// Non-standard payloads are rejected by our (compliant) server with
		// Illegal Data Value, without the handler being involved.
		dev.setCoils(41, true)
		for _, raw := range []uint16{0x0001, 0x00FF, 0x5A5A, 0xFF01, 0xFFFF} {
			err := c.WriteCoilRaw(ctx, e2eUnit, 41, raw)
			e2eWantException(t, "WriteCoilRaw(non-standard)", err, FCWriteSingleCoil, exIllegalDataValue)
			if calls := dev.takeCalls(); len(calls) != 0 {
				t.Errorf("WriteCoilRaw(0x%04X) reached the handler: %+v", raw, calls)
			}
		}
		if !dev.coilsAt(41, 1)[0] {
			t.Error("rejected WriteCoilRaw changed the coil")
		}

		// WriteCoils (FC15) at quantities around byte boundaries: exactly the
		// addressed coils change, their neighbours keep their value.
		for _, qty := range []int{1, 2, 7, 8, 9, 13, 16, 17, 1967, 1968} {
			const base = 3000
			dev.mu.Lock()
			for i := base - 8; i < base+2000; i++ {
				dev.coils[i] = false
			}
			dev.coils[base-1] = true
			dev.coils[base+qty] = true
			dev.mu.Unlock()

			vals := e2eBitPattern(qty, uint32(qty))
			if err := c.WriteCoils(ctx, e2eUnit, base, vals); err != nil {
				t.Fatalf("WriteCoils(qty=%d): %v", qty, err)
			}
			call := e2eOneCall(t, "WriteCoils", dev)
			if call.FC != FCWriteMultipleCoils || call.Addr != base || int(call.Quantity) != qty || !call.IsWrite ||
				!reflect.DeepEqual(call.Bools, vals) {
				t.Errorf("WriteCoils(qty=%d): server saw fc=0x%02X addr=%d qty=%d write=%v",
					qty, uint8(call.FC), call.Addr, call.Quantity, call.IsWrite)
			}
			if got := dev.coilsAt(base, qty); !reflect.DeepEqual(got, vals) {
				t.Errorf("WriteCoils(qty=%d): device holds wrong bits", qty)
			}
			if !dev.coilsAt(base-1, 1)[0] || !dev.coilsAt(base+qty, 1)[0] || dev.coilsAt(base+qty+1, 1)[0] {
				t.Errorf("WriteCoils(qty=%d) touched neighbouring coils", qty)
			}
			back, err := c.ReadCoils(ctx, e2eUnit, base, uint16(qty))
			if err != nil || !reflect.DeepEqual(back, vals) {
				t.Errorf("WriteCoils(qty=%d): read-back mismatch (err=%v)", qty, err)
			}
			dev.takeCalls()
		}

		// Quantity limits.
		e2eWantParamError(t, "WriteCoils(empty)", c.WriteCoils(ctx, e2eUnit, 0, nil), dev)
		e2eWantParamError(t, "WriteCoils(1969)", c.WriteCoils(ctx, e2eUnit, 0, make([]bool, 1969)), dev)

		// End of the address range.
		if err := c.WriteCoil(ctx, e2eUnit, 0xFFFF, true); err != nil {
			t.Errorf("WriteCoil(65535): %v", err)
		}
		if err := c.WriteCoils(ctx, e2eUnit, 0xFFFE, []bool{true, false}); err != nil {
			t.Errorf("WriteCoils(65534, 2): %v", err)
		}
		if got := dev.coilsAt(0xFFFE, 2); !reflect.DeepEqual(got, []bool{true, false}) {
			t.Errorf("coils at end of range = %v, want [true false]", got)
		}
		tail := make([]bool, 1968)
		tail[1967] = true
		if err := c.WriteCoils(ctx, e2eUnit, 0xFFFF-1967, tail); err != nil {
			t.Errorf("WriteCoils(63568, 1968): %v", err)
		}
		if !dev.coilsAt(0xFFFF, 1)[0] {
			t.Error("WriteCoils up to 65535 did not set the last coil")
		}
		dev.takeCalls()
		e2eWantParamError(t, "WriteCoils(65535, 2)", c.WriteCoils(ctx, e2eUnit, 0xFFFF, []bool{true, true}), dev)

		// Server-side limit enforcement for FC15 (quantity 1969 with a
		// consistent byte count, and quantity 0).
		over := append(u16(0, 1969), 247)
		over = append(over, make([]byte, 247)...)
		assertExceptionResponse(t, sendRawFC(t, c, e2eUnit, byte(FCWriteMultipleCoils), over), exIllegalDataValue)
		zero := append(u16(0, 0), 0, 0)
		assertExceptionResponse(t, sendRawFC(t, c, e2eUnit, byte(FCWriteMultipleCoils), zero), exIllegalDataValue)
		wrap := append(u16(0xFFFF, 2), 1, 0x03)
		assertExceptionResponse(t, sendRawFC(t, c, e2eUnit, byte(FCWriteMultipleCoils), wrap), exIllegalDataAddress)
		if calls := dev.takeCalls(); len(calls) != 0 {
			t.Errorf("out-of-limit raw FC15 requests reached the handler: %+v", calls)
		}
	})
}

// A device with a smaller address space answers Illegal Data Address, and the
// client reports it for every bit access method.
func TestE2E_Bits_Exceptions(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		dev.setHook(func(_ context.Context, c e2eCall) error {
			if int(c.Addr)+int(c.Quantity) > 100 {
				return ErrIllegalDataAddress
			}
			return nil
		})
		p := e2eStart(t, kind, dev, e2eOpts{})
		c := p.client
		ctx := context.Background()

		_, err := c.ReadCoils(ctx, e2eUnit, 96, 5)
		e2eWantException(t, "ReadCoils", err, FCReadCoils, exIllegalDataAddress)
		_, err = c.ReadCoil(ctx, e2eUnit, 100)
		e2eWantException(t, "ReadCoil", err, FCReadCoils, exIllegalDataAddress)
		_, err = c.ReadDiscreteInputs(ctx, e2eUnit, 96, 5)
		e2eWantException(t, "ReadDiscreteInputs", err, FCReadDiscreteInputs, exIllegalDataAddress)
		_, err = c.ReadDiscreteInput(ctx, e2eUnit, 100)
		e2eWantException(t, "ReadDiscreteInput", err, FCReadDiscreteInputs, exIllegalDataAddress)
		e2eWantException(t, "WriteCoil", c.WriteCoil(ctx, e2eUnit, 100, true), FCWriteSingleCoil, exIllegalDataAddress)
		e2eWantException(t, "WriteCoilRaw", c.WriteCoilRaw(ctx, e2eUnit, 100, 0xFF00), FCWriteSingleCoil, exIllegalDataAddress)
		e2eWantException(t, "WriteCoils", c.WriteCoils(ctx, e2eUnit, 99, []bool{true, true}), FCWriteMultipleCoils, exIllegalDataAddress)

		if n := len(dev.takeCalls()); n != 7 {
			t.Errorf("server received %d requests, want 7", n)
		}
		for _, v := range dev.coilsAt(99, 2) {
			if v {
				t.Error("a rejected write changed the device")
			}
		}
		// The last in-range address still works on the same connection.
		if err := c.WriteCoil(ctx, e2eUnit, 99, true); err != nil {
			t.Errorf("WriteCoil(99) after exceptions: %v", err)
		}
		if got, err := c.ReadCoils(ctx, e2eUnit, 95, 5); err != nil || !reflect.DeepEqual(got, []bool{false, false, false, false, true}) {
			t.Errorf("ReadCoils(95, 5) = %v, %v", got, err)
		}
		if p.conns() != 1 {
			t.Errorf("server connections = %d, want 1", p.conns())
		}
	})
}
