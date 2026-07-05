package modbus

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// This file holds deterministic, spec-focused conformance tests that run our own
// client against our own server (the reference device) over every transport in
// the matrix. Randomized coverage lives in property_test.go; adversarial
// robustness lives in fuzz_test.go.

func ctx() context.Context { return context.Background() }

// --- FC01 / FC05 / FC15: coils ------------------------------------------------

func TestB2B_Coils_RoundTrip(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		client, cleanup := startPair(t, kind, newRefDevice())
		defer cleanup()

		// Single-coil write (FC05) then read back (FC01).
		if err := client.WriteCoil(ctx(), refUnitID, 4, true); err != nil {
			t.Fatalf("WriteCoil: %v", err)
		}
		got, err := client.ReadCoils(ctx(), refUnitID, 0, 8)
		if err != nil {
			t.Fatalf("ReadCoils: %v", err)
		}
		want := []bool{false, false, false, false, true, false, false, false}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("coils = %v, want %v", got, want)
		}

		// Multiple-coil write (FC15) spanning a byte boundary (17 coils).
		vals := make([]bool, 17)
		for i := range vals {
			vals[i] = i%3 == 0
		}
		if err := client.WriteCoils(ctx(), refUnitID, 10, vals); err != nil {
			t.Fatalf("WriteCoils: %v", err)
		}
		got, err = client.ReadCoils(ctx(), refUnitID, 10, 17)
		if err != nil {
			t.Fatalf("ReadCoils(17): %v", err)
		}
		if !reflect.DeepEqual(got, vals) {
			t.Errorf("17-coil round-trip = %v, want %v", got, vals)
		}
	})
}

// --- FC02: discrete inputs ----------------------------------------------------

func TestB2B_DiscreteInputs_Read(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := newRefDevice()
		seed := []bool{true, false, true, true, false, false, true, false, true}
		copy(dev.discreteInputs, seed)
		client, cleanup := startPair(t, kind, dev)
		defer cleanup()

		got, err := client.ReadDiscreteInputs(ctx(), refUnitID, 0, uint16(len(seed)))
		if err != nil {
			t.Fatalf("ReadDiscreteInputs: %v", err)
		}
		if !reflect.DeepEqual(got, seed) {
			t.Errorf("discrete inputs = %v, want %v", got, seed)
		}
	})
}

// --- FC03 / FC06 / FC16: holding registers -----------------------------------

func TestB2B_HoldingRegisters_RoundTrip(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		client, cleanup := startPair(t, kind, newRefDevice())
		defer cleanup()

		// Single-register write (FC06).
		if err := client.WriteRegister(ctx(), refUnitID, 7, 0xBEEF); err != nil {
			t.Fatalf("WriteRegister: %v", err)
		}
		v, err := client.ReadHoldingRegisters(ctx(), refUnitID, 7, 1)
		if err != nil {
			t.Fatalf("ReadHoldingRegisters: %v", err)
		}
		if v[0] != 0xBEEF {
			t.Errorf("reg 7 = 0x%04x, want 0xBEEF", v[0])
		}

		// Multiple-register write (FC16).
		want := []uint16{0x0102, 0x0304, 0xFFFF, 0x0000, 0xABCD}
		if err := client.WriteRegisters(ctx(), refUnitID, 20, want); err != nil {
			t.Fatalf("WriteRegisters: %v", err)
		}
		got, err := client.ReadHoldingRegisters(ctx(), refUnitID, 20, uint16(len(want)))
		if err != nil {
			t.Fatalf("ReadHoldingRegisters(multi): %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("holding regs = %v, want %v", got, want)
		}
	})
}

// --- FC04: input registers ----------------------------------------------------

func TestB2B_InputRegisters_Read(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := newRefDevice()
		seed := []uint16{0x1111, 0x2222, 0x3333, 0x4444}
		copy(dev.input, seed)
		client, cleanup := startPair(t, kind, dev)
		defer cleanup()

		got, err := client.ReadInputRegisters(ctx(), refUnitID, 0, uint16(len(seed)))
		if err != nil {
			t.Fatalf("ReadInputRegisters: %v", err)
		}
		if !reflect.DeepEqual(got, seed) {
			t.Errorf("input regs = %v, want %v", got, seed)
		}
	})
}

// --- FC22: mask write register ------------------------------------------------

func TestB2B_MaskWriteRegister(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		client, cleanup := startPair(t, kind, newRefDevice())
		defer cleanup()

		if err := client.WriteRegister(ctx(), refUnitID, 3, 0x0012); err != nil {
			t.Fatalf("seed WriteRegister: %v", err)
		}
		// result = (0x0012 & 0x00F2) | (0x0025 & ^0x00F2) = 0x0012 | 0x0005 = 0x0017
		if err := client.MaskWriteRegister(ctx(), refUnitID, 3, 0x00F2, 0x0025); err != nil {
			t.Fatalf("MaskWriteRegister: %v", err)
		}
		v, err := client.ReadHoldingRegisters(ctx(), refUnitID, 3, 1)
		if err != nil {
			t.Fatalf("ReadHoldingRegisters: %v", err)
		}
		if v[0] != 0x0017 {
			t.Errorf("mask write result = 0x%04x, want 0x0017", v[0])
		}
	})
}

// --- FC23: read/write multiple registers -------------------------------------

func TestB2B_ReadWriteMultipleRegisters_WriteBeforeRead(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		client, cleanup := startPair(t, kind, newRefDevice())
		defer cleanup()

		// Write and read overlap: the spec requires the write to happen first,
		// so the read must observe the freshly written values.
		writeVals := []uint16{0xAA01, 0xAA02, 0xAA03}
		got, err := client.ReadWriteMultipleRegisters(ctx(), refUnitID, 100, 3, 100, writeVals)
		if err != nil {
			t.Fatalf("ReadWriteMultipleRegisters: %v", err)
		}
		if !reflect.DeepEqual(got, writeVals) {
			t.Errorf("read-back = %v, want write-before-read %v", got, writeVals)
		}
	})
}

// --- FC43: device identification ---------------------------------------------

func TestB2B_DeviceIdentification(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		client, cleanup := startPair(t, kind, newRefDevice())
		defer cleanup()

		// Basic category returns exactly objects 0x00-0x02.
		di, err := client.ReadDeviceIdentification(ctx(), refUnitID, DeviceIDBasic, 0x00)
		if err != nil {
			t.Fatalf("ReadDeviceIdentification(basic): %v", err)
		}
		if len(di.Objects) != 3 {
			t.Errorf("basic objects = %d, want 3", len(di.Objects))
		}

		// Extended returns the full set.
		all, err := client.ReadAllDeviceIdentification(ctx(), refUnitID)
		if err != nil {
			t.Fatalf("ReadAllDeviceIdentification: %v", err)
		}
		if len(all.Objects) != len(regularDeviceIDObjects()) {
			t.Errorf("extended objects = %d, want %d", len(all.Objects), len(regularDeviceIDObjects()))
		}

		// Individual access to a known object returns exactly one.
		ind, err := client.ReadDeviceIdentification(ctx(), refUnitID, DeviceIDIndividual, 0x01)
		if err != nil {
			t.Fatalf("ReadDeviceIdentification(individual): %v", err)
		}
		if len(ind.Objects) != 1 || ind.Objects[0].ID != 0x01 {
			t.Errorf("individual = %+v, want single object 0x01", ind.Objects)
		}
	})
}

// --- Exception agreement between client and server ---------------------------

func TestB2B_ExceptionAgreement(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		client, cleanup := startPair(t, kind, newRefDevice())
		defer cleanup()

		// Address beyond the device space -> ErrIllegalDataAddress.
		_, err := client.ReadHoldingRegisters(ctx(), refUnitID, uint16(refSpace-1), 5)
		if !errors.Is(err, ErrIllegalDataAddress) {
			t.Errorf("out-of-range read: got %v, want ErrIllegalDataAddress", err)
		}

		// Unserved unit ID -> ErrIllegalFunction (device only serves refUnitID).
		_, err = client.ReadCoils(ctx(), refUnitID+1, 0, 1)
		if !errors.Is(err, ErrIllegalFunction) {
			t.Errorf("wrong unit: got %v, want ErrIllegalFunction", err)
		}

		// Individual FC43 access to an unknown object -> ErrIllegalDataAddress.
		_, err = client.ReadDeviceIdentification(ctx(), refUnitID, DeviceIDIndividual, 0x77)
		if !errors.Is(err, ErrIllegalDataAddress) {
			t.Errorf("unknown devid object: got %v, want ErrIllegalDataAddress", err)
		}
	})
}

// --- Unsupported function codes ----------------------------------------------

func TestB2B_UnsupportedFunctionCodes(t *testing.T) {
	// FCs the server does not implement must return an Illegal Function
	// exception, regardless of payload.
	unsupported := []byte{
		byte(FCDiagnostics),     // 0x08
		byte(FCReportServerID),  // 0x11
		byte(FCReadFileRecord),  // 0x14
		byte(FCWriteFileRecord), // 0x15
		byte(FCReadFIFOQueue),   // 0x18
		0x65,                    // vendor/unknown
	}
	forEachTransport(t, func(t *testing.T, kind string) {
		client, cleanup := startPair(t, kind, newRefDevice())
		defer cleanup()

		for _, fc := range unsupported {
			res := sendRawFC(t, client, refUnitID, fc, []byte{0x00, 0x00, 0x00, 0x01})
			assertMBAPResponseWellFormed(t, res, refUnitID, fc)
			assertExceptionResponse(t, res, exIllegalFunction)
		}
	})
}

// --- Boundary / limit agreement ----------------------------------------------

func TestB2B_BoundaryLimits(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		client, cleanup := startPair(t, kind, newRefDevice())
		defer cleanup()

		// Zero quantity is rejected client-side or server-side; either way the
		// operation must fail rather than silently succeed.
		if _, err := client.ReadHoldingRegisters(ctx(), refUnitID, 0, 0); err == nil {
			t.Error("ReadHoldingRegisters(qty=0) should fail")
		}

		// Over the FC03 limit (125 registers) must fail.
		if _, err := client.ReadHoldingRegisters(ctx(), refUnitID, 0, uint16(maxReadRegisters)+1); err == nil {
			t.Error("ReadHoldingRegisters over limit should fail")
		}

		// Over the FC01 limit (2000 coils) must fail.
		if _, err := client.ReadCoils(ctx(), refUnitID, 0, uint16(maxReadCoils)+1); err == nil {
			t.Error("ReadCoils over limit should fail")
		}
	})
}
