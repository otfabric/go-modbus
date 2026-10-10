// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/otfabric/go-modbus/sunspec"
)

// e2eSunSpecReader adapts a Client to sunspec.Reader, as applications do.
type e2eSunSpecReader struct{ c *Client }

func (r e2eSunSpecReader) ReadRawBytes(ctx context.Context, unitID uint8, addr uint16, byteCount uint16, regType sunspec.RegType) ([]byte, error) {
	return r.c.ReadRegisterBytes(ctx, unitID, addr, byteCount, regType)
}

// e2eSunSpecModel is one model of a SunSpec map: its ID and body length.
type e2eSunSpecModel struct{ id, length uint16 }

// e2eSunSpecMap lays out a SunSpec map at base: the "SunS" marker, the models
// (header plus a recognisable body) and the end model. It returns the register
// image and the headers discovery must report.
func e2eSunSpecMap(base uint16, models []e2eSunSpecModel) ([]uint16, []sunspec.ModelHeader) {
	regs := []uint16{sunspec.MarkerReg0, sunspec.MarkerReg1}
	var headers []sunspec.ModelHeader
	addr := uint32(base) + 2
	add := func(id, length uint16) {
		regs = append(regs, id, length)
		for i := uint16(0); i < length; i++ {
			regs = append(regs, 0xB000+i)
		}
		end := addr + 2 + uint32(length)
		headers = append(headers, sunspec.ModelHeader{
			ID:           id,
			Length:       length,
			StartAddress: uint16(addr),
			EndAddress:   uint16(end - 1),
			NextAddress:  uint16(end),
			HeaderRaw:    [2]uint16{id, length},
			IsEndModel:   id == sunspec.EndModelID && length == sunspec.EndModelLength,
		})
		addr = end
	}
	for _, m := range models {
		add(m.id, m.length)
	}
	add(sunspec.EndModelID, sunspec.EndModelLength)
	return regs, headers
}

func TestE2E_SunSpec_DetectAndDiscover(t *testing.T) {
	models := []e2eSunSpecModel{{1, 66}, {103, 50}, {120, 26}, {160, 128}, {64001, 3}}
	tables := []struct {
		name    string
		regType RegType
		fc      FunctionCode
	}{
		{"holding", HoldingRegister, FCReadHoldingRegisters},
		{"input", InputRegister, FCReadInputRegisters},
	}
	forEachTransport(t, func(t *testing.T, kind string) {
		for _, tb := range tables {
			for _, base := range []uint16{40000, 0, 50000, 40001, 0xFE00} {
				dev := e2eNewDevice()
				image, wantModels := e2eSunSpecMap(base, models)
				// The map lives in one table only; the other one holds no marker.
				if tb.regType == HoldingRegister {
					dev.setHolding(int(base), image...)
				} else {
					dev.setInput(int(base), image...)
				}
				p := e2eStart(t, kind, dev, e2eOpts{})
				r := e2eSunSpecReader{p.client}
				ctx := context.Background()
				opts := &sunspec.Options{UnitID: e2eUnit, RegType: tb.regType}
				if base == 0xFE00 {
					opts.BaseAddresses = []uint16{100, 0xFE00, 40000}
				}

				det, err := sunspec.Detect(ctx, r, opts)
				if err != nil {
					t.Fatalf("%s base %d: Detect: %v", tb.name, base, err)
				}
				if !det.Detected || det.BaseAddress != base || det.UnitID != e2eUnit || det.RegType != tb.regType ||
					det.Marker != [2]uint16{sunspec.MarkerReg0, sunspec.MarkerReg1} {
					t.Errorf("%s base %d: Detect = %+v", tb.name, base, det)
				}
				// Detection probed the candidate addresses in order, stopping
				// at the match; the server saw exactly those reads.
				candidates := sunspec.DefaultBaseAddresses
				if opts.BaseAddresses != nil {
					candidates = opts.BaseAddresses
				}
				var probed []uint16
				for _, c := range candidates {
					probed = append(probed, c)
					if c == base {
						break
					}
				}
				calls := dev.takeCalls()
				if len(calls) != len(probed) || len(det.Attempts) != len(probed) {
					t.Fatalf("%s base %d: %d reads, %d attempts, want %d", tb.name, base, len(calls), len(det.Attempts), len(probed))
				}
				for i, call := range calls {
					if call.FC != tb.fc || call.Addr != probed[i] || call.Quantity != 2 || call.UnitID != e2eUnit {
						t.Errorf("%s base %d: probe %d reached the server as %+v", tb.name, base, i, call)
					}
					a := det.Attempts[i]
					if a.BaseAddress != probed[i] || a.Matched != (probed[i] == base) || a.Error != nil || len(a.Registers) != 2 {
						t.Errorf("%s base %d: attempt %d = %+v", tb.name, base, i, a)
					}
				}

				// The model chain, read header by header.
				got, err := sunspec.ReadModelHeaders(ctx, r, opts, base)
				if err != nil {
					t.Fatalf("%s base %d: ReadModelHeaders: %v", tb.name, base, err)
				}
				if !reflect.DeepEqual(got, wantModels) {
					t.Errorf("%s base %d: models\n got %+v\nwant %+v", tb.name, base, got, wantModels)
				}
				calls = dev.takeCalls()
				if len(calls) != len(wantModels) {
					t.Fatalf("%s base %d: %d header reads, want %d", tb.name, base, len(calls), len(wantModels))
				}
				for i, call := range calls {
					if call.FC != tb.fc || call.Addr != wantModels[i].StartAddress || call.Quantity != 2 {
						t.Errorf("%s base %d: header read %d reached the server as %+v", tb.name, base, i, call)
					}
				}

				// Discover is both in one call.
				disc, err := sunspec.Discover(ctx, r, opts)
				if err != nil {
					t.Fatalf("%s base %d: Discover: %v", tb.name, base, err)
				}
				if !disc.Detection.Detected || disc.Detection.BaseAddress != base || !reflect.DeepEqual(disc.Models, wantModels) {
					t.Errorf("%s base %d: Discover = %+v", tb.name, base, disc)
				}
				if n := len(dev.takeCalls()); n != len(probed)+len(wantModels) {
					t.Errorf("%s base %d: Discover took %d reads, want %d", tb.name, base, n, len(probed)+len(wantModels))
				}

				// MaxModels cuts the enumeration short.
				limited := *opts
				limited.MaxModels = 2
				got, err = sunspec.ReadModelHeaders(ctx, r, &limited, base)
				if err != nil || !reflect.DeepEqual(got, wantModels[:2]) {
					t.Errorf("%s base %d: MaxModels=2: %+v, %v", tb.name, base, got, err)
				}
				// MaxAddressSpan stops a chain that runs too far.
				limited = *opts
				limited.MaxAddressSpan = 100
				got, err = sunspec.ReadModelHeaders(ctx, r, &limited, base)
				if !errors.Is(err, ErrSunSpecModelChainLimitExceeded) || len(got) != 2 {
					t.Errorf("%s base %d: MaxAddressSpan=100: %d models, %v", tb.name, base, len(got), err)
				}

				// Looking in the other register table finds nothing.
				other := *opts
				other.RegType = HoldingRegister
				if tb.regType == HoldingRegister {
					other.RegType = InputRegister
				}
				det, err = sunspec.Detect(ctx, r, &other)
				if err != nil || det.Detected || len(det.Attempts) != len(candidates) {
					t.Errorf("%s base %d: Detect in the other table = %+v, %v", tb.name, base, det, err)
				}
				_ = p.client.Close()
				_ = p.server.Stop()
			}
		}
	})
}

func TestE2E_SunSpec_EdgesAndErrors(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		r := e2eSunSpecReader{p.client}
		ctx := context.Background()

		// Default options: unit 1, holding registers, default candidates. A
		// device without a marker is "not detected", not an error.
		det, err := sunspec.Detect(ctx, r, nil)
		if err != nil || det.Detected || len(det.Attempts) != len(sunspec.DefaultBaseAddresses) {
			t.Errorf("Detect on a non-SunSpec device = %+v, %v", det, err)
		}
		calls := dev.takeCalls()
		if len(calls) != len(sunspec.DefaultBaseAddresses) {
			t.Fatalf("Detect made %d reads, want %d", len(calls), len(sunspec.DefaultBaseAddresses))
		}
		for i, call := range calls {
			if call.FC != FCReadHoldingRegisters || call.UnitID != 1 || call.Addr != sunspec.DefaultBaseAddresses[i] {
				t.Errorf("default probe %d reached the server as %+v", i, call)
			}
		}
		disc, err := sunspec.Discover(ctx, r, nil)
		if err != nil || disc.Detection.Detected || disc.Models != nil {
			t.Errorf("Discover on a non-SunSpec device = %+v, %v", disc, err)
		}
		dev.takeCalls()

		// A map that ends exactly at the end of the address space.
		image, want := e2eSunSpecMap(0xFFF8, []e2eSunSpecModel{{7, 2}})
		dev.setHolding(0xFFF8, image...)
		edge := &sunspec.Options{UnitID: e2eUnit, BaseAddresses: []uint16{0xFFF8}}
		disc, err = sunspec.Discover(ctx, r, edge)
		if err != nil {
			t.Fatalf("Discover at the end of the address space: %v", err)
		}
		if len(disc.Models) != 2 || !disc.Models[1].IsEndModel || disc.Models[1].StartAddress != 0xFFFE ||
			disc.Models[1].EndAddress != 0xFFFF || disc.Models[0] != want[0] {
			t.Errorf("models at the end of the address space = %+v", disc.Models)
		}
		// A marker in the last two registers leaves no room for a model.
		dev.setHolding(0xFFFE, sunspec.MarkerReg0, sunspec.MarkerReg1)
		if _, err := sunspec.ReadModelHeaders(ctx, r, edge, 0xFFFE); !errors.Is(err, ErrSunSpecModelChainInvalid) {
			t.Errorf("ReadModelHeaders with base 65534: %v", err)
		}
		dev.takeCalls()

		// A zero-length model that is not the end model breaks the chain.
		dev.setHolding(1000, sunspec.MarkerReg0, sunspec.MarkerReg1, 1, 4, 0, 0, 0, 0, 99, 0)
		broken := &sunspec.Options{UnitID: e2eUnit, BaseAddresses: []uint16{1000}}
		disc, err = sunspec.Discover(ctx, r, broken)
		if !errors.Is(err, ErrSunSpecModelChainInvalid) || disc == nil || len(disc.Models) != 2 {
			t.Errorf("Discover on a broken chain = %+v, %v", disc, err)
		}
		// A model running past the address space is a protocol error.
		dev.setHolding(2000, sunspec.MarkerReg0, sunspec.MarkerReg1, 1, 0xFFF0)
		if _, err := sunspec.ReadModelHeaders(ctx, r, broken, 2000); !errors.Is(err, ErrProtocolError) {
			t.Errorf("ReadModelHeaders with an oversized model: %v", err)
		}
		dev.takeCalls()

		// Another unit: the server sees it; a unit the device does not serve
		// answers every probe with an exception, which Detect reports.
		opts := &sunspec.Options{UnitID: 5, BaseAddresses: []uint16{1000, 2000}}
		det, err = sunspec.Detect(ctx, r, opts)
		if !errors.Is(err, ErrGWPathUnavailable) || det == nil || det.Detected || len(det.Attempts) != 2 {
			t.Errorf("Detect on an unserved unit = %+v, %v", det, err)
		}
		for _, a := range det.Attempts {
			if !errors.Is(a.Error, ErrGWPathUnavailable) || a.ErrorString == "" {
				t.Errorf("attempt on an unserved unit = %+v", a)
			}
		}
		for _, call := range dev.takeCalls() {
			if call.UnitID != 5 {
				t.Errorf("probe for unit 5 reached the server as unit %d", call.UnitID)
			}
		}
		dev.setUnit(5)
		det, err = sunspec.Detect(ctx, r, opts)
		if err != nil || !det.Detected || det.BaseAddress != 1000 || det.UnitID != 5 {
			t.Errorf("Detect on unit 5 = %+v, %v", det, err)
		}
		dev.setUnit(e2eUnit)

		// A device with a small address space: probes outside of it fail, the
		// one inside still detects.
		dev.setHolding(0, sunspec.MarkerReg0, sunspec.MarkerReg1, sunspec.EndModelID, 0)
		dev.setHook(func(_ context.Context, c e2eCall) error {
			if int(c.Addr)+int(c.Quantity) > 100 {
				return ErrIllegalDataAddress
			}
			return nil
		})
		disc, err = sunspec.Discover(ctx, r, &sunspec.Options{UnitID: e2eUnit, BaseAddresses: []uint16{40000, 50000, 0}})
		if err != nil || !disc.Detection.Detected || disc.Detection.BaseAddress != 0 || len(disc.Models) != 1 {
			t.Fatalf("Discover on a small device = %+v, %v", disc, err)
		}
		if a := disc.Detection.Attempts; len(a) != 3 || !errors.Is(a[0].Error, ErrIllegalDataAddress) ||
			!errors.Is(a[1].Error, ErrIllegalDataAddress) || !a[2].Matched {
			t.Errorf("attempts on a small device = %+v", a)
		}
		// A chain that leads outside the device's address space.
		dev.setHolding(0, sunspec.MarkerReg0, sunspec.MarkerReg1, 1, 200)
		models, err := sunspec.ReadModelHeaders(ctx, r, &sunspec.Options{UnitID: e2eUnit}, 0)
		if !errors.Is(err, ErrIllegalDataAddress) || len(models) != 1 {
			t.Errorf("chain leaving the address space: %+v, %v", models, err)
		}
		dev.setHook(nil)
		dev.takeCalls()

		// Invalid options and a cancelled context never reach the server.
		if _, err := sunspec.Detect(ctx, r, &sunspec.Options{RegType: RegType(7)}); !errors.Is(err, ErrUnexpectedParameters) {
			t.Errorf("Detect with an invalid register type: %v", err)
		}
		if _, err := sunspec.Detect(ctx, r, &sunspec.Options{BaseAddresses: []uint16{}}); !errors.Is(err, ErrUnexpectedParameters) {
			t.Errorf("Detect without candidates: %v", err)
		}
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := sunspec.Detect(cancelled, r, nil); !errors.Is(err, context.Canceled) {
			t.Errorf("Detect with a cancelled context: %v", err)
		}
		if _, err := sunspec.ReadModelHeaders(cancelled, r, nil, 0); !errors.Is(err, context.Canceled) {
			t.Errorf("ReadModelHeaders with a cancelled context: %v", err)
		}
		if _, err := sunspec.Discover(cancelled, r, nil); !errors.Is(err, context.Canceled) {
			t.Errorf("Discover with a cancelled context: %v", err)
		}
		if n := dev.callCount(); n != 0 {
			t.Errorf("%d requests reached the server", n)
		}

		// A closed client surfaces as the probe error.
		_ = p.client.Close()
		if _, err := sunspec.Detect(ctx, r, nil); !errors.Is(err, ErrClientNotOpen) {
			t.Errorf("Detect on a closed client: %v", err)
		}
	})
}
