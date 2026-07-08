// SPDX-License-Identifier: MIT

package protocol

import (
	"strings"
	"testing"
)

func TestFunctionCode_IsException(t *testing.T) {
	if FCReadCoils.IsException() {
		t.Error("FCReadCoils should not be an exception")
	}
	exc := FunctionCode(0x81)
	if !exc.IsException() {
		t.Error("0x81 should be an exception")
	}
}

func TestFunctionCode_Base(t *testing.T) {
	exc := FunctionCode(0x83)
	if base := exc.Base(); base != FCReadHoldingRegisters {
		t.Errorf("Base() = 0x%02x, want 0x%02x", uint8(base), uint8(FCReadHoldingRegisters))
	}
}

func TestFunctionCode_Valid(t *testing.T) {
	if !FCReadCoils.Valid() {
		t.Error("FCReadCoils should be valid")
	}
	if FunctionCode(0x7F).Valid() {
		t.Error("0x7F should not be valid")
	}
}

func TestFunctionCode_String(t *testing.T) {
	s := FCReadCoils.String()
	if !strings.Contains(s, "Read Coils") {
		t.Errorf("String() = %q, want substring 'Read Coils'", s)
	}
	exc := FunctionCode(0x81)
	s = exc.String()
	if !strings.Contains(s, "Exception") {
		t.Errorf("String() = %q, want substring 'Exception'", s)
	}
	unknown := FunctionCode(0x7F)
	s = unknown.String()
	if !strings.Contains(s, "Unknown") {
		t.Errorf("String() = %q, want substring 'Unknown'", s)
	}
}

func TestKnownFunctionCodes(t *testing.T) {
	codes := KnownFunctionCodes()
	if len(codes) == 0 {
		t.Fatal("expected at least one known function code")
	}
	for _, fc := range codes {
		if !fc.Valid() {
			t.Errorf("KnownFunctionCodes includes invalid FC 0x%02x", uint8(fc))
		}
	}
}

func TestParseFunctionCode(t *testing.T) {
	fc, err := ParseFunctionCode(0x03)
	if err != nil {
		t.Fatal(err)
	}
	if fc != FCReadHoldingRegisters {
		t.Errorf("got 0x%02x, want 0x%02x", uint8(fc), uint8(FCReadHoldingRegisters))
	}

	fc, err = ParseFunctionCode(0x83)
	if err != nil {
		t.Fatal(err)
	}
	if fc != FunctionCode(0x83) {
		t.Errorf("got 0x%02x, want 0x83", uint8(fc))
	}

	_, err = ParseFunctionCode(0x7F)
	if err == nil {
		t.Error("expected error for invalid function code 0x7F")
	}
}
