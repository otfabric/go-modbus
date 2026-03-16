package protocol

import (
	"errors"
	"testing"
)

func TestExpectedRTUResponseLength(t *testing.T) {
	tests := []struct {
		name           string
		responseCode   FunctionCode
		responseLength uint8
		wantLen        int
		wantErr        error
	}{
		// Read FCs: return responseLength
		{"ReadHoldingRegisters", FCReadHoldingRegisters, 10, 10, nil},
		{"ReadInputRegisters", FCReadInputRegisters, 20, 20, nil},
		{"ReadCoils", FCReadCoils, 5, 5, nil},
		{"ReadDiscreteInputs", FCReadDiscreteInputs, 8, 8, nil},

		// Write FCs: return 3
		{"WriteSingleRegister", FCWriteSingleRegister, 0, 3, nil},
		{"WriteMultipleRegisters", FCWriteMultipleRegisters, 0, 3, nil},
		{"WriteSingleCoil", FCWriteSingleCoil, 0, 3, nil},
		{"WriteMultipleCoils", FCWriteMultipleCoils, 0, 3, nil},

		// FCMaskWriteRegister: return 5
		{"MaskWriteRegister", FCMaskWriteRegister, 0, 5, nil},

		// File FCs: return responseLength
		{"ReadFileRecord", FCReadFileRecord, 42, 42, nil},
		{"WriteFileRecord", FCWriteFileRecord, 16, 16, nil},
		{"ReadWriteMultipleRegs", FCReadWriteMultipleRegs, 24, 24, nil},

		// FCReadFIFOQueue: return RTUResponseLengthFIFO
		{"ReadFIFOQueue", FCReadFIFOQueue, 0, RTUResponseLengthFIFO, nil},

		// FCDiagnostics, FCEncapsulatedInterface: return RTUResponseLengthVariable
		{"Diagnostics", FCDiagnostics, 0, RTUResponseLengthVariable, nil},
		{"EncapsulatedInterface", FCEncapsulatedInterface, 0, RTUResponseLengthVariable, nil},

		// FCReportServerID: return responseLength
		{"ReportServerID", FCReportServerID, 6, 6, nil},

		// FCGetCommEventCounters: return 3 (fixed 4-byte payload, first byte already read)
		{"GetCommEventCounters", FCGetCommEventCounters, 0, 3, nil},

		// FCGetCommEventLog: return responseLength (byte-count-prefixed variable payload)
		{"GetCommEventLog", FCGetCommEventLog, 12, 12, nil},

		// FCReadExceptionStatus: return 0
		{"ReadExceptionStatus", FCReadExceptionStatus, 0, 0, nil},

		// Exception responses (0x80 | FC): return 0
		{"Exception ReadHoldingRegisters", FunctionCode(0x80 | uint8(FCReadHoldingRegisters)), 0, 0, nil},
		{"Exception ReadInputRegisters", FunctionCode(0x80 | uint8(FCReadInputRegisters)), 0, 0, nil},
		{"Exception ReadCoils", FunctionCode(0x80 | uint8(FCReadCoils)), 0, 0, nil},
		{"Exception ReadDiscreteInputs", FunctionCode(0x80 | uint8(FCReadDiscreteInputs)), 0, 0, nil},
		{"Exception WriteSingleRegister", FunctionCode(0x80 | uint8(FCWriteSingleRegister)), 0, 0, nil},
		{"Exception WriteMultipleRegisters", FunctionCode(0x80 | uint8(FCWriteMultipleRegisters)), 0, 0, nil},
		{"Exception WriteSingleCoil", FunctionCode(0x80 | uint8(FCWriteSingleCoil)), 0, 0, nil},
		{"Exception WriteMultipleCoils", FunctionCode(0x80 | uint8(FCWriteMultipleCoils)), 0, 0, nil},
		{"Exception MaskWriteRegister", FunctionCode(0x80 | uint8(FCMaskWriteRegister)), 0, 0, nil},
		{"Exception Diagnostics", FunctionCode(0x80 | uint8(FCDiagnostics)), 0, 0, nil},
		{"Exception ReportServerID", FunctionCode(0x80 | uint8(FCReportServerID)), 0, 0, nil},
		{"Exception ReadExceptionStatus", FunctionCode(0x80 | uint8(FCReadExceptionStatus)), 0, 0, nil},
		{"Exception GetCommEventCounters", FunctionCode(0x80 | uint8(FCGetCommEventCounters)), 0, 0, nil},
		{"Exception GetCommEventLog", FunctionCode(0x80 | uint8(FCGetCommEventLog)), 0, 0, nil},
		{"Exception ReadFileRecord", FunctionCode(0x80 | uint8(FCReadFileRecord)), 0, 0, nil},
		{"Exception WriteFileRecord", FunctionCode(0x80 | uint8(FCWriteFileRecord)), 0, 0, nil},
		{"Exception ReadWriteMultipleRegs", FunctionCode(0x80 | uint8(FCReadWriteMultipleRegs)), 0, 0, nil},
		{"Exception ReadFIFOQueue", FunctionCode(0x80 | uint8(FCReadFIFOQueue)), 0, 0, nil},
		{"Exception EncapsulatedInterface", FunctionCode(0x80 | uint8(FCEncapsulatedInterface)), 0, 0, nil},

		// Unknown FC: return ErrProtocolError
		{"Unknown FC 0x00", FunctionCode(0x00), 0, 0, ErrProtocolError},
		{"Unknown FC 0x09", FunctionCode(0x09), 0, 0, ErrProtocolError},
		{"Unknown FC 0xFF", FunctionCode(0xFF), 0, 0, ErrProtocolError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLen, gotErr := ExpectedRTUResponseLength(tt.responseCode, tt.responseLength)
			if gotLen != tt.wantLen {
				t.Errorf("ExpectedRTUResponseLength() gotLen = %d, want %d", gotLen, tt.wantLen)
			}
			if !errors.Is(gotErr, tt.wantErr) {
				t.Errorf("ExpectedRTUResponseLength() gotErr = %v, want %v", gotErr, tt.wantErr)
			}
		})
	}
}
