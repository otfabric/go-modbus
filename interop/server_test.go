//go:build interop

// SPDX-License-Identifier: MIT

package interop

import (
	"encoding/hex"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// scenario is one operation of a reference client and the outcome expected
// of a device that serves the baseline fixture.
type scenario struct {
	// mutates marks operations that change the device: they get a fresh
	// device (and a fresh reference server to compare with).
	mutates bool
	args    string
	// want is the expected [result.summary].
	want string
	// ref lists the features a reference server must declare to behave as
	// FIXTURES.md says for this operation, separated by commas. The result
	// document of the client against go-modbus is compared with the one
	// against such a server. specOnly instead of a list: the fixture rules
	// do not cover the request and the reference servers differ, so the
	// outcome is checked against the specification alone.
	ref string
	// check looks at what the summary does not show: the values in the
	// response and the state of the device afterwards.
	check func(t *testing.T, fx *fixture, r *result, d *device)
}

const specOnly = "spec-only"

// name is the scenario as a subtest name: long value lists are cut.
func (sc scenario) name() string {
	s := strings.ReplaceAll(sc.args, " ", "_")
	if len(s) > 90 {
		s = fmt.Sprintf("%s..._%d_chars", s[:80], len(s))
	}
	return s
}

func (sc scenario) operation() string { return strings.Fields(sc.args)[0] }

// needs returns the features a reference client must declare to run the
// scenario.
func (sc scenario) needs() []string {
	var out []string
	switch sc.operation() {
	case "mask-write-register":
		out = append(out, featClientMaskWrite)
	case "read-write-multiple-registers":
		out = append(out, featClientReadWriteMultiple)
	case "read-device-identification":
		out = append(out, featClientDeviceIdentification)
	case "raw":
		out = append(out, featClientRaw)
	}
	if strings.Contains(sc.args, "--repeat") {
		out = append(out, featClientRepeat)
	}
	return out
}

func (sc scenario) refFeatures() []string {
	if sc.ref == "" {
		return nil
	}
	return strings.Split(sc.ref, ",")
}

// flag returns the value of a numeric flag of the scenario, or def.
func (sc scenario) flag(name string, def int) int {
	f := strings.Fields(sc.args)
	for i := 0; i+1 < len(f); i++ {
		if f[i] == name {
			n, err := strconv.ParseInt(f[i+1], 0, 32)
			if err != nil {
				panic(fmt.Sprintf("scenario %q: %s is not a number", sc.args, name))
			}
			return int(n)
		}
	}
	return def
}

// csv renders n values as the argument of --values.
func csv(n int, value func(i int) int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = strconv.Itoa(value(i))
	}
	return strings.Join(parts, ",")
}

// Values written by the scenarios that write the largest request there is.
func bulkRegister(i int) int { return (i*7919 + 5) % 65536 }
func bulkCoil(i int) int     { return (i / 3) % 2 }

// holdingIs checks that the holding registers are the fixture's with the
// given registers replaced: what was written arrived, and nothing else
// changed.
func holdingIs(addr int, values ...int) func(*testing.T, *fixture, *result, *device) {
	return func(t *testing.T, fx *fixture, _ *result, d *device) {
		t.Helper()
		want := append([]uint16(nil), fx.HoldingRegisters.Values...)
		for i, v := range values {
			want[addr-int(fx.HoldingRegisters.Address)+i] = uint16(v)
		}
		if got := d.holdingNow(); !reflect.DeepEqual(got, want) {
			t.Errorf("holding registers after the operation: %s", diffRegisters(got, want))
		}
		if got := d.coilsNow(); !reflect.DeepEqual(got, fx.Coils.Values) {
			t.Errorf("coils changed: %s", diffBits(got, fx.Coils.Values))
		}
	}
}

// coilsAre is holdingIs for coils.
func coilsAre(addr int, values ...int) func(*testing.T, *fixture, *result, *device) {
	return func(t *testing.T, fx *fixture, _ *result, d *device) {
		t.Helper()
		want := append([]bool(nil), fx.Coils.Values...)
		for i, v := range values {
			want[addr-int(fx.Coils.Address)+i] = v != 0
		}
		if got := d.coilsNow(); !reflect.DeepEqual(got, want) {
			t.Errorf("coils after the operation: %s", diffBits(got, want))
		}
		if got := d.holdingNow(); !reflect.DeepEqual(got, fx.HoldingRegisters.Values) {
			t.Errorf("holding registers changed: %s", diffRegisters(got, fx.HoldingRegisters.Values))
		}
	}
}

func ints(n int, value func(i int) int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = value(i)
	}
	return out
}

// all runs several checks.
func all(checks ...func(*testing.T, *fixture, *result, *device)) func(*testing.T, *fixture, *result, *device) {
	return func(t *testing.T, fx *fixture, r *result, d *device) {
		t.Helper()
		for _, c := range checks {
			c(t, fx, r, d)
		}
	}
}

// registersAre checks the registers of the response.
func registersAre(want ...uint16) func(*testing.T, *fixture, *result, *device) {
	return func(t *testing.T, _ *fixture, r *result, _ *device) {
		t.Helper()
		if r.Response == nil || !reflect.DeepEqual(r.Response.Registers, want) {
			t.Errorf("registers in the response: got %v, want %v", r.raw["response"], want)
		}
	}
}

// rawIs checks the response PDU of a raw operation, given without the
// function code as hex; blanks are ignored.
func rawIs(want string) func(*testing.T, *fixture, *result, *device) {
	want = strings.ReplaceAll(want, " ", "")
	return func(t *testing.T, _ *fixture, r *result, _ *device) {
		t.Helper()
		if r.Response == nil || r.Response.Data == nil || *r.Response.Data != want {
			t.Errorf("response PDU: got %v, want data %s", r.raw["response"], want)
		}
	}
}

// oneConnection checks that the device saw n requests, all on one connection.
func oneConnection(n int) func(*testing.T, *fixture, *result, *device) {
	return func(t *testing.T, _ *fixture, _ *result, d *device) {
		t.Helper()
		reqs := d.takeRequests()
		peers := map[string]bool{}
		for _, r := range reqs {
			peers[r.peer] = true
		}
		if len(reqs) != n || len(peers) != 1 {
			t.Errorf("the device saw %d request(s) on %d connection(s), want %d on 1", len(reqs), len(peers), n)
		}
	}
}

// identificationIs checks the objects and the conformity level of a Read
// Device Identification response against the fixture. go-modbus serves
// individual access as well, so the level carries the bit for it (0x80).
func identificationIs(category int) func(*testing.T, *fixture, *result, *device) {
	return func(t *testing.T, fx *fixture, r *result, _ *device) {
		t.Helper()
		if r.Response == nil {
			t.Errorf("no response")
			return
		}
		want := fx.objects(modbusCategory(category))
		if len(r.Response.Objects) != len(want) {
			t.Errorf("objects: got %v, want %v", r.Response.Objects, want)
			return
		}
		for i, o := range r.Response.Objects {
			if o.ID != int(want[i].ID) || o.Value != want[i].Value {
				t.Errorf("object %d: got %d=%q, want %d=%q", i, o.ID, o.Value, want[i].ID, want[i].Value)
			}
		}
		if level := fx.conformity() | 0x80; r.Response.ConformityLevel == nil || *r.Response.ConformityLevel != level {
			t.Errorf("conformity level: got %v, want 0x%02X", r.raw["response"], level)
		}
	}
}

// basicIdentificationPDU is the response PDU, without the function code, to
// FC43/14 for the basic category from object 0: MEI type, read code,
// conformity level, more follows, next object, number of objects, objects.
func basicIdentificationPDU(fx *fixture) string {
	objs := fx.objects(modbusCategory(1))
	pdu := []byte{0x0E, 0x01, byte(fx.conformity() | 0x80), 0x00, 0x00, byte(len(objs))}
	for _, o := range objs {
		pdu = append(append(pdu, o.ID, byte(len(o.Value))), o.Value...)
	}
	return hex.EncodeToString(pdu)
}

const (
	okRead       = "exit=0 ok=true error=- exc=- fc=%d n=%d req=1 res=1"
	failedWith   = "exit=1 ok=false error=exception exc=%d/%d fc=- n=0 req=1 res=0"
	okWrite      = "exit=0 ok=true error=- exc=- fc=%d n=0 req=1 res=1"
	okRepeat     = "exit=0 ok=true error=- exc=- fc=%d n=%d req=%d res=%d"
	illegalFunc  = 1
	illegalAddr  = 2
	illegalValue = 3
	gatewayNoRsp = 11
)

func ok(fc, n int) string           { return fmt.Sprintf(okRead, fc, n) }
func written(fc int) string         { return fmt.Sprintf(okWrite, fc) }
func exception(fc, code int) string { return fmt.Sprintf(failedWith, fc+128, code) }

// The baseline fixture: coils 0..1999, discrete inputs 1000..2999, holding
// registers 0..255, input registers 65280..65535, unit identifier 1.
var scenarios = []scenario{
	// FC01
	{args: "read-coils --address 0 --quantity 1", want: ok(1, 1)},
	{args: "read-coils --address 1993 --quantity 7", want: ok(1, 7)},
	{args: "read-coils --address 3 --quantity 17", want: ok(1, 17)},
	{args: "read-coils --address 0 --quantity 2000", want: ok(1, 2000)},
	{args: "read-coils --address 1999 --quantity 2", want: exception(1, illegalAddr)},
	{args: "read-coils --address 2000 --quantity 1", want: exception(1, illegalAddr)},
	{args: "read-coils --address 65535 --quantity 1", want: exception(1, illegalAddr)},
	// FC02
	{args: "read-discrete-inputs --address 1000 --quantity 2000", want: ok(2, 2000)},
	{args: "read-discrete-inputs --address 2991 --quantity 9", want: ok(2, 9)},
	{args: "read-discrete-inputs --address 1000 --quantity 1", want: ok(2, 1)},
	{args: "read-discrete-inputs --address 999 --quantity 2", want: exception(2, illegalAddr)},
	{args: "read-discrete-inputs --address 0 --quantity 1", want: exception(2, illegalAddr)},
	{args: "read-discrete-inputs --address 2999 --quantity 2", want: exception(2, illegalAddr)},
	{args: "read-discrete-inputs --address 3000 --quantity 8", want: exception(2, illegalAddr)},
	// FC03
	{args: "read-holding-registers --address 0 --quantity 1", want: ok(3, 1)},
	{args: "read-holding-registers --address 0 --quantity 125", want: ok(3, 125)},
	{args: "read-holding-registers --address 131 --quantity 125", want: ok(3, 125)},
	{args: "read-holding-registers --address 255 --quantity 1", want: ok(3, 1)},
	{args: "read-holding-registers --address 200 --quantity 57", want: exception(3, illegalAddr)},
	{args: "read-holding-registers --address 256 --quantity 1", want: exception(3, illegalAddr)},
	{args: "read-holding-registers --address 65535 --quantity 1", want: exception(3, illegalAddr)},
	// FC04
	{args: "read-input-registers --address 65280 --quantity 125", want: ok(4, 125)},
	{args: "read-input-registers --address 65411 --quantity 125", want: ok(4, 125)},
	{args: "read-input-registers --address 65535 --quantity 1", want: ok(4, 1)},
	{args: "read-input-registers --address 0 --quantity 1", want: exception(4, illegalAddr)},
	{args: "read-input-registers --address 65279 --quantity 2", want: exception(4, illegalAddr)},
	// Another unit identifier: exception 11, 0 and 255 included.
	{args: "read-holding-registers --address 0 --quantity 1 --unit-id 2", want: exception(3, gatewayNoRsp), ref: featServerUnitIDFiltering},
	{args: "read-coils --address 0 --quantity 1 --unit-id 247", want: exception(1, gatewayNoRsp), ref: featServerUnitIDFiltering},
	{args: "read-holding-registers --address 0 --quantity 1 --unit-id 255", want: exception(3, gatewayNoRsp), ref: featServerUnitIDFiltering},
	{args: "read-input-registers --address 65280 --quantity 1 --unit-id 0", want: exception(4, gatewayNoRsp), ref: featServerUnitIDFiltering},
	{args: "write-single-register --address 10 --value 1 --unit-id 9", want: exception(6, gatewayNoRsp), ref: featServerUnitIDFiltering},
	{args: "write-multiple-coils --address 0 --values 1,1,1 --unit-id 9", want: exception(15, gatewayNoRsp), ref: featServerUnitIDFiltering},
	// Several requests on one connection.
	{args: "read-holding-registers --address 0 --quantity 10 --repeat 50", want: fmt.Sprintf(okRepeat, 3, 10, 50, 50), check: oneConnection(50)},
	{args: "read-coils --address 0 --quantity 64 --repeat 50", want: fmt.Sprintf(okRepeat, 1, 64, 50, 50), check: oneConnection(50)},
	{args: "read-input-registers --address 65411 --quantity 125 --repeat 1000", want: fmt.Sprintf(okRepeat, 4, 125, 1000, 1000), check: oneConnection(1000)},
	{args: "read-holding-registers --address 256 --quantity 1 --repeat 5", want: exception(3, illegalAddr), check: oneConnection(1)},
	// Writes outside the blocks: exception 2, and nothing is written (the
	// shared device is compared with the fixture at the end).
	{args: "write-single-coil --address 2000 --value true", want: exception(5, illegalAddr)},
	{args: "write-single-register --address 256 --value 1", want: exception(6, illegalAddr)},
	{args: "write-multiple-coils --address 1998 --values 1,0,1", want: exception(15, illegalAddr)},
	{args: "write-multiple-registers --address 254 --values 1,2,3", want: exception(16, illegalAddr)},
	{args: "mask-write-register --address 256 --and-mask 0 --or-mask 0xFFFF", want: exception(22, illegalAddr), ref: featServerMaskWrite},
	{args: "read-write-multiple-registers --read-address 250 --read-quantity 10 --write-address 40 --values 7,8", want: exception(23, illegalAddr), ref: featServerReadWriteMultiple},
	{args: "read-write-multiple-registers --read-address 0 --read-quantity 2 --write-address 255 --values 7,8", want: exception(23, illegalAddr), ref: featServerReadWriteMultiple},
	// Raw PDUs: a normal request, an unknown function code, quantities
	// outside the protocol's range (checked before the address).
	{args: "raw --function 3 --data 00000002", want: ok(3, 0), check: rawIs("04 0000 0001")},
	{args: "raw --function 65 --data 0102", want: exception(65, illegalFunc)},
	{args: "raw --function 100 --data 00", want: exception(100, illegalFunc)},
	{args: "raw --function 3 --data 00000000", want: exception(3, illegalValue), ref: featServerQuantityCheck},
	{args: "raw --function 3 --data 0000007e", want: exception(3, illegalValue), ref: featServerQuantityCheck},
	{args: "raw --function 1 --data 000007d1", want: exception(1, illegalValue), ref: featServerQuantityCheck},
	{args: "raw --function 2 --data 03e80000", want: exception(2, illegalValue), ref: featServerQuantityCheck},
	{args: "raw --function 4 --data 00000000", want: exception(4, illegalValue), ref: featServerQuantityCheck},
	{args: "raw --function 4 --data ffffffff", want: exception(4, illegalValue), ref: featServerQuantityCheck},
	// Read Device Identification.
	{args: "read-device-identification", want: ok(43, 3), ref: featServerDeviceIdentification, check: identificationIs(1)},
	{args: "read-device-identification --category regular", want: ok(43, 7), ref: featServerDeviceIdentification, check: identificationIs(2)},
	{args: "read-device-identification --category extended", want: ok(43, 7), ref: featServerDeviceIdentification, check: identificationIs(3)},
	{args: "read-device-identification --unit-id 2", want: exception(43, gatewayNoRsp), ref: featServerDeviceIdentification + "," + featServerUnitIDFiltering},

	// Writes, each against a fresh device.
	{mutates: true, args: "write-single-coil --address 5 --value true", want: written(5), check: coilsAre(5, 1)},
	{mutates: true, args: "write-single-coil --address 1999 --value false", want: written(5), check: coilsAre(1999, 0)},
	{mutates: true, args: "write-single-coil --address 0 --value true", want: written(5), check: coilsAre(0, 1)},
	{mutates: true, args: "write-single-register --address 10 --value 4660", want: written(6), check: holdingIs(10, 4660)},
	{mutates: true, args: "write-single-register --address 255 --value 65535", want: written(6), check: holdingIs(255, 65535)},
	{mutates: true, args: "write-single-register --address 4 --value 0", want: written(6), check: holdingIs(4, 0)},
	{mutates: true, args: "write-multiple-coils --address 100 --values 1,0,1,1,0,0,1,0,1", want: written(15), check: coilsAre(100, 1, 0, 1, 1, 0, 0, 1, 0, 1)},
	{mutates: true, args: "write-multiple-coils --address 1999 --values 0", want: written(15), check: coilsAre(1999, 0)},
	{mutates: true, args: "write-multiple-coils --address 32 --values " + csv(1968, bulkCoil), want: written(15), check: coilsAre(32, ints(1968, bulkCoil)...)},
	{mutates: true, args: "write-multiple-registers --address 20 --values 1,2,65535", want: written(16), check: holdingIs(20, 1, 2, 65535)},
	{mutates: true, args: "write-multiple-registers --address 133 --values " + csv(123, bulkRegister), want: written(16), check: holdingIs(133, ints(123, bulkRegister)...)},
	// Register 5 holds 0x1234: (0x1234 AND 0x00F2) OR (0x0025 AND NOT 0x00F2) = 0x0035.
	{mutates: true, args: "mask-write-register --address 5 --and-mask 0x00F2 --or-mask 0x0025", want: written(22), ref: featServerMaskWrite, check: holdingIs(5, 0x0035)},
	{mutates: true, args: "mask-write-register --address 255 --and-mask 0xFFFF --or-mask 0", want: written(22), ref: featServerMaskWrite, check: holdingIs(0)},
	// FC23 writes before it reads: registers 30..33 are read after 31 and
	// 32 were written.
	{mutates: true, args: "read-write-multiple-registers --read-address 30 --read-quantity 4 --write-address 31 --values 7,8", want: ok(23, 4), ref: featServerReadWriteMultiple,
		check: all(holdingIs(31, 7, 8), registersAre(uint16(30*257+13), 7, 8, uint16(33*257+13)))},
	{mutates: true, args: "read-write-multiple-registers --read-address 0 --read-quantity 125 --write-address 135 --values " + csv(121, bulkRegister), want: ok(23, 125), ref: featServerReadWriteMultiple,
		check: holdingIs(135, ints(121, bulkRegister)...)},

	// Outside the fixture rules: requests the specification answers with
	// exception 3 and on which the reference servers differ
	// (COMPATIBILITY.md § Findings). go-modbus is checked against the
	// specification alone.
	//
	// FC05 with a value other than 0x0000 and 0xFF00 (spec 6.5).
	{args: "raw --function 5 --data 00051234", want: exception(5, illegalValue), ref: specOnly},
	// FC15 with quantity 0 and with 1969 coils (spec 6.11).
	{args: "raw --function 15 --data 000000000100", want: exception(15, illegalValue), ref: specOnly},
	{args: "raw --function 15 --data 000007b1f7" + strings.Repeat("00", 247), want: exception(15, illegalValue), ref: specOnly},
	// FC16 with quantity 0 (spec 6.12).
	{args: "raw --function 16 --data 00000000020000", want: exception(16, illegalValue), ref: specOnly},
	// A byte count that is not the one the quantity asks for (spec figures
	// 21, 22 and 27: exception 3): too large and too small, with as much
	// data as the byte count says. go-modbus closed the connection on
	// these before v1.3.0; libmodbus v3.2.0 and tokio-modbus 0.17.0 serve
	// the FC15 request with one byte too many.
	{args: "raw --function 15 --data 00000008020000", want: exception(15, illegalValue), ref: specOnly},
	{args: "raw --function 15 --data 000000090100", want: exception(15, illegalValue), ref: specOnly},
	{args: "raw --function 16 --data 0000000103000100", want: exception(16, illegalValue), ref: specOnly},
	{args: "raw --function 16 --data 00000002020001", want: exception(16, illegalValue), ref: specOnly},
	{args: "raw --function 23 --data 00000001000000010400010002", want: exception(23, illegalValue), ref: specOnly},
	// FC23 with read quantity 0 and 126, and write quantity 0 (spec 6.17).
	{args: "raw --function 23 --data 0000000000000001020001", want: exception(23, illegalValue), ref: specOnly},
	{args: "raw --function 23 --data 0000007e00000001020001", want: exception(23, illegalValue), ref: specOnly},
	{args: "raw --function 23 --data 0000000100000000020001", want: exception(23, illegalValue), ref: specOnly},
	// FC43 with an MEI type the device does not have (spec 6.19: the
	// function code is known, the encapsulated interface is not).
	{args: "raw --function 43 --data 0d0000", want: exception(43, illegalFunc), ref: specOnly},
	// FC43/14 with read code 0 and 5 (spec 6.21: exception 3).
	{args: "raw --function 43 --data 0e0000", want: exception(43, illegalValue), ref: specOnly},
	{args: "raw --function 43 --data 0e0500", want: exception(43, illegalValue), ref: specOnly},
	// FC43/14 individual access to an object the device does not have
	// (spec 6.21: exception 2).
	{args: "raw --function 43 --data 0e0407", want: exception(43, illegalAddr), ref: specOnly},

	// Where a go-modbus device cannot follow FIXTURES.md rule 1 (any request
	// for another unit identifier is answered with exception 11): the
	// server checks the function code and the quantity before it hands the
	// request, and with it the unit identifier, to the RequestHandler. A
	// request for another unit that fails those checks gets their
	// exception, not the handler's. The specification does not rule on
	// this; the reference servers all answer 11. Recorded here so that a
	// change is noticed.
	{args: "raw --function 65 --data 0102 --unit-id 2", want: exception(65, illegalFunc), ref: specOnly},
	{args: "raw --function 3 --data 00000000 --unit-id 2", want: exception(3, illegalValue), ref: specOnly},
}

// rawIdentification are scenarios that need the fixture to state what they
// expect: FC43/14 as a raw PDU, which also exercises the go-modbus server
// with the clients that have no operation for it.
func rawIdentification(fx *fixture) []scenario {
	return []scenario{
		{args: "raw --function 43 --data 0e0100", want: ok(43, 0), ref: specOnly, check: rawIs(basicIdentificationPDU(fx))},
		// Stream access from an object the device does not have starts
		// at object 0 (spec 6.21). PyModbus 3.15.0 answers with no object.
		{args: "raw --function 43 --data 0e0180", want: ok(43, 0), ref: specOnly, check: rawIs(basicIdentificationPDU(fx))},
		// Individual access to object 1 (ProductCode).
		{args: "raw --function 43 --data 0e0401", want: ok(43, 0), ref: specOnly,
			check: rawIs(hex.EncodeToString(append([]byte{0x0E, 0x04, byte(fx.conformity() | 0x80), 0, 0, 1, 1, byte(len(fx.objects(1)[1].Value))}, fx.objects(1)[1].Value...)))},
	}
}

// reference returns the server a scenario's outcome is compared with: the
// one of the client's own stack, or, for what that stack does not serve as
// FIXTURES.md says, another reference that does. It returns nil when there
// is none, or when the scenario is checked against the specification alone.
func reference(t *testing.T, a adapter, sc scenario) *adapter {
	if sc.ref == specOnly {
		return nil
	}
	if a.hasAll(t, sc.refFeatures()...) {
		return &a
	}
	for _, other := range adapters(t) {
		if other.hasAll(t, sc.refFeatures()...) {
			return &other
		}
	}
	return nil
}

// skipFor names the scenarios a reference client cannot run although it
// declares the operation, and why.
func skipFor(a adapter, sc scenario) string {
	// libmodbus v3.2.0 frames a response by its function code, not by the
	// MBAP length, so its raw operation cannot read the response to a
	// function the library does not know: COMPATIBILITY.md § libmodbus.
	if a.name == libmodbus && sc.operation() == "raw" && sc.flag("--function", 0) == 43 &&
		strings.HasPrefix(sc.want, "exit=0") {
		return "libmodbus cannot read an FC43 response: it frames responses by function code"
	}
	return ""
}

// TestServerScenarios drives a go-modbus device with every reference client
// and checks, for each operation: that the outcome is what the fixture rules
// and the specification prescribe, that the values and the state of the
// device are right, and that the client cannot tell the go-modbus device from
// a reference server. The last check compares the complete result documents.
func TestServerScenarios(t *testing.T) {
	forEachAdapter(t, func(t *testing.T, a adapter) {
		logReference(t, a)
		fx := loadFixture(t, a)
		list := append(append([]scenario(nil), scenarios...), rawIdentification(fx)...)
		sharedDevice := startDevice(t, fx)
		// The reference servers that the scenarios which leave the device
		// alone are compared with, by adapter.
		shared := map[string]*refServer{}
		for _, sc := range list {
			if sc.mutates || !a.hasAll(t, sc.needs()...) || skipFor(a, sc) != "" {
				continue
			}
			if other := reference(t, a, sc); other != nil && shared[other.name] == nil {
				shared[other.name] = startServer(t, *other)
			}
		}

		for _, sc := range list {
			t.Run(sc.name(), func(t *testing.T) {
				a.need(t, sc.needs()...)
				if why := skipFor(a, sc); why != "" {
					t.Skip(why)
				}
				dev := sharedDevice
				if sc.mutates {
					dev = startDevice(t, fx)
				}
				dev.takeRequests()
				args := strings.Fields(sc.args)
				got := runClient(t, a, dev.Addr, args...)
				if s := got.summary(); s != sc.want {
					t.Fatalf("%s client against go-modbus:\n got  %s\n want %s\n document: %s\n stderr: %s",
						a.name, s, sc.want, clip(got.normalized()), clip(got.stderr))
				}
				checkDocument(t, a, sc, got)
				checkReadValues(t, fx, sc, got)
				if sc.check != nil {
					sc.check(t, fx, got, dev)
				}

				other := reference(t, a, sc)
				if other == nil {
					if sc.ref != specOnly {
						t.Logf("no reference serves this: the outcome is checked, the documents are not compared")
					}
					return
				}
				ref := shared[other.name]
				if sc.mutates {
					ref = startServer(t, *other)
				}
				want := runClientAgainst(t, a, ref, args...)
				if s := want.summary(); s != sc.want {
					t.Fatalf("%s client against the %s server (the comparison is void):\n got  %s\n want %s\n document: %s",
						a.name, other.name, s, sc.want, clip(want.normalized()))
				}
				if g, w := normalizedFor(sc, got), normalizedFor(sc, want); g != w {
					t.Errorf("%s client sees a difference between go-modbus and the %s server.\n"+
						"--- go-modbus ---\n%s\n--- %s server ---\n%s", a.name, other.name, clip(g), other.name, clip(w))
				}
			})
		}

		// Nothing but the scenarios that are marked as such changed the
		// device.
		if got := sharedDevice.holdingNow(); !reflect.DeepEqual(got, fx.HoldingRegisters.Values) {
			t.Errorf("a scenario that should not write changed the holding registers: %s", diffRegisters(got, fx.HoldingRegisters.Values))
		}
		if got := sharedDevice.coilsNow(); !reflect.DeepEqual(got, fx.Coils.Values) {
			t.Errorf("a scenario that should not write changed the coils: %s", diffBits(got, fx.Coils.Values))
		}
	})
}

// normalizedFor is [result.normalized] without what a go-modbus device and a
// reference server may legitimately answer differently.
func normalizedFor(sc scenario, r *result) string {
	if sc.operation() != "read-device-identification" || r.Response == nil {
		return r.normalized()
	}
	// The conformity level says whether the server offers individual
	// access, which is the server's choice: go-modbus does (0x8x). The
	// level itself is checked by identificationIs.
	c := *r
	c.raw = map[string]any{}
	for k, v := range r.raw {
		c.raw[k] = v
	}
	if resp, ok := r.raw["response"].(map[string]any); ok {
		cp := map[string]any{}
		for k, v := range resp {
			cp[k] = v
		}
		if level, ok := cp["conformityLevel"].(float64); ok {
			cp["conformityLevel"] = int(level) &^ 0x80
		}
		c.raw["response"] = cp
	}
	return c.normalized()
}

// checkDocument checks the parts of the result document that are the same
// for every operation.
func checkDocument(t *testing.T, a adapter, sc scenario, r *result) {
	t.Helper()
	if r.SchemaVersion != "1.0" || r.Adapter != a.name || r.Operation != sc.operation() {
		t.Errorf("document is schemaVersion=%q adapter=%q operation=%q", r.SchemaVersion, r.Adapter, r.Operation)
	}
	if !r.Connected {
		t.Errorf("the client did not connect: %s", clip(r.stderr))
	}
	if want := sc.flag("--unit-id", 1); r.UnitID != want {
		t.Errorf("unitId = %d, want %d", r.UnitID, want)
	}
	if r.OK != (r.Error == nil) || r.OK != (r.exit == 0) || (r.OK && r.Exception != nil) {
		t.Errorf("ok=%v, error=%v, exception=%v and exit %d contradict each other", r.OK, r.Error, r.Exception, r.exit)
	}
}

// checkReadValues compares what a read returned with the fixture, and what a
// write response echoes with the request.
func checkReadValues(t *testing.T, fx *fixture, sc scenario, r *result) {
	t.Helper()
	if r.Response == nil {
		return
	}
	addr, qty := sc.flag("--address", 0), sc.flag("--quantity", 0)
	switch sc.operation() {
	case "read-coils":
		wantBits(t, r.Response.Bits, fx.Coils, addr, qty)
	case "read-discrete-inputs":
		wantBits(t, r.Response.Bits, fx.DiscreteInputs, addr, qty)
	case "read-holding-registers":
		wantRegisters(t, r.Response.Registers, fx.HoldingRegisters, addr, qty)
	case "read-input-registers":
		wantRegisters(t, r.Response.Registers, fx.InputRegisters, addr, qty)
	case "write-single-coil", "write-single-register", "write-multiple-coils", "write-multiple-registers", "mask-write-register":
		if r.Response.Address == nil || *r.Response.Address != addr {
			t.Errorf("the response echoes address %v, want %d", r.raw["response"], addr)
		}
	}
}

func wantBits(t *testing.T, got []bool, b *bitBlock, addr, qty int) {
	t.Helper()
	at := addr - int(b.Address)
	if want := b.Values[at : at+qty]; !reflect.DeepEqual(got, want) {
		t.Errorf("%d bits from %d: %s", qty, addr, diffBits(got, want))
	}
}

func wantRegisters(t *testing.T, got []uint16, b *registerBlock, addr, qty int) {
	t.Helper()
	at := addr - int(b.Address)
	if want := b.Values[at : at+qty]; !reflect.DeepEqual(got, want) {
		t.Errorf("%d registers from %d: %s", qty, addr, diffRegisters(got, want))
	}
}

// diffBits and diffRegisters say where two tables differ, without printing
// thousands of values.
func diffBits(got, want []bool) string {
	if len(got) != len(want) {
		return fmt.Sprintf("got %d values, want %d", len(got), len(want))
	}
	var at []string
	for i := range got {
		if got[i] != want[i] && len(at) < 12 {
			at = append(at, fmt.Sprintf("[%d]=%v", i, got[i]))
		}
	}
	return "differs from what is expected at " + strings.Join(at, " ") + " (first 12 at most)"
}

func diffRegisters(got, want []uint16) string {
	if len(got) != len(want) {
		return fmt.Sprintf("got %d values, want %d", len(got), len(want))
	}
	var at []string
	for i := range got {
		if got[i] != want[i] && len(at) < 12 {
			at = append(at, fmt.Sprintf("[%d]=%d want %d", i, got[i], want[i]))
		}
	}
	return "differs from what is expected at " + strings.Join(at, ", ") + " (first 12 at most)"
}

// clip shortens a long text for a failure message.
func clip(s string) string {
	if len(s) > 1500 {
		return s[:1500] + fmt.Sprintf("... (%d more bytes)", len(s)-1500)
	}
	return s
}
