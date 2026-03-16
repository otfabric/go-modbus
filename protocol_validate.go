package modbus

import (
	"fmt"

	"github.com/otfabric/modbus/internal/adu"
)

// validateReadBitsRange validates addr/quantity for FC01 and FC02.
func validateReadBitsRange(addr, quantity uint16) error {
	if quantity == 0 || quantity > maxReadCoils {
		return newParameterError("ReadCoils/ReadDiscreteInputs", "quantity",
			fmt.Sprintf("must be 1..%d, got %d", maxReadCoils, quantity))
	}
	if uint32(addr)+uint32(quantity)-1 > 0xffff {
		return newParameterError("ReadCoils/ReadDiscreteInputs", "addr+quantity",
			fmt.Sprintf("range 0x%04X+%d overflows address space", addr, quantity))
	}
	return nil
}

// validateWriteBitsRange validates addr/quantity for FC15.
func validateWriteBitsRange(addr, quantity uint16) error {
	if quantity == 0 || quantity > maxWriteCoils {
		return newParameterError("WriteCoils", "quantity",
			fmt.Sprintf("must be 1..%d, got %d", maxWriteCoils, quantity))
	}
	if uint32(addr)+uint32(quantity)-1 > 0xffff {
		return newParameterError("WriteCoils", "addr+quantity",
			fmt.Sprintf("range 0x%04X+%d overflows address space", addr, quantity))
	}
	return nil
}

// validateReadRegsRange validates addr/quantity for FC03 and FC04.
func validateReadRegsRange(addr, quantity uint16) error {
	if quantity == 0 || quantity > maxReadRegisters {
		return newParameterError("ReadRegisters", "quantity",
			fmt.Sprintf("must be 1..%d, got %d", maxReadRegisters, quantity))
	}
	if uint32(addr)+uint32(quantity)-1 > 0xffff {
		return newParameterError("ReadRegisters", "addr+quantity",
			fmt.Sprintf("range 0x%04X+%d overflows address space", addr, quantity))
	}
	return nil
}

// validateWriteRegsRange validates addr/quantity for FC16.
func validateWriteRegsRange(addr, quantity uint16) error {
	if quantity == 0 || quantity > maxWriteRegisters {
		return newParameterError("WriteRegisters", "quantity",
			fmt.Sprintf("must be 1..%d, got %d", maxWriteRegisters, quantity))
	}
	if uint32(addr)+uint32(quantity)-1 > 0xffff {
		return newParameterError("WriteRegisters", "addr+quantity",
			fmt.Sprintf("range 0x%04X+%d overflows address space", addr, quantity))
	}
	return nil
}

// checkResponseFC validates that res has the expected function code.
func checkResponseFC(res *adu.Response, reqFC byte) error {
	switch res.FunctionCode {
	case reqFC:
		return nil
	case reqFC | 0x80:
		if len(res.Payload) != 1 {
			return newProtocolError("response",
				fmt.Sprintf("exception response for FC 0x%02X has %d payload bytes, expected 1", reqFC, len(res.Payload)))
		}
		return mapExceptionCodeToError(FunctionCode(reqFC), ExceptionCode(res.Payload[0]))
	default:
		return newProtocolError("response",
			fmt.Sprintf("expected FC 0x%02X, got 0x%02X", reqFC, res.FunctionCode))
	}
}

// extractByteCountPayload validates that a success response starts with a
// byte-count prefix consistent with the remaining payload.
func extractByteCountPayload(res *adu.Response) ([]byte, error) {
	if len(res.Payload) < 1 {
		return nil, newProtocolError("response", "empty payload, expected byte count prefix")
	}
	bc := int(res.Payload[0])
	if len(res.Payload) != 1+bc {
		return nil, newProtocolError("response",
			fmt.Sprintf("byte count %d does not match payload length %d", bc, len(res.Payload)-1))
	}
	return res.Payload[1:], nil
}

// expectEchoAddrValue validates that a success response echoes addr and
// value as two consecutive big-endian uint16 words (4 bytes total).
func expectEchoAddrValue(res *adu.Response, addr, value uint16) error {
	if len(res.Payload) != 4 {
		return newProtocolError("response",
			fmt.Sprintf("expected 4-byte echo, got %d bytes", len(res.Payload)))
	}
	echoAddr := bytesToUint16(BigEndian, res.Payload[0:2])
	echoValue := bytesToUint16(BigEndian, res.Payload[2:4])
	if echoAddr != addr || echoValue != value {
		return newProtocolError("response",
			fmt.Sprintf("expected addr=0x%04X value=0x%04X, got addr=0x%04X value=0x%04X", addr, value, echoAddr, echoValue))
	}
	return nil
}

// expectEchoAddrQuantity validates that a success response echoes addr and
// quantity as two consecutive big-endian uint16 words (4 bytes total).
func expectEchoAddrQuantity(res *adu.Response, addr, quantity uint16) error {
	if len(res.Payload) != 4 {
		return newProtocolError("response",
			fmt.Sprintf("expected 4-byte echo, got %d bytes", len(res.Payload)))
	}
	echoAddr := bytesToUint16(BigEndian, res.Payload[0:2])
	echoQty := bytesToUint16(BigEndian, res.Payload[2:4])
	if echoAddr != addr || echoQty != quantity {
		return newProtocolError("response",
			fmt.Sprintf("expected addr=0x%04X qty=%d, got addr=0x%04X qty=%d", addr, quantity, echoAddr, echoQty))
	}
	return nil
}
