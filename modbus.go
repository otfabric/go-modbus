package modbus

import (
	"github.com/otfabric/go-modbus/internal/protocol"
)

//
// Re-exports from internal/protocol (Modbus protocol semantics).
//

type (
	FunctionCode       = protocol.FunctionCode
	ExceptionCode      = protocol.ExceptionCode
	MEIType            = protocol.MEIType
	RegType            = protocol.RegType
	ExceptionError     = protocol.ExceptionError
	ProtocolError      = protocol.ProtocolError
	ParameterError     = protocol.ParameterError
	ConfigurationError = protocol.ConfigurationError
)

const (
	FCReadCoils              = protocol.FCReadCoils
	FCReadDiscreteInputs     = protocol.FCReadDiscreteInputs
	FCReadHoldingRegisters   = protocol.FCReadHoldingRegisters
	FCReadInputRegisters     = protocol.FCReadInputRegisters
	FCWriteSingleCoil        = protocol.FCWriteSingleCoil
	FCWriteSingleRegister    = protocol.FCWriteSingleRegister
	FCReadExceptionStatus    = protocol.FCReadExceptionStatus
	FCDiagnostics            = protocol.FCDiagnostics
	FCGetCommEventCounters   = protocol.FCGetCommEventCounters
	FCGetCommEventLog        = protocol.FCGetCommEventLog
	FCWriteMultipleCoils     = protocol.FCWriteMultipleCoils
	FCWriteMultipleRegisters = protocol.FCWriteMultipleRegisters
	FCReportServerID         = protocol.FCReportServerID
	FCReadFileRecord         = protocol.FCReadFileRecord
	FCWriteFileRecord        = protocol.FCWriteFileRecord
	FCMaskWriteRegister      = protocol.FCMaskWriteRegister
	FCReadWriteMultipleRegs  = protocol.FCReadWriteMultipleRegs
	FCReadFIFOQueue          = protocol.FCReadFIFOQueue
	FCEncapsulatedInterface  = protocol.FCEncapsulatedInterface
)

const (
	MEIReadDeviceIdentification = protocol.MEIReadDeviceIdentification
	ReadDeviceIDBasic           = protocol.ReadDeviceIDBasic
	ReadDeviceIDRegular         = protocol.ReadDeviceIDRegular
	ReadDeviceIDExtended        = protocol.ReadDeviceIDExtended
	ReadDeviceIDIndividual      = protocol.ReadDeviceIDIndividual
)

const (
	HoldingRegister = protocol.HoldingRegister
	InputRegister   = protocol.InputRegister
)

const (
	PortModbusTCP = protocol.PortModbusTCP
	PortModbusTLS = protocol.PortModbusTLS
)

// Protocol limits (unexported for use by client/server).
const (
	maxReadCoils      = protocol.MaxReadCoils
	maxWriteCoils     = protocol.MaxWriteCoils
	maxReadRegisters  = protocol.MaxReadRegisters
	maxWriteRegisters = protocol.MaxWriteRegisters
	maxRWReadRegs     = protocol.MaxRWReadRegs
	maxRWWriteRegs    = protocol.MaxRWWriteRegs
	maxFIFOCount      = protocol.MaxFIFOCount
	maxFileByteCount  = protocol.MaxFileByteCount
	maxFileReqDataLen = protocol.MaxFileReqDataLen
)

// Exception code constants (unexported; used by tests and exception mapping).
const (
	exIllegalFunction         = protocol.ExIllegalFunction
	exIllegalDataAddress      = protocol.ExIllegalDataAddress
	exIllegalDataValue        = protocol.ExIllegalDataValue
	exServerDeviceFailure     = protocol.ExServerDeviceFailure
	exAcknowledge             = protocol.ExAcknowledge
	exServerDeviceBusy        = protocol.ExServerDeviceBusy
	exMemoryParityError       = protocol.ExMemoryParityError
	exGWPathUnavailable       = protocol.ExGWPathUnavailable
	exGWTargetFailedToRespond = protocol.ExGWTargetFailedToRespond
)

var (
	ErrConfigurationError             = protocol.ErrConfigurationError
	ErrClientNotOpen                  = protocol.ErrClientNotOpen
	ErrRequestTimedOut                = protocol.ErrRequestTimedOut
	ErrIllegalFunction                = protocol.ErrIllegalFunction
	ErrIllegalDataAddress             = protocol.ErrIllegalDataAddress
	ErrIllegalDataValue               = protocol.ErrIllegalDataValue
	ErrServerDeviceFailure            = protocol.ErrServerDeviceFailure
	ErrAcknowledge                    = protocol.ErrAcknowledge
	ErrServerDeviceBusy               = protocol.ErrServerDeviceBusy
	ErrMemoryParityError              = protocol.ErrMemoryParityError
	ErrGWPathUnavailable              = protocol.ErrGWPathUnavailable
	ErrGWTargetFailedToRespond        = protocol.ErrGWTargetFailedToRespond
	ErrBadCRC                         = protocol.ErrBadCRC
	ErrShortFrame                     = protocol.ErrShortFrame
	ErrProtocolError                  = protocol.ErrProtocolError
	ErrBadUnitID                      = protocol.ErrBadUnitID
	ErrBadTransactionID               = protocol.ErrBadTransactionID
	ErrUnknownProtocolID              = protocol.ErrUnknownProtocolID
	ErrInvalidMBAPLength              = protocol.ErrInvalidMBAPLength
	ErrUnexpectedParameters           = protocol.ErrUnexpectedParameters
	ErrSunSpecModelChainInvalid       = protocol.ErrSunSpecModelChainInvalid
	ErrSunSpecModelChainLimitExceeded = protocol.ErrSunSpecModelChainLimitExceeded
)

//
// Delegations to protocol.
//

func KnownFunctionCodes() []FunctionCode {
	return protocol.KnownFunctionCodes()
}

func ParseFunctionCode(b byte) (FunctionCode, error) {
	return protocol.ParseFunctionCode(b)
}

func mapExceptionCodeToError(fc FunctionCode, ec ExceptionCode) error {
	return protocol.MapExceptionCodeToError(fc, ec)
}

func mapErrorToExceptionCode(err error) ExceptionCode {
	return protocol.MapErrorToExceptionCode(err)
}

var (
	newParameterError     = protocol.NewParameterError
	newProtocolError      = protocol.NewProtocolError
	newConfigurationError = protocol.NewConfigurationError
)
