// SPDX-License-Identifier: MIT

package codec

import (
	"testing"
)

func TestUint32ToBytes(t *testing.T) {
	var out []byte

	out = uint32ToBytes(BigEndian, HighWordFirst, 0x87654321)
	if len(out) != 4 {
		t.Errorf("expected 4 bytes, got %v", len(out))
	}
	if out[0] != 0x87 || out[1] != 0x65 || out[2] != 0x43 || out[3] != 0x21 {
		t.Errorf("expected {0x87, 0x65, 0x43, 0x21}, got {0x%02x, 0x%02x, 0x%02x, 0x%02x}",
			out[0], out[1], out[2], out[3])
	}

	out = uint32ToBytes(BigEndian, LowWordFirst, 0x87654321)
	if len(out) != 4 {
		t.Errorf("expected 4 bytes, got %v", len(out))
	}
	if out[0] != 0x43 || out[1] != 0x21 || out[2] != 0x87 || out[3] != 0x65 {
		t.Errorf("expected {0x43, 0x21, 0x87, 0x65}, got {0x%02x, 0x%02x, 0x%02x, 0x%02x}",
			out[0], out[1], out[2], out[3])
	}

	out = uint32ToBytes(LittleEndian, LowWordFirst, 0x87654321)
	if len(out) != 4 {
		t.Errorf("expected 4 bytes, got %v", len(out))
	}
	if out[0] != 0x21 || out[1] != 0x43 || out[2] != 0x65 || out[3] != 0x87 {
		t.Errorf("expected {0x21, 0x43, 0x65, 0x87}, got {0x%02x, 0x%02x, 0x%02x, 0x%02x}",
			out[0], out[1], out[2], out[3])
	}

	out = uint32ToBytes(LittleEndian, HighWordFirst, 0x87654321)
	if len(out) != 4 {
		t.Errorf("expected 4 bytes, got %v", len(out))
	}
	if out[0] != 0x65 || out[1] != 0x87 || out[2] != 0x21 || out[3] != 0x43 {
		t.Errorf("expected {0x65, 0x87, 0x21, 0x43}, got {0x%02x, 0x%02x, 0x%02x, 0x%02x}",
			out[0], out[1], out[2], out[3])
	}
}

func TestBytesToUint32s(t *testing.T) {
	var results []uint32

	results = bytesToUint32s(BigEndian, HighWordFirst, []byte{
		0x87, 0x65, 0x43, 0x21,
		0x00, 0x11, 0x22, 0x33,
	})
	if len(results) != 2 {
		t.Errorf("expected 2 values, got %v", len(results))
	}
	if results[0] != 0x87654321 {
		t.Errorf("expected 0x87654321, got 0x%08x", results[0])
	}
	if results[1] != 0x00112233 {
		t.Errorf("expected 0x00112233, got 0x%08x", results[1])
	}

	results = bytesToUint32s(BigEndian, LowWordFirst, []byte{
		0x87, 0x65, 0x43, 0x21,
		0x00, 0x11, 0x22, 0x33,
	})
	if len(results) != 2 {
		t.Errorf("expected 2 values, got %v", len(results))
	}
	if results[0] != 0x43218765 {
		t.Errorf("expected 0x43218765, got 0x%08x", results[0])
	}
	if results[1] != 0x22330011 {
		t.Errorf("expected 0x22330011, got 0x%08x", results[1])
	}

	results = bytesToUint32s(LittleEndian, LowWordFirst, []byte{
		0x87, 0x65, 0x43, 0x21,
		0x00, 0x11, 0x22, 0x33,
	})
	if len(results) != 2 {
		t.Errorf("expected 2 values, got %v", len(results))
	}
	if results[0] != 0x21436587 {
		t.Errorf("expected 0x21436587, got 0x%08x", results[0])
	}
	if results[1] != 0x33221100 {
		t.Errorf("expected 0x33221100, got 0x%08x", results[1])
	}

	results = bytesToUint32s(LittleEndian, HighWordFirst, []byte{
		0x87, 0x65, 0x43, 0x21,
		0x00, 0x11, 0x22, 0x33,
	})
	if len(results) != 2 {
		t.Errorf("expected 2 values, got %v", len(results))
	}
	if results[0] != 0x65872143 {
		t.Errorf("expected 0x65872143, got 0x%08x", results[0])
	}
	if results[1] != 0x11003322 {
		t.Errorf("expected 0x11003322, got 0x%08x", results[1])
	}
}

func TestFloat32ToBytes(t *testing.T) {
	var out []byte

	out = float32ToBytes(BigEndian, HighWordFirst, 1.234)
	if len(out) != 4 {
		t.Errorf("expected 4 bytes, got %v", len(out))
	}
	if out[0] != 0x3f || out[1] != 0x9d || out[2] != 0xf3 || out[3] != 0xb6 {
		t.Errorf("expected {0x3f, 0x9d, 0xf3, 0xb6}, got {0x%02x, 0x%02x, 0x%02x, 0x%02x}",
			out[0], out[1], out[2], out[3])
	}

	out = float32ToBytes(BigEndian, LowWordFirst, 1.234)
	if out[0] != 0xf3 || out[1] != 0xb6 || out[2] != 0x3f || out[3] != 0x9d {
		t.Errorf("expected {0xf3, 0xb6, 0x3f, 0x9d}, got {0x%02x, 0x%02x, 0x%02x, 0x%02x}",
			out[0], out[1], out[2], out[3])
	}

	out = float32ToBytes(LittleEndian, LowWordFirst, 1.234)
	if out[0] != 0xb6 || out[1] != 0xf3 || out[2] != 0x9d || out[3] != 0x3f {
		t.Errorf("expected {0xb6, 0xf3, 0x9d, 0x3f}, got {0x%02x, 0x%02x, 0x%02x, 0x%02x}",
			out[0], out[1], out[2], out[3])
	}

	out = float32ToBytes(LittleEndian, HighWordFirst, 1.234)
	if out[0] != 0x9d || out[1] != 0x3f || out[2] != 0xb6 || out[3] != 0xf3 {
		t.Errorf("expected {0x9d, 0x3f, 0xb6, 0xf3}, got {0x%02x, 0x%02x, 0x%02x, 0x%02x}",
			out[0], out[1], out[2], out[3])
	}
}

func TestBytesToFloat32s(t *testing.T) {
	results := bytesToFloat32s(BigEndian, HighWordFirst, []byte{
		0x3f, 0x9d, 0xf3, 0xb6,
		0x40, 0x49, 0x0f, 0xdb,
	})
	if len(results) != 2 {
		t.Errorf("expected 2 values, got %v", len(results))
	}
	if results[0] != 1.234 {
		t.Errorf("expected 1.234, got %.04f", results[0])
	}
	if results[1] != 3.14159274101 {
		t.Errorf("expected 3.14159274101, got %.09f", results[1])
	}
}

func TestUint64ToBytes(t *testing.T) {
	out := uint64ToBytes(BigEndian, HighWordFirst, 0x0fedcba987654321)
	if len(out) != 8 {
		t.Errorf("expected 8 bytes, got %v", len(out))
	}
	if out[0] != 0x0f || out[1] != 0xed || out[2] != 0xcb || out[3] != 0xa9 ||
		out[4] != 0x87 || out[5] != 0x65 || out[6] != 0x43 || out[7] != 0x21 {
		t.Errorf("unexpected bytes: %x", out)
	}
}

func TestBytesToUint64s(t *testing.T) {
	results := bytesToUint64s(BigEndian, HighWordFirst, []byte{
		0x0f, 0xed, 0xcb, 0xa9, 0x87, 0x65, 0x43, 0x21,
	})
	if len(results) != 1 || results[0] != 0x0fedcba987654321 {
		t.Errorf("unexpected result: %x", results)
	}
}

func TestBytesToInt16s(t *testing.T) {
	results := bytesToInt16s(BigEndian, []byte{0xFF, 0xFF, 0x80, 0x00, 0x7F, 0xFF})
	if len(results) != 3 {
		t.Fatalf("expected 3 values, got %v", len(results))
	}
	if results[0] != -1 {
		t.Errorf("expected -1, got %v", results[0])
	}
	if results[1] != -32768 {
		t.Errorf("expected -32768, got %v", results[1])
	}
	if results[2] != 32767 {
		t.Errorf("expected 32767, got %v", results[2])
	}
}

func TestBytesToInt32s(t *testing.T) {
	results := bytesToInt32s(BigEndian, HighWordFirst, []byte{0xFF, 0xFF, 0xFF, 0xFF})
	if len(results) != 1 || results[0] != -1 {
		t.Errorf("expected -1, got %v", results)
	}
}

func TestBytesToInt64s(t *testing.T) {
	results := bytesToInt64s(BigEndian, HighWordFirst, []byte{
		0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	})
	if len(results) != 1 || results[0] != -1 {
		t.Errorf("expected -1, got %v", results)
	}
}

func TestBytesToUint48s(t *testing.T) {
	results := bytesToUint48s(BigEndian, HighWordFirst, []byte{
		0x00, 0x01, 0x00, 0x02, 0x00, 0x03,
	})
	if len(results) != 1 || results[0] != 0x000100020003 {
		t.Errorf("expected 0x000100020003, got 0x%012x", results[0])
	}
}

func TestBytesToInt48s(t *testing.T) {
	results := bytesToInt48s(BigEndian, HighWordFirst, []byte{
		0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	})
	if len(results) != 1 || results[0] != -1 {
		t.Errorf("expected -1, got %v", results[0])
	}
}

func TestUint48ToBytesRoundtrip(t *testing.T) {
	u48 := uint64(0x123456789ABC)
	for _, e := range []Endianness{BigEndian, LittleEndian} {
		for _, w := range []WordOrder{HighWordFirst, LowWordFirst} {
			b := uint48ToBytes(e, w, u48)
			if len(b) != 6 {
				t.Fatalf("uint48ToBytes: expected 6 bytes, got %d", len(b))
			}
			decoded := bytesToUint48s(e, w, b)
			if len(decoded) != 1 || decoded[0] != u48 {
				t.Errorf("roundtrip: endianness=%v wordOrder=%v: got %x", e, w, decoded)
			}
		}
	}
}

func TestBytesToAscii(t *testing.T) {
	result := bytesToAscii([]byte{0x48, 0x65, 0x6C, 0x6C, 0x6F, 0x20})
	if result != "Hello" {
		t.Errorf("expected \"Hello\", got %q", result)
	}
}

func TestBytesToAsciiReverse(t *testing.T) {
	result := bytesToAsciiReverse([]byte{0x65, 0x48, 0x6C, 0x6C, 0x20, 0x6F})
	if result != "Hello" {
		t.Errorf("expected \"Hello\", got %q", result)
	}
}

func TestBytesToBCD(t *testing.T) {
	result := bytesToBCD([]byte{0x01, 0x02, 0x03, 0x04})
	if result != "1234" {
		t.Errorf("expected \"1234\", got %q", result)
	}
}

func TestBytesToPackedBCD(t *testing.T) {
	result := bytesToPackedBCD([]byte{0x92})
	if result != "92" {
		t.Errorf("expected \"92\", got %q", result)
	}
}

func TestAsciiToBytes(t *testing.T) {
	b := asciiToBytes("Hi")
	if len(b) != 2 || b[0] != 'H' || b[1] != 'i' {
		t.Errorf("asciiToBytes(\"Hi\") = %v", b)
	}
	b = asciiToBytes("H")
	if len(b) != 2 || b[0] != 'H' || b[1] != 0 {
		t.Errorf("asciiToBytes(\"H\") = %v (expected pad)", b)
	}
}

func TestAsciiToBytesReverse(t *testing.T) {
	b := asciiToBytesReverse("Hi")
	if len(b) != 2 || b[0] != 'i' || b[1] != 'H' {
		t.Errorf("asciiToBytesReverse(\"Hi\") = %v", b)
	}
	if bytesToAsciiReverse(b) != "Hi" {
		t.Errorf("roundtrip: got %q", bytesToAsciiReverse(b))
	}
}

func TestBcdToBytes(t *testing.T) {
	b, err := bcdToBytes("1234")
	if err != nil || len(b) != 4 || b[0] != 1 || b[3] != 4 {
		t.Errorf("bcdToBytes(\"1234\") = %v, %v", b, err)
	}
	if _, err := bcdToBytes("12a4"); err == nil {
		t.Error("bcdToBytes(non-digit) should error")
	}
}

func TestPackedBCDToBytes(t *testing.T) {
	b, err := packedBCDToBytes("92")
	if err != nil || len(b) != 1 || b[0] != 0x92 {
		t.Errorf("packedBCDToBytes(\"92\") = %v, %v", b, err)
	}
	b, _ = packedBCDToBytes("1234")
	if len(b) != 2 || b[0] != 0x12 || b[1] != 0x34 {
		t.Errorf("packedBCDToBytes(\"1234\") = %v", b)
	}
	if _, err := packedBCDToBytes("9x"); err == nil {
		t.Error("packedBCDToBytes(non-digit) should error")
	}
}
