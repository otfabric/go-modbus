// SPDX-License-Identifier: MIT

package modbus

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// This file plays a hostile device against the client's serial framings
// (RTU and ASCII, tunnelled over TCP so that no pseudo-terminal is needed) and
// against Modbus/TCP and RTU over UDP: garbage before a frame, bad CRC/LRC,
// another unit's answer, cut-short and trickled frames, late answers, and
// datagrams with trailing bytes. The client must return a typed error within
// its deadline and never take bytes of one exchange for the answer to another.

// hostileSerialPeer accepts one connection at a time and hands each request
// frame to fn. Requests are FC03 or FC08 (8 bytes in RTU) or ASCII lines.
type hostileSerialPeer struct {
	ln    net.Listener
	n     atomic.Int64
	wg    sync.WaitGroup
	mu    sync.Mutex
	conns []net.Conn
}

func hostileStartSerialPeer(t *testing.T, ascii bool, fn func(conn net.Conn, n int, req []byte) bool) *hostileSerialPeer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	p := &hostileSerialPeer{ln: ln}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.conns = append(p.conns, conn)
			p.mu.Unlock()
			p.wg.Add(1)
			go func() {
				defer p.wg.Done()
				defer func() { _ = conn.Close() }()
				br := bufio.NewReader(conn)
				for {
					var req []byte
					if ascii {
						line, err := br.ReadBytes('\n')
						if err != nil {
							return
						}
						u, fc, payload, err := adu.ParseASCIIFrame(line)
						if err != nil {
							return
						}
						req = append([]byte{u, fc}, payload...)
					} else {
						req = make([]byte, 8)
						if _, err := ioReadFull(br, req); err != nil {
							return
						}
						req = req[:6]
					}
					if !fn(conn, int(p.n.Add(1)-1), req) {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(p.stop)
	return p
}

func (p *hostileSerialPeer) stop() {
	_ = p.ln.Close()
	p.mu.Lock()
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.mu.Unlock()
	p.wg.Wait()
}

func ioReadFull(br *bufio.Reader, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := br.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// hostileFC03Answer is the well-behaved answer to an FC03 request PDU
// (unit, fc, addr, qty) as (unit, fc, payload).
func hostileFC03Answer(req []byte) (byte, byte, []byte) {
	addr := int(binary.BigEndian.Uint16(req[2:]))
	qty := int(binary.BigEndian.Uint16(req[4:]))
	data := []byte{byte(2 * qty)}
	for i := range qty {
		data = append(data, hostileU16(int(hostileReg(addr+i)))...)
	}
	return req[0], req[1], data
}

func hostileRTUAnswer(req []byte) []byte {
	return adu.AssembleRTUFrame(hostileFC03Answer(req))
}

func hostileASCIIAnswer(req []byte) []byte {
	return adu.AssembleASCIIFrame(hostileFC03Answer(req))
}

// TestHostileSerial_LateAnswerTakenForNext: the device answers the first
// request only after the client has given up on it, and answers the second
// one on time. The serial framings keep their link across a timeout (API.md
// §2.2) and carry no transaction ID, so the late answer is the first frame the
// second request reads.
//
// Required: the second request returns its own registers or an error, never
// the registers of the first.
func TestHostileSerial_LateAnswerTakenForNext(t *testing.T) {
	for _, kind := range []string{"rtuovertcp", "asciiovertcp"} {
		t.Run(kind, func(t *testing.T) {
			const timeout = 200 * time.Millisecond
			ascii := kind == "asciiovertcp"
			answer := hostileRTUAnswer
			if ascii {
				answer = hostileASCIIAnswer
			}
			lateSent := make(chan struct{})
			peer := hostileStartSerialPeer(t, ascii, func(conn net.Conn, n int, req []byte) bool {
				if n == 0 {
					time.Sleep(timeout + 100*time.Millisecond)
					ok := hostileWrite(conn, answer(req))
					close(lateSent)
					return ok
				}
				return hostileWrite(conn, answer(req))
			})
			c := hostileOpenClient(t, Config{URL: kind + "://" + peer.ln.Addr().String(), Timeout: timeout})
			ctx := context.Background()
			if _, err := c.ReadRegisters(ctx, 1, 100, 2, HoldingRegister); !errors.Is(err, ErrRequestTimedOut) {
				t.Fatalf("first request: got %v, want ErrRequestTimedOut", err)
			}
			<-lateSent
			time.Sleep(20 * time.Millisecond) // let the late answer reach the client's socket
			regs, err := c.ReadRegisters(ctx, 1, 200, 2, HoldingRegister)
			if err == nil && (regs[0] != hostileReg(200) || regs[1] != hostileReg(201)) {
				t.Fatalf("second request (addr 200) returned %v with no error: the late answer to the first request (addr 100: [%d %d])",
					regs, hostileReg(100), hostileReg(101))
			}
		})
	}
}

// TestHostileSerial_RTUMalformed: RTU answers that break the framing, each
// followed by a well-behaved answer to the next request.
func TestHostileSerial_RTUMalformed(t *testing.T) {
	const timeout = 300 * time.Millisecond
	crcFlip := func(f []byte) []byte { f[len(f)-1] ^= 0xFF; return f }
	cases := []struct {
		name string
		bad  func(req []byte) []byte
		want error // nil: any error
	}{
		{"bad CRC", func(req []byte) []byte { return crcFlip(hostileRTUAnswer(req)) }, ErrBadCRC},
		{"another unit", func(req []byte) []byte {
			r := append([]byte(nil), req...)
			r[0]++
			return hostileRTUAnswer(r)
		}, ErrBadUnitID},
		{"garbage before the frame", func(req []byte) []byte {
			return append([]byte{0xFF, 0xFF, 0xFF}, hostileRTUAnswer(req)...)
		}, ErrProtocolError},
		{"one noise byte before the frame", func(req []byte) []byte {
			return append([]byte{0x00}, hostileRTUAnswer(req)...)
		}, nil},
		{"byte count 255", func(req []byte) []byte {
			return adu.AssembleRTUFrame(req[0], 0x03, append([]byte{255}, make([]byte, 250)...))
		}, ErrProtocolError},
		{"unknown function code", func(req []byte) []byte {
			return adu.AssembleRTUFrame(req[0], 0x41, []byte{4, 0, 0, 0, 0})
		}, ErrProtocolError},
		{"exception", func(req []byte) []byte { return adu.AssembleRTUFrame(req[0], 0x83, []byte{2}) }, ErrIllegalDataAddress},
		{"exception with bad CRC", func(req []byte) []byte { return crcFlip(adu.AssembleRTUFrame(req[0], 0x83, []byte{2})) }, ErrBadCRC},
		{"cut short", func(req []byte) []byte { f := hostileRTUAnswer(req); return f[:len(f)-3] }, nil},
		{"two bytes only", func(req []byte) []byte { return hostileRTUAnswer(req)[:2] }, nil},
		{"wrong byte count, right CRC", func(req []byte) []byte {
			return adu.AssembleRTUFrame(req[0], 0x03, []byte{2, 0, 1})
		}, ErrProtocolError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := hostileStartSerialPeer(t, false, func(conn net.Conn, n int, req []byte) bool {
				if n == 0 {
					return hostileWrite(conn, tc.bad(req))
				}
				return hostileWrite(conn, hostileRTUAnswer(req))
			})
			c := hostileOpenClient(t, Config{URL: "rtuovertcp://" + peer.ln.Addr().String(), Timeout: timeout})
			start := time.Now()
			regs, err := c.ReadRegisters(context.Background(), 1, 100, 2, HoldingRegister)
			if d := time.Since(start); d > timeout+time.Second {
				t.Errorf("request took %v with a %v timeout", d, timeout)
			}
			if err == nil {
				t.Fatalf("got %v and no error", regs)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			// Let the rest of the bad answer go by, then the link must work.
			time.Sleep(timeout)
			var lastErr error
			for range 3 {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				regs, lastErr = c.ReadRegisters(ctx, 1, 300, 2, HoldingRegister)
				cancel()
				if lastErr == nil {
					break
				}
			}
			if lastErr != nil {
				t.Fatalf("link unusable after the bad answer: %v", lastErr)
			}
			if regs[0] != hostileReg(300) || regs[1] != hostileReg(301) {
				t.Fatalf("after the bad answer: got %v, want the registers at 300", regs)
			}
		})
	}
}

// TestHostileSerial_RTUVariableLengthTrickle: an FC08 answer (whose length an
// RTU client can only learn from line silence) trickled a byte at a time,
// never pausing long enough to end the frame. The request must still end at
// its Timeout (API.md: Timeout is the per-request I/O deadline), and a
// cancelled context must end it promptly.
func TestHostileSerial_RTUVariableLengthTrickle(t *testing.T) {
	const timeout = 200 * time.Millisecond
	const speed = 2400 // t3.5 = 16 ms
	for _, mode := range []string{"timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			peer := hostileStartSerialPeer(t, false, func(conn net.Conn, n int, req []byte) bool {
				if !hostileWrite(conn, []byte{req[0], 0x08, 0x00}) {
					return false
				}
				for range 300 {
					if !hostileWrite(conn, []byte{0x55}) {
						return false
					}
					time.Sleep(6 * time.Millisecond)
				}
				return true
			})
			c := hostileOpenClient(t, Config{URL: "rtuovertcp://" + peer.ln.Addr().String(), Speed: speed, Timeout: timeout})
			ctx := context.Background()
			limit := timeout + 500*time.Millisecond
			if mode == "cancel" {
				c2, cancel := context.WithCancel(ctx)
				time.AfterFunc(100*time.Millisecond, cancel)
				ctx = c2
				c = hostileOpenClient(t, Config{URL: "rtuovertcp://" + peer.ln.Addr().String(), Speed: speed, Timeout: 10 * time.Second})
				limit = 600 * time.Millisecond
			}
			start := time.Now()
			_, err := c.DiagnosticLoopback(ctx, 1, 0x1234)
			d := time.Since(start)
			if err == nil {
				t.Fatal("trickled FC08 answer accepted")
			}
			if d > limit {
				t.Errorf("request took %v (error %v); want at most %v", d, err, limit)
			}
		})
	}
}

// TestHostileSerial_ASCIIMalformed: ASCII answers that break the framing.
func TestHostileSerial_ASCIIMalformed(t *testing.T) {
	const timeout = 300 * time.Millisecond
	cases := []struct {
		name string
		bad  func(req []byte) []byte
		want error
	}{
		{"bad LRC", func(req []byte) []byte {
			f := hostileASCIIAnswer(req)
			f[len(f)-3] ^= 0x01
			return f
		}, ErrBadLRC},
		{"not hex", func(req []byte) []byte { return []byte(":01XX0400\r\n") }, ErrProtocolError},
		{"odd length", func(req []byte) []byte { return []byte(":010\r\n") }, ErrProtocolError},
		{"noise and a stray start", func(req []byte) []byte {
			return append([]byte("garbage:01:"), hostileASCIIAnswer(req)[1:]...)
		}, nil},
		{"endless line without LF", func(req []byte) []byte {
			b := []byte{':'}
			for range 600 {
				b = append(b, '0')
			}
			return b
		}, nil},
		{"another unit", func(req []byte) []byte {
			r := append([]byte(nil), req...)
			r[0]++
			return hostileASCIIAnswer(r)
		}, ErrBadUnitID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := hostileStartSerialPeer(t, true, func(conn net.Conn, n int, req []byte) bool {
				if n == 0 {
					return hostileWrite(conn, tc.bad(req))
				}
				return hostileWrite(conn, hostileASCIIAnswer(req))
			})
			c := hostileOpenClient(t, Config{URL: "asciiovertcp://" + peer.ln.Addr().String(), Timeout: timeout})
			start := time.Now()
			regs, err := c.ReadRegisters(context.Background(), 1, 100, 2, HoldingRegister)
			if d := time.Since(start); d > timeout+500*time.Millisecond {
				t.Errorf("request took %v", d)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("got %v, %v; want %v", regs, err, tc.want)
			}
			if tc.want == nil && err == nil && (regs[0] != hostileReg(100) || regs[1] != hostileReg(101)) {
				t.Fatalf("got wrong registers %v", regs)
			}
			time.Sleep(50 * time.Millisecond)
			regs, err = c.ReadRegisters(context.Background(), 1, 300, 2, HoldingRegister)
			if err != nil || regs[0] != hostileReg(300) {
				t.Fatalf("after the bad answer: %v, %v", regs, err)
			}
		})
	}
}

// hostileUDPPeer answers each datagram with fn's datagrams.
func hostileStartUDPPeer(t *testing.T, fn func(n int, req []byte) [][]byte) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		for n := 0; ; n++ {
			m, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			for _, d := range fn(n, append([]byte(nil), buf[:m]...)) {
				_, _ = pc.WriteTo(d, from)
			}
		}
	}()
	t.Cleanup(func() { _ = pc.Close(); <-done })
	return pc.LocalAddr().String()
}

// TestHostileUDP_Datagrams: Modbus/TCP over UDP (one ADU per datagram) and RTU
// over UDP, with answers that carry trailing bytes, are cut short, come in
// pairs, or are far too long. Each request must return its own registers or
// an error, and the request after it must work: a datagram's bytes may not
// leak into the next exchange.
func TestHostileUDP_Datagrams(t *testing.T) {
	const timeout = 300 * time.Millisecond
	mbapAnswer := func(req []byte) []byte {
		u, fc, data := hostileFC03Answer(req[6:])
		return rawRequestFrame(binary.BigEndian.Uint16(req), u, fc, data)
	}
	rtuAnswer := func(req []byte) []byte { return hostileRTUAnswer(req[:6]) }
	type tcase struct {
		name string
		bad  func(answer []byte) [][]byte
	}
	cases := []tcase{
		{"trailing zero bytes", func(a []byte) [][]byte { return [][]byte{append(a, 0, 0, 0, 0)} }},
		{"trailing copy of the answer", func(a []byte) [][]byte { return [][]byte{append(a, a...)} }},
		{"cut short", func(a []byte) [][]byte { return [][]byte{a[:len(a)-2]} }},
		{"cut short, then the rest", func(a []byte) [][]byte { return [][]byte{a[:len(a)-2], a[len(a)-2:]} }},
		{"1000 bytes", func(a []byte) [][]byte { return [][]byte{append(a, make([]byte, 1000)...)} }},
	}
	for _, kind := range []string{"udp", "rtuoverudp"} {
		for _, tc := range cases {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				addr := hostileStartUDPPeer(t, func(n int, req []byte) [][]byte {
					var a []byte
					if kind == "udp" {
						a = mbapAnswer(req)
					} else {
						a = rtuAnswer(req)
					}
					if n == 0 {
						return tc.bad(a)
					}
					return [][]byte{a}
				})
				c := hostileOpenClient(t, Config{URL: kind + "://" + addr, Timeout: timeout})
				regs, err := c.ReadRegisters(context.Background(), 1, 100, 2, HoldingRegister)
				if err == nil && (regs[0] != hostileReg(100) || regs[1] != hostileReg(101)) {
					t.Fatalf("first request: wrong registers %v", regs)
				}
				time.Sleep(50 * time.Millisecond)
				regs, err = c.ReadRegisters(context.Background(), 1, 300, 2, HoldingRegister)
				if err != nil {
					t.Fatalf("request after the bad datagram: %v", err)
				}
				if regs[0] != hostileReg(300) || regs[1] != hostileReg(301) {
					t.Fatalf("request after the bad datagram: wrong registers %v", regs)
				}
			})
		}
	}
}
