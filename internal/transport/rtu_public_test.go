// SPDX-License-Identifier: MIT

package transport

import (
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/protocol"
)

func TestAssembleRTUFrame(t *testing.T) {
	var frame []byte

	frame = adu.AssembleRTUFrame(0x33, byte(protocol.FCReportServerID), []byte{0x22, 0x33, 0x44, 0x55})
	// expect 1 byte of unit id, 1 byte of function code, 4 bytes of payload and
	// 2 bytes of CRC
	if len(frame) != 8 {
		t.Errorf("expected 8 bytes, got %v", len(frame))
	}
	for i, b := range []byte{
		0x33, 0x11, // unit id and function code
		0x22, 0x33, // payload
		0x44, 0x55, // payload
		0xf0, 0x93, // CRC
	} {
		if frame[i] != b {
			t.Errorf("expected 0x%02x at position %v, got 0x%02x", b, i, frame[i])
		}
	}

	frame = adu.AssembleRTUFrame(0x31, byte(protocol.FCWriteSingleRegister), []byte{0x12, 0x34})
	// expect 1 byte of unit if, 1 byte of function code, 2 bytes of payload and
	// 2 bytes of CRC
	if len(frame) != 6 {
		t.Errorf("expected 6 bytes, got %v", len(frame))
	}
	for i, b := range []byte{
		0x31, 0x06, // unit id and function code
		0x12, 0x34, // payload
		0xe3, 0xae, // CRC
	} {
		if frame[i] != b {
			t.Errorf("expected 0x%02x at position %v, got 0x%02x", b, i, frame[i])
		}
	}
}

func TestModbusRTUSerialCharTime(t *testing.T) {
	var d time.Duration

	d = SerialCharTime(38400)
	// expect 11 bits at 38400bps: 11 * (1/38400) = 286.458uS
	if d != time.Duration(286458)*time.Nanosecond {
		t.Errorf("unexpected serial char duration: %v", d)
	}

	d = SerialCharTime(19200)
	// expect 11 bits at 19200bps: 11 * (1/19200) = 572.916uS
	if d != time.Duration(572916)*time.Nanosecond {
		t.Errorf("unexpected serial char duration: %v", d)
	}

	d = SerialCharTime(9600)
	// expect 11 bits at 9600bps: 11 * (1/9600) = 1.145833ms
	if d != time.Duration(1145833)*time.Nanosecond {
		t.Errorf("unexpected serial char duration: %v", d)
	}
}
