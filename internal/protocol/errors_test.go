package protocol

import (
	"errors"
	"strings"
	"testing"
)

func TestProtocolError_Error(t *testing.T) {
	err := &ProtocolError{Op: "ReadRegisters", Reason: "invalid length"}
	got := err.Error()
	want := "modbus: protocol error in ReadRegisters: invalid length"
	if got != want {
		t.Errorf("ProtocolError.Error() = %q, want %q", got, want)
	}
}

func TestProtocolError_Unwrap(t *testing.T) {
	err := &ProtocolError{Op: "Parse", Reason: "bad frame"}
	if err.Unwrap() != ErrProtocolError {
		t.Errorf("ProtocolError.Unwrap() = %v, want ErrProtocolError", err.Unwrap())
	}
}

func TestNewProtocolError(t *testing.T) {
	err := NewProtocolError("Validate", "unit id out of range")
	var pe *ProtocolError
	if !errors.As(err, &pe) {
		t.Fatal("NewProtocolError should return *ProtocolError")
	}
	if pe.Op != "Validate" || pe.Reason != "unit id out of range" {
		t.Errorf("NewProtocolError created wrong error: Op=%q Reason=%q", pe.Op, pe.Reason)
	}
	if !errors.Is(err, ErrProtocolError) {
		t.Error("NewProtocolError should wrap ErrProtocolError")
	}
}

func TestParameterError_Error(t *testing.T) {
	err := &ParameterError{Method: "ReadHoldingRegisters", Param: "count", Reason: "must be positive"}
	got := err.Error()
	want := `modbus: ReadHoldingRegisters: parameter "count": must be positive`
	if got != want {
		t.Errorf("ParameterError.Error() = %q, want %q", got, want)
	}
}

func TestParameterError_Unwrap(t *testing.T) {
	err := &ParameterError{Method: "WriteCoils", Param: "address", Reason: "out of range"}
	if err.Unwrap() != ErrUnexpectedParameters {
		t.Errorf("ParameterError.Unwrap() = %v, want ErrUnexpectedParameters", err.Unwrap())
	}
}

func TestNewParameterError(t *testing.T) {
	err := NewParameterError("ReadCoils", "count", "exceeds max")
	var pe *ParameterError
	if !errors.As(err, &pe) {
		t.Fatal("NewParameterError should return *ParameterError")
	}
	if pe.Method != "ReadCoils" || pe.Param != "count" || pe.Reason != "exceeds max" {
		t.Errorf("NewParameterError created wrong error: Method=%q Param=%q Reason=%q", pe.Method, pe.Param, pe.Reason)
	}
	if !errors.Is(err, ErrUnexpectedParameters) {
		t.Error("NewParameterError should wrap ErrUnexpectedParameters")
	}
}

func TestExceptionError_Error(t *testing.T) {
	err := MapExceptionCodeToError(FCReadHoldingRegisters, ExIllegalFunction)
	got := err.Error()
	// Format: "Read Holding Registers (0x03): Illegal Function (0x01)"
	if !strings.Contains(got, "Read Holding Registers") {
		t.Errorf("ExceptionError.Error() should contain FC name, got %q", got)
	}
	if !strings.Contains(got, "Illegal Function") {
		t.Errorf("ExceptionError.Error() should contain exception name, got %q", got)
	}
}

func TestMapErrorToExceptionCode_AllCodes(t *testing.T) {
	tests := []struct {
		err  error
		want ExceptionCode
	}{
		{ErrIllegalFunction, ExIllegalFunction},
		{ErrIllegalDataAddress, ExIllegalDataAddress},
		{ErrIllegalDataValue, ExIllegalDataValue},
		{ErrServerDeviceFailure, ExServerDeviceFailure},
		{ErrAcknowledge, ExAcknowledge},
		{ErrServerDeviceBusy, ExServerDeviceBusy},
		{ErrMemoryParityError, ExMemoryParityError},
		{ErrGWPathUnavailable, ExGWPathUnavailable},
		{ErrGWTargetFailedToRespond, ExGWTargetFailedToRespond},
	}
	for _, tt := range tests {
		if got := MapErrorToExceptionCode(tt.err); got != tt.want {
			t.Errorf("MapErrorToExceptionCode(%v) = 0x%02x, want 0x%02x", tt.err, uint8(got), uint8(tt.want))
		}
	}
}

func TestMapErrorToExceptionCode_Unknown(t *testing.T) {
	if got := MapErrorToExceptionCode(errors.New("unknown")); got != ExServerDeviceFailure {
		t.Errorf("unknown error should map to ExServerDeviceFailure, got 0x%02x", uint8(got))
	}
}

func TestExceptionCodeString_Known(t *testing.T) {
	s := ExIllegalFunction.String()
	if !strings.Contains(s, "Illegal Function") {
		t.Errorf("ExIllegalFunction.String() = %q, want substring 'Illegal Function'", s)
	}
}

func TestExceptionCodeString_Unknown(t *testing.T) {
	s := ExceptionCode(0xFF).String()
	if !strings.Contains(s, "Unknown") {
		t.Errorf("ExceptionCode(0xFF).String() = %q, want substring 'Unknown'", s)
	}
}

func TestExceptionCodeToError(t *testing.T) {
	tests := []struct {
		code     ExceptionCode
		sentinel error
	}{
		{ExIllegalFunction, ErrIllegalFunction},
		{ExIllegalDataAddress, ErrIllegalDataAddress},
		{ExIllegalDataValue, ErrIllegalDataValue},
		{ExServerDeviceFailure, ErrServerDeviceFailure},
		{ExAcknowledge, ErrAcknowledge},
		{ExServerDeviceBusy, ErrServerDeviceBusy},
		{ExMemoryParityError, ErrMemoryParityError},
		{ExGWPathUnavailable, ErrGWPathUnavailable},
		{ExGWTargetFailedToRespond, ErrGWTargetFailedToRespond},
	}
	for _, tt := range tests {
		if got := tt.code.ToError(); got != tt.sentinel {
			t.Errorf("ExceptionCode(0x%02X).ToError() = %v, want %v", uint8(tt.code), got, tt.sentinel)
		}
	}
}

func TestExceptionCodeToError_Unknown(t *testing.T) {
	err := ExceptionCode(0xFF).ToError()
	if err == nil {
		t.Fatal("expected non-nil error for unknown exception code")
	}
	if !strings.Contains(err.Error(), "0xFF") {
		t.Errorf("expected error message to contain hex code, got %q", err.Error())
	}
}

func TestExceptionError_Is(t *testing.T) {
	err := MapExceptionCodeToError(FCReadHoldingRegisters, ExIllegalFunction)
	if !errors.Is(err, ErrIllegalFunction) {
		t.Error("ExceptionError should match its sentinel via errors.Is")
	}
	if errors.Is(err, ErrIllegalDataAddress) {
		t.Error("ExceptionError should not match a different sentinel")
	}
}

func TestExceptionError_Unwrap(t *testing.T) {
	err := MapExceptionCodeToError(FCReadCoils, ExIllegalDataAddress)
	var excErr *ExceptionError
	if !errors.As(err, &excErr) {
		t.Fatal("errors.As should find *ExceptionError")
	}
	if excErr.FunctionCode != FCReadCoils {
		t.Errorf("FunctionCode = 0x%02x, want 0x%02x", uint8(excErr.FunctionCode), uint8(FCReadCoils))
	}
	if excErr.ExceptionCode != ExIllegalDataAddress {
		t.Errorf("ExceptionCode = 0x%02x, want 0x%02x", uint8(excErr.ExceptionCode), uint8(ExIllegalDataAddress))
	}
}

func TestMapErrorToExceptionCode(t *testing.T) {
	if got := MapErrorToExceptionCode(ErrIllegalFunction); got != ExIllegalFunction {
		t.Errorf("got 0x%02x, want 0x%02x", uint8(got), uint8(ExIllegalFunction))
	}
	if got := MapErrorToExceptionCode(errors.New("unknown")); got != ExServerDeviceFailure {
		t.Errorf("unknown error should map to ExServerDeviceFailure, got 0x%02x", uint8(got))
	}
}
