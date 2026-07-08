// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/codec"
)

// writeMBAPRegs sends an FC03/FC04 normal response with the given register bytes (payload only: byte count + data).
func writeMBAPRegs(conn net.Conn, txid []byte, unitID, fc byte, payload []byte) error {
	return writeMBAPNormal(conn, txid, unitID, fc, payload)
}

func TestReadUint16Pair_HoldingRegisters(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid, unitID, fc := frame[0:2], frame[6], frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			addr := int(frame[8])<<8 | int(frame[9])
			qty := int(frame[10])<<8 | int(frame[11])
			if addr == 0 && qty == 2 {
				// 0x5375, 0x6E53
				payload := []byte{0x04, 0x53, 0x75, 0x6E, 0x53}
				_ = writeMBAPRegs(sock, txid, unitID, fc, payload)
			} else {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	regs, err := client.ReadRegisters(context.Background(), 1, 0, 2, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisters: %v", err)
	}
	if len(regs) != 2 {
		t.Fatalf("expected 2 registers, got %d", len(regs))
	}
	if regs[0] != 0x5375 || regs[1] != 0x6E53 {
		t.Errorf("expected [0x5375, 0x6E53], got [0x%04X, 0x%04X]", regs[0], regs[1])
	}
}

func TestReadUint16Pair_InputRegisters(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid, unitID, fc := frame[0:2], frame[6], frame[7]
			if fc != byte(FCReadInputRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			qty := int(frame[10])<<8 | int(frame[11])
			if qty == 2 {
				payload := []byte{0x04, 0x00, 0x01, 0x00, 0x02}
				_ = writeMBAPRegs(sock, txid, unitID, fc, payload)
			} else {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	regs, err := client.ReadRegisters(context.Background(), 1, 0, 2, InputRegister)
	if err != nil {
		t.Fatalf("ReadRegisters: %v", err)
	}
	if len(regs) != 2 || regs[0] != 1 || regs[1] != 2 {
		t.Errorf("expected [1, 2], got %v", regs)
	}
}

func TestReadUint16Pair_Exception(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid, unitID, fc := frame[0:2], frame[6], frame[7]
			_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
		}
	}()

	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.ReadRegisters(context.Background(), 1, 0, 2, HoldingRegister)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrIllegalDataAddress) {
		t.Errorf("expected ErrIllegalDataAddress, got %v", err)
	}
}

func TestReadAsciiFixed_TrailingSpacePreserved(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid, unitID, fc := frame[0:2], frame[6], frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			qty := int(frame[10])<<8 | int(frame[11])
			if qty == 2 {
				// 0x4142 'AB', 0x4320 'C '
				payload := []byte{0x04, 0x41, 0x42, 0x43, 0x20}
				_ = writeMBAPRegs(sock, txid, unitID, fc, payload)
			} else {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	raw, err := client.ReadRegisterBytes(context.Background(), 1, 0, 4, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisterBytes: %v", err)
	}
	s := string(raw)
	if s != "ABC " {
		t.Errorf("expected \"ABC \" (with trailing space), got %q", s)
	}
	ascDec, _ := codec.NewAsciiCodec(2)
	trimmed, _ := codec.ReadFromClient(client, context.Background(), 1, 0, HoldingRegister, ascDec)
	if trimmed != "ABC" {
		t.Errorf("ReadWithCodec Ascii expected \"ABC\", got %q", trimmed)
	}
}

func TestReadAsciiFixed_ZeroQuantity(t *testing.T) {
	client, err := New(Config{URL: "tcp://127.0.0.1:1", Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_ = client.Open()
	defer func() { _ = client.Close() }()

	_, err = client.ReadRegisterBytes(context.Background(), 1, 0, 0, HoldingRegister)
	if err == nil {
		t.Fatal("expected error for quantity 0")
	}
	if !errors.Is(err, ErrUnexpectedParameters) {
		t.Errorf("expected ErrUnexpectedParameters, got %v", err)
	}
}

func TestReadUint8s_WireOrder(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid, unitID, fc := frame[0:2], frame[6], frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			qty := int(frame[10])<<8 | int(frame[11])
			if qty == 2 {
				payload := []byte{0x04, 0x01, 0x02, 0x03, 0x04}
				_ = writeMBAPRegs(sock, txid, unitID, fc, payload)
			} else {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	b, err := client.ReadRegisterBytes(context.Background(), 1, 0, 4, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisterBytes: %v", err)
	}
	if len(b) != 4 {
		t.Fatalf("expected 4 bytes, got %d", len(b))
	}
	expect := []uint8{0x01, 0x02, 0x03, 0x04}
	for i := range expect {
		if b[i] != expect[i] {
			t.Errorf("byte %d: expected 0x%02x, got 0x%02x", i, expect[i], b[i])
		}
	}
}

func TestReadUint8s_OddByteCount(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid, unitID, fc := frame[0:2], frame[6], frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			qty := int(frame[10])<<8 | int(frame[11])
			if qty == 2 {
				payload := []byte{0x04, 0xAA, 0xBB, 0xCC, 0xDD}
				_ = writeMBAPRegs(sock, txid, unitID, fc, payload)
			} else {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	b, err := client.ReadRegisterBytes(context.Background(), 1, 0, 3, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisterBytes: %v", err)
	}
	if len(b) != 3 {
		t.Fatalf("expected 3 bytes, got %d", len(b))
	}
	if b[0] != 0xAA || b[1] != 0xBB || b[2] != 0xCC {
		t.Errorf("expected [0xAA, 0xBB, 0xCC], got %v", b)
	}
}

func TestReadUint8s_ZeroQuantity(t *testing.T) {
	client, err := New(Config{URL: "tcp://127.0.0.1:1", Timeout: time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_ = client.Open()
	defer func() { _ = client.Close() }()

	_, err = client.ReadRegisterBytes(context.Background(), 1, 0, 0, HoldingRegister)
	if err == nil {
		t.Fatal("expected error for quantity 0")
	}
	if !errors.Is(err, ErrUnexpectedParameters) {
		t.Errorf("expected ErrUnexpectedParameters, got %v", err)
	}
}

func TestReadIPAddr(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid, unitID, fc := frame[0:2], frame[6], frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			qty := int(frame[10])<<8 | int(frame[11])
			if qty == 2 {
				// 192.168.1.10
				payload := []byte{0x04, 192, 168, 1, 10}
				_ = writeMBAPRegs(sock, txid, unitID, fc, payload)
			} else {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	ipDec := codec.NewIPAddrCodec()
	ip, err := codec.ReadFromClient(client, context.Background(), 1, 0, HoldingRegister, ipDec)
	if err != nil {
		t.Fatalf("ReadWithCodec IP: %v", err)
	}
	if len(ip) != 4 {
		t.Fatalf("expected 4 bytes, got %d", len(ip))
	}
	if ip[0] != 192 || ip[1] != 168 || ip[2] != 1 || ip[3] != 10 {
		t.Errorf("expected 192.168.1.10, got %v", ip)
	}
}

func TestReadIPv6Addr(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	expect := net.IP{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}

	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid, unitID, fc := frame[0:2], frame[6], frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			qty := int(frame[10])<<8 | int(frame[11])
			if qty == 8 {
				payload := append([]byte{16}, expect...)
				_ = writeMBAPRegs(sock, txid, unitID, fc, payload)
			} else {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	ipDec := codec.NewIPv6AddrCodec()
	ip, err := codec.ReadFromClient(client, context.Background(), 1, 0, HoldingRegister, ipDec)
	if err != nil {
		t.Fatalf("ReadWithCodec IPv6: %v", err)
	}
	if len(ip) != 16 {
		t.Fatalf("expected 16 bytes, got %d", len(ip))
	}
	if !ip.Equal(expect) {
		t.Errorf("expected %v, got %v", expect, ip)
	}
}

func TestReadEUI48(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	expect := net.HardwareAddr{0x00, 0x1A, 0x2B, 0x3C, 0x4D, 0x5E}

	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid, unitID, fc := frame[0:2], frame[6], frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			qty := int(frame[10])<<8 | int(frame[11])
			if qty == 3 {
				payload := append([]byte{6}, expect...)
				_ = writeMBAPRegs(sock, txid, unitID, fc, payload)
			} else {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	euiDec := codec.NewEUI48Codec()
	hw, err := codec.ReadFromClient(client, context.Background(), 1, 0, HoldingRegister, euiDec)
	if err != nil {
		t.Fatalf("ReadWithCodec EUI48: %v", err)
	}
	if len(hw) != 6 {
		t.Fatalf("expected 6 bytes, got %d", len(hw))
	}
	for i := range expect {
		if hw[i] != expect[i] {
			t.Errorf("byte %d: expected 0x%02x, got 0x%02x", i, expect[i], hw[i])
		}
	}
}

func TestReadHelpers_UnaffectedBySetEncoding(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Raw bytes 1,2,3,4 for IPv4
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid, unitID, fc := frame[0:2], frame[6], frame[7]
			if fc != byte(FCReadHoldingRegisters) {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalFunction))
				continue
			}
			qty := int(frame[10])<<8 | int(frame[11])
			if qty == 2 {
				payload := []byte{0x04, 0x01, 0x02, 0x03, 0x04}
				_ = writeMBAPRegs(sock, txid, unitID, fc, payload)
			} else {
				_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
			}
		}
	}()

	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Raw byte and IP codec always use wire order
	ipCodec := codec.NewIPAddrCodec()
	ip, err := codec.ReadFromClient(client, context.Background(), 1, 0, HoldingRegister, ipCodec)
	if err != nil {
		t.Fatalf("ReadWithCodec IP: %v", err)
	}
	if ip[0] != 1 || ip[1] != 2 || ip[2] != 3 || ip[3] != 4 {
		t.Errorf("expected raw bytes [1,2,3,4], got %v", ip)
	}

	b, err := client.ReadRegisterBytes(context.Background(), 1, 0, 4, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisterBytes: %v", err)
	}
	if b[0] != 1 || b[1] != 2 || b[2] != 3 || b[3] != 4 {
		t.Errorf("expected raw bytes [1,2,3,4], got %v", b)
	}
}

// TestReadHelpers_ErrorPropagation verifies that server exceptions propagate through
// all read helpers (ReadAsciiFixed, ReadUint8s, ReadIPAddr, ReadIPv6Addr, ReadEUI48).
func TestReadHelpers_ErrorPropagation(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	// Server always returns IllegalDataAddress exception
	go func() {
		sock, _ := ln.Accept()
		if sock == nil {
			return
		}
		defer func() { _ = sock.Close() }()
		for {
			frame, err := readMBAPFrame(sock)
			if err != nil {
				return
			}
			txid, unitID, fc := frame[0:2], frame[6], frame[7]
			_ = writeMBAPException(sock, txid, unitID, fc, byte(exIllegalDataAddress))
		}
	}()

	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	ctx := context.Background()

	t.Run("ReadRawBytes", func(t *testing.T) {
		_, err := client.ReadRegisterBytes(ctx, 1, 0, 4, HoldingRegister)
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, ErrIllegalDataAddress) {
			t.Errorf("expected ErrIllegalDataAddress, got %v", err)
		}
	})
	t.Run("ReadIPAddr", func(t *testing.T) {
		ipDec := codec.NewIPAddrCodec()
		_, err := codec.ReadFromClient(client, ctx, 1, 0, HoldingRegister, ipDec)
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, ErrIllegalDataAddress) {
			t.Errorf("expected ErrIllegalDataAddress, got %v", err)
		}
	})
	t.Run("ReadIPv6Addr", func(t *testing.T) {
		ipDec := codec.NewIPv6AddrCodec()
		_, err := codec.ReadFromClient(client, ctx, 1, 0, HoldingRegister, ipDec)
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, ErrIllegalDataAddress) {
			t.Errorf("expected ErrIllegalDataAddress, got %v", err)
		}
	})
	t.Run("ReadEUI48", func(t *testing.T) {
		euiDec := codec.NewEUI48Codec()
		_, err := codec.ReadFromClient(client, ctx, 1, 0, HoldingRegister, euiDec)
		if err == nil {
			t.Fatal("expected error")
		}
		if !errors.Is(err, ErrIllegalDataAddress) {
			t.Errorf("expected ErrIllegalDataAddress, got %v", err)
		}
	})
}
