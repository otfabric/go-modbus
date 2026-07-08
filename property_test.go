// SPDX-License-Identifier: MIT

package modbus

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"
)

// This file holds seeded, randomized differential tests. A local shadow model
// is updated in lockstep with operations sent to the reference device through
// our client; random read-backs must always agree with the model. This surfaces
// offset, packing, endianness, and ordering bugs that fixed cases can miss.

// propSeed is fixed so failures are reproducible in CI. It is logged on start.
const propSeed int64 = 0xC0FFEE

// shadowModel mirrors the reference device address space.
type shadowModel struct {
	coils   []bool
	holding []uint16
}

func newShadowModel() *shadowModel {
	return &shadowModel{
		coils:   make([]bool, refSpace),
		holding: make([]uint16, refSpace),
	}
}

func TestB2B_Property_ReadWriteDifferential(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		client, cleanup := startPair(t, kind, newRefDevice())
		defer cleanup()

		rng := rand.New(rand.NewSource(propSeed))
		t.Logf("property seed = %d", propSeed)
		model := newShadowModel()

		const iterations = 300
		for i := 0; i < iterations; i++ {
			switch rng.Intn(6) {
			case 0: // FC05 write single coil
				addr := rng.Intn(refSpace)
				val := rng.Intn(2) == 0
				if err := client.WriteCoil(ctx(), refUnitID, uint16(addr), val); err != nil {
					t.Fatalf("iter %d WriteCoil: %v", i, err)
				}
				model.coils[addr] = val

			case 1: // FC15 write multiple coils
				addr, qty := randWindow(rng, 64)
				vals := make([]bool, qty)
				for j := range vals {
					vals[j] = rng.Intn(2) == 0
				}
				if err := client.WriteCoils(ctx(), refUnitID, uint16(addr), vals); err != nil {
					t.Fatalf("iter %d WriteCoils: %v", i, err)
				}
				copy(model.coils[addr:addr+qty], vals)

			case 2: // FC06 write single register
				addr := rng.Intn(refSpace)
				val := uint16(rng.Intn(0x10000))
				if err := client.WriteRegister(ctx(), refUnitID, uint16(addr), val); err != nil {
					t.Fatalf("iter %d WriteRegister: %v", i, err)
				}
				model.holding[addr] = val

			case 3: // FC16 write multiple registers
				addr, qty := randWindow(rng, 50)
				vals := make([]uint16, qty)
				for j := range vals {
					vals[j] = uint16(rng.Intn(0x10000))
				}
				if err := client.WriteRegisters(ctx(), refUnitID, uint16(addr), vals); err != nil {
					t.Fatalf("iter %d WriteRegisters: %v", i, err)
				}
				copy(model.holding[addr:addr+qty], vals)

			case 4: // FC22 mask write register
				addr := rng.Intn(refSpace)
				and := uint16(rng.Intn(0x10000))
				or := uint16(rng.Intn(0x10000))
				if err := client.MaskWriteRegister(ctx(), refUnitID, uint16(addr), and, or); err != nil {
					t.Fatalf("iter %d MaskWriteRegister: %v", i, err)
				}
				model.holding[addr] = (model.holding[addr] & and) | (or &^ and)

			case 5: // FC23 read/write multiple registers
				waddr, wqty := randWindow(rng, 40)
				raddr, rqty := randWindow(rng, 40)
				vals := make([]uint16, wqty)
				for j := range vals {
					vals[j] = uint16(rng.Intn(0x10000))
				}
				// Apply the write to the model first (spec: write before read).
				copy(model.holding[waddr:waddr+wqty], vals)
				got, err := client.ReadWriteMultipleRegisters(ctx(), refUnitID,
					uint16(raddr), uint16(rqty), uint16(waddr), vals)
				if err != nil {
					t.Fatalf("iter %d ReadWriteMultipleRegisters: %v", i, err)
				}
				want := model.holding[raddr : raddr+rqty]
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("iter %d FC23 read-back = %v, want %v", i, got, want)
				}
			}

			// After every op, verify a random coil window and register window.
			caddr, cqty := randWindow(rng, 64)
			gotCoils, err := client.ReadCoils(ctx(), refUnitID, uint16(caddr), uint16(cqty))
			if err != nil {
				t.Fatalf("iter %d ReadCoils verify: %v", i, err)
			}
			if !reflect.DeepEqual(gotCoils, model.coils[caddr:caddr+cqty]) {
				t.Fatalf("iter %d coil mismatch at [%d:%d]: got %v want %v",
					i, caddr, caddr+cqty, gotCoils, model.coils[caddr:caddr+cqty])
			}

			raddr, rqty := randWindow(rng, 50)
			gotRegs, err := client.ReadHoldingRegisters(ctx(), refUnitID, uint16(raddr), uint16(rqty))
			if err != nil {
				t.Fatalf("iter %d ReadHoldingRegisters verify: %v", i, err)
			}
			if !reflect.DeepEqual(gotRegs, model.holding[raddr:raddr+rqty]) {
				t.Fatalf("iter %d register mismatch at [%d:%d]: got %v want %v",
					i, raddr, raddr+rqty, gotRegs, model.holding[raddr:raddr+rqty])
			}
		}
	})
}

// TestB2B_Property_DeviceIDReassembly checks that the client reassembles exactly
// the object set the device exposes, across randomized sizes that force
// MoreFollows pagination.
func TestB2B_Property_DeviceIDReassembly(t *testing.T) {
	rng := rand.New(rand.NewSource(propSeed))
	t.Logf("devid seed = %d", propSeed)

	for trial := 0; trial < 20; trial++ {
		objs := randomDeviceIDObjects(rng)
		dev := newRefDevice()
		dev.devObjects = objs
		dev.conformity = 0x83

		client, cleanup := startPair(t, "tcp", dev)
		all, err := client.ReadAllDeviceIdentification(ctx(), refUnitID)
		cleanup()
		if err != nil {
			t.Fatalf("trial %d ReadAllDeviceIdentification: %v", trial, err)
		}

		want := append([]DeviceIdentificationObject(nil), objs...)
		sort.Slice(want, func(i, j int) bool { return want[i].ID < want[j].ID })
		got := append([]DeviceIdentificationObject(nil), all.Objects...)
		sort.Slice(got, func(i, j int) bool { return got[i].ID < got[j].ID })

		if len(got) != len(want) {
			t.Fatalf("trial %d: got %d objects, want %d", trial, len(got), len(want))
		}
		for i := range want {
			if got[i].ID != want[i].ID || got[i].Value != want[i].Value {
				t.Fatalf("trial %d object %d: got {0x%02x %q}, want {0x%02x %q}",
					trial, i, got[i].ID, got[i].Value, want[i].ID, want[i].Value)
			}
		}
	}
}

// randWindow returns a random (addr, qty) with qty in [1, maxQty] such that
// addr+qty <= refSpace.
func randWindow(rng *rand.Rand, maxQty int) (addr, qty int) {
	qty = 1 + rng.Intn(maxQty)
	if qty > refSpace {
		qty = refSpace
	}
	addr = rng.Intn(refSpace - qty + 1)
	return addr, qty
}

// randomDeviceIDObjects builds a random set of unique-ID objects. It always
// includes the three mandatory basic objects and adds random regular/extended
// objects with values large enough to sometimes force pagination.
func randomDeviceIDObjects(rng *rand.Rand) []DeviceIdentificationObject {
	objs := []DeviceIdentificationObject{
		{ID: 0x00, Value: "vendor"},
		{ID: 0x01, Value: "product"},
		{ID: 0x02, Value: "1.0"},
	}
	used := map[int]bool{0x00: true, 0x01: true, 0x02: true}
	extra := rng.Intn(8)
	for i := 0; i < extra; i++ {
		id := 0x03 + rng.Intn(0xFD) // 0x03..0xFF
		if used[id] {
			continue
		}
		used[id] = true
		n := rng.Intn(200)
		val := make([]byte, n)
		for j := range val {
			val[j] = byte('A' + rng.Intn(26))
		}
		objs = append(objs, DeviceIdentificationObject{ID: DeviceIDObjectID(id), Value: string(val)})
	}
	return objs
}
