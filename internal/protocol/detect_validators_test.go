// SPDX-License-Identifier: MIT

package protocol

import "testing"

// TestDetectionProbeValidators_NormalResponses checks, for every probe, which
// non-exception responses count as proof that the function code is supported.
func TestDetectionProbeValidators_NormalResponses(t *testing.T) {
	type resp struct {
		name    string
		fc      FunctionCode // zero means "same as the request"
		payload []byte
		want    bool
	}
	cases := map[FunctionCode][]resp{
		// FC08 is only accepted through an exception: echoes are too easy to fake.
		FCDiagnostics: {
			{name: "echo", payload: []byte{0x00, 0x00, 0x12, 0x34}, want: true},
			{name: "echo with other data", payload: []byte{0x00, 0x00, 0x43, 0x21}, want: false},
			{name: "other sub-function", payload: []byte{0x00, 0x01, 0x12, 0x34}, want: false},
			{name: "too short", payload: []byte{0x00, 0x00, 0x12}, want: false},
			{name: "wrong fc", fc: FCReadCoils, payload: []byte{0x00, 0x00, 0x12, 0x34}, want: false},
		},
		FCEncapsulatedInterface: {
			{name: "minimal device id header", payload: []byte{0x0E, 0x01, 0x01, 0x00, 0x00, 0x00}, want: true},
			{name: "too short", payload: []byte{0x0E, 0x01, 0x01, 0x00, 0x00}, want: false},
			{name: "wrong fc", fc: FCReadCoils, payload: []byte{0x0E, 0x01, 0x01, 0x00, 0x00, 0x00}, want: false},
		},
		FCReadHoldingRegisters: {
			{name: "one register", payload: []byte{2, 0xAB, 0xCD}, want: true},
			{name: "wrong byte count", payload: []byte{4, 0xAB, 0xCD}, want: false},
			{name: "wrong length", payload: []byte{2, 0xAB}, want: false},
			{name: "wrong fc", fc: FCReadInputRegisters, payload: []byte{2, 0xAB, 0xCD}, want: false},
		},
		FCReadInputRegisters: {
			{name: "one register", payload: []byte{2, 0xAB, 0xCD}, want: true},
			{name: "wrong byte count", payload: []byte{1, 0xAB, 0xCD}, want: false},
			{name: "wrong length", payload: []byte{2, 0xAB, 0xCD, 0xEF}, want: false},
			{name: "wrong fc", fc: FCReadHoldingRegisters, payload: []byte{2, 0xAB, 0xCD}, want: false},
		},
		FCReadCoils: {
			{name: "one coil", payload: []byte{1, 0x01}, want: true},
			{name: "wrong byte count", payload: []byte{2, 0x01}, want: false},
			{name: "wrong length", payload: []byte{1}, want: false},
			{name: "wrong fc", fc: FCReadDiscreteInputs, payload: []byte{1, 0x01}, want: false},
		},
		FCReadDiscreteInputs: {
			{name: "one input", payload: []byte{1, 0x00}, want: true},
			{name: "wrong byte count", payload: []byte{0, 0x00}, want: false},
			{name: "wrong length", payload: []byte{1, 0x00, 0x00}, want: false},
			{name: "wrong fc", fc: FCReadCoils, payload: []byte{1, 0x00}, want: false},
		},
		FCReportServerID: {
			{name: "id and run indicator", payload: []byte{2, 0x42, 0xFF}, want: true},
			{name: "longer id", payload: []byte{4, 'a', 'b', 'c', 0xFF}, want: true},
			{name: "byte count below two", payload: []byte{1, 0x42}, want: false},
			{name: "byte count mismatch", payload: []byte{3, 0x42, 0xFF}, want: false},
			{name: "too short", payload: []byte{0}, want: false},
			{name: "wrong fc", fc: FCReadCoils, payload: []byte{2, 0x42, 0xFF}, want: false},
		},
		FCReadFIFOQueue: {
			{name: "empty queue", payload: []byte{0x00, 0x02, 0x00, 0x00}, want: true},
			{name: "too short", payload: []byte{0x00, 0x02, 0x00}, want: false},
			{name: "wrong fc", fc: FCReadCoils, payload: []byte{0x00, 0x02, 0x00, 0x00}, want: false},
		},
		FCReadFileRecord: {
			{name: "one record", payload: []byte{3, 0x02, 0x06, 0x00}, want: true},
			{name: "byte count not three", payload: []byte{4, 0x03, 0x06, 0x00, 0x00}, want: false},
			{name: "byte count mismatch", payload: []byte{3, 0x02, 0x06, 0x00, 0x00}, want: false},
			{name: "too short", payload: []byte{2, 0x01, 0x06}, want: false},
			{name: "wrong fc", fc: FCWriteFileRecord, payload: []byte{3, 0x02, 0x06, 0x00}, want: false},
		},
	}

	probes := AllDetectionProbes()
	if len(probes) != len(cases) {
		t.Fatalf("probe table has %d entries, test covers %d", len(probes), len(cases))
	}
	for _, p := range probes {
		tcs, ok := cases[p.FC]
		if !ok {
			t.Errorf("no test cases for probe %v", p.FC)
			continue
		}
		for _, tc := range tcs {
			t.Run(p.FC.String()+"/"+tc.name, func(t *testing.T) {
				fc := tc.fc
				if fc == 0 {
					fc = p.FC
				}
				if got := p.Validate(p.FC, Response{FunctionCode: fc, Payload: tc.payload}); got != tc.want {
					t.Errorf("Validate(fc=%v, payload=% X) = %v, want %v", fc, tc.payload, got, tc.want)
				}
			})
		}
		// An exception for a different function code never validates.
		other := Response{FunctionCode: FunctionCode(uint8(p.FC)^0x01) | 0x80, Payload: []byte{0x01}}
		if p.Validate(p.FC, other) {
			t.Errorf("%v: accepted an exception response for another function code", p.FC)
		}
	}
}
