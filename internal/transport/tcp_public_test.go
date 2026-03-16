package transport

import (
	"testing"

	"github.com/otfabric/modbus/internal/adu"
	"github.com/otfabric/modbus/internal/protocol"
)

func TestAssembleMBAPFrame(t *testing.T) {
	var frame []byte

	frame = adu.AssembleMBAP(0x9219, 0x33, byte(protocol.FCReportServerID), []byte{0x22, 0x33, 0x44, 0x55})
	// expect 7 bytes of MBAP header + 1 bytes of function code + 4 bytes of payload
	if len(frame) != 12 {
		t.Errorf("expected 12 bytes, got %v", len(frame))
	}
	for i, b := range []byte{
		0x92, 0x19, // transaction identifier (big endian)
		0x00, 0x00, // protocol identifier
		0x00, 0x06, // length (big endian)
		0x33, 0x11, // unit id and function code
		0x22, 0x33, // payload
		0x44, 0x55, // payload
	} {
		if frame[i] != b {
			t.Errorf("expected 0x%02x at position %v, got 0x%02x", b, i, frame[i])
		}
	}

	frame = adu.AssembleMBAP(0x921a, 0x31, byte(protocol.FCWriteSingleRegister), []byte{0x12, 0x34})
	// expect 7 bytes of MBAP header + 1 bytes of function code + 2 bytes of payload
	if len(frame) != 10 {
		t.Errorf("expected 10 bytes, got %v", len(frame))
	}
	for i, b := range []byte{
		0x92, 0x1a, // transaction identifier (big endian)
		0x00, 0x00, // protocol identifier
		0x00, 0x04, // length (big endian)
		0x31, 0x06, // unit id and function code
		0x12, 0x34, // payload
	} {
		if frame[i] != b {
			t.Errorf("expected 0x%02x at position %v, got 0x%02x", b, i, frame[i])
		}
	}
}
