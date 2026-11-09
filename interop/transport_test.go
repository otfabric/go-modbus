//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	modbus "github.com/otfabric/go-modbus"
)

// The transports of the container contract (1.2) other than tcp, and the
// URL scheme of the go-modbus client for each. The go-modbus server has Modbus
// TCP only, so these are client tests against reference servers.
var clientTransports = []struct {
	name   string // the value of --transport
	scheme string // the go-modbus URL scheme
}{
	{"udp", "udp"},
	{"rtu-over-tcp", "rtuovertcp"},
}

// TestClientTransports runs the go-modbus client over UDP and over RTU
// framing on TCP against every reference server that declares the transport:
// every table reads back as the fixture defines it at the largest quantities,
// every write is visible to a later read, an address outside a block is
// exception 2, and a request for another unit identifier is exception 11
// over UDP (where the server filters) and not answered at all over RTU (only
// the addressed server replies, Modbus over Serial Line V1.02, 2.2).
func TestClientTransports(t *testing.T) {
	forEachAdapter(t, func(t *testing.T, a adapter) {
		for _, tr := range clientTransports {
			t.Run(tr.name, func(t *testing.T) {
				if !a.servesOver(t, tr.name) {
					t.Skipf("%s does not declare the %s transport for its server", a.name, tr.name)
				}
				fx := loadFixture(t, a)
				srv := startServerOver(t, a, tr.name)
				c := newClient(t, srv, func(conf *modbus.Config) { conf.URL = tr.scheme + "://" + srv.addr })
				ctx := context.Background()
				u := fx.unit()

				t.Run("reads", func(t *testing.T) {
					coils, err := readAllBits(ctx, c.ReadCoils, u, fx.Coils)
					if err != nil || !reflect.DeepEqual(coils, fx.Coils.Values) {
						t.Errorf("coils: %v, %s", err, diffBits(coils, fx.Coils.Values))
					}
					inputs, err := readAllBits(ctx, c.ReadDiscreteInputs, u, fx.DiscreteInputs)
					if err != nil || !reflect.DeepEqual(inputs, fx.DiscreteInputs.Values) {
						t.Errorf("discrete inputs: %v, %s", err, diffBits(inputs, fx.DiscreteInputs.Values))
					}
					holding, err := readAllRegisters(ctx, c.ReadHoldingRegisters, u, fx.HoldingRegisters)
					if err != nil || !reflect.DeepEqual(holding, fx.HoldingRegisters.Values) {
						t.Errorf("holding registers: %v, %s", err, diffRegisters(holding, fx.HoldingRegisters.Values))
					}
					if a.has(t, featServerHighestAddress) {
						input, err := readAllRegisters(ctx, c.ReadInputRegisters, u, fx.InputRegisters)
						if err != nil || !reflect.DeepEqual(input, fx.InputRegisters.Values) {
							t.Errorf("input registers: %v, %s", err, diffRegisters(input, fx.InputRegisters.Values))
						}
					}
				})

				t.Run("exceptions", func(t *testing.T) {
					_, err := c.ReadHoldingRegisters(ctx, u, 256, 1)
					wantException(t, "ReadHoldingRegisters(256, 1)", err, modbus.FCReadHoldingRegisters, excIllegalDataAddress)
					_, err = c.ReadCoils(ctx, u, 1999, 2)
					wantException(t, "ReadCoils(1999, 2)", err, modbus.FCReadCoils, excIllegalDataAddress)
					err = c.WriteRegisters(ctx, u, 254, []uint16{1, 2, 3})
					wantException(t, "WriteRegisters(254, 3)", err, modbus.FCWriteMultipleRegisters, excIllegalDataAddress)
					// The connection, or the socket, is still usable.
					if _, err := c.ReadHoldingRegisters(ctx, u, 0, 1); err != nil {
						t.Errorf("read after the exceptions: %v", err)
					}
				})

				t.Run("another-unit", func(t *testing.T) {
					switch tr.name {
					case "udp":
						a.need(t, featServerUnitIDFiltering)
						_, err := c.ReadHoldingRegisters(ctx, 2, 0, 1)
						wantException(t, "ReadHoldingRegisters for unit 2", err, modbus.FCReadHoldingRegisters, excGatewayNoResponse)
					case "rtu-over-tcp":
						short, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
						defer cancel()
						_, err := c.ReadHoldingRegisters(short, 2, 0, 1)
						if !errors.Is(err, modbus.ErrRequestTimedOut) && !errors.Is(err, context.DeadlineExceeded) {
							t.Errorf("ReadHoldingRegisters for unit 2: got %v, want no answer", err)
						}
					}
					if _, err := c.ReadHoldingRegisters(ctx, u, 0, 1); err != nil {
						t.Errorf("read for the device after the request for unit 2: %v", err)
					}
				})

				t.Run("writes", func(t *testing.T) {
					a.need(t, featServerLargestRegisterWrites)
					regs := ints(123, bulkRegister)
					values := make([]uint16, len(regs))
					for i, v := range regs {
						values[i] = uint16(v)
					}
					if err := c.WriteRegisters(ctx, u, 100, values); err != nil {
						t.Fatalf("WriteRegisters(100, 123): %v", err)
					}
					if got, err := c.ReadHoldingRegisters(ctx, u, 100, 123); err != nil || !reflect.DeepEqual(got, values) {
						t.Errorf("read after WriteRegisters: %v, %s", err, diffRegisters(got, values))
					}
					bits := make([]bool, 1968)
					for i := range bits {
						bits[i] = bulkCoil(i) == 1
					}
					if err := c.WriteCoils(ctx, u, 16, bits); err != nil {
						t.Fatalf("WriteCoils(16, 1968): %v", err)
					}
					if got, err := c.ReadCoils(ctx, u, 16, 1968); err != nil || !reflect.DeepEqual(got, bits) {
						t.Errorf("read after WriteCoils: %v, %s", err, diffBits(got, bits))
					}
					if err := c.WriteRegister(ctx, u, 200, 0xCAFE); err != nil {
						t.Fatalf("WriteRegister(200): %v", err)
					}
					if err := c.WriteCoil(ctx, u, 1, true); err != nil {
						t.Fatalf("WriteCoil(1): %v", err)
					}
					if v, err := c.ReadHoldingRegister(ctx, u, 200); err != nil || v != 0xCAFE {
						t.Errorf("ReadHoldingRegister(200) after WriteRegister = %#x, %v", v, err)
					}
					if v, err := c.ReadCoil(ctx, u, 1); err != nil || !v {
						t.Errorf("ReadCoil(1) after WriteCoil = %v, %v", v, err)
					}
					if a.has(t, featServerMaskWrite) {
						// 0x1234 AND 0x00F2 OR (0x0025 AND NOT 0x00F2) = 0x0035.
						if err := c.MaskWriteRegister(ctx, u, 5, 0x00F2, 0x0025); err != nil {
							t.Errorf("MaskWriteRegister(5): %v", err)
						} else if v, err := c.ReadHoldingRegister(ctx, u, 5); err != nil || v != 0x0035 {
							t.Errorf("register 5 after MaskWriteRegister = %#x, %v, want 0x35", v, err)
						}
					}
					if a.has(t, featServerReadWriteMultiple) {
						got, err := c.ReadWriteMultipleRegisters(ctx, u, 30, 4, 31, []uint16{7, 8})
						want := []uint16{fx.HoldingRegisters.Values[30], 7, 8, fx.HoldingRegisters.Values[33]}
						if err != nil || !reflect.DeepEqual(got, want) {
							t.Errorf("ReadWriteMultipleRegisters: %v, %s", err, diffRegisters(got, want))
						}
					}
					if a.has(t, featRequestEvents) {
						srv.requestsThrough(t, "FC16 of 123 registers at 100", func(e event) bool {
							return e.num("function") == 16 && e.num("address") == 100 && e.num("quantity") == 123 && e["exception"] == nil
						})
					}
				})
			})
		}
	})
}

// servesOver reports whether the reference server of a declares a transport
// (contract 1.2). An image without the field has tcp only.
func (a adapter) servesOver(t testing.TB, transport string) bool {
	t.Helper()
	caps := a.capabilities(t)
	if caps.Transports == nil {
		return transport == "tcp"
	}
	return slices.Contains(caps.Transports.Server, transport)
}
