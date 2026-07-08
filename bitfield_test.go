// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// bitfieldMockServer serves FC03 (read 1 register at addr 0) with value 0xBEEF, and FC16 with success.
func bitfieldMockServer(t *testing.T) (addr string, cleanup func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() {
		for {
			sock, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				for {
					frame, err := readMBAPFrame(conn)
					if err != nil {
						return
					}
					txid, unitID, fc := frame[0:2], frame[6], frame[7]
					if len(frame) < 12 {
						continue
					}
					payload := frame[8:]
					if fc == byte(FCReadHoldingRegisters) {
						qty := int(payload[2])<<8 | int(payload[3])
						if qty == 1 {
							// 0xBEEF = 1011 1110 1110 1111 (bit0-3 set, bit4 clear, ...)
							resp := []byte{0x02, 0xBE, 0xEF}
							_ = writeMBAPNormal(conn, txid, unitID, fc, resp)
						} else {
							_ = writeMBAPException(conn, txid, unitID, fc, byte(exIllegalDataAddress))
						}
						continue
					}
					if fc == byte(FCReadInputRegisters) {
						qty := int(payload[2])<<8 | int(payload[3])
						if qty == 1 {
							resp := []byte{0x02, 0x12, 0x34}
							_ = writeMBAPNormal(conn, txid, unitID, fc, resp)
						} else {
							_ = writeMBAPException(conn, txid, unitID, fc, byte(exIllegalDataAddress))
						}
						continue
					}
					if fc == byte(FCWriteMultipleRegisters) && len(payload) >= 4 {
						_ = writeMBAPNormal(conn, txid, unitID, fc, payload[0:4])
						continue
					}
					_ = writeMBAPException(conn, txid, unitID, fc, byte(exIllegalFunction))
				}
			}(sock)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func TestReadRegisterBit(t *testing.T) {
	addr, cleanup := bitfieldMockServer(t)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	ctx := context.Background()

	// 0xBEEF = 1011 1110 1110 1111 (big-endian bytes 0xBE 0xEF → register 0xBEEF)
	// bit 0 (LSB) = 1, bit 1 = 1, bit 2 = 1, bit 3 = 1, bit 4 = 0
	ok, err := client.ReadRegisterBit(ctx, 1, 0, 0, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisterBit(0): %v", err)
	}
	if !ok {
		t.Error("ReadRegisterBit(0) expected true (LSB)")
	}
	ok, err = client.ReadRegisterBit(ctx, 1, 0, 4, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisterBit(4): %v", err)
	}
	if ok {
		t.Error("ReadRegisterBit(4) expected false")
	}
	ok, err = client.ReadRegisterBit(ctx, 1, 0, 15, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisterBit(15): %v", err)
	}
	if !ok {
		t.Error("ReadRegisterBit(15) expected true (MSB of 0xBEEF)")
	}

	// Input register 0x1234: bit 2 set, bit 3 set
	ok, err = client.ReadRegisterBit(ctx, 1, 0, 2, InputRegister)
	if err != nil {
		t.Fatalf("ReadRegisterBit input: %v", err)
	}
	if !ok {
		t.Error("ReadRegisterBit(2) input expected true")
	}

	// Invalid bit index
	_, err = client.ReadRegisterBit(ctx, 1, 0, 16, HoldingRegister)
	if !errors.Is(err, ErrUnexpectedParameters) {
		t.Errorf("ReadRegisterBit(16) want ErrUnexpectedParameters, got %v", err)
	}
}

func TestReadRegisterBits(t *testing.T) {
	addr, cleanup := bitfieldMockServer(t)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	ctx := context.Background()

	// 0xBEEF low 4 bits: 1,1,1,1
	bits, err := client.ReadRegisterBits(ctx, 1, 0, 0, 4, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisterBits: %v", err)
	}
	if len(bits) != 4 {
		t.Fatalf("len(bits)=%d want 4", len(bits))
	}
	for i := 0; i < 4; i++ {
		if !bits[i] {
			t.Errorf("bits[%d] want true", i)
		}
	}

	// bits 4-7 of 0xBEEF: 0,1,1,1
	bits, err = client.ReadRegisterBits(ctx, 1, 0, 4, 4, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisterBits(4,4): %v", err)
	}
	if bits[0] {
		t.Error("bits[4] want false")
	}
	for i := 1; i < 4; i++ {
		if !bits[i] {
			t.Errorf("bits[%d] want true", 4+i)
		}
	}

	// Invalid: count 0, count > 16, bitIndex+count > 16
	_, err = client.ReadRegisterBits(ctx, 1, 0, 0, 0, HoldingRegister)
	if !errors.Is(err, ErrUnexpectedParameters) {
		t.Errorf("ReadRegisterBits(0,0) want ErrUnexpectedParameters, got %v", err)
	}
	_, err = client.ReadRegisterBits(ctx, 1, 0, 14, 3, HoldingRegister)
	if !errors.Is(err, ErrUnexpectedParameters) {
		t.Errorf("ReadRegisterBits(14,3) want ErrUnexpectedParameters, got %v", err)
	}
}

func TestWriteRegisterBit(t *testing.T) {
	addr, cleanup := bitfieldMockServer(t)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	ctx := context.Background()

	if err := client.WriteRegisterBit(ctx, 1, 0, 0, true); err != nil {
		t.Fatalf("WriteRegisterBit: %v", err)
	}
	if err := client.WriteRegisterBit(ctx, 1, 0, 7, false); err != nil {
		t.Fatalf("WriteRegisterBit clear: %v", err)
	}

	err = client.WriteRegisterBit(ctx, 1, 0, 16, true)
	if !errors.Is(err, ErrUnexpectedParameters) {
		t.Errorf("WriteRegisterBit(16) want ErrUnexpectedParameters, got %v", err)
	}
}

func TestUpdateRegisterMask(t *testing.T) {
	addr, cleanup := bitfieldMockServer(t)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	ctx := context.Background()

	// Update only low nibble to 0xA: mask=0x000F, value=0x000A
	// Mock returns 0xBEEF; (0xBEEF & ^0xF) | (0xA & 0xF) = 0xBEEA
	if err := client.UpdateRegisterMask(ctx, 1, 0, 0x000F, 0x000A); err != nil {
		t.Fatalf("UpdateRegisterMask: %v", err)
	}
}
