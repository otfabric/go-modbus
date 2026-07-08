// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/codec"
)

// writeMockServer runs a TCP server that accepts FC06 and FC16 and responds with success (echo addr + value/qty).
func writeMockServer(t *testing.T, acceptFC06, acceptFC16 bool) (addr string, cleanup func()) {
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
					if len(frame) < 8 {
						continue
					}
					payload := frame[8:]
					if fc == byte(FCWriteSingleRegister) && acceptFC06 {
						if len(payload) >= 4 {
							_ = writeMBAPNormal(conn, txid, unitID, fc, payload[0:4])
						}
						continue
					}
					if fc == byte(FCWriteMultipleRegisters) && acceptFC16 {
						if len(payload) >= 4 {
							// response: addr (2) + quantity (2)
							_ = writeMBAPNormal(conn, txid, unitID, fc, payload[0:4])
						}
						continue
					}
					_ = writeMBAPException(conn, txid, unitID, fc, byte(exIllegalFunction))
				}
			}(sock)
		}
	}()
	return ln.Addr().String(), func() { _ = ln.Close() }
}

func TestWriteInt16(t *testing.T) {
	addr, cleanup := writeMockServer(t, true, true)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.WriteRegister(context.Background(), 1, 0, 0xFFFF); err != nil { // -1 as int16
		t.Fatalf("WriteRegister: %v", err)
	}
}

func TestWriteInt16s(t *testing.T) {
	addr, cleanup := writeMockServer(t, false, true)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.WriteRegisters(context.Background(), 1, 0, []uint16{1, 0xFFFE, 3}); err != nil { // -2 as int16
		t.Fatalf("WriteRegisters: %v", err)
	}
}

func TestWriteInt32(t *testing.T) {
	addr, cleanup := writeMockServer(t, true, true)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	c := codec.MustNewInt32Codec(codec.Layout32_4321)
	if err := codec.WriteToClient(client, context.Background(), 1, 0, int32(-123456789), c); err != nil {
		t.Fatalf("WriteWithCodec int32: %v", err)
	}
}

func TestWriteInt32s(t *testing.T) {
	addr, cleanup := writeMockServer(t, false, true)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	c := codec.MustNewInt32Codec(codec.Layout32_4321)
	for i, v := range []int32{1, -1} {
		if err := codec.WriteToClient(client, context.Background(), 1, uint16(i*2), v, c); err != nil {
			t.Fatalf("WriteWithCodec int32: %v", err)
		}
	}
}

func TestWriteInt48(t *testing.T) {
	addr, cleanup := writeMockServer(t, false, true)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	c := codec.MustNewInt48Codec(codec.Layout48_654321)
	if err := codec.WriteToClient(client, context.Background(), 1, 0, int64(0x123456789ABC), c); err != nil {
		t.Fatalf("WriteWithCodec int48: %v", err)
	}
}

func TestWriteInt48s(t *testing.T) {
	addr, cleanup := writeMockServer(t, false, true)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	c := codec.MustNewInt48Codec(codec.Layout48_654321)
	for i, v := range []int64{1, 2} {
		if err := codec.WriteToClient(client, context.Background(), 1, uint16(i*3), v, c); err != nil {
			t.Fatalf("WriteWithCodec int48: %v", err)
		}
	}
}

func TestWriteInt64(t *testing.T) {
	addr, cleanup := writeMockServer(t, false, true)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	c := codec.MustNewInt64Codec(codec.Layout64_87654321)
	if err := codec.WriteToClient(client, context.Background(), 1, 0, int64(-1), c); err != nil {
		t.Fatalf("WriteWithCodec int64: %v", err)
	}
}

func TestWriteInt64s(t *testing.T) {
	addr, cleanup := writeMockServer(t, false, true)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	c := codec.MustNewInt64Codec(codec.Layout64_87654321)
	for i, v := range []int64{0, 1} {
		if err := codec.WriteToClient(client, context.Background(), 1, uint16(i*4), v, c); err != nil {
			t.Fatalf("WriteWithCodec int64: %v", err)
		}
	}
}

func TestWriteAscii(t *testing.T) {
	addr, cleanup := writeMockServer(t, false, true)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	c, _ := codec.NewAsciiCodec(1)
	if err := codec.WriteToClient(client, context.Background(), 1, 0, "Hi", c); err != nil {
		t.Fatalf("WriteWithCodec Ascii: %v", err)
	}
}

func TestWriteAsciiFixed(t *testing.T) {
	addr, cleanup := writeMockServer(t, false, true)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	c, _ := codec.NewAsciiFixedCodec(2)
	if err := codec.WriteToClient(client, context.Background(), 1, 0, "AB ", c); err != nil {
		t.Fatalf("WriteWithCodec AsciiFixed: %v", err)
	}
}

func TestWriteAsciiReverse(t *testing.T) {
	addr, cleanup := writeMockServer(t, false, true)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	c, _ := codec.NewAsciiReverseCodec(1)
	if err := codec.WriteToClient(client, context.Background(), 1, 0, "Hi", c); err != nil {
		t.Fatalf("WriteWithCodec AsciiReverse: %v", err)
	}
}

func TestWriteBCD(t *testing.T) {
	addr, cleanup := writeMockServer(t, false, true)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	c, _ := codec.NewBCDCodec(2)
	if err := codec.WriteToClient(client, context.Background(), 1, 0, "1234", c); err != nil {
		t.Fatalf("WriteWithCodec BCD: %v", err)
	}
}

func TestWritePackedBCD(t *testing.T) {
	addr, cleanup := writeMockServer(t, false, true)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	c, _ := codec.NewPackedBCDCodec(1)
	if err := codec.WriteToClient(client, context.Background(), 1, 0, "92", c); err != nil {
		t.Fatalf("WriteWithCodec PackedBCD: %v", err)
	}
}

func TestWriteUint8s(t *testing.T) {
	addr, cleanup := writeMockServer(t, false, true)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.WriteRegisterBytes(context.Background(), 1, 0, []byte{0xC0, 0xA8, 0x01, 0x0A}); err != nil {
		t.Fatalf("WriteRawBytes: %v", err)
	}
}

func TestWriteIPAddr(t *testing.T) {
	addr, cleanup := writeMockServer(t, false, true)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	ip := net.IP{192, 168, 1, 10}
	c := codec.NewIPAddrCodec()
	if err := codec.WriteToClient(client, context.Background(), 1, 0, ip, c); err != nil {
		t.Fatalf("WriteWithCodec IP: %v", err)
	}
}

func TestWriteIPv6Addr(t *testing.T) {
	addr, cleanup := writeMockServer(t, false, true)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	ip := net.IP{0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
	c := codec.NewIPv6AddrCodec()
	if err := codec.WriteToClient(client, context.Background(), 1, 0, ip, c); err != nil {
		t.Fatalf("WriteWithCodec IPv6: %v", err)
	}
}

func TestWriteEUI48(t *testing.T) {
	addr, cleanup := writeMockServer(t, false, true)
	defer cleanup()
	client, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	mac := net.HardwareAddr{0x00, 0x1A, 0x2B, 0x3C, 0x4D, 0x5E}
	c := codec.NewEUI48Codec()
	if err := codec.WriteToClient(client, context.Background(), 1, 0, mac, c); err != nil {
		t.Fatalf("WriteWithCodec EUI48: %v", err)
	}
}

func TestWriteHelpers_InvalidInputs(t *testing.T) {
	addr, cleanup := writeMockServer(t, false, true)
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

	if err := client.WriteRegisters(ctx, 1, 0, nil); err == nil {
		t.Error("WriteRegisters(nil) should error")
	}
	if err := client.WriteRegisters(ctx, 1, 0, []uint16{}); err == nil {
		t.Error("WriteRegisters(empty) should error")
	}
	// AsciiFixedCodec with empty string may encode to zero registers (codec-defined).
	bcdCodec, _ := codec.NewBCDCodec(2)
	if err := codec.WriteToClient(client, ctx, 1, 0, "12a4", bcdCodec); err == nil {
		t.Error("WriteWithCodec BCD(non-digit) should error")
	}
	packedCodec, _ := codec.NewPackedBCDCodec(1)
	if err := codec.WriteToClient(client, ctx, 1, 0, "9x", packedCodec); err == nil {
		t.Error("WriteWithCodec PackedBCD(non-digit) should error")
	}
	if err := client.WriteRegisterBytes(ctx, 1, 0, nil); err == nil {
		t.Error("WriteRegisterBytes(nil) should error")
	}
	ipCodec := codec.NewIPAddrCodec()
	if err := codec.WriteToClient(client, ctx, 1, 0, net.IP(nil), ipCodec); err == nil {
		t.Error("WriteWithCodec IP(nil) should error")
	}
	euiCodec := codec.NewEUI48Codec()
	if err := codec.WriteToClient(client, ctx, 1, 0, net.HardwareAddr(nil), euiCodec); err == nil {
		t.Error("WriteWithCodec EUI48(nil) should error")
	}
	if err := codec.WriteToClient(client, ctx, 1, 0, net.HardwareAddr{1, 2, 3}, euiCodec); err == nil {
		t.Error("WriteWithCodec EUI48(short) should error")
	}
}
