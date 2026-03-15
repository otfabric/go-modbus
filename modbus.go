package modbus

import (
	"errors"
	"fmt"
)

//
// Typed Protocol Primitives
//

type FunctionCode uint8
type ExceptionCode uint8
type MEIType uint8

//
// PDU
//

type pdu struct {
	unitID                uint8
	functionCode          FunctionCode
	payload               []byte
	responseTransactionID uint16 // set by TCP transport on response; 0 for RTU
}

//
// Exception Handling
//

// ExceptionError is returned by client methods when the remote device responds
// with a Modbus exception.
//
//	errors.As(err, &excErr)
//	errors.Is(err, modbus.ErrIllegalDataAddress)
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

//
// Function Codes (Public FC)
//

const (
	FCReadCoils              FunctionCode = 0x01
	FCReadDiscreteInputs     FunctionCode = 0x02
	FCReadHoldingRegisters   FunctionCode = 0x03
	FCReadInputRegisters     FunctionCode = 0x04
	FCWriteSingleCoil        FunctionCode = 0x05
	FCWriteSingleRegister    FunctionCode = 0x06
	FCReadExceptionStatus    FunctionCode = 0x07
	FCDiagnostics            FunctionCode = 0x08
	FCGetCommEventCounters   FunctionCode = 0x0B
	FCGetCommEventLog        FunctionCode = 0x0C
	FCWriteMultipleCoils     FunctionCode = 0x0F
	FCWriteMultipleRegisters FunctionCode = 0x10
	FCReportServerID         FunctionCode = 0x11
	FCReadFileRecord         FunctionCode = 0x14
	FCWriteFileRecord        FunctionCode = 0x15
	FCMaskWriteRegister      FunctionCode = 0x16
	FCReadWriteMultipleRegs  FunctionCode = 0x17
	FCReadFIFOQueue          FunctionCode = 0x18
	FCEncapsulatedInterface  FunctionCode = 0x2B
)

var functionCodeNames = map[FunctionCode]string{
	FCReadCoils:              "Read Coils",
	FCReadDiscreteInputs:     "Read Discrete Inputs",
	FCReadHoldingRegisters:   "Read Holding Registers",
	FCReadInputRegisters:     "Read Input Registers",
	FCWriteSingleCoil:        "Write Single Coil",
	FCWriteSingleRegister:    "Write Single Register",
	FCReadExceptionStatus:    "Read Exception Status",
	FCDiagnostics:            "Diagnostics",
	FCGetCommEventCounters:   "Get Comm Event Counters",
	FCGetCommEventLog:        "Get Comm Event Log",
	FCWriteMultipleCoils:     "Write Multiple Coils",
	FCWriteMultipleRegisters: "Write Multiple Registers",
	FCReportServerID:         "Report Server ID",
	FCReadFileRecord:         "Read File Record",
	FCWriteFileRecord:        "Write File Record",
	FCMaskWriteRegister:      "Mask Write Register",
	FCReadWriteMultipleRegs:  "Read/Write Multiple Registers",
	FCReadFIFOQueue:          "Read FIFO Queue",
	FCEncapsulatedInterface:  "Encapsulated Interface",
}

// IsException reports whether the function code has the Modbus exception bit set (MSB).
func (fc FunctionCode) IsException() bool {
	return uint8(fc)&0x80 != 0
}

// Base returns the function code with the exception bit cleared.
func (fc FunctionCode) Base() FunctionCode {
	return FunctionCode(uint8(fc) & 0x7F)
}

// String returns a human-readable name and the raw value (e.g. "Read Holding Registers (0x03)" or "Read Holding Registers Exception (0x83)").
func (fc FunctionCode) String() string {
	base := fc.Base()
	name, ok := functionCodeNames[base]
	if !ok {
		return fmt.Sprintf("Unknown Function (0x%02X)", uint8(fc))
	}
	if fc.IsException() {
		return fmt.Sprintf("%s Exception (0x%02X)", name, uint8(fc))
	}
	return fmt.Sprintf("%s (0x%02X)", name, uint8(fc))
}

// Valid reports whether the function code (after stripping the exception bit) is a known public function code.
func (fc FunctionCode) Valid() bool {
	_, ok := functionCodeNames[fc.Base()]
	return ok
}

// KnownFunctionCodes returns all supported base function codes (no exception variants).
func KnownFunctionCodes() []FunctionCode {
	return []FunctionCode{
		FCReadCoils,
		FCReadDiscreteInputs,
		FCReadHoldingRegisters,
		FCReadInputRegisters,
		FCWriteSingleCoil,
		FCWriteSingleRegister,
		FCReadExceptionStatus,
		FCDiagnostics,
		FCGetCommEventCounters,
		FCGetCommEventLog,
		FCWriteMultipleCoils,
		FCWriteMultipleRegisters,
		FCReportServerID,
		FCReadFileRecord,
		FCWriteFileRecord,
		FCMaskWriteRegister,
		FCReadWriteMultipleRegs,
		FCReadFIFOQueue,
		FCEncapsulatedInterface,
	}
}

// ParseFunctionCode validates a raw byte as a known Modbus function code (normal or exception) and returns it as FunctionCode.
func ParseFunctionCode(b byte) (FunctionCode, error) {
	fc := FunctionCode(b)
	if !fc.Base().Valid() {
		return 0, fmt.Errorf("modbus: invalid function code 0x%02X", b)
	}
	return fc, nil
}

//
// Encapsulated Interface (FC43)
//

const (
	MEIReadDeviceIdentification MEIType = 0x0E
)

// FC43 Read Device ID object types.
const (
	ReadDeviceIdBasic      = 0x01
	ReadDeviceIdRegular    = 0x02
	ReadDeviceIdExtended   = 0x03
	ReadDeviceIdIndividual = 0x04
)

//
// Exception Codes
//

const (
	exIllegalFunction         ExceptionCode = 0x01
	exIllegalDataAddress      ExceptionCode = 0x02
	exIllegalDataValue        ExceptionCode = 0x03
	exServerDeviceFailure     ExceptionCode = 0x04
	exAcknowledge             ExceptionCode = 0x05
	exServerDeviceBusy        ExceptionCode = 0x06
	exMemoryParityError       ExceptionCode = 0x08
	exGWPathUnavailable       ExceptionCode = 0x0A
	exGWTargetFailedToRespond ExceptionCode = 0x0B
)

var exceptionCodeNames = map[ExceptionCode]string{
	exIllegalFunction:         "Illegal Function",
	exIllegalDataAddress:      "Illegal Data Address",
	exIllegalDataValue:        "Illegal Data Value",
	exServerDeviceFailure:     "Server Device Failure",
	exAcknowledge:             "Acknowledge",
	exServerDeviceBusy:        "Server Device Busy",
	exMemoryParityError:       "Memory Parity Error",
	exGWPathUnavailable:       "Gateway Path Unavailable",
	exGWTargetFailedToRespond: "Gateway Target Failed To Respond",
}

// String returns a human-readable name and the raw value (e.g. "Illegal Data Address (0x02)").
func (ec ExceptionCode) String() string {
	name, ok := exceptionCodeNames[ec]
	if !ok {
		return fmt.Sprintf("Unknown Exception (0x%02X)", uint8(ec))
	}
	return fmt.Sprintf("%s (0x%02X)", name, uint8(ec))
}

// ToError returns the corresponding sentinel error for known exception codes, or fmt.Errorf for unknown codes.
func (ec ExceptionCode) ToError() error {
	switch ec {
	case exIllegalFunction:
		return ErrIllegalFunction
	case exIllegalDataAddress:
		return ErrIllegalDataAddress
	case exIllegalDataValue:
		return ErrIllegalDataValue
	case exServerDeviceFailure:
		return ErrServerDeviceFailure
	case exAcknowledge:
		return ErrAcknowledge
	case exMemoryParityError:
		return ErrMemoryParityError
	case exServerDeviceBusy:
		return ErrServerDeviceBusy
	case exGWPathUnavailable:
		return ErrGWPathUnavailable
	case exGWTargetFailedToRespond:
		return ErrGWTargetFailedToRespond
	default:
		return fmt.Errorf("modbus: unknown exception code (0x%02X)", uint8(ec))
	}
}

//
// Standard ports (IANA / common practice)
//

// PortModbusTCP is the well-known port for Modbus/TCP (IANA registered).
const PortModbusTCP = 502

// PortModbusTLS is the well-known port for Modbus/TCP Security (Modbus over TLS).
const PortModbusTLS = 802

// Modbus RTU over TCP has no standard port; use a URL like rtuovertcp://host:port with a port of your choice.

//
// Protocol Limits
//

const (
	maxReadCoils      = 2000
	maxWriteCoils     = 1968
	maxReadRegisters  = 125
	maxWriteRegisters = 123
	maxRWReadRegs     = 125
	maxRWWriteRegs    = 121
	maxFIFOCount      = 31
	maxFileByteCount  = 0xF5
	maxFileReqDataLen = 0xFB
)

//
// Sentinel Errors
//

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
	ErrBadUnitId                      = errors.New("modbus: bad unit id")
	ErrBadTransactionId               = errors.New("modbus: bad transaction id")
	ErrUnknownProtocolId              = errors.New("modbus: unknown protocol identifier")
	ErrInvalidMBAPLength              = errors.New("modbus: invalid mbap length")
	ErrUnexpectedParameters           = errors.New("modbus: unexpected parameters")
	ErrSunSpecModelChainInvalid       = errors.New("modbus: sunspec model chain invalid")
	ErrSunSpecModelChainLimitExceeded = errors.New("modbus: sunspec model chain limit exceeded")
)

//
// Exception Mapping
//

func mapExceptionCodeToError(fc FunctionCode, ec ExceptionCode) error {
	sentinel := ec.ToError()
	if _, ok := exceptionCodeNames[ec]; !ok {
		return sentinel
	}
	return &ExceptionError{
		FunctionCode:  fc,
		ExceptionCode: ec,
		Sentinel:      sentinel,
	}
}

func mapErrorToExceptionCode(err error) ExceptionCode {
	switch {
	case errors.Is(err, ErrIllegalFunction):
		return exIllegalFunction
	case errors.Is(err, ErrIllegalDataAddress):
		return exIllegalDataAddress
	case errors.Is(err, ErrIllegalDataValue):
		return exIllegalDataValue
	case errors.Is(err, ErrServerDeviceFailure):
		return exServerDeviceFailure
	case errors.Is(err, ErrAcknowledge):
		return exAcknowledge
	case errors.Is(err, ErrMemoryParityError):
		return exMemoryParityError
	case errors.Is(err, ErrServerDeviceBusy):
		return exServerDeviceBusy
	case errors.Is(err, ErrGWPathUnavailable):
		return exGWPathUnavailable
	case errors.Is(err, ErrGWTargetFailedToRespond):
		return exGWTargetFailedToRespond
	default:
		return exServerDeviceFailure
	}
}
