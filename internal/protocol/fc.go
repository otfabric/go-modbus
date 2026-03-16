package protocol

import "fmt"

// FunctionCode is the Modbus function code (0x01–0x2B; exception bit 0x80).
type FunctionCode uint8

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

// String returns a human-readable name and the raw value.
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
		FCReadCoils, FCReadDiscreteInputs, FCReadHoldingRegisters, FCReadInputRegisters,
		FCWriteSingleCoil, FCWriteSingleRegister, FCReadExceptionStatus, FCDiagnostics,
		FCGetCommEventCounters, FCGetCommEventLog, FCWriteMultipleCoils, FCWriteMultipleRegisters,
		FCReportServerID, FCReadFileRecord, FCWriteFileRecord, FCMaskWriteRegister,
		FCReadWriteMultipleRegs, FCReadFIFOQueue, FCEncapsulatedInterface,
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
