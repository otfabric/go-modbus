// SPDX-License-Identifier: MIT

package protocol

import (
	"errors"
	"fmt"
)

// ExceptionCode is the Modbus exception code in an exception response.
type ExceptionCode uint8

const (
	ExIllegalFunction         ExceptionCode = 0x01
	ExIllegalDataAddress      ExceptionCode = 0x02
	ExIllegalDataValue        ExceptionCode = 0x03
	ExServerDeviceFailure     ExceptionCode = 0x04
	ExAcknowledge             ExceptionCode = 0x05
	ExServerDeviceBusy        ExceptionCode = 0x06
	ExMemoryParityError       ExceptionCode = 0x08
	ExGWPathUnavailable       ExceptionCode = 0x0A
	ExGWTargetFailedToRespond ExceptionCode = 0x0B
)

var exceptionCodeNames = map[ExceptionCode]string{
	ExIllegalFunction:         "Illegal Function",
	ExIllegalDataAddress:      "Illegal Data Address",
	ExIllegalDataValue:        "Illegal Data Value",
	ExServerDeviceFailure:     "Server Device Failure",
	ExAcknowledge:             "Acknowledge",
	ExServerDeviceBusy:        "Server Device Busy",
	ExMemoryParityError:       "Memory Parity Error",
	ExGWPathUnavailable:       "Gateway Path Unavailable",
	ExGWTargetFailedToRespond: "Gateway Target Failed To Respond",
}

// String returns a human-readable name and the raw value.
func (ec ExceptionCode) String() string {
	name, ok := exceptionCodeNames[ec]
	if !ok {
		return fmt.Sprintf("Unknown Exception (0x%02X)", uint8(ec))
	}
	return fmt.Sprintf("%s (0x%02X)", name, uint8(ec))
}

// Sentinel errors for Modbus exceptions and transport. Re-exported by the public modbus package.
var (
	ErrConfigurationError             = errors.New("modbus: configuration error")
	ErrClientNotOpen                  = errors.New("modbus: client is not open")
	ErrRequestTimedOut                = errors.New("modbus: request timed out")
	ErrIllegalFunction                = errors.New("modbus: illegal function")
	ErrIllegalDataAddress             = errors.New("modbus: illegal data address")
	ErrIllegalDataValue               = errors.New("modbus: illegal data value")
	ErrServerDeviceFailure            = errors.New("modbus: server device failure")
	ErrAcknowledge                    = errors.New("modbus: acknowledge")
	ErrServerDeviceBusy               = errors.New("modbus: server device busy")
	ErrMemoryParityError              = errors.New("modbus: memory parity error")
	ErrGWPathUnavailable              = errors.New("modbus: gateway path unavailable")
	ErrGWTargetFailedToRespond        = errors.New("modbus: gateway target failed to respond")
	ErrBadCRC                         = errors.New("modbus: bad crc")
	ErrShortFrame                     = errors.New("modbus: short frame")
	ErrProtocolError                  = errors.New("modbus: protocol error")
	ErrBadUnitID                      = errors.New("modbus: bad unit id")
	ErrBadTransactionID               = errors.New("modbus: bad transaction id")
	ErrUnknownProtocolID              = errors.New("modbus: unknown protocol identifier")
	ErrInvalidMBAPLength              = errors.New("modbus: invalid mbap length")
	ErrUnexpectedParameters           = errors.New("modbus: unexpected parameters")
	ErrSunSpecModelChainInvalid       = errors.New("modbus: sunspec model chain invalid")
	ErrSunSpecModelChainLimitExceeded = errors.New("modbus: sunspec model chain limit exceeded")
)

// ToError returns the corresponding sentinel error for known exception codes, or fmt.Errorf for unknown codes.
func (ec ExceptionCode) ToError() error {
	switch ec {
	case ExIllegalFunction:
		return ErrIllegalFunction
	case ExIllegalDataAddress:
		return ErrIllegalDataAddress
	case ExIllegalDataValue:
		return ErrIllegalDataValue
	case ExServerDeviceFailure:
		return ErrServerDeviceFailure
	case ExAcknowledge:
		return ErrAcknowledge
	case ExMemoryParityError:
		return ErrMemoryParityError
	case ExServerDeviceBusy:
		return ErrServerDeviceBusy
	case ExGWPathUnavailable:
		return ErrGWPathUnavailable
	case ExGWTargetFailedToRespond:
		return ErrGWTargetFailedToRespond
	default:
		return fmt.Errorf("modbus: unknown exception code (0x%02X)", uint8(ec))
	}
}

// ExceptionError is returned when the remote device responds with a Modbus exception.
type ExceptionError struct {
	FunctionCode  FunctionCode
	ExceptionCode ExceptionCode
	Sentinel      error
}

func (e *ExceptionError) Error() string {
	return fmt.Sprintf("%s: %s", e.FunctionCode.Base(), e.ExceptionCode)
}

func (e *ExceptionError) Unwrap() error        { return e.Sentinel }
func (e *ExceptionError) Is(target error) bool { return target == e.Sentinel }

// MapExceptionCodeToError builds an ExceptionError for the given FC and exception code.
func MapExceptionCodeToError(fc FunctionCode, ec ExceptionCode) error {
	sentinel := ec.ToError()
	return &ExceptionError{
		FunctionCode:  fc,
		ExceptionCode: ec,
		Sentinel:      sentinel,
	}
}

// ProtocolError is a typed protocol error that wraps ErrProtocolError with
// an operation name and reason for better diagnostics in logs and bug reports.
type ProtocolError struct {
	Op     string
	Reason string
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("modbus: protocol error in %s: %s", e.Op, e.Reason)
}

func (e *ProtocolError) Unwrap() error { return ErrProtocolError }

// NewProtocolError creates a typed protocol error.
func NewProtocolError(op, reason string) error {
	return &ProtocolError{Op: op, Reason: reason}
}

// ParameterError is returned when a public API method receives invalid arguments.
// It wraps ErrUnexpectedParameters for errors.Is compatibility.
type ParameterError struct {
	Method string
	Param  string
	Reason string
}

func (e *ParameterError) Error() string {
	return fmt.Sprintf("modbus: %s: parameter %q: %s", e.Method, e.Param, e.Reason)
}

func (e *ParameterError) Unwrap() error { return ErrUnexpectedParameters }

// NewParameterError creates a typed parameter validation error.
func NewParameterError(method, param, reason string) error {
	return &ParameterError{Method: method, Param: param, Reason: reason}
}

// ConfigurationError is returned when New() or NewServer() receives invalid
// configuration. It wraps ErrConfigurationError for errors.Is compatibility.
type ConfigurationError struct {
	Field  string
	Reason string
}

func (e *ConfigurationError) Error() string {
	return fmt.Sprintf("modbus: configuration error: %s: %s", e.Field, e.Reason)
}

func (e *ConfigurationError) Unwrap() error { return ErrConfigurationError }

// NewConfigurationError creates a typed configuration error.
func NewConfigurationError(field, reason string) error {
	return &ConfigurationError{Field: field, Reason: reason}
}

// MapErrorToExceptionCode returns the ExceptionCode for a known sentinel, or ExServerDeviceFailure.
func MapErrorToExceptionCode(err error) ExceptionCode {
	switch {
	case errors.Is(err, ErrIllegalFunction):
		return ExIllegalFunction
	case errors.Is(err, ErrIllegalDataAddress):
		return ExIllegalDataAddress
	case errors.Is(err, ErrIllegalDataValue):
		return ExIllegalDataValue
	case errors.Is(err, ErrServerDeviceFailure):
		return ExServerDeviceFailure
	case errors.Is(err, ErrAcknowledge):
		return ExAcknowledge
	case errors.Is(err, ErrMemoryParityError):
		return ExMemoryParityError
	case errors.Is(err, ErrServerDeviceBusy):
		return ExServerDeviceBusy
	case errors.Is(err, ErrGWPathUnavailable):
		return ExGWPathUnavailable
	case errors.Is(err, ErrGWTargetFailedToRespond):
		return ExGWTargetFailedToRespond
	default:
		return ExServerDeviceFailure
	}
}
