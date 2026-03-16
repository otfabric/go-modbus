package adu

import (
	"testing"
)

func TestCRC(t *testing.T) {
	var c crc
	var out []byte

	c.init()
	if c.crc != 0xffff {
		t.Errorf("expected 0xffff, saw 0x%04x", c.crc)
	}

	out = c.value()
	if len(out) != 2 {
		t.Errorf("value() should have returned 2 bytes, got %v", len(out))
	}
	if out[0] != 0xff || out[1] != 0xff {
		t.Errorf("expected {0xff, 0xff} got {0x%02x, 0x%02x}", out[0], out[1])
	}

	c.add([]byte{0x01, 0x02, 0x03, 0x04, 0x05})
	if c.crc != 0xbb2a {
		t.Errorf("expected 0xbb2a, saw 0x%04x", c.crc)
	}

	out = c.value()
	if len(out) != 2 {
		t.Errorf("value() should have returned 2 bytes, got %v", len(out))
	}
	if out[0] != 0x2a || out[1] != 0xbb {
		t.Errorf("expected {0x2a, 0xbb} got {0x%02x, 0x%02x}", out[0], out[1])
	}

	c.add([]byte{0x06})
	if c.crc != 0xddba {
		t.Errorf("expected 0xddba, saw 0x%04x", c.crc)
	}

	out = c.value()
	if len(out) != 2 {
		t.Errorf("value() should have returned 2 bytes, got %v", len(out))
	}
	if out[0] != 0xba || out[1] != 0xdd {
		t.Errorf("expected {0xba, 0xdd} got {0x%02x, 0x%02x}", out[0], out[1])
	}

	c.init()
	if c.crc != 0xffff {
		t.Errorf("expected 0xffff, saw 0x%04x", c.crc)
	}

	out = c.value()
	if len(out) != 2 {
		t.Errorf("value() should have returned 2 bytes, got %v", len(out))
	}
	if out[0] != 0xff || out[1] != 0xff {
		t.Errorf("expected {0xff, 0xff} got {0x%02x, 0x%02x}", out[0], out[1])
	}
}

func TestCRCIsEqual(t *testing.T) {
	var c crc

	c.init()
	c.add([]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06})

	if c.crc != 0xddba {
		t.Errorf("expected 0xddba, saw 0x%04x", c.crc)
	}

	if !c.isEqual(0xba, 0xdd) {
		t.Error("isEqual() should have returned true")
	}

	if c.isEqual(0xdd, 0xba) {
		t.Error("isEqual() should have returned false")
	}

	out := c.value()
	if !c.isEqual(out[0], out[1]) {
		t.Error("isEqual() should have returned true")
	}

	c.init()
	if !c.isEqual(0xff, 0xff) {
		t.Error("isEqual() should have returned true")
	}
}
