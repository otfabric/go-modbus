// SPDX-License-Identifier: MIT

package adu

import (
	"testing"
)

func TestUint16sToBytes_BigEndian(t *testing.T) {
	got := Uint16sToBytes(BigEndian, []uint16{0x0102, 0x0304})
	want := []byte{0x01, 0x02, 0x03, 0x04}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("byte[%d] = 0x%02x, want 0x%02x", i, got[i], want[i])
		}
	}
}

func TestUint16sToBytes_LittleEndian(t *testing.T) {
	got := Uint16sToBytes(LittleEndian, []uint16{0x0102, 0x0304})
	want := []byte{0x02, 0x01, 0x04, 0x03}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("byte[%d] = 0x%02x, want 0x%02x", i, got[i], want[i])
		}
	}
}

func TestUint16sToBytes_Empty(t *testing.T) {
	got := Uint16sToBytes(BigEndian, []uint16{})
	if len(got) != 0 {
		t.Errorf("expected empty slice, got len %d", len(got))
	}
}

func TestBytesToUint16s_BigEndian(t *testing.T) {
	got := BytesToUint16s(BigEndian, []byte{0x01, 0x02, 0x03, 0x04})
	want := []uint16{0x0102, 0x0304}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("uint16[%d] = 0x%04x, want 0x%04x", i, got[i], want[i])
		}
	}
}

func TestBytesToUint16s_LittleEndian(t *testing.T) {
	got := BytesToUint16s(LittleEndian, []byte{0x02, 0x01, 0x04, 0x03})
	want := []uint16{0x0102, 0x0304}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("uint16[%d] = 0x%04x, want 0x%04x", i, got[i], want[i])
		}
	}
}

func TestBytesToUint16s_Empty(t *testing.T) {
	got := BytesToUint16s(BigEndian, []byte{})
	if len(got) != 0 {
		t.Errorf("expected empty slice, got len %d", len(got))
	}
}
