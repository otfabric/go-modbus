// SPDX-License-Identifier: MIT

package adu

import "testing"

func TestRTUFrame_TooShort(t *testing.T) {
	for _, frame := range [][]byte{nil, {}, {0x01}, {0x01, 0x03}, {0x01, 0x03, 0x00}} {
		if ValidateRTUCRC(frame) {
			t.Errorf("ValidateRTUCRC(% X) = true for a frame shorter than 4 bytes", frame)
		}
		unitID, fc, payload := ParseRTUFrame(frame)
		if unitID != 0 || fc != 0 || payload != nil {
			t.Errorf("ParseRTUFrame(% X) = (%d, %d, % X), want zero values", frame, unitID, fc, payload)
		}
	}
}

func TestRTUFrame_MinimalRoundTrip(t *testing.T) {
	frame := AssembleRTUFrame(0x11, 0x07, nil)
	if len(frame) != 4 {
		t.Fatalf("frame length = %d, want 4", len(frame))
	}
	if !ValidateRTUCRC(frame) {
		t.Fatal("CRC of minimal frame did not validate")
	}
	unitID, fc, payload := ParseRTUFrame(frame)
	if unitID != 0x11 || fc != 0x07 || len(payload) != 0 {
		t.Fatalf("ParseRTUFrame = (%#x, %#x, % X)", unitID, fc, payload)
	}
	frame[3] ^= 0x01
	if ValidateRTUCRC(frame) {
		t.Fatal("corrupted CRC validated")
	}
}
