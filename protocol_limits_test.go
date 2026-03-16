package modbus

import (
	"testing"

	"github.com/otfabric/modbus/internal/adu"
	"github.com/otfabric/modbus/internal/protocol"
)

func TestProtocolSizeLimits_Constants(t *testing.T) {
	// Modbus spec: PDU max = 253 bytes
	// serial ADU max = 256 bytes (unitID 1 + PDU 253 + CRC 2)
	// TCP ADU max = 260 bytes (MBAP header 7 + PDU 253)

	if adu.MaxRTUFrameLength != 256 {
		t.Errorf("MaxRTUFrameLength = %d, want 256", adu.MaxRTUFrameLength)
	}
	if adu.MBAPHeaderLength != 7 {
		t.Errorf("MBAPHeaderLength = %d, want 7", adu.MBAPHeaderLength)
	}
	// MBAPLengthMax = 254 (unitID + FC + payload = 1 + 1 + 252 = max 254)
	// This means PDU max = MBAPLengthMax - 1 (unitID) = 253
	if adu.MBAPLengthMax != 254 {
		t.Errorf("MBAPLengthMax = %d, want 254", adu.MBAPLengthMax)
	}
	if adu.MBAPLengthMin != 2 {
		t.Errorf("MBAPLengthMin = %d, want 2", adu.MBAPLengthMin)
	}

	const maxPDU = adu.MBAPLengthMax - 1 // unitID byte is in MBAP length but not PDU
	if maxPDU != 253 {
		t.Errorf("derived PDU max = %d, want 253", maxPDU)
	}

	const tcpADUMax = adu.MBAPHeaderLength + maxPDU
	if tcpADUMax != 260 {
		t.Errorf("derived TCP ADU max = %d, want 260", tcpADUMax)
	}
}

func TestMBAPHeader_LengthBounds(t *testing.T) {
	validHeader := func(mbapLen uint16) []byte {
		return []byte{
			0x00, 0x01, // transaction ID
			0x00, 0x00, // protocol ID
			byte(mbapLen >> 8), byte(mbapLen), // length
			0x01, // unit ID
		}
	}

	// Valid: MBAPLengthMin (2) and MBAPLengthMax (254)
	for _, validLen := range []uint16{2, 100, 254} {
		_, _, _, err := adu.ParseMBAPHeader(validHeader(validLen))
		if err != nil {
			t.Errorf("MBAP length %d should be valid, got: %v", validLen, err)
		}
	}

	// Invalid: below min, above max
	for _, invalidLen := range []uint16{0, 1, 255, 256, 1000} {
		_, _, _, err := adu.ParseMBAPHeader(validHeader(invalidLen))
		if err == nil {
			t.Errorf("MBAP length %d should be rejected", invalidLen)
		}
	}
}

func TestProtocolSizeLimits_QuantityConstants(t *testing.T) {
	if protocol.MaxReadCoils != 2000 {
		t.Errorf("MaxReadCoils = %d, want 2000", protocol.MaxReadCoils)
	}
	if protocol.MaxWriteCoils != 1968 {
		t.Errorf("MaxWriteCoils = %d, want 1968", protocol.MaxWriteCoils)
	}
	if protocol.MaxReadRegisters != 125 {
		t.Errorf("MaxReadRegisters = %d, want 125", protocol.MaxReadRegisters)
	}
	if protocol.MaxWriteRegisters != 123 {
		t.Errorf("MaxWriteRegisters = %d, want 123", protocol.MaxWriteRegisters)
	}
	if protocol.MaxRWReadRegs != 125 {
		t.Errorf("MaxRWReadRegs = %d, want 125", protocol.MaxRWReadRegs)
	}
	if protocol.MaxRWWriteRegs != 121 {
		t.Errorf("MaxRWWriteRegs = %d, want 121", protocol.MaxRWWriteRegs)
	}
	if protocol.MaxFIFOCount != 31 {
		t.Errorf("MaxFIFOCount = %d, want 31", protocol.MaxFIFOCount)
	}
}

func TestMBAPHeader_InvalidProtocolID(t *testing.T) {
	header := []byte{
		0x00, 0x01, // transaction ID
		0x00, 0x01, // non-zero protocol ID
		0x00, 0x06, // length
		0x01, // unit ID
	}
	_, _, _, err := adu.ParseMBAPHeader(header)
	if err == nil {
		t.Error("non-zero protocol ID should be rejected")
	}
}

func TestRTUFrame_MaxSizeWithCRC(t *testing.T) {
	// Max PDU = 253 bytes (FC + payload); RTU ADU = unitID(1) + PDU(253) + CRC(2) = 256
	payload := make([]byte, 252) // FC is separate, so payload = 252
	frame := adu.AssembleRTUFrame(0x01, 0x03, payload)
	if len(frame) != 256 {
		t.Errorf("max RTU frame size = %d, want 256", len(frame))
	}
	if !adu.ValidateRTUCRC(frame) {
		t.Error("CRC validation failed for max-size RTU frame")
	}
}
