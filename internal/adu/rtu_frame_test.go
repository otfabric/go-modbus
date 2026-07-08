// SPDX-License-Identifier: MIT

package adu

import (
	"testing"
)

func TestAssembleRTUFrame(t *testing.T) {
	frame := AssembleRTUFrame(0x01, 0x03, []byte{0x00, 0x00, 0x00, 0x0a})
	if len(frame) != 1+1+4+2 {
		t.Fatalf("expected 8 bytes (unit+fc+payload+crc), got %d", len(frame))
	}
	if frame[0] != 0x01 || frame[1] != 0x03 {
		t.Errorf("header: got %02x %02x", frame[0], frame[1])
	}
	if !ValidateRTUCRC(frame) {
		t.Error("ValidateRTUCRC failed on AssembleRTUFrame output")
	}
}

func TestValidateRTUCRC(t *testing.T) {
	frame := AssembleRTUFrame(0x01, 0x03, []byte{0x00, 0x00, 0x00, 0x0a})
	if !ValidateRTUCRC(frame) {
		t.Error("expected valid CRC")
	}
	// corrupt CRC
	frame[len(frame)-1] ^= 0xff
	if ValidateRTUCRC(frame) {
		t.Error("expected invalid CRC after corruption")
	}
}

func TestParseRTUFrame(t *testing.T) {
	frame := AssembleRTUFrame(0x02, 0x04, []byte{0x01, 0x02})
	unitID, fc, payload := ParseRTUFrame(frame)
	if unitID != 0x02 || fc != 0x04 {
		t.Errorf("got unitID=%02x fc=%02x", unitID, fc)
	}
	if len(payload) != 2 || payload[0] != 0x01 || payload[1] != 0x02 {
		t.Errorf("got payload %v", payload)
	}
}
