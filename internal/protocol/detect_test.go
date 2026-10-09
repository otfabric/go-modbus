// SPDX-License-Identifier: MIT

package protocol

import "testing"

func TestAllDetectionProbes_ReturnsCopy(t *testing.T) {
	a := AllDetectionProbes()
	b := AllDetectionProbes()
	if len(a) == 0 {
		t.Fatal("expected at least one probe")
	}
	if &a[0] == &b[0] {
		t.Error("AllDetectionProbes should return a new slice each call")
	}
}

func TestAllDetectionProbes_HasExpectedFCs(t *testing.T) {
	probes := AllDetectionProbes()
	expected := []FunctionCode{
		FCDiagnostics, FCEncapsulatedInterface,
		FCReadHoldingRegisters, FCReadInputRegisters,
		FCReadCoils, FCReadDiscreteInputs,
		FCReportServerID, FCReadFIFOQueue, FCReadFileRecord,
	}
	got := make(map[FunctionCode]bool, len(probes))
	for _, p := range probes {
		got[p.FC] = true
	}
	for _, fc := range expected {
		if !got[fc] {
			t.Errorf("missing probe for FC 0x%02x", uint8(fc))
		}
	}
}

func TestGetProbeForFC_Known(t *testing.T) {
	p, ok := GetProbeForFC(FCReadHoldingRegisters)
	if !ok {
		t.Fatal("expected probe for FCReadHoldingRegisters")
	}
	if p.FC != FCReadHoldingRegisters {
		t.Errorf("got FC 0x%02x, want 0x%02x", uint8(p.FC), uint8(FCReadHoldingRegisters))
	}
}

func TestGetProbeForFC_Unknown(t *testing.T) {
	_, ok := GetProbeForFC(FCWriteSingleCoil)
	if ok {
		t.Error("FCWriteSingleCoil should not have a detection probe")
	}
}

func TestIsValidModbusException(t *testing.T) {
	tests := []struct {
		name  string
		reqFC FunctionCode
		res   Response
		want  bool
	}{
		{
			name:  "valid exception",
			reqFC: FCReadHoldingRegisters,
			res:   Response{FunctionCode: FunctionCode(0x83), Payload: []byte{0x02}},
			want:  true,
		},
		{
			name:  "normal response",
			reqFC: FCReadHoldingRegisters,
			res:   Response{FunctionCode: FCReadHoldingRegisters, Payload: []byte{0x02, 0x00, 0x01}},
			want:  false,
		},
		{
			name:  "wrong exception FC",
			reqFC: FCReadHoldingRegisters,
			res:   Response{FunctionCode: FunctionCode(0x84), Payload: []byte{0x02}},
			want:  false,
		},
		{
			name:  "exception code 0x00 out of range",
			reqFC: FCReadHoldingRegisters,
			res:   Response{FunctionCode: FunctionCode(0x83), Payload: []byte{0x00}},
			want:  false,
		},
		{
			name:  "exception code 0x0B valid",
			reqFC: FCReadHoldingRegisters,
			res:   Response{FunctionCode: FunctionCode(0x83), Payload: []byte{0x0b}},
			want:  true,
		},
		{
			name:  "exception code 0x0C out of range",
			reqFC: FCReadHoldingRegisters,
			res:   Response{FunctionCode: FunctionCode(0x83), Payload: []byte{0x0c}},
			want:  false,
		},
		{
			name:  "empty payload",
			reqFC: FCReadCoils,
			res:   Response{FunctionCode: FunctionCode(0x81), Payload: nil},
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidModbusException(tt.reqFC, tt.res); got != tt.want {
				t.Errorf("IsValidModbusException() = %v, want %v", got, tt.want)
			}
		})
	}
}

// An exception response counts as "function implemented" only when its code shows that the
// device recognised the function: Illegal Function and the gateway exceptions do not.
func TestDetectionProbeValidators(t *testing.T) {
	supporting := map[uint8]bool{0x02: true, 0x03: true, 0x04: true, 0x05: true, 0x06: true, 0x08: true}
	for code := uint8(0x00); code <= 0x0C; code++ {
		if got := ExceptionImpliesSupport(code); got != supporting[code] {
			t.Errorf("ExceptionImpliesSupport(0x%02X) = %v, want %v", code, got, supporting[code])
		}
	}
	for _, p := range AllDetectionProbes() {
		for code := uint8(0x01); code <= 0x0B; code++ {
			excRes := Response{
				FunctionCode: FunctionCode(uint8(p.FC) | 0x80),
				Payload:      []byte{code},
			}
			if got := p.Validate(p.FC, excRes); got != supporting[code] {
				t.Errorf("%v: Validate(exception 0x%02X) = %v, want %v", p.FC, code, got, supporting[code])
			}
		}
		// An exception to a different function code is never accepted.
		if p.Validate(p.FC, Response{FunctionCode: FunctionCode(uint8(p.FC)|0x80) ^ 0x01, Payload: []byte{0x02}}) {
			t.Errorf("%v: accepted an exception for another function code", p.FC)
		}
	}
}
