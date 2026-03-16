package adu

import (
	"errors"
	"testing"
)

func TestAssembleMBAP(t *testing.T) {
	frame := AssembleMBAP(0x0001, 0x01, 0x03, []byte{0x00, 0x00, 0x00, 0x0a})
	// 7-byte header (txn+proto+len+unit) + 1 fc + 4 payload = 12
	if len(frame) != 12 {
		t.Fatalf("expected 12 bytes, got %d", len(frame))
	}
	// txn 0x0001 big-endian
	if frame[0] != 0x00 || frame[1] != 0x01 {
		t.Errorf("txn id: got %02x %02x", frame[0], frame[1])
	}
	// protocol 0x0000
	if frame[2] != 0x00 || frame[3] != 0x00 {
		t.Errorf("protocol: got %02x %02x", frame[2], frame[3])
	}
	// length = 2 + 4 = 6
	if frame[4] != 0x00 || frame[5] != 0x06 {
		t.Errorf("length: got %02x %02x", frame[4], frame[5])
	}
	if frame[6] != 0x01 {
		t.Errorf("unit id: got %02x", frame[6])
	}
	if frame[7] != 0x03 {
		t.Errorf("fc: got %02x", frame[7])
	}
	payloadWant := []byte{0x00, 0x00, 0x00, 0x0a}
	for i := 0; i < 4; i++ {
		if frame[8+i] != payloadWant[i] {
			t.Errorf("payload[%d]: got %02x want %02x", i, frame[8+i], payloadWant[i])
		}
	}
}

func TestParseMBAPHeader(t *testing.T) {
	// valid header: txn=1, proto=0, len=6, unit=1
	header := []byte{0x00, 0x01, 0x00, 0x00, 0x00, 0x06, 0x01}
	txnID, unitID, mbapLen, err := ParseMBAPHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if txnID != 1 || unitID != 0x01 || mbapLen != 6 {
		t.Errorf("got txnID=%d unitID=%d mbapLen=%d", txnID, unitID, mbapLen)
	}
}

func TestParseMBAPHeader_InvalidLength(t *testing.T) {
	header := []byte{0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0x01} // len=1 < min 2
	_, _, _, err := ParseMBAPHeader(header)
	if err == nil {
		t.Fatal("expected error for length 1")
	}
	if !errors.Is(err, ErrInvalidMBAPLength) {
		t.Errorf("expected ErrInvalidMBAPLength, got %v", err)
	}
}

func TestParseMBAPHeader_UnknownProtocol(t *testing.T) {
	header := []byte{0x00, 0x01, 0x00, 0x01, 0x00, 0x06, 0x01} // protocol 0x0001
	_, _, _, err := ParseMBAPHeader(header)
	if err == nil {
		t.Fatal("expected error for non-Modbus protocol")
	}
	if !errors.Is(err, ErrUnknownProtocolID) {
		t.Errorf("expected ErrUnknownProtocolID, got %v", err)
	}
}

func TestParseMBAPHeader_TooShort(t *testing.T) {
	header := []byte{0x00, 0x01, 0x00}
	_, _, _, err := ParseMBAPHeader(header)
	if err == nil {
		t.Fatal("expected error for short header")
	}
}
