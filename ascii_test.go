// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	inttrans "github.com/otfabric/go-modbus/internal/transport"
)

// startASCIISlave serves Modbus ASCII over TCP on a loopback port, answering
// each request with handle's result. It stands in for a serial-to-Ethernet
// bridge with an ASCII device behind it.
func startASCIISlave(t *testing.T, handle func(req *adu.Request) *adu.Response) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				at := inttrans.NewASCII(conn, 5*time.Second, NopLogger())
				for {
					req, _, err := at.ReadRequest()
					if err != nil {
						return
					}
					if res := handle(req); res != nil {
						if err := at.WriteResponse(res); err != nil {
							return
						}
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func TestASCIIOverTCPClient(t *testing.T) {
	holding := map[uint16]uint16{0x10: 0x1234, 0x11: 0xABCD}
	addr := startASCIISlave(t, func(req *adu.Request) *adu.Response {
		res := &adu.Response{UnitID: req.UnitID, FunctionCode: req.FunctionCode}
		start := uint16(req.Payload[0])<<8 | uint16(req.Payload[1])
		switch FunctionCode(req.FunctionCode) {
		case FCReadHoldingRegisters:
			qty := uint16(req.Payload[2])<<8 | uint16(req.Payload[3])
			res.Payload = []byte{byte(qty * 2)}
			for i := uint16(0); i < qty; i++ {
				v, ok := holding[start+i]
				if !ok {
					res.FunctionCode |= 0x80
					res.Payload = []byte{0x02}
					return res
				}
				res.Payload = append(res.Payload, byte(v>>8), byte(v))
			}
		case FCWriteSingleRegister:
			holding[start] = uint16(req.Payload[2])<<8 | uint16(req.Payload[3])
			res.Payload = req.Payload
		default:
			res.FunctionCode |= 0x80
			res.Payload = []byte{0x01}
		}
		return res
	})

	client, err := New(Config{URL: "asciiovertcp://" + addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	regs, err := client.ReadRegisters(ctx, 7, 0x10, 2, HoldingRegister)
	if err != nil {
		t.Fatalf("ReadRegisters: %v", err)
	}
	if len(regs) != 2 || regs[0] != 0x1234 || regs[1] != 0xABCD {
		t.Fatalf("ReadRegisters: got %04X", regs)
	}

	if err := client.WriteRegister(ctx, 7, 0x10, 0x4321); err != nil {
		t.Fatalf("WriteRegister: %v", err)
	}
	if regs, err = client.ReadRegisters(ctx, 7, 0x10, 1, HoldingRegister); err != nil || regs[0] != 0x4321 {
		t.Fatalf("read back: regs=%04X err=%v", regs, err)
	}

	// Exceptions are carried through ASCII framing unchanged.
	if _, err = client.ReadRegisters(ctx, 7, 0x100, 1, HoldingRegister); !errors.Is(err, ErrIllegalDataAddress) {
		t.Fatalf("want ErrIllegalDataAddress, got %v", err)
	}
	if _, err = client.ReadCoils(ctx, 7, 0, 1); !errors.Is(err, ErrIllegalFunction) {
		t.Fatalf("want ErrIllegalFunction, got %v", err)
	}
	if id := client.LastObservedTransactionID(); id != 0 {
		t.Fatalf("LastObservedTransactionID: want 0, got %d", id)
	}
}

func TestASCIIOverTCPClientTimeout(t *testing.T) {
	addr := startASCIISlave(t, func(*adu.Request) *adu.Response { return nil })

	client, err := New(Config{URL: "asciiovertcp://" + addr, Timeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	if _, err := client.ReadRegisters(context.Background(), 1, 0, 1, HoldingRegister); !errors.Is(err, ErrRequestTimedOut) {
		t.Fatalf("want ErrRequestTimedOut, got %v", err)
	}
}

func TestASCIIOverTCPClientBadLRC(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 64)
		if _, err := conn.Read(buf); err != nil {
			return
		}
		_, _ = conn.Write([]byte(":0103021234FF\r\n"))
	}()

	client, err := New(Config{URL: "asciiovertcp://" + ln.Addr().String(), Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	if _, err := client.ReadRegisters(context.Background(), 1, 0, 1, HoldingRegister); !errors.Is(err, ErrBadLRC) {
		t.Fatalf("want ErrBadLRC, got %v", err)
	}
}

func TestASCIIClientDefaults(t *testing.T) {
	t.Run("serial without parity", func(t *testing.T) {
		c, err := New(Config{URL: "ascii:///dev/ttyUSB0"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.conf.Speed != 19200 || c.conf.DataBits != 7 || c.conf.StopBits != 2 || c.conf.Timeout != time.Second {
			t.Fatalf("defaults: %+v", c.conf)
		}
		info := c.Info()
		if info.Transport != TransportASCII || info.Endpoint != "/dev/ttyUSB0" || info.PoolEnabled {
			t.Fatalf("info: %+v", info)
		}
	})
	t.Run("serial with parity", func(t *testing.T) {
		c, err := New(Config{URL: "ascii:///dev/ttyUSB0", Parity: ParityEven, Speed: 9600, DataBits: 8})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.conf.Speed != 9600 || c.conf.DataBits != 8 || c.conf.StopBits != 1 {
			t.Fatalf("defaults: %+v", c.conf)
		}
	})
	t.Run("serial ignores pooling", func(t *testing.T) {
		c, err := New(Config{URL: "ascii:///dev/ttyUSB0", MaxConns: 4, MinConns: 2})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if c.conf.MaxConns != 1 || c.conf.MinConns != 0 {
			t.Fatalf("pool settings not reset: %+v", c.conf)
		}
	})
	t.Run("over tcp", func(t *testing.T) {
		c, err := New(Config{URL: "asciiovertcp://127.0.0.1:502", MaxConns: 4})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		info := c.Info()
		if info.Transport != TransportASCIIOverTCP || !info.PoolEnabled || c.conf.Timeout != time.Second {
			t.Fatalf("info: %+v conf: %+v", info, c.conf)
		}
	})
	t.Run("open failures", func(t *testing.T) {
		c, err := New(Config{URL: "ascii:///nonexistent/modbus-ascii-test-port"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := c.Open(); err == nil {
			_ = c.Close()
			t.Fatal("Open: want error for missing serial device")
		}

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		refused := ln.Addr().String()
		_ = ln.Close()
		c, err = New(Config{URL: "asciiovertcp://" + refused, DialTimeout: 500 * time.Millisecond})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := c.Open(); err == nil {
			_ = c.Close()
			t.Fatal("Open: want error for refused connection")
		}
	})
}
