package protocol

// RTU response length sentinels for variable-length and FIFO responses.
const (
	RTUResponseLengthVariable = -1 // FC08, FC2B: read until inter-frame silence
	RTUResponseLengthFIFO     = -2 // FC18: byte count is 2 bytes; read one more then use it
)

// ExpectedRTUResponseLength returns the number of payload bytes still to read
// after the first 3 bytes (unitID, functionCode, first payload byte).
// Returns RTUResponseLengthVariable or RTUResponseLengthFIFO for special cases.
func ExpectedRTUResponseLength(responseCode FunctionCode, responseLength uint8) (int, error) {
	switch responseCode {
	case FCReadHoldingRegisters,
		FCReadInputRegisters,
		FCReadCoils,
		FCReadDiscreteInputs:
		return int(responseLength), nil
	case FCWriteSingleRegister,
		FCWriteMultipleRegisters,
		FCWriteSingleCoil,
		FCWriteMultipleCoils:
		return 3, nil
	case FCMaskWriteRegister:
		return 5, nil
	case FCReadFileRecord,
		FCWriteFileRecord,
		FCReadWriteMultipleRegs:
		return int(responseLength), nil
	case FCReadFIFOQueue:
		return RTUResponseLengthFIFO, nil
	case FCGetCommEventCounters:
		return 3, nil
	case FCGetCommEventLog:
		return int(responseLength), nil
	case FCDiagnostics,
		FCEncapsulatedInterface:
		return RTUResponseLengthVariable, nil
	case FCReportServerID:
		return int(responseLength), nil
	case FCReadExceptionStatus:
		return 0, nil
	case FunctionCode(0x80 | uint8(FCReadHoldingRegisters)),
		FunctionCode(0x80 | uint8(FCReadInputRegisters)),
		FunctionCode(0x80 | uint8(FCReadCoils)),
		FunctionCode(0x80 | uint8(FCReadDiscreteInputs)),
		FunctionCode(0x80 | uint8(FCWriteSingleRegister)),
		FunctionCode(0x80 | uint8(FCWriteMultipleRegisters)),
		FunctionCode(0x80 | uint8(FCWriteSingleCoil)),
		FunctionCode(0x80 | uint8(FCWriteMultipleCoils)),
		FunctionCode(0x80 | uint8(FCMaskWriteRegister)),
		FunctionCode(0x80 | uint8(FCDiagnostics)),
		FunctionCode(0x80 | uint8(FCReportServerID)),
		FunctionCode(0x80 | uint8(FCReadExceptionStatus)),
		FunctionCode(0x80 | uint8(FCGetCommEventCounters)),
		FunctionCode(0x80 | uint8(FCGetCommEventLog)),
		FunctionCode(0x80 | uint8(FCReadFileRecord)),
		FunctionCode(0x80 | uint8(FCWriteFileRecord)),
		FunctionCode(0x80 | uint8(FCReadWriteMultipleRegs)),
		FunctionCode(0x80 | uint8(FCReadFIFOQueue)),
		FunctionCode(0x80 | uint8(FCEncapsulatedInterface)):
		return 0, nil
	default:
		return 0, ErrProtocolError
	}
}
