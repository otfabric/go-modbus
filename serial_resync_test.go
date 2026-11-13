// SPDX-License-Identifier: MIT

package modbus

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// resyncDevice is a device behind an RTU or ASCII gateway, reached over TCP or
// UDP. Its holding register N holds N unless written. When lateFirst is set the
// answer to the very first request is held back until the next request arrives
// on the same connection (or datagram socket), and is then sent before that
// request's own answer: an answer that comes after its request timed out.
type resyncDevice struct {
	ascii     bool
	lateFirst bool
	// badEcho makes the answer to the first FC06 echo another value.
	badEcho bool

	mu     sync.Mutex
	regs   map[uint16]uint16
	served int
}

func newResyncDevice(ascii, lateFirst bool) *resyncDevice {
	return &resyncDevice{ascii: ascii, lateFirst: lateFirst, regs: map[uint16]uint16{600: 0x00F0}}
}

func (d *resyncDevice) reg(addr uint16) uint16 {
	d.mu.Lock()
	defer d.mu.Unlock()
	if v, ok := d.regs[addr]; ok {
		return v
	}
	return addr
}

// first reports whether this is the first request the device sees.
func (d *resyncDevice) first() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.served++
	return d.served == 1 && d.lateFirst
}

func (d *resyncDevice) parse(frame []byte) (uint8, byte, []byte, error) {
	if d.ascii {
		return adu.ParseASCIIFrame(frame)
	}
	if len(frame) < 4 || !adu.ValidateRTUCRC(frame) {
		return 0, 0, nil, errors.New("bad frame")
	}
	u, fc, p := adu.ParseRTUFrame(frame)
	return u, fc, p, nil
}

// readStream reads one request from a TCP stream.
func (d *resyncDevice) readStream(r *bufio.Reader) ([]byte, error) {
	if d.ascii {
		return r.ReadBytes('\n')
	}
	hdr := make([]byte, 8)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	if hdr[1] == 0x10 {
		// FC16: unit, FC, address, quantity, byte count, values, CRC. The
		// first 8 bytes end with the byte count and the first value byte.
		more := make([]byte, int(hdr[6])-1+2)
		if _, err := io.ReadFull(r, more); err != nil {
			return nil, err
		}
		return append(hdr, more...), nil
	}
	return hdr, nil
}

func (d *resyncDevice) answer(frame []byte) []byte {
	u, fc, p, err := d.parse(frame)
	if err != nil {
		return nil
	}
	var out []byte
	switch fc {
	case 0x03:
		addr := binary.BigEndian.Uint16(p[0:2])
		v := d.reg(addr)
		out = []byte{2, byte(v >> 8), byte(v)}
	case 0x06:
		d.mu.Lock()
		d.regs[binary.BigEndian.Uint16(p[0:2])] = binary.BigEndian.Uint16(p[2:4])
		out = append([]byte{}, p[:4]...)
		if d.badEcho {
			d.badEcho = false
			out[3]++
		}
		d.mu.Unlock()
	case 0x10:
		d.mu.Lock()
		d.regs[binary.BigEndian.Uint16(p[0:2])] = binary.BigEndian.Uint16(p[5:7])
		d.mu.Unlock()
		out = append([]byte{}, p[:4]...)
	default:
		return nil
	}
	if d.ascii {
		return adu.AssembleASCIIFrame(u, fc, out)
	}
	return adu.AssembleRTUFrame(u, fc, out)
}

func (d *resyncDevice) serveTCP(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = conn.Close() }()
			r := bufio.NewReader(conn)
			var late []byte
			for {
				req, err := d.readStream(r)
				if err != nil {
					return
				}
				if d.first() {
					late = d.answer(req)
					continue
				}
				if late != nil {
					if _, err := conn.Write(late); err != nil {
						return
					}
					late = nil
				}
				if _, err := conn.Write(d.answer(req)); err != nil {
					return
				}
			}
		}()
	}
}

func (d *resyncDevice) serveUDP(pc net.PacketConn) {
	buf := make([]byte, 512)
	var late []byte
	var lateTo net.Addr
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		req := append([]byte{}, buf[:n]...)
		if d.first() {
			late, lateTo = d.answer(req), from
			continue
		}
		if late != nil {
			// Sent to where the first request came from.
			_, _ = pc.WriteTo(late, lateTo)
			late = nil
		}
		_, _ = pc.WriteTo(d.answer(req), from)
	}
}

// startResyncDevice serves d on a loopback endpoint for scheme.
func startResyncDevice(t *testing.T, scheme string, d *resyncDevice) string {
	t.Helper()
	if scheme == "rtuoverudp" {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pc.Close() })
		go d.serveUDP(pc)
		return pc.LocalAddr().String()
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go d.serveTCP(ln)
	return ln.Addr().String()
}

// After an answer that arrives once its request has timed out, the tunnelled
// serial framings do not read it as the answer to a later request: the socket
// is replaced after the timeout. Reads return their own registers, and a
// client-side read-modify-write writes the value derived from the register it
// read.
func TestSerialTunnelLateAnswerNotMisread(t *testing.T) {
	for _, scheme := range []string{"rtuovertcp", "asciiovertcp", "rtuoverudp"} {
		t.Run(scheme, func(t *testing.T) {
			d := newResyncDevice(scheme == "asciiovertcp", true)
			addr := startResyncDevice(t, scheme, d)
			c, err := New(Config{URL: scheme + "://" + addr, Timeout: 150 * time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Open(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			ctx := context.Background()

			if _, err := c.ReadRegister(ctx, 1, 100, HoldingRegister); !errors.Is(err, ErrRequestTimedOut) {
				t.Fatalf("first read: %v, want a timeout", err)
			}
			time.Sleep(50 * time.Millisecond)
			for _, a := range []uint16{200, 300, 400} {
				v, err := c.ReadRegister(ctx, 1, a, HoldingRegister)
				if err != nil || v != a {
					t.Fatalf("read %d: %d, %v; want %d", a, v, err, a)
				}
			}
			if err := c.WriteRegister(ctx, 1, 500, 7); err != nil {
				t.Fatalf("WriteRegister: %v", err)
			}
			if err := c.UpdateRegisterMask(ctx, 1, 600, 0x0001, 0x0001); err != nil {
				t.Fatalf("UpdateRegisterMask: %v", err)
			}
			if got := d.reg(600); got != 0x00F1 {
				t.Fatalf("register 600 = 0x%04X after setting bit 0 of 0x00F0, want 0x00F1", got)
			}
			if v, err := c.ReadRegister(ctx, 1, 430, HoldingRegister); err != nil || v != 430 {
				t.Fatalf("read 430: %d, %v", v, err)
			}
		})
	}
}

// An exchange whose context ends after the request was written leaves no
// answer behind for the next request on a tunnelled RTU link.
func TestRTUOverTCPCancelAfterWriteNotMisread(t *testing.T) {
	for _, tc := range []struct {
		name     string
		speed    uint
		after    time.Duration
		deadline bool
	}{
		{"cancel during gap 1200", 1200, 40 * time.Millisecond, false},
		{"deadline during gap 19200", 19200, 3 * time.Millisecond, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newResyncDevice(false, false)
			addr := startResyncDevice(t, "rtuovertcp", d)
			c, err := New(Config{URL: "rtuovertcp://" + addr, Speed: tc.speed, Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Open(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()

			var ctx1 context.Context
			var cancel context.CancelFunc
			if tc.deadline {
				ctx1, cancel = context.WithTimeout(context.Background(), tc.after)
			} else {
				ctx1, cancel = context.WithCancel(context.Background())
				time.AfterFunc(tc.after, cancel)
			}
			defer cancel()
			_, _ = c.ReadRegister(ctx1, 1, 100, HoldingRegister) // may or may not complete
			time.Sleep(20 * time.Millisecond)
			ctx := context.Background()
			for _, a := range []uint16{200, 300} {
				v, err := c.ReadRegister(ctx, 1, a, HoldingRegister)
				if err != nil || v != a {
					t.Fatalf("read %d: %d, %v; want %d", a, v, err, a)
				}
			}
			if err := c.UpdateRegisterMask(ctx, 1, 600, 0x0001, 0x0001); err != nil {
				t.Fatalf("UpdateRegisterMask: %v", err)
			}
			if got := d.reg(600); got != 0x00F1 {
				t.Fatalf("register 600 = 0x%04X, want 0x00F1", got)
			}
		})
	}
}

// A response that does not match its request (here a write echo with another
// value) makes a tunnelled serial client replace its socket.
func TestSerialTunnelSuspectResponseRedials(t *testing.T) {
	for _, scheme := range []string{"rtuovertcp", "asciiovertcp", "rtuoverudp"} {
		t.Run(scheme, func(t *testing.T) {
			d := newResyncDevice(scheme == "asciiovertcp", false)
			d.badEcho = true
			addr := startResyncDevice(t, scheme, d)
			var dials int
			var mu sync.Mutex
			c, err := New(Config{
				URL:     scheme + "://" + addr,
				Timeout: time.Second,
				Dialer: func(ctx context.Context, network, addr string) (net.Conn, error) {
					mu.Lock()
					dials++
					mu.Unlock()
					var nd net.Dialer
					return nd.DialContext(ctx, network, addr)
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Open(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			ctx := context.Background()
			if err := c.WriteRegister(ctx, 1, 500, 7); err == nil {
				t.Fatal("WriteRegister with a wrong echo succeeded")
			}
			if v, err := c.ReadRegister(ctx, 1, 200, HoldingRegister); err != nil || v != 200 {
				t.Fatalf("read 200: %d, %v", v, err)
			}
			mu.Lock()
			defer mu.Unlock()
			if dials != 2 {
				t.Fatalf("%d dials, want 2 (the socket is replaced after the wrong echo)", dials)
			}
		})
	}
}

func TestSerialResponseOutOfSync(t *testing.T) {
	req := func(fc byte, p ...byte) *adu.Request { return &adu.Request{UnitID: 1, FunctionCode: fc, Payload: p} }
	res := func(unit uint8, fc byte, p ...byte) *adu.Response {
		return &adu.Response{UnitID: unit, FunctionCode: fc, Payload: p}
	}
	for _, tc := range []struct {
		name string
		req  *adu.Request
		res  *adu.Response
		want bool
	}{
		{"read ok", req(0x03, 0, 1, 0, 1), res(1, 0x03, 2, 0, 1), false},
		{"other unit", req(0x03, 0, 1, 0, 1), res(2, 0x03, 2, 0, 1), true},
		{"other function", req(0x03, 0, 1, 0, 1), res(1, 0x06, 0, 1, 0, 1), true},
		{"exception", req(0x06, 0, 1, 0, 7), res(1, 0x86, 2), false},
		{"FC06 echo", req(0x06, 0, 1, 0, 7), res(1, 0x06, 0, 1, 0, 7), false},
		{"FC06 other echo", req(0x06, 0, 1, 0, 7), res(1, 0x06, 0, 2, 0, 7), true},
		{"FC05 other echo", req(0x05, 0, 1, 0xFF, 0), res(1, 0x05, 0, 1, 0, 0), true},
		{"FC16 echo", req(0x10, 0, 9, 0, 1, 2, 0, 5), res(1, 0x10, 0, 9, 0, 1), false},
		{"FC16 other echo", req(0x10, 0, 9, 0, 1, 2, 0, 5), res(1, 0x10, 0, 8, 0, 1), true},
		{"FC15 short request", req(0x0F, 0, 9), res(1, 0x0F, 0, 9, 0, 1), true},
		{"FC22 echo", req(0x16, 0, 1, 0, 2, 0, 3), res(1, 0x16, 0, 1, 0, 2, 0, 3), false},
		{"FC22 other echo", req(0x16, 0, 1, 0, 2, 0, 3), res(1, 0x16, 0, 1, 0, 2, 0, 4), true},
		{"diagnostics", req(0x08, 0, 0, 1, 2), res(1, 0x08, 0, 0, 1, 3), false},
	} {
		if got := serialResponseOutOfSync(tc.req, tc.res); got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}
