//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	modbus "github.com/otfabric/go-modbus"
)

// Exception codes, as numbers on the wire.
const (
	excIllegalFunction    modbus.ExceptionCode = 1
	excIllegalDataAddress modbus.ExceptionCode = 2
	excIllegalDataValue   modbus.ExceptionCode = 3
	excGatewayNoResponse  modbus.ExceptionCode = 11
)

// requestTimeout is the response timeout of the go-modbus clients. Generous:
// the suite must pass on a loaded machine.
const requestTimeout = 20 * time.Second

func modbusCategory(n int) modbus.DeviceIDCategory { return modbus.DeviceIDCategory(n) }

// newClient returns an open go-modbus client for a reference server.
func newClient(t testing.TB, srv *refServer, mut func(*modbus.Config)) *modbus.Client {
	t.Helper()
	conf := modbus.Config{
		URL:         "tcp://" + srv.addr,
		Timeout:     requestTimeout,
		DialTimeout: requestTimeout,
		Logger:      newWireLog(t, "go-modbus client"),
	}
	if mut != nil {
		mut(&conf)
	}
	c, err := modbus.New(conf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Open(); err != nil {
		t.Fatalf("open go-modbus client to the %s server: %v", srv.adapter.name, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// wantException checks that err is the exception response code to a request
// with function code fc.
func wantException(t testing.TB, what string, err error, fc modbus.FunctionCode, code modbus.ExceptionCode) {
	t.Helper()
	var exc *modbus.ExceptionError
	if !errors.As(err, &exc) {
		t.Errorf("%s: got %v, want exception %d", what, err, code)
		return
	}
	if exc.ExceptionCode != code || exc.FunctionCode != fc {
		t.Errorf("%s: got exception %d to function %d, want exception %d to function %d",
			what, exc.ExceptionCode, exc.FunctionCode, code, fc)
	}
}

// unit is the unit identifier of the fixture device.
func (fx *fixture) unit() uint8 { return fx.UnitID }

// TestFixtureRules checks that the fixture of every image is the baseline
// the tests are written for, and that its values follow the generation rules
// of FIXTURES.md, so that the two ways of knowing a value agree.
func TestFixtureRules(t *testing.T) {
	forEachAdapter(t, func(t *testing.T, a adapter) {
		fx := loadFixture(t, a)
		if fx.Name != "baseline" || fx.UnitID != 1 {
			t.Fatalf("fixture %q for unit %d, want baseline for unit 1", fx.Name, fx.UnitID)
		}
		if fx.Coils.Address != 0 || len(fx.Coils.Values) != 2000 ||
			fx.DiscreteInputs.Address != 1000 || len(fx.DiscreteInputs.Values) != 2000 ||
			fx.HoldingRegisters.Address != 0 || len(fx.HoldingRegisters.Values) != 256 ||
			fx.InputRegisters.Address != 65280 || len(fx.InputRegisters.Values) != 256 {
			t.Fatalf("the tables are not where the baseline has them")
		}
		for i, v := range fx.Coils.Values {
			if v != baselineCoil(i) {
				t.Fatalf("coil %d is %v, the rule says %v", i, v, baselineCoil(i))
			}
		}
		for i, v := range fx.DiscreteInputs.Values {
			if v != baselineDiscreteInput(i) {
				t.Fatalf("discrete input index %d is %v, the rule says %v", i, v, baselineDiscreteInput(i))
			}
		}
		for i, v := range fx.HoldingRegisters.Values {
			if v != baselineHoldingRegister(i) {
				t.Fatalf("holding register %d is %d, the rule says %d", i, v, baselineHoldingRegister(i))
			}
		}
		for i, v := range fx.InputRegisters.Values {
			if v != baselineInputRegister(i) {
				t.Fatalf("input register index %d is %d, the rule says %d", i, v, baselineInputRegister(i))
			}
		}
		want := []fixtureObject{
			{0, "otfabric"}, {1, "MBI-BASELINE"}, {2, "1.0"},
			{3, "https://github.com/otfabric/modbus-interop"},
			{4, "modbus-interop baseline device"}, {5, "baseline"}, {6, "interop"},
		}
		if fx.DeviceID == nil || !reflect.DeepEqual(fx.DeviceID.Objects, want) {
			t.Fatalf("device identification objects: got %v, want %v", fx.DeviceID, want)
		}
	})
}

// readAllBits reads a whole bit table with the largest requests there are.
func readAllBits(ctx context.Context, read func(context.Context, uint8, uint16, uint16) ([]bool, error), unit uint8, b *bitBlock) ([]bool, error) {
	var out []bool
	for at := 0; at < len(b.Values); at += 2000 {
		n := min(2000, len(b.Values)-at)
		got, err := read(ctx, unit, b.Address+uint16(at), uint16(n))
		if err != nil {
			return nil, fmt.Errorf("%d bits from %d: %w", n, int(b.Address)+at, err)
		}
		out = append(out, got...)
	}
	return out, nil
}

// readAllRegisters is readAllBits for a register table.
func readAllRegisters(ctx context.Context, read func(context.Context, uint8, uint16, uint16) ([]uint16, error), unit uint8, b *registerBlock) ([]uint16, error) {
	var out []uint16
	for at := 0; at < len(b.Values); at += 125 {
		n := min(125, len(b.Values)-at)
		got, err := read(ctx, unit, b.Address+uint16(at), uint16(n))
		if err != nil {
			return nil, fmt.Errorf("%d registers from %d: %w", n, int(b.Address)+at, err)
		}
		out = append(out, got...)
	}
	return out, nil
}

// wantState reads the coils and the holding registers of the server through
// c and compares them with what they should be.
func wantState(t testing.TB, what string, c *modbus.Client, fx *fixture, coils []bool, holding []uint16) {
	t.Helper()
	ctx := context.Background()
	got, err := readAllBits(ctx, c.ReadCoils, fx.unit(), fx.Coils)
	if err != nil {
		t.Fatalf("%s: read the coils back: %v", what, err)
	}
	if !reflect.DeepEqual(got, coils) {
		t.Errorf("%s: coils: %s", what, diffBits(got, coils))
	}
	regs, err := readAllRegisters(ctx, c.ReadHoldingRegisters, fx.unit(), fx.HoldingRegisters)
	if err != nil {
		t.Fatalf("%s: read the holding registers back: %v", what, err)
	}
	if !reflect.DeepEqual(regs, holding) {
		t.Errorf("%s: holding registers: %s", what, diffRegisters(regs, holding))
	}
}

// span is a range of a table, by index.
type span struct{ at, n int }

// TestClientReads reads the four tables of every reference server with the
// go-modbus client: single elements, the boundaries of each block, ranges
// that are not byte aligned and the largest request of each function. The
// values are compared with the fixture of the image and with the rules it
// was generated by.
func TestClientReads(t *testing.T) {
	forEachAdapter(t, func(t *testing.T, a adapter) {
		logReference(t, a)
		fx := loadFixture(t, a)
		srv := startServer(t, a)
		c := newClient(t, srv, nil)
		ctx := context.Background()
		u := fx.unit()

		bitSpans := []span{{0, 1}, {1999, 1}, {1993, 7}, {3, 17}, {8, 8}, {1, 1999}, {0, 2000}, {1000, 1000}}
		t.Run("coils", func(t *testing.T) {
			for _, s := range bitSpans {
				got, err := c.ReadCoils(ctx, u, fx.Coils.Address+uint16(s.at), uint16(s.n))
				if err != nil {
					t.Fatalf("ReadCoils %+v: %v", s, err)
				}
				if want := fx.Coils.Values[s.at : s.at+s.n]; !reflect.DeepEqual(got, want) {
					t.Errorf("ReadCoils %+v: %s", s, diffBits(got, want))
				}
				for i, v := range got {
					if v != baselineCoil(s.at+i) {
						t.Fatalf("ReadCoils %+v: coil %d is %v, the rule says otherwise", s, s.at+i, v)
					}
				}
			}
			if v, err := c.ReadCoil(ctx, u, 1); err != nil || v != baselineCoil(1) {
				t.Errorf("ReadCoil(1) = %v, %v", v, err)
			}
		})
		t.Run("discrete-inputs", func(t *testing.T) {
			for _, s := range bitSpans {
				got, err := c.ReadDiscreteInputs(ctx, u, fx.DiscreteInputs.Address+uint16(s.at), uint16(s.n))
				if err != nil {
					t.Fatalf("ReadDiscreteInputs %+v: %v", s, err)
				}
				if want := fx.DiscreteInputs.Values[s.at : s.at+s.n]; !reflect.DeepEqual(got, want) {
					t.Errorf("ReadDiscreteInputs %+v: %s", s, diffBits(got, want))
				}
				for i, v := range got {
					if v != baselineDiscreteInput(s.at+i) {
						t.Fatalf("ReadDiscreteInputs %+v: index %d is %v, the rule says otherwise", s, s.at+i, v)
					}
				}
			}
			if v, err := c.ReadDiscreteInput(ctx, u, 2999); err != nil || v != baselineDiscreteInput(1999) {
				t.Errorf("ReadDiscreteInput(2999) = %v, %v", v, err)
			}
		})
		regSpans := []span{{0, 1}, {255, 1}, {0, 8}, {0, 125}, {131, 125}, {125, 125}, {250, 6}}
		t.Run("holding-registers", func(t *testing.T) {
			for _, s := range regSpans {
				got, err := c.ReadHoldingRegisters(ctx, u, fx.HoldingRegisters.Address+uint16(s.at), uint16(s.n))
				if err != nil {
					t.Fatalf("ReadHoldingRegisters %+v: %v", s, err)
				}
				if want := fx.HoldingRegisters.Values[s.at : s.at+s.n]; !reflect.DeepEqual(got, want) {
					t.Errorf("ReadHoldingRegisters %+v: %s", s, diffRegisters(got, want))
				}
				for i, v := range got {
					if v != baselineHoldingRegister(s.at+i) {
						t.Fatalf("ReadHoldingRegisters %+v: register %d is %d, the rule says %d", s, s.at+i, v, baselineHoldingRegister(s.at+i))
					}
				}
			}
			// The byte order of a register, seen as bytes: 0x1234 at 5.
			if b, err := c.ReadRegisterBytes(ctx, u, 5, 2, modbus.HoldingRegister); err != nil || !reflect.DeepEqual(b, []byte{0x12, 0x34}) {
				t.Errorf("ReadRegisterBytes(5) = % X, %v, want 12 34", b, err)
			}
		})
		t.Run("input-registers", func(t *testing.T) {
			for _, s := range regSpans {
				got, err := c.ReadInputRegisters(ctx, u, fx.InputRegisters.Address+uint16(s.at), uint16(s.n))
				if err != nil {
					t.Fatalf("ReadInputRegisters %+v: %v", s, err)
				}
				if want := fx.InputRegisters.Values[s.at : s.at+s.n]; !reflect.DeepEqual(got, want) {
					t.Errorf("ReadInputRegisters %+v: %s", s, diffRegisters(got, want))
				}
				for i, v := range got {
					if v != baselineInputRegister(s.at+i) {
						t.Fatalf("ReadInputRegisters %+v: index %d is %d, the rule says %d", s, s.at+i, v, baselineInputRegister(s.at+i))
					}
				}
			}
			// The last address there is.
			if v, err := c.ReadInputRegister(ctx, u, 65535); err != nil || v != baselineInputRegister(255) {
				t.Errorf("ReadInputRegister(65535) = %d, %v", v, err)
			}
		})

		// The server saw what the client meant: the function, the address
		// as a number on the wire and the quantity.
		t.Run("as-the-server-saw-it", func(t *testing.T) {
			a.need(t, featRequestEvents)
			before := len(srv.requestEvents(t, 1))
			if _, err := c.ReadInputRegisters(ctx, u, 65411, 125); err != nil {
				t.Fatal(err)
			}
			_, err := c.ReadDiscreteInputs(ctx, u, 999, 2)
			wantException(t, "ReadDiscreteInputs(999, 2)", err, modbus.FCReadDiscreteInputs, excIllegalDataAddress)
			evs := srv.requestEvents(t, before+2)[before:]
			if e := evs[0]; e.num("unitId") != 1 || e.num("function") != 4 || e.num("address") != 65411 ||
				e.num("quantity") != 125 || e["exception"] != nil {
				t.Errorf("the server reports the read as %v", e)
			}
			if e := evs[1]; e.num("function") != 2 || e.num("address") != 999 || e.num("quantity") != 2 || e.num("exception") != 2 {
				t.Errorf("the server reports the rejected read as %v", e)
			}
		})
	})
}

// TestClientExceptions checks the exception responses of the reference
// servers as the go-modbus client reports them: exception 2 for anything
// that touches an address outside a block, exception 11 for another unit
// identifier and exception 1 for a function the server does not have. An
// exception leaves the connection usable, and a rejected write writes
// nothing.
func TestClientExceptions(t *testing.T) {
	forEachAdapter(t, func(t *testing.T, a adapter) {
		fx := loadFixture(t, a)
		srv := startServer(t, a)
		c := newClient(t, srv, nil)
		ctx := context.Background()
		u := fx.unit()

		t.Run("illegal-data-address", func(t *testing.T) {
			for _, s := range [][2]uint16{{1999, 2}, {2000, 1}, {65535, 1}, {1, 2000}} {
				_, err := c.ReadCoils(ctx, u, s[0], s[1])
				wantException(t, fmt.Sprintf("ReadCoils%v", s), err, modbus.FCReadCoils, excIllegalDataAddress)
			}
			for _, s := range [][2]uint16{{999, 2}, {0, 1}, {2999, 2}, {3000, 8}} {
				_, err := c.ReadDiscreteInputs(ctx, u, s[0], s[1])
				wantException(t, fmt.Sprintf("ReadDiscreteInputs%v", s), err, modbus.FCReadDiscreteInputs, excIllegalDataAddress)
			}
			for _, s := range [][2]uint16{{200, 57}, {256, 1}, {65535, 1}, {132, 125}} {
				_, err := c.ReadHoldingRegisters(ctx, u, s[0], s[1])
				wantException(t, fmt.Sprintf("ReadHoldingRegisters%v", s), err, modbus.FCReadHoldingRegisters, excIllegalDataAddress)
			}
			for _, s := range [][2]uint16{{0, 1}, {65279, 2}, {65279, 1}} {
				_, err := c.ReadInputRegisters(ctx, u, s[0], s[1])
				wantException(t, fmt.Sprintf("ReadInputRegisters%v", s), err, modbus.FCReadInputRegisters, excIllegalDataAddress)
			}
			wantException(t, "WriteCoil(2000)", c.WriteCoil(ctx, u, 2000, true), modbus.FCWriteSingleCoil, excIllegalDataAddress)
			wantException(t, "WriteRegister(256)", c.WriteRegister(ctx, u, 256, 1), modbus.FCWriteSingleRegister, excIllegalDataAddress)
			wantException(t, "WriteCoils(1998, 3)", c.WriteCoils(ctx, u, 1998, []bool{true, true, true}), modbus.FCWriteMultipleCoils, excIllegalDataAddress)
			wantException(t, "WriteRegisters(254, 3)", c.WriteRegisters(ctx, u, 254, []uint16{1, 2, 3}), modbus.FCWriteMultipleRegisters, excIllegalDataAddress)
			if a.has(t, featServerMaskWrite) {
				wantException(t, "MaskWriteRegister(256)", c.MaskWriteRegister(ctx, u, 256, 0, 0xFFFF), modbus.FCMaskWriteRegister, excIllegalDataAddress)
			}
			if a.has(t, featServerReadWriteMultiple) {
				// Both ranges are checked before anything is written.
				_, err := c.ReadWriteMultipleRegisters(ctx, u, 250, 10, 40, []uint16{7, 8})
				wantException(t, "ReadWriteMultipleRegisters with the read range outside", err, modbus.FCReadWriteMultipleRegs, excIllegalDataAddress)
				_, err = c.ReadWriteMultipleRegisters(ctx, u, 0, 2, 255, []uint16{7, 8})
				wantException(t, "ReadWriteMultipleRegisters with the write range outside", err, modbus.FCReadWriteMultipleRegs, excIllegalDataAddress)
			}
		})

		t.Run("another-unit-identifier", func(t *testing.T) {
			a.need(t, featServerUnitIDFiltering)
			for _, other := range []uint8{0, 2, 9, 247, 248, 255} {
				_, err := c.ReadHoldingRegisters(ctx, other, 0, 1)
				wantException(t, fmt.Sprintf("unit %d: ReadHoldingRegisters", other), err, modbus.FCReadHoldingRegisters, excGatewayNoResponse)
				if !errors.Is(err, modbus.ErrGWTargetFailedToRespond) {
					t.Errorf("unit %d: %v is not ErrGWTargetFailedToRespond", other, err)
				}
				_, err = c.ReadCoils(ctx, other, 0, 1)
				wantException(t, fmt.Sprintf("unit %d: ReadCoils", other), err, modbus.FCReadCoils, excGatewayNoResponse)
				_, err = c.ReadInputRegisters(ctx, other, 65280, 1)
				wantException(t, fmt.Sprintf("unit %d: ReadInputRegisters", other), err, modbus.FCReadInputRegisters, excGatewayNoResponse)
				_, err = c.ReadDiscreteInputs(ctx, other, 1000, 1)
				wantException(t, fmt.Sprintf("unit %d: ReadDiscreteInputs", other), err, modbus.FCReadDiscreteInputs, excGatewayNoResponse)
				wantException(t, fmt.Sprintf("unit %d: WriteRegister", other), c.WriteRegister(ctx, other, 10, 1), modbus.FCWriteSingleRegister, excGatewayNoResponse)
				wantException(t, fmt.Sprintf("unit %d: WriteCoils", other), c.WriteCoils(ctx, other, 0, []bool{true, true, true}), modbus.FCWriteMultipleCoils, excGatewayNoResponse)
			}
		})

		t.Run("illegal-function", func(t *testing.T) {
			// A function no reference server has, with data bytes: FC24.
			_, err := c.ReadFIFOQueue(ctx, u, 0)
			wantException(t, "ReadFIFOQueue", err, modbus.FCReadFIFOQueue, excIllegalFunction)
			if !errors.Is(err, modbus.ErrIllegalFunction) {
				t.Errorf("%v is not ErrIllegalFunction", err)
			}
			// The functions a server declares it does not have.
			if !a.serves(t, 22) {
				wantException(t, "MaskWriteRegister", c.MaskWriteRegister(ctx, u, 5, 0xFFFF, 0), modbus.FCMaskWriteRegister, excIllegalFunction)
			}
			if !a.serves(t, 43) {
				_, err := c.ReadDeviceIdentification(ctx, u, modbus.DeviceIDBasic, 0)
				wantException(t, "ReadDeviceIdentification", err, modbus.FCEncapsulatedInterface, excIllegalFunction)
				if ok, err := c.SupportsDeviceIdentification(ctx, u); ok || err != nil {
					t.Errorf("SupportsDeviceIdentification = %v, %v, want false", ok, err)
				}
			}
		})

		// Nothing was written, and all of the above ran on one connection:
		// an exception response does not cost the connection.
		wantState(t, "after the rejected requests", c, fx, fx.Coils.Values, fx.HoldingRegisters.Values)
		if a.has(t, featConnectionEvents) {
			opened := srv.waitEvents(t, "connection-opened", 1, func(e event) bool { return e.name() == "connection-opened" })
			if len(opened) != 1 {
				t.Errorf("the server saw %d connections, want 1", len(opened))
			}
		}
	})
}

// TestClientQuantityLimits checks what the go-modbus client does with a
// quantity the protocol does not allow: it refuses the call and sends
// nothing. The library has no way to send such a request, so the exception 3
// a reference server answers it with (serverQuantityCheck) cannot be provoked
// from here; TestClientInvalidCoilValue receives an exception 3 another way.
func TestClientQuantityLimits(t *testing.T) {
	forEachAdapter(t, func(t *testing.T, a adapter) {
		a.need(t, featRequestEvents)
		fx := loadFixture(t, a)
		srv := startServer(t, a)
		c := newClient(t, srv, nil)
		ctx := context.Background()
		u := fx.unit()

		refused := func(what string, err error) {
			t.Helper()
			var pe *modbus.ParameterError
			if !errors.As(err, &pe) {
				t.Errorf("%s: got %v, want a *ParameterError", what, err)
			}
		}
		_, err := c.ReadCoils(ctx, u, 0, 0)
		refused("ReadCoils quantity 0", err)
		_, err = c.ReadCoils(ctx, u, 0, 2001)
		refused("ReadCoils quantity 2001", err)
		_, err = c.ReadDiscreteInputs(ctx, u, 1000, 2001)
		refused("ReadDiscreteInputs quantity 2001", err)
		_, err = c.ReadHoldingRegisters(ctx, u, 0, 0)
		refused("ReadHoldingRegisters quantity 0", err)
		_, err = c.ReadHoldingRegisters(ctx, u, 0, 126)
		refused("ReadHoldingRegisters quantity 126", err)
		_, err = c.ReadInputRegisters(ctx, u, 65280, 126)
		refused("ReadInputRegisters quantity 126", err)
		_, err = c.ReadInputRegisters(ctx, u, 65535, 2)
		refused("ReadInputRegisters beyond address 65535", err)
		refused("WriteCoils with 1969 values", c.WriteCoils(ctx, u, 0, make([]bool, 1969)))
		refused("WriteCoils with no value", c.WriteCoils(ctx, u, 0, nil))
		refused("WriteRegisters with 124 values", c.WriteRegisters(ctx, u, 0, make([]uint16, 124)))
		refused("WriteRegisters with no value", c.WriteRegisters(ctx, u, 0, nil))
		_, err = c.ReadWriteMultipleRegisters(ctx, u, 0, 126, 0, []uint16{1})
		refused("ReadWriteMultipleRegisters reading 126", err)
		_, err = c.ReadWriteMultipleRegisters(ctx, u, 0, 1, 0, make([]uint16, 122))
		refused("ReadWriteMultipleRegisters writing 122", err)

		// The largest requests are not refused.
		if _, err := c.ReadHoldingRegisters(ctx, u, 0, 125); err != nil {
			t.Errorf("ReadHoldingRegisters quantity 125: %v", err)
		}
		// One request reached the server: the last one.
		evs := srv.requestEvents(t, 1)
		if len(evs) != 1 || evs[0].num("function") != 3 || evs[0].num("quantity") != 125 {
			t.Errorf("the server saw %d request(s), want only the read of 125 registers: %v", len(evs), evs)
		}
	})
}

// TestClientInvalidCoilValue sends FC05 with a value that is neither 0x0000
// nor 0xFF00, which WriteCoilRaw allows on purpose. The specification asks
// for exception 3; COMPATIBILITY.md § Findings records that the reference
// stacks differ here, and this is outside the fixture rules. Whatever the
// server does, the go-modbus client must report it and work afterwards.
func TestClientInvalidCoilValue(t *testing.T) {
	forEachAdapter(t, func(t *testing.T, a adapter) {
		fx := loadFixture(t, a)
		srv := startServer(t, a)
		c := newClient(t, srv, nil)
		ctx := context.Background()
		u := fx.unit()

		start := time.Now()
		err := c.WriteCoilRaw(ctx, u, 5, 0x1234)
		t.Logf("%s answers FC05 with value 0x1234 with: %v (after %v)", a.name, err, time.Since(start).Round(time.Millisecond))
		if a.name == libmodbus {
			// libmodbus v3.2.0 follows the specification.
			wantException(t, "WriteCoilRaw(0x1234)", err, modbus.FCWriteSingleCoil, excIllegalDataValue)
			if !errors.Is(err, modbus.ErrIllegalDataValue) {
				t.Errorf("%v is not ErrIllegalDataValue", err)
			}
		}
		if a.name == pymodbus {
			// PyModbus 3.15.0 takes any value but 0x0000 for ON and
			// answers with 0xFF00. That is not the echo of the request,
			// which go-modbus reports instead of a success.
			if !errors.Is(err, modbus.ErrProtocolError) {
				t.Errorf("got %v, want a protocol error for the wrong echo", err)
			}
		}
		if errors.Is(err, modbus.ErrRequestTimedOut) {
			t.Errorf("the request timed out: a server that closes the connection must be noticed at once")
		}
		// The client recovers by itself, on a new connection if the server
		// closed this one.
		got, err := c.ReadHoldingRegisters(ctx, u, 0, 8)
		if err != nil {
			t.Fatalf("the request after: %v", err)
		}
		if want := fx.HoldingRegisters.Values[:8]; !reflect.DeepEqual(got, want) {
			t.Errorf("the request after: got %v, want %v", got, want)
		}
	})
}

// TestClientWrites writes with every function the go-modbus client has and
// reads the result back on another connection: what was written arrived, at
// the right place, and nothing else changed. Each function gets a freshly
// started server. Where the server reports its requests, what it received is
// checked as well.
func TestClientWrites(t *testing.T) {
	forEachAdapter(t, func(t *testing.T, a adapter) {
		fx := loadFixture(t, a)
		ctx := context.Background()
		u := fx.unit()

		// run starts a server and hands the test a client, the expected
		// state to update, and a check that reads the state back.
		run := func(name string, needs []string, fn func(t *testing.T, srv *refServer, c *modbus.Client, coils []bool, holding []uint16, verify func(what string))) {
			t.Run(name, func(t *testing.T) {
				a.need(t, needs...)
				srv := startServer(t, a)
				c := newClient(t, srv, nil)
				reader := newClient(t, srv, nil)
				coils := append([]bool(nil), fx.Coils.Values...)
				holding := append([]uint16(nil), fx.HoldingRegisters.Values...)
				fn(t, srv, c, coils, holding, func(what string) {
					t.Helper()
					wantState(t, what, reader, fx, coils, holding)
				})
			})
		}
		// lastWrite returns the newest request event with the function.
		lastWrite := func(t *testing.T, srv *refServer, function int) event {
			t.Helper()
			evs := srv.waitEvents(t, "request", 1, func(e event) bool { return e.name() == "request" && e.num("function") == function })
			return evs[len(evs)-1]
		}

		run("FC05-write-single-coil", nil, func(t *testing.T, srv *refServer, c *modbus.Client, coils []bool, _ []uint16, verify func(string)) {
			for _, w := range []struct {
				addr  uint16
				value bool
			}{{5, true}, {1999, false}, {0, true}, {6, false}, {1000, true}} {
				if coils[w.addr] == w.value {
					t.Fatalf("coil %d is already %v: the write would prove nothing", w.addr, w.value)
				}
				if err := c.WriteCoil(ctx, u, w.addr, w.value); err != nil {
					t.Fatalf("WriteCoil(%d, %v): %v", w.addr, w.value, err)
				}
				coils[w.addr] = w.value
				verify(fmt.Sprintf("after WriteCoil(%d, %v)", w.addr, w.value))
			}
			if a.has(t, featRequestEvents) {
				if e := lastWrite(t, srv, 5); e.num("address") != 1000 || !reflect.DeepEqual(e["bits"], []any{true}) || e["exception"] != nil {
					t.Errorf("the server reports the last write as %v", e)
				}
			}
		})

		run("FC06-write-single-register", nil, func(t *testing.T, srv *refServer, c *modbus.Client, _ []bool, holding []uint16, verify func(string)) {
			for _, w := range [][2]uint16{{10, 4660}, {255, 65535}, {4, 0}, {0, 0xABCD}, {128, 0x8000}} {
				if err := c.WriteRegister(ctx, u, w[0], w[1]); err != nil {
					t.Fatalf("WriteRegister(%d, %d): %v", w[0], w[1], err)
				}
				holding[w[0]] = w[1]
				verify(fmt.Sprintf("after WriteRegister(%d, %d)", w[0], w[1]))
			}
			if a.has(t, featRequestEvents) {
				if e := lastWrite(t, srv, 6); e.num("address") != 128 || !reflect.DeepEqual(e["registers"], []any{float64(0x8000)}) {
					t.Errorf("the server reports the last write as %v", e)
				}
			}
		})

		run("FC15-write-multiple-coils", nil, func(t *testing.T, srv *refServer, c *modbus.Client, coils []bool, _ []uint16, verify func(string)) {
			bulk := make([]bool, 1968)
			for i := range bulk {
				bulk[i] = bulkCoil(i) != 0
			}
			inverted := make([]bool, 2000)
			for i := range inverted {
				inverted[i] = !fx.Coils.Values[i]
			}
			for _, w := range []struct {
				addr   uint16
				values []bool
			}{
				{100, []bool{true, false, true, true, false, false, true, false, true}},
				{1999, []bool{false}},
				{7, []bool{true, true, true, true, true, true, true, true}},
				{32, bulk},
				// Every coil changes, in two requests.
				{0, inverted[:1968]},
				{1968, inverted[1968:]},
			} {
				if err := c.WriteCoils(ctx, u, w.addr, w.values); err != nil {
					t.Fatalf("WriteCoils(%d, %d values): %v", w.addr, len(w.values), err)
				}
				copy(coils[w.addr:], w.values)
				verify(fmt.Sprintf("after WriteCoils(%d, %d values)", w.addr, len(w.values)))
			}
			if a.has(t, featRequestEvents) {
				if e := lastWrite(t, srv, 15); e.num("address") != 1968 || e.num("quantity") != 32 {
					t.Errorf("the server reports the last write as %v", e)
				}
			}
		})

		run("FC16-write-multiple-registers", nil, func(t *testing.T, srv *refServer, c *modbus.Client, _ []bool, holding []uint16, verify func(string)) {
			bulk := make([]uint16, 123)
			for i := range bulk {
				bulk[i] = uint16(bulkRegister(i))
			}
			for _, w := range []struct {
				addr   uint16
				values []uint16
			}{
				{20, []uint16{1, 2, 65535}},
				{0, []uint16{0x0102}},
				{255, []uint16{0xFFFE}},
				{133, bulk},
				{0, bulk},
			} {
				if err := c.WriteRegisters(ctx, u, w.addr, w.values); err != nil {
					t.Fatalf("WriteRegisters(%d, %d values): %v", w.addr, len(w.values), err)
				}
				copy(holding[w.addr:], w.values)
				verify(fmt.Sprintf("after WriteRegisters(%d, %d values)", w.addr, len(w.values)))
			}
			// Bytes go out in the order given: 0xCAFE, 0xBABE.
			if err := c.WriteRegisterBytes(ctx, u, 200, []byte{0xCA, 0xFE, 0xBA, 0xBE}); err != nil {
				t.Fatalf("WriteRegisterBytes: %v", err)
			}
			holding[200], holding[201] = 0xCAFE, 0xBABE
			verify("after WriteRegisterBytes(200)")
			if a.has(t, featRequestEvents) {
				if e := lastWrite(t, srv, 16); e.num("address") != 200 || e.num("quantity") != 2 ||
					!reflect.DeepEqual(e["registers"], []any{float64(0xCAFE), float64(0xBABE)}) {
					t.Errorf("the server reports the last write as %v", e)
				}
			}
		})

		run("FC22-mask-write-register", []string{featServerMaskWrite}, func(t *testing.T, srv *refServer, c *modbus.Client, _ []bool, holding []uint16, verify func(string)) {
			for _, w := range []struct{ addr, and, or uint16 }{
				{5, 0x00F2, 0x0025}, // the example of the specification
				{5, 0xFFFF, 0x0000}, // changes nothing
				{4, 0x0000, 0x0000}, // clears 0xFFFF
				{1, 0x0000, 0xA5A5}, // sets
				{2, 0xFF00, 0xFFFF}, // keeps the high byte of 0x7FFF, sets the low one
				{255, 0x0F0F, 0x1234},
			} {
				if err := c.MaskWriteRegister(ctx, u, w.addr, w.and, w.or); err != nil {
					t.Fatalf("MaskWriteRegister(%d, 0x%04X, 0x%04X): %v", w.addr, w.and, w.or, err)
				}
				holding[w.addr] = (holding[w.addr] & w.and) | (w.or &^ w.and)
				verify(fmt.Sprintf("after MaskWriteRegister(%d, 0x%04X, 0x%04X)", w.addr, w.and, w.or))
			}
			if holding[5] != 0x0035 {
				t.Fatalf("the test computes register 5 as 0x%04X, the specification's example gives 0x0035 for 0x1234", holding[5])
			}
			if a.has(t, featRequestEvents) {
				if e := lastWrite(t, srv, 22); e.num("address") != 255 || e.num("andMask") != 0x0F0F || e.num("orMask") != 0x1234 {
					t.Errorf("the server reports the last write as %v", e)
				}
			}
		})

		run("FC23-read-write-multiple-registers", []string{featServerReadWriteMultiple}, func(t *testing.T, srv *refServer, c *modbus.Client, _ []bool, holding []uint16, verify func(string)) {
			bulk := make([]uint16, 121)
			for i := range bulk {
				bulk[i] = uint16(bulkRegister(i))
			}
			for _, w := range []struct {
				readAddr, readQty, writeAddr uint16
				values                       []uint16
			}{
				{30, 4, 31, []uint16{7, 8}},   // the read range contains the write range
				{0, 1, 255, []uint16{0xBEEF}}, // apart
				{100, 3, 100, []uint16{1, 2, 3}},
				{0, 125, 135, bulk}, // the largest request
				{131, 125, 0, bulk},
			} {
				got, err := c.ReadWriteMultipleRegisters(ctx, u, w.readAddr, w.readQty, w.writeAddr, w.values)
				if err != nil {
					t.Fatalf("ReadWriteMultipleRegisters(%d, %d, %d, %d values): %v", w.readAddr, w.readQty, w.writeAddr, len(w.values), err)
				}
				// The write comes first: the read sees it.
				copy(holding[w.writeAddr:], w.values)
				if want := holding[w.readAddr : w.readAddr+w.readQty]; !reflect.DeepEqual(got, want) {
					t.Errorf("ReadWriteMultipleRegisters(%d, %d, %d, %d values) returned: %s",
						w.readAddr, w.readQty, w.writeAddr, len(w.values), diffRegisters(got, want))
				}
				verify(fmt.Sprintf("after ReadWriteMultipleRegisters(%d, %d, %d, %d values)", w.readAddr, w.readQty, w.writeAddr, len(w.values)))
			}
			// A valid write with an invalid read range writes nothing.
			_, err := c.ReadWriteMultipleRegisters(ctx, u, 250, 10, 40, []uint16{0xDEAD, 0xDEAD})
			wantException(t, "ReadWriteMultipleRegisters with the read range outside", err, modbus.FCReadWriteMultipleRegs, excIllegalDataAddress)
			verify("after the rejected ReadWriteMultipleRegisters")
			if a.has(t, featRequestEvents) {
				e := lastWrite(t, srv, 23)
				if e.num("address") != 250 || e.num("quantity") != 10 || e.num("writeAddress") != 40 || e.num("exception") != 2 ||
					!reflect.DeepEqual(e["writeRegisters"], []any{float64(0xDEAD), float64(0xDEAD)}) {
					t.Errorf("the server reports the rejected request as %v", e)
				}
			}
		})
	})
}

// TestClientDeviceIdentification reads the device identification of the
// reference servers that have it, in every category, and compares the
// objects with the fixture.
func TestClientDeviceIdentification(t *testing.T) {
	forEachAdapter(t, func(t *testing.T, a adapter) {
		a.need(t, featServerDeviceIdentification)
		fx := loadFixture(t, a)
		srv := startServer(t, a)
		c := newClient(t, srv, nil)
		ctx := context.Background()
		u := fx.unit()

		sameObjects := func(what string, got []modbus.DeviceIdentificationObject, want []fixtureObject) {
			t.Helper()
			if len(got) != len(want) {
				t.Errorf("%s: got %d objects %v, want %v", what, len(got), got, want)
				return
			}
			for i, o := range got {
				if uint8(o.ID) != want[i].ID || o.Value != want[i].Value {
					t.Errorf("%s: object %d is %d=%q, want %d=%q", what, i, o.ID, o.Value, want[i].ID, want[i].Value)
				}
			}
		}
		// The level FIXTURES.md prescribes, with or without the bit for
		// individual access.
		wantLevel := uint8(fx.conformity())
		var individual bool
		for _, cat := range []modbus.DeviceIDCategory{modbus.DeviceIDBasic, modbus.DeviceIDRegular, modbus.DeviceIDExtended} {
			di, err := c.ReadDeviceIdentification(ctx, u, cat, 0)
			if err != nil {
				t.Fatalf("ReadDeviceIdentification(category %d): %v", cat, err)
			}
			sameObjects(fmt.Sprintf("category %d", cat), di.Objects, fx.objects(cat))
			if di.ConformityLevel&^0x80 != wantLevel {
				t.Errorf("category %d: conformity level 0x%02X, want 0x%02X or 0x%02X", cat, di.ConformityLevel, wantLevel, wantLevel|0x80)
			}
			if di.MoreFollows {
				t.Errorf("category %d: MoreFollows is set on the complete result", cat)
			}
			individual = di.SupportsIndividualAccess()
		}

		all, err := c.ReadAllDeviceIdentification(ctx, u)
		if err != nil {
			t.Fatalf("ReadAllDeviceIdentification: %v", err)
		}
		sameObjects("ReadAllDeviceIdentification", all.Objects, fx.objects(modbus.DeviceIDExtended))

		if ok, err := c.SupportsDeviceIdentification(ctx, u); !ok || err != nil {
			t.Errorf("SupportsDeviceIdentification = %v, %v, want true", ok, err)
		}

		// Stream access from a later object: the rest of the category.
		di, err := c.ReadDeviceIdentification(ctx, u, modbus.DeviceIDRegular, 4)
		if err != nil {
			t.Fatalf("ReadDeviceIdentification(regular, from object 4): %v", err)
		}
		sameObjects("regular from object 4", di.Objects, fx.objects(modbus.DeviceIDRegular)[4:])

		if individual {
			for _, want := range fx.objects(modbus.DeviceIDExtended) {
				di, err := c.ReadDeviceIdentification(ctx, u, modbus.DeviceIDIndividual, modbus.DeviceIDObjectID(want.ID))
				if err != nil {
					t.Fatalf("individual access to object %d: %v", want.ID, err)
				}
				sameObjects(fmt.Sprintf("individual access to object %d", want.ID), di.Objects, []fixtureObject{want})
			}
		} else {
			t.Logf("%s does not offer individual access", a.name)
		}

		if a.has(t, featServerUnitIDFiltering) {
			_, err := c.ReadDeviceIdentification(ctx, 2, modbus.DeviceIDBasic, 0)
			wantException(t, "ReadDeviceIdentification for unit 2", err, modbus.FCEncapsulatedInterface, excGatewayNoResponse)
		}
	})
}

// TestClientManyRequests sends two thousand requests of all four read
// functions on one connection and checks every answer: no response is
// attributed to the wrong request, and the connection is never replaced.
func TestClientManyRequests(t *testing.T) {
	forEachAdapter(t, func(t *testing.T, a adapter) {
		fx := loadFixture(t, a)
		srv := startServer(t, a)
		c := newClient(t, srv, nil)
		ctx := context.Background()
		u := fx.unit()

		const total = 2000
		for i := 0; i < total; i++ {
			if err := readAndCheck(ctx, c, fx, u, i); err != nil {
				t.Fatalf("request %d: %v", i, err)
			}
		}
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		if a.has(t, featRequestEvents) {
			evs := srv.requestEvents(t, total)
			if len(evs) != total {
				t.Errorf("the server answered %d requests, want %d", len(evs), total)
			}
			for i, e := range evs {
				if e["exception"] != nil || e.num("function") != i%4+1 {
					t.Fatalf("request %d as the server saw it: %v", i, e)
				}
			}
		}
		if a.has(t, featConnectionEvents) {
			closed := srv.waitEvents(t, "connection-closed", 1, func(e event) bool { return e.name() == "connection-closed" })
			opened := srv.waitEvents(t, "connection-opened", 1, func(e event) bool { return e.name() == "connection-opened" })
			if len(opened) != 1 || len(closed) != 1 {
				t.Errorf("the server saw %d connection(s) opened and %d closed, want 1 and 1", len(opened), len(closed))
			}
		}
	})
}

// readAndCheck sends the i-th request of a deterministic sequence that
// cycles through FC01..FC04 with varying addresses and quantities, and
// compares the answer with the fixture.
func readAndCheck(ctx context.Context, c *modbus.Client, fx *fixture, u uint8, i int) error {
	switch i % 4 {
	case 0:
		at, n := (i*37)%1900, 1+(i*13)%100
		got, err := c.ReadCoils(ctx, u, fx.Coils.Address+uint16(at), uint16(n))
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got, fx.Coils.Values[at:at+n]) {
			return fmt.Errorf("%d coils from %d: %s", n, at, diffBits(got, fx.Coils.Values[at:at+n]))
		}
	case 1:
		at, n := (i*41)%1900, 1+(i*17)%100
		got, err := c.ReadDiscreteInputs(ctx, u, fx.DiscreteInputs.Address+uint16(at), uint16(n))
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got, fx.DiscreteInputs.Values[at:at+n]) {
			return fmt.Errorf("%d discrete inputs from index %d: %s", n, at, diffBits(got, fx.DiscreteInputs.Values[at:at+n]))
		}
	case 2:
		at, n := (i*7)%200, 1+(i*5)%56
		got, err := c.ReadHoldingRegisters(ctx, u, fx.HoldingRegisters.Address+uint16(at), uint16(n))
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got, fx.HoldingRegisters.Values[at:at+n]) {
			return fmt.Errorf("%d holding registers from %d: %s", n, at, diffRegisters(got, fx.HoldingRegisters.Values[at:at+n]))
		}
	default:
		at, n := (i*11)%200, 1+(i*3)%56
		got, err := c.ReadInputRegisters(ctx, u, fx.InputRegisters.Address+uint16(at), uint16(n))
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(got, fx.InputRegisters.Values[at:at+n]) {
			return fmt.Errorf("%d input registers from index %d: %s", n, at, diffRegisters(got, fx.InputRegisters.Values[at:at+n]))
		}
	}
	return nil
}

// TestClientConcurrentConnections runs sixteen go-modbus clients against one
// reference server at the same time, each on its own connection, and then a
// pooled client shared by many goroutines. Every client reads and checks, and
// writes registers of its own; in the end all writes are there, read on yet
// another connection: the state is shared by all connections.
func TestClientConcurrentConnections(t *testing.T) {
	forEachAdapter(t, func(t *testing.T, a adapter) {
		fx := loadFixture(t, a)
		srv := startServer(t, a)
		ctx := context.Background()
		u := fx.unit()

		// The contract promises at least 16 simultaneous connections.
		const clients, rounds = 16, 40
		// The clients write to registers 64..191 and coils 1000..1127; the
		// reads stay below.
		holding := append([]uint16(nil), fx.HoldingRegisters.Values...)
		coils := append([]bool(nil), fx.Coils.Values...)
		conns := make([]*modbus.Client, clients)
		for i := range conns {
			conns[i] = newClient(t, srv, nil)
		}
		var wg sync.WaitGroup
		errs := make(chan error, clients)
		for i, c := range conns {
			wg.Add(1)
			go func() {
				defer wg.Done()
				reg, coil := uint16(64+i*8), uint16(1000+i*8)
				for r := 0; r < rounds; r++ {
					values := make([]uint16, 8)
					bits := make([]bool, 8)
					for k := range values {
						values[k] = uint16(i<<12 | r<<4 | k)
						bits[k] = (i+r+k)%3 == 0
					}
					if err := c.WriteRegisters(ctx, u, reg, values); err != nil {
						errs <- fmt.Errorf("client %d round %d: WriteRegisters: %w", i, r, err)
						return
					}
					if err := c.WriteCoils(ctx, u, coil, bits); err != nil {
						errs <- fmt.Errorf("client %d round %d: WriteCoils: %w", i, r, err)
						return
					}
					// Its own registers hold what it just wrote: no other
					// client writes there.
					got, err := c.ReadHoldingRegisters(ctx, u, reg, 8)
					if err != nil || !reflect.DeepEqual(got, values) {
						errs <- fmt.Errorf("client %d round %d: read back %v, %v, want %v", i, r, got, err, values)
						return
					}
					gotBits, err := c.ReadCoils(ctx, u, coil, 8)
					if err != nil || !reflect.DeepEqual(gotBits, bits) {
						errs <- fmt.Errorf("client %d round %d: read back %v, %v, want %v", i, r, gotBits, err, bits)
						return
					}
					// And a read of data nobody writes, different per
					// client, so that a mixed-up response shows.
					at, n := i*3, 20+i
					in, err := c.ReadInputRegisters(ctx, u, fx.InputRegisters.Address+uint16(at), uint16(n))
					if err != nil || !reflect.DeepEqual(in, fx.InputRegisters.Values[at:at+n]) {
						errs <- fmt.Errorf("client %d round %d: input registers %v, %v", i, r, in, err)
						return
					}
					if r == rounds-1 {
						// Each goroutine owns its part of the tables.
						copy(holding[reg:], values)
						copy(coils[coil:], bits)
					}
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
		if t.Failed() {
			return
		}
		if a.has(t, featConnectionEvents) {
			opened := srv.waitEvents(t, "connection-opened", clients, func(e event) bool { return e.name() == "connection-opened" })
			if len(opened) != clients {
				t.Errorf("the server saw %d connections, want %d", len(opened), clients)
			}
		}
		for _, c := range conns {
			_ = c.Close()
		}

		// One client, a pool of connections, many goroutines.
		const poolSize, workers, each = 8, 32, 25
		pool := newClient(t, srv, func(c *modbus.Config) { c.MaxConns = poolSize })
		errs = make(chan error, workers)
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < each; i++ {
					// Only the tables nobody wrote to.
					if err := readAndCheck(ctx, pool, fx, u, (w*each+i)*2+1); err != nil {
						errs <- fmt.Errorf("pool worker %d request %d: %w", w, i, err)
						return
					}
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(err)
		}
		if a.has(t, featConnectionEvents) {
			opened := srv.waitEvents(t, "connection-opened", clients+1, func(e event) bool { return e.name() == "connection-opened" })
			if n := len(opened) - clients; n < 1 || n > poolSize {
				t.Errorf("the pool opened %d connections, want 1..%d", n, poolSize)
			}
		}

		wantState(t, "after the concurrent writes", pool, fx, coils, holding)
	})
}
