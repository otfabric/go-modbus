// SPDX-License-Identifier: MIT

package modbus

import (
	"testing"
)

func TestUint16ToBytes(t *testing.T) {
	var out []byte

	out = uint16ToBytes(BigEndian, 0x4321)
	if len(out) != 2 {
		t.Errorf("expected 2 bytes, got %v", len(out))
	}
	if out[0] != 0x43 || out[1] != 0x21 {
		t.Errorf("expected {0x43, 0x21}, got {0x%02x, 0x%02x}", out[0], out[1])
	}

	out = uint16ToBytes(LittleEndian, 0x4321)
	if len(out) != 2 {
		t.Errorf("expected 2 bytes, got %v", len(out))
	}
	if out[0] != 0x21 || out[1] != 0x43 {
		t.Errorf("expected {0x21, 0x43}, got {0x%02x, 0x%02x}", out[0], out[1])
	}
}

func TestUint16sToBytes(t *testing.T) {
	var out []byte

	out = uint16sToBytes(BigEndian, []uint16{0x4321, 0x8765, 0xcba9})
	if len(out) != 6 {
		t.Errorf("expected 6 bytes, got %v", len(out))
	}
	if out[0] != 0x43 || out[1] != 0x21 {
		t.Errorf("expected {0x43, 0x21}, got {0x%02x, 0x%02x}", out[0], out[1])
	}
	if out[2] != 0x87 || out[3] != 0x65 {
		t.Errorf("expected {0x87, 0x65}, got {0x%02x, 0x%02x}", out[2], out[3])
	}
	if out[4] != 0xcb || out[5] != 0xa9 {
		t.Errorf("expected {0xcb, 0xa9}, got {0x%02x, 0x%02x}", out[4], out[5])
	}

	out = uint16sToBytes(LittleEndian, []uint16{0x4321, 0x8765, 0xcba9})
	if len(out) != 6 {
		t.Errorf("expected 6 bytes, got %v", len(out))
	}
	if out[0] != 0x21 || out[1] != 0x43 {
		t.Errorf("expected {0x21, 0x43}, got {0x%02x, 0x%02x}", out[0], out[1])
	}
	if out[2] != 0x65 || out[3] != 0x87 {
		t.Errorf("expected {0x65, 0x87}, got {0x%02x, 0x%02x}", out[2], out[3])
	}
	if out[4] != 0xa9 || out[5] != 0xcb {
		t.Errorf("expected {0xa9, 0xcb}, got {0x%02x, 0x%02x}", out[4], out[5])
	}
}

func TestBytesToUint16(t *testing.T) {
	var result uint16

	result = bytesToUint16(BigEndian, []byte{0x43, 0x21})
	if result != 0x4321 {
		t.Errorf("expected 0x4321, got 0x%04x", result)
	}

	result = bytesToUint16(LittleEndian, []byte{0x43, 0x21})
	if result != 0x2143 {
		t.Errorf("expected 0x2143, got 0x%04x", result)
	}
}

func TestBytesToUint16s(t *testing.T) {
	var results []uint16

	results = bytesToUint16s(BigEndian, []byte{0x11, 0x22, 0x33, 0x44})
	if len(results) != 2 {
		t.Errorf("expected 2 values, got %v", len(results))
	}
	if results[0] != 0x1122 {
		t.Errorf("expected 0x1122, got 0x%04x", results[0])
	}
	if results[1] != 0x3344 {
		t.Errorf("expected 0x3344, got 0x%04x", results[1])
	}

	results = bytesToUint16s(LittleEndian, []byte{0x11, 0x22, 0x33, 0x44})
	if len(results) != 2 {
		t.Errorf("expected 2 values, got %v", len(results))
	}
	if results[0] != 0x2211 {
		t.Errorf("expected 0x2211, got 0x%04x", results[0])
	}
	if results[1] != 0x4433 {
		t.Errorf("expected 0x4433, got 0x%04x", results[1])
	}
}

func TestDecodeBools(t *testing.T) {
	var results []bool

	results = decodeBools(1, []byte{0x01})
	if len(results) != 1 {
		t.Errorf("expected 1 value, got %v", len(results))
	}
	if results[0] != true {
		t.Errorf("expected true, got false")
	}

	results = decodeBools(1, []byte{0x0f})
	if len(results) != 1 {
		t.Errorf("expected 1 value, got %v", len(results))
	}
	if results[0] != true {
		t.Errorf("expected true, got false")
	}

	results = decodeBools(9, []byte{0x75, 0x03})
	if len(results) != 9 {
		t.Errorf("expected 9 values, got %v", len(results))
	}
	for i, b := range []bool{
		true, false, true, false,
		true, true, true, false,
		true} {
		if b != results[i] {
			t.Errorf("expected %v at %v, got %v", b, i, results[i])
		}
	}
}

func TestEncodeBools(t *testing.T) {
	var results []byte

	results = encodeBools([]bool{false, true, false, true})
	if len(results) != 1 {
		t.Errorf("expected 1 byte, got %v", len(results))
	}
	if results[0] != 0x0a {
		t.Errorf("expected 0x0a, got 0x%02x", results[0])
	}

	results = encodeBools([]bool{true, false, true})
	if len(results) != 1 {
		t.Errorf("expected 1 byte, got %v", len(results))
	}
	if results[0] != 0x05 {
		t.Errorf("expected 0x05, got 0x%02x", results[0])
	}

	results = encodeBools([]bool{true, false, false, true, false, true, true, false,
		true, true, true, false, true, true, true, false,
		false, true})
	if len(results) != 3 {
		t.Errorf("expected 3 bytes, got %v", len(results))
	}
	if results[0] != 0x69 || results[1] != 0x77 || results[2] != 0x02 {
		t.Errorf("expected {0x69, 0x77, 0x02}, got {0x%02x, 0x%02x, 0x%02x}",
			results[0], results[1], results[2])
	}
}
