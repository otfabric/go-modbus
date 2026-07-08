// SPDX-License-Identifier: MIT

package modbus

import (
	"errors"
	"testing"
)

func TestFunctionCode_BaseAndIsException(t *testing.T) {
	fc := FCReadHoldingRegisters
	if fc.IsException() {
		t.Error("FC03 should not be exception")
	}
	if got := fc.Base(); got != fc {
		t.Errorf("Base() = %v, want %v", got, fc)
	}
	ex := FunctionCode(0x83) // exception
	if !ex.IsException() {
		t.Error("0x83 should be exception")
	}
	if got := ex.Base(); got != FCReadHoldingRegisters {
		t.Errorf("Base() = 0x%02x, want 0x03", got)
	}
}

func TestFunctionCode_String(t *testing.T) {
	if s := FCReadHoldingRegisters.String(); s != "Read Holding Registers (0x03)" {
		t.Errorf("String() = %q", s)
	}
	if s := FunctionCode(0x83).String(); s != "Read Holding Registers Exception (0x83)" {
		t.Errorf("exception String() = %q", s)
	}
	if s := FunctionCode(0xFF).String(); s != "Unknown Function (0xFF)" {
		t.Errorf("unknown String() = %q", s)
	}
}

func TestFunctionCode_Valid(t *testing.T) {
	if !FCReadHoldingRegisters.Valid() {
		t.Error("FC03 should be valid")
	}
	if FunctionCode(0x99).Valid() {
		t.Error("0x99 should not be valid")
	}
}

func TestKnownFunctionCodes(t *testing.T) {
	codes := KnownFunctionCodes()
	if len(codes) == 0 {
		t.Fatal("KnownFunctionCodes() returned empty")
	}
	seen := make(map[FunctionCode]bool)
	for _, fc := range codes {
		if seen[fc] {
			t.Errorf("duplicate function code %v", fc)
		}
		seen[fc] = true
		if !fc.Valid() {
			t.Errorf("code %v not valid", fc)
		}
	}
}

func TestParseFunctionCode(t *testing.T) {
	fc, err := ParseFunctionCode(0x03)
	if err != nil {
		t.Fatal(err)
	}
	if fc != FCReadHoldingRegisters {
		t.Errorf("got %v", fc)
	}
	_, err = ParseFunctionCode(0x99)
	if err == nil {
		t.Fatal("expected error for invalid function code")
	}
}

func TestExceptionCode_String(t *testing.T) {
	if s := exIllegalDataAddress.String(); s != "Illegal Data Address (0x02)" {
		t.Errorf("String() = %q", s)
	}
	if s := ExceptionCode(0x99).String(); s != "Unknown Exception (0x99)" {
		t.Errorf("unknown String() = %q", s)
	}
}

func TestExceptionCode_ToError(t *testing.T) {
	tests := []struct {
		ec   ExceptionCode
		want error
	}{
		{exIllegalFunction, ErrIllegalFunction},
		{exIllegalDataAddress, ErrIllegalDataAddress},
		{exIllegalDataValue, ErrIllegalDataValue},
		{exServerDeviceFailure, ErrServerDeviceFailure},
		{exAcknowledge, ErrAcknowledge},
		{exServerDeviceBusy, ErrServerDeviceBusy},
		{exMemoryParityError, ErrMemoryParityError},
		{exGWPathUnavailable, ErrGWPathUnavailable},
		{exGWTargetFailedToRespond, ErrGWTargetFailedToRespond},
	}
	for _, tt := range tests {
		got := tt.ec.ToError()
		if !errors.Is(got, tt.want) {
			t.Errorf("ToError(%v): want %v, got %v", tt.ec, tt.want, got)
		}
	}
	// unknown exception returns wrapped error
	got := ExceptionCode(0x99).ToError()
	if got == nil {
		t.Fatal("unknown exception should return non-nil error")
	}
	if errors.Is(got, ErrIllegalFunction) {
		t.Error("unknown should not be ErrIllegalFunction")
	}
}

func TestMapExceptionCodeToError(t *testing.T) {
	err := mapExceptionCodeToError(FCReadHoldingRegisters, exIllegalDataAddress)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrIllegalDataAddress) {
		t.Errorf("want ErrIllegalDataAddress, got %v", err)
	}
	var exErr *ExceptionError
	if errors.As(err, &exErr) {
		if exErr.FunctionCode != FCReadHoldingRegisters || exErr.ExceptionCode != exIllegalDataAddress {
			t.Errorf("ExceptionError: fc=%v ec=%v", exErr.FunctionCode, exErr.ExceptionCode)
		}
		// Cover ExceptionError.Error(), Unwrap(), Is()
		if exErr.Error() == "" {
			t.Error("ExceptionError.Error() should not be empty")
		}
		if exErr.Unwrap() != ErrIllegalDataAddress {
			t.Error("Unwrap should return sentinel")
		}
		if !exErr.Is(ErrIllegalDataAddress) {
			t.Error("Is(ErrIllegalDataAddress) should be true")
		}
	}
}

func TestMapErrorToExceptionCode(t *testing.T) {
	if c := mapErrorToExceptionCode(ErrIllegalDataAddress); c != exIllegalDataAddress {
		t.Errorf("got %v", c)
	}
	if c := mapErrorToExceptionCode(ErrIllegalFunction); c != exIllegalFunction {
		t.Errorf("got %v", c)
	}
	// Unknown errors map to exServerDeviceFailure per implementation default.
	if c := mapErrorToExceptionCode(errors.New("other")); c != exServerDeviceFailure {
		t.Errorf("unknown error: got %v", c)
	}
}
