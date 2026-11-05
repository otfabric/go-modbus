// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This file plays a hostile Modbus/TCP server against the go-modbus client:
// responses with the wrong transaction ID, unit ID or function code, byte
// counts that disagree with the request, truncated and trickled frames,
// exception responses with odd codes, unsolicited frames, huge MBAP lengths,
// and FC43 / FC24 replies that break their own rules. The client must return
// a typed error from ERRORS.md within its deadline, never panic or return
// another request's data, and work again on the next request.

// hostileExchange is what the peer saw of one request.
type hostileExchange struct {
	n       int // index of the request across all connections, from 0
	txn     uint16
	unit    byte
	fc      byte
	payload []byte
}

// hostilePeerFn answers one request by writing whatever it likes to conn. It
// returns false to close the connection.
type hostilePeerFn func(conn net.Conn, ex hostileExchange) bool

type hostilePeer struct {
	ln       net.Listener
	fn       hostilePeerFn
	n        atomic.Int64
	accepted atomic.Int64
	wg       sync.WaitGroup
	mu       sync.Mutex
	conns    []net.Conn
}

func hostileStartPeer(t *testing.T, fn hostilePeerFn) *hostilePeer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	p := &hostilePeer{ln: ln, fn: fn}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			p.accepted.Add(1)
			p.mu.Lock()
			p.conns = append(p.conns, conn)
			p.mu.Unlock()
			p.wg.Add(1)
			go p.serve(conn)
		}
	}()
	t.Cleanup(p.stop)
	return p
}

func (p *hostilePeer) stop() {
	_ = p.ln.Close()
	p.mu.Lock()
	for _, c := range p.conns {
		_ = c.Close()
	}
	p.mu.Unlock()
	p.wg.Wait()
}

func (p *hostilePeer) serve(conn net.Conn) {
	defer p.wg.Done()
	defer func() { _ = conn.Close() }()
	for {
		hdr := make([]byte, 7)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			return
		}
		length := int(binary.BigEndian.Uint16(hdr[4:]))
		if length < 2 {
			return
		}
		body := make([]byte, length-1)
		if _, err := io.ReadFull(conn, body); err != nil {
			return
		}
		ex := hostileExchange{
			n:   int(p.n.Add(1) - 1),
			txn: binary.BigEndian.Uint16(hdr), unit: hdr[6], fc: body[0], payload: body[1:],
		}
		if !p.fn(conn, ex) {
			return
		}
	}
}

func (p *hostilePeer) url() string { return "tcp://" + p.ln.Addr().String() }

// hostileGoodAnswer is a well-behaved answer to the requests these tests send.
func hostileGoodAnswer(ex hostileExchange) []byte {
	switch ex.fc {
	case 0x03, 0x04:
		addr := int(binary.BigEndian.Uint16(ex.payload))
		qty := int(binary.BigEndian.Uint16(ex.payload[2:]))
		data := []byte{byte(2 * qty)}
		for i := range qty {
			data = append(data, hostileU16(int(hostileReg(addr+i)))...)
		}
		return rawRequestFrame(ex.txn, ex.unit, ex.fc, data)
	default:
		return rawRequestFrame(ex.txn, ex.unit, ex.fc|0x80, []byte{1})
	}
}

func hostileWrite(conn net.Conn, b []byte) bool {
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, err := conn.Write(b)
	return err == nil
}

func hostileOpenClient(t *testing.T, conf Config) *Client {
	t.Helper()
	c, err := New(conf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// hostileExpectGood asserts a register read on c returns the right data.
func hostileExpectGood(t *testing.T, c *Client, addr uint16) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	regs, err := c.ReadRegisters(ctx, 1, addr, 3, HoldingRegister)
	if err != nil {
		t.Fatalf("well-behaved answer after the hostile one: %v", err)
	}
	for i, v := range regs {
		if v != hostileReg(int(addr)+i) {
			t.Fatalf("well-behaved answer after the hostile one: regs %v: wrong data at %d", regs, i)
		}
	}
}

func TestHostileClient_MalformedReadResponses(t *testing.T) {
	const timeout = 300 * time.Millisecond
	type check func(t *testing.T, regs []uint16, err error)
	wantIs := func(target error) check {
		return func(t *testing.T, regs []uint16, err error) {
			t.Helper()
			if !errors.Is(err, target) {
				t.Fatalf("got %v, %v; want %v", regs, err, target)
			}
		}
	}
	wantException := func(code ExceptionCode) check {
		return func(t *testing.T, regs []uint16, err error) {
			t.Helper()
			var exc *ExceptionError
			if !errors.As(err, &exc) || exc.ExceptionCode != code || exc.FunctionCode != FCReadHoldingRegisters {
				t.Fatalf("got %v, %v; want *ExceptionError with code 0x%02x", regs, err, byte(code))
			}
			_ = exc.Error()
		}
	}
	wantData := func(t *testing.T, regs []uint16, err error) {
		t.Helper()
		if err != nil || len(regs) != 2 || regs[0] != hostileReg(100) || regs[1] != hostileReg(101) {
			t.Fatalf("got %v, %v; want the right registers", regs, err)
		}
	}
	wantAnyErr := func(t *testing.T, regs []uint16, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("got %v and no error", regs)
		}
	}
	good := func(ex hostileExchange) []byte { return hostileGoodAnswer(ex) }

	cases := []struct {
		name string
		bad  func(conn net.Conn, ex hostileExchange) bool
		want check
	}{
		{"wrong transaction ID forever", func(conn net.Conn, ex hostileExchange) bool {
			for i := range 12 {
				g := ex
				g.txn = ex.txn + 1 + uint16(i)
				if !hostileWrite(conn, good(g)) {
					return false
				}
			}
			return true
		}, wantIs(ErrBadTransactionID)},
		{"stale frames, then the answer", func(conn net.Conn, ex hostileExchange) bool {
			for i := range 5 {
				g := ex
				g.txn = ex.txn - 1 - uint16(i)
				g.payload = hostileU16(999, 2)
				hostileWrite(conn, good(g))
			}
			return hostileWrite(conn, good(ex))
		}, wantData},
		{"unknown protocol ID forever", func(conn net.Conn, ex hostileExchange) bool {
			f := good(ex)
			f[3] = 1
			for range 12 {
				if !hostileWrite(conn, f) {
					return false
				}
			}
			return true
		}, wantIs(ErrUnknownProtocolID)},
		{"unknown protocol ID, then the answer", func(conn net.Conn, ex hostileExchange) bool {
			f := good(ex)
			f[2] = 0x12
			hostileWrite(conn, f)
			return hostileWrite(conn, good(ex))
		}, wantData},
		{"wrong unit ID", func(conn net.Conn, ex hostileExchange) bool {
			ex.unit++
			return hostileWrite(conn, good(ex))
		}, wantIs(ErrBadUnitID)},
		{"wrong function code", func(conn net.Conn, ex hostileExchange) bool {
			ex.fc = 0x04
			return hostileWrite(conn, good(ex))
		}, wantIs(ErrProtocolError)},
		{"exception without code", func(conn net.Conn, ex hostileExchange) bool {
			return hostileWrite(conn, rawRequestFrame(ex.txn, ex.unit, 0x83, nil))
		}, wantIs(ErrProtocolError)},
		{"exception with two code bytes", func(conn net.Conn, ex hostileExchange) bool {
			return hostileWrite(conn, rawRequestFrame(ex.txn, ex.unit, 0x83, []byte{2, 2}))
		}, wantIs(ErrProtocolError)},
		{"exception code 0x00", func(conn net.Conn, ex hostileExchange) bool {
			return hostileWrite(conn, rawRequestFrame(ex.txn, ex.unit, 0x83, []byte{0x00}))
		}, wantException(0x00)},
		{"exception code 0x07", func(conn net.Conn, ex hostileExchange) bool {
			return hostileWrite(conn, rawRequestFrame(ex.txn, ex.unit, 0x83, []byte{0x07}))
		}, wantException(0x07)},
		{"exception code 0x09", func(conn net.Conn, ex hostileExchange) bool {
			return hostileWrite(conn, rawRequestFrame(ex.txn, ex.unit, 0x83, []byte{0x09}))
		}, wantException(0x09)},
		{"exception code 0xFF", func(conn net.Conn, ex hostileExchange) bool {
			return hostileWrite(conn, rawRequestFrame(ex.txn, ex.unit, 0x83, []byte{0xFF}))
		}, wantException(0xFF)},
		{"exception code 0x0B", func(conn net.Conn, ex hostileExchange) bool {
			return hostileWrite(conn, rawRequestFrame(ex.txn, ex.unit, 0x83, []byte{0x0B}))
		}, wantIs(ErrGWTargetFailedToRespond)},
		{"byte count larger than data", func(conn net.Conn, ex hostileExchange) bool {
			return hostileWrite(conn, rawRequestFrame(ex.txn, ex.unit, 0x03, []byte{4, 0, 1}))
		}, wantIs(ErrProtocolError)},
		{"byte count for 3 registers", func(conn net.Conn, ex hostileExchange) bool {
			return hostileWrite(conn, rawRequestFrame(ex.txn, ex.unit, 0x03, []byte{6, 0, 1, 0, 2, 0, 3}))
		}, wantIs(ErrProtocolError)},
		{"byte count for 1 register", func(conn net.Conn, ex hostileExchange) bool {
			return hostileWrite(conn, rawRequestFrame(ex.txn, ex.unit, 0x03, []byte{2, 0, 1}))
		}, wantIs(ErrProtocolError)},
		{"odd byte count", func(conn net.Conn, ex hostileExchange) bool {
			return hostileWrite(conn, rawRequestFrame(ex.txn, ex.unit, 0x03, []byte{3, 0, 1, 0}))
		}, wantIs(ErrProtocolError)},
		{"empty payload", func(conn net.Conn, ex hostileExchange) bool {
			return hostileWrite(conn, rawRequestFrame(ex.txn, ex.unit, 0x03, nil))
		}, wantIs(ErrProtocolError)},
		{"MBAP length 0xFFFF", func(conn net.Conn, ex hostileExchange) bool {
			return hostileWrite(conn, hostileFrame(ex.txn, 0, 0xFFFF, ex.unit, make([]byte, 300)))
		}, wantIs(ErrInvalidMBAPLength)},
		{"MBAP length 255", func(conn net.Conn, ex hostileExchange) bool {
			return hostileWrite(conn, hostileFrame(ex.txn, 0, 255, ex.unit, make([]byte, 254)))
		}, wantIs(ErrInvalidMBAPLength)},
		{"MBAP length 1", func(conn net.Conn, ex hostileExchange) bool {
			return hostileWrite(conn, hostileFrame(ex.txn, 0, 1, ex.unit, []byte{3}))
		}, wantIs(ErrInvalidMBAPLength)},
		{"MBAP length 0", func(conn net.Conn, ex hostileExchange) bool {
			return hostileWrite(conn, hostileFrame(ex.txn, 0, 0, ex.unit, nil))
		}, wantIs(ErrInvalidMBAPLength)},
		{"MBAP length one short of the payload", func(conn net.Conn, ex hostileExchange) bool {
			f := good(ex)
			f[5]--
			return hostileWrite(conn, f)
		}, wantIs(ErrProtocolError)},
		{"MBAP length announces more, then silence", func(conn net.Conn, ex hostileExchange) bool {
			f := good(ex)
			return hostileWrite(conn, f[:len(f)-2])
		}, wantIs(ErrRequestTimedOut)},
		{"half a header, then silence", func(conn net.Conn, ex hostileExchange) bool {
			return hostileWrite(conn, good(ex)[:3])
		}, wantIs(ErrRequestTimedOut)},
		{"endless trickle", func(conn net.Conn, ex hostileExchange) bool {
			// One byte every 10 ms of frames that all carry the wrong ID:
			// the client must still give up at its deadline.
			g := ex
			g.txn++
			f := good(g)
			for i := 0; i < 400; i++ {
				if !hostileWrite(conn, f[i%len(f):i%len(f)+1]) {
					return false
				}
				time.Sleep(10 * time.Millisecond)
			}
			return true
		}, wantIs(ErrRequestTimedOut)},
		{"connection closed", func(net.Conn, hostileExchange) bool { return false }, wantAnyErr},
		{"connection closed mid-frame", func(conn net.Conn, ex hostileExchange) bool {
			hostileWrite(conn, good(ex)[:9])
			return false
		}, wantAnyErr},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			baseline := chaosGoroutines()
			peer := hostileStartPeer(t, func(conn net.Conn, ex hostileExchange) bool {
				if ex.n == 0 {
					return tc.bad(conn, ex)
				}
				return hostileWrite(conn, hostileGoodAnswer(ex))
			})
			c, err := New(Config{URL: peer.url(), Timeout: timeout})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := c.Open(); err != nil {
				t.Fatalf("Open: %v", err)
			}
			start := time.Now()
			regs, err := c.ReadRegisters(context.Background(), 1, 100, 2, HoldingRegister)
			if d := time.Since(start); d > timeout+700*time.Millisecond {
				t.Errorf("request took %v with a %v timeout", d, timeout)
			}
			tc.want(t, regs, err)
			hostileExpectGood(t, c, 200)
			_ = c.Close()
			peer.stop()
			chaosLeakCheck(t, baseline)
		})
	}
}

// TestHostileClient_UnsolicitedFrame: the peer pushes a frame before the
// client has asked anything, under a transaction ID the client has not used.
// It is skipped, and the real answer is taken.
func TestHostileClient_UnsolicitedFrame(t *testing.T) {
	var pushed atomic.Bool
	// Before the first answer, a frame for transaction 0x7777, which the
	// client never used, carrying other registers.
	peer := hostileStartPeer(t, func(conn net.Conn, ex hostileExchange) bool {
		if !pushed.Swap(true) {
			g := ex
			g.txn = 0x7777
			g.payload = hostileU16(4000, 3)
			hostileWrite(conn, hostileGoodAnswer(g))
		}
		return hostileWrite(conn, hostileGoodAnswer(ex))
	})
	c := hostileOpenClient(t, Config{URL: peer.url(), Timeout: time.Second})
	hostileExpectGood(t, c, 10)
	hostileExpectGood(t, c, 20)
}

// TestHostileClient_FunctionSpecificReplies: replies to FC24, FC43, FC20,
// FC17, FC12 and the write echoes that break their own rules.
func TestHostileClient_FunctionSpecificReplies(t *testing.T) {
	const timeout = 500 * time.Millisecond
	type call func(ctx context.Context, c *Client) error
	fifo := func(ctx context.Context, c *Client) error { _, err := c.ReadFIFOQueue(ctx, 1, 0); return err }
	devID := func(cat DeviceIDCategory) call {
		return func(ctx context.Context, c *Client) error {
			_, err := c.ReadDeviceIdentification(ctx, 1, cat, 0)
			return err
		}
	}
	devPage := func(ex hostileExchange, conformity, more, next byte, objs ...[]byte) []byte {
		p := []byte{0x0E, ex.payload[1], conformity, more, next, byte(len(objs))}
		for _, o := range objs {
			p = append(p, o...)
		}
		return rawRequestFrame(ex.txn, ex.unit, 0x2B, p)
	}
	obj := func(id byte, v string) []byte { return append([]byte{id, byte(len(v))}, v...) }

	cases := []struct {
		name   string
		call   call
		answer func(ex hostileExchange) []byte
		want   error
	}{
		{"FC24 count 32", fifo, func(ex hostileExchange) []byte {
			p := hostileU16(2+64, 32)
			return rawRequestFrame(ex.txn, ex.unit, 0x18, append(p, make([]byte, 64)...))
		}, ErrProtocolError},
		{"FC24 count 0xFFFF", fifo, func(ex hostileExchange) []byte {
			return rawRequestFrame(ex.txn, ex.unit, 0x18, hostileU16(2, 0xFFFF))
		}, ErrProtocolError},
		{"FC24 byte count disagrees with count", fifo, func(ex hostileExchange) []byte {
			return rawRequestFrame(ex.txn, ex.unit, 0x18, append(hostileU16(6, 1), 0, 1, 0, 2))
		}, ErrProtocolError},
		{"FC24 byte count disagrees with payload", fifo, func(ex hostileExchange) []byte {
			return rawRequestFrame(ex.txn, ex.unit, 0x18, append(hostileU16(4, 1), 0, 1, 0, 2))
		}, ErrProtocolError},
		{"FC24 short", fifo, func(ex hostileExchange) []byte {
			return rawRequestFrame(ex.txn, ex.unit, 0x18, []byte{0, 2, 0})
		}, ErrProtocolError},

		{"FC43 MoreFollows to the same object forever", devID(DeviceIDExtended), func(ex hostileExchange) []byte {
			return devPage(ex, 0x83, 0xFF, 0x05, obj(byte(ex.n), "x"))
		}, ErrProtocolError},
		{"FC43 MoreFollows walks every object ID", devID(DeviceIDExtended), func(ex hostileExchange) []byte {
			next := byte(ex.payload[2] + 1)
			return devPage(ex, 0x83, 0xFF, next, obj(ex.payload[2], "v"))
		}, ErrProtocolError},
		{"FC43 MoreFollows back to the start", devID(DeviceIDRegular), func(ex hostileExchange) []byte {
			return devPage(ex, 0x82, 0xFF, 0x00, obj(0, "v"))
		}, ErrProtocolError},
		{"FC43 more objects announced than sent", devID(DeviceIDBasic), func(ex hostileExchange) []byte {
			p := []byte{0x0E, 1, 0x81, 0, 0, 255}
			return rawRequestFrame(ex.txn, ex.unit, 0x2B, append(p, obj(0, "a")...))
		}, ErrProtocolError},
		{"FC43 object longer than the frame", devID(DeviceIDBasic), func(ex hostileExchange) []byte {
			p := []byte{0x0E, 1, 0x81, 0, 0, 1, 0, 200, 'a'}
			return rawRequestFrame(ex.txn, ex.unit, 0x2B, p)
		}, ErrProtocolError},
		{"FC43 MoreFollows 0x01", devID(DeviceIDBasic), func(ex hostileExchange) []byte {
			return devPage(ex, 0x81, 0x01, 0x01, obj(0, "a"))
		}, ErrProtocolError},
		{"FC43 conflicting duplicate across pages", devID(DeviceIDBasic), func(ex hostileExchange) []byte {
			if ex.payload[2] == 0 {
				return devPage(ex, 0x81, 0xFF, 0x01, obj(0, "a"))
			}
			return devPage(ex, 0x81, 0x00, 0x00, obj(0, "b"))
		}, ErrProtocolError},
		{"FC43 individual with MoreFollows", devID(DeviceIDIndividual), func(ex hostileExchange) []byte {
			return devPage(ex, 0x81, 0xFF, 0x01, obj(0, "a"))
		}, ErrProtocolError},
		{"FC43 individual with two objects", devID(DeviceIDIndividual), func(ex hostileExchange) []byte {
			return devPage(ex, 0x81, 0x00, 0x00, obj(0, "a"), obj(1, "b"))
		}, ErrProtocolError},
		{"FC43 wrong MEI type", devID(DeviceIDBasic), func(ex hostileExchange) []byte {
			return rawRequestFrame(ex.txn, ex.unit, 0x2B, []byte{0x0D, 1, 0x81, 0, 0, 0})
		}, ErrProtocolError},

		{"FC20 sub-response length 0", func(ctx context.Context, c *Client) error {
			_, err := c.ReadFileRecords(ctx, 1, []FileRecordRequest{{FileNumber: 1, RecordLength: 1}})
			return err
		}, func(ex hostileExchange) []byte {
			return rawRequestFrame(ex.txn, ex.unit, 0x14, []byte{4, 0, 6, 0, 1})
		}, ErrProtocolError},
		{"FC20 sub-response runs past the payload", func(ctx context.Context, c *Client) error {
			_, err := c.ReadFileRecords(ctx, 1, []FileRecordRequest{{FileNumber: 1, RecordLength: 1}})
			return err
		}, func(ex hostileExchange) []byte {
			return rawRequestFrame(ex.txn, ex.unit, 0x14, []byte{4, 0xFF, 6, 0, 1})
		}, ErrProtocolError},
		{"FC17 byte count disagrees", func(ctx context.Context, c *Client) error {
			_, err := c.ReportServerID(ctx, 1)
			return err
		}, func(ex hostileExchange) []byte {
			return rawRequestFrame(ex.txn, ex.unit, 0x11, []byte{9, 1, 0xFF})
		}, ErrProtocolError},
		{"FC12 byte count disagrees", func(ctx context.Context, c *Client) error {
			_, err := c.GetCommEventLog(ctx, 1)
			return err
		}, func(ex hostileExchange) []byte {
			return rawRequestFrame(ex.txn, ex.unit, 0x0C, []byte{200, 0, 0, 0, 0, 0, 0})
		}, ErrProtocolError},
		{"FC08 sub-function not echoed", func(ctx context.Context, c *Client) error {
			_, err := c.DiagnosticLoopback(ctx, 1, 0x1234)
			return err
		}, func(ex hostileExchange) []byte {
			return rawRequestFrame(ex.txn, ex.unit, 0x08, []byte{0, 1, 0x12, 0x34})
		}, ErrProtocolError},
		{"FC16 echo of another quantity", func(ctx context.Context, c *Client) error {
			return c.WriteRegisters(ctx, 1, 0, []uint16{1, 2})
		}, func(ex hostileExchange) []byte {
			return rawRequestFrame(ex.txn, ex.unit, 0x10, hostileU16(0, 3))
		}, ErrProtocolError},
		{"FC23 byte count disagrees", func(ctx context.Context, c *Client) error {
			_, err := c.ReadWriteMultipleRegisters(ctx, 1, 0, 2, 0, []uint16{1})
			return err
		}, func(ex hostileExchange) []byte {
			return rawRequestFrame(ex.txn, ex.unit, 0x17, []byte{2, 0, 1})
		}, ErrProtocolError},
		{"FC01 byte count too large", func(ctx context.Context, c *Client) error {
			_, err := c.ReadCoils(ctx, 1, 0, 8)
			return err
		}, func(ex hostileExchange) []byte {
			return rawRequestFrame(ex.txn, ex.unit, 0x01, []byte{2, 0xFF, 0xFF})
		}, ErrProtocolError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var bad atomic.Bool
			bad.Store(true)
			peer := hostileStartPeer(t, func(conn net.Conn, ex hostileExchange) bool {
				if ex.fc != 0x03 && bad.Load() {
					return hostileWrite(conn, tc.answer(ex))
				}
				return hostileWrite(conn, hostileGoodAnswer(ex))
			})
			c := hostileOpenClient(t, Config{URL: peer.url(), Timeout: timeout})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			start := time.Now()
			err := tc.call(ctx, c)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if d := time.Since(start); d > 3*time.Second {
				t.Errorf("call took %v", d)
			}
			if n := peer.n.Load(); n > 300 {
				t.Errorf("client sent %d requests", n)
			}
			bad.Store(false)
			hostileExpectGood(t, c, 50)
		})
	}
}

// TestHostileClient_ConcurrentNoCrossTalk drives one client from many
// goroutines against a peer that, seeded at random, answers with stale frames
// of earlier exchanges, wrong transaction or unit IDs, cut-short frames,
// answers that come after the client's deadline, or closes the connection.
// Required: every successful call returns the data of its own request, every
// failure is a typed error, and nothing outlives the client.
func TestHostileClient_ConcurrentNoCrossTalk(t *testing.T) {
	for _, maxConns := range []int{1, 4} {
		t.Run(fmt.Sprintf("MaxConns=%d", maxConns), func(t *testing.T) {
			baseline := chaosGoroutines()
			const timeout = 150 * time.Millisecond
			var seq atomic.Int64
			peer := hostileStartPeer(t, func(conn net.Conn, ex hostileExchange) bool {
				rng := rand.New(rand.NewSource(int64(ex.n) * 7919))
				ans := hostileGoodAnswer(ex)
				switch rng.Intn(12) {
				case 0: // a stale frame of an earlier exchange first
					g := ex
					g.txn -= uint16(1 + rng.Intn(3))
					g.payload = hostileU16(int(seq.Add(1)%5000), 3)
					hostileWrite(conn, hostileGoodAnswer(g))
				case 1: // wrong unit
					g := ex
					g.unit++
					return hostileWrite(conn, hostileGoodAnswer(g))
				case 2: // cut short, then the connection goes
					hostileWrite(conn, ans[:rng.Intn(len(ans))])
					return false
				case 3: // too late
					time.Sleep(timeout + 50*time.Millisecond)
				case 4:
					return false
				case 5: // in two pieces
					cut := rng.Intn(len(ans))
					hostileWrite(conn, ans[:cut])
					time.Sleep(time.Millisecond)
					return hostileWrite(conn, ans[cut:])
				}
				return hostileWrite(conn, ans)
			})
			c := hostileOpenClient(t, Config{URL: peer.url(), Timeout: timeout, MaxConns: maxConns})

			var wg sync.WaitGroup
			var ok, failed atomic.Int64
			for w := range 12 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := range 25 {
						addr := uint16(w*1000 + i)
						regs, err := c.ReadRegisters(context.Background(), 1, addr, 3, HoldingRegister)
						if err != nil {
							failed.Add(1)
							if errors.Is(err, ErrRequestTimedOut) || errors.Is(err, ErrBadUnitID) ||
								errors.Is(err, ErrProtocolError) || errors.Is(err, ErrBadTransactionID) ||
								errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
								continue
							}
							var ne net.Error
							if errors.As(err, &ne) {
								continue
							}
							t.Errorf("untyped error: %v", err)
							continue
						}
						ok.Add(1)
						for j, v := range regs {
							if v != hostileReg(int(addr)+j) {
								t.Errorf("ReadRegisters(%d) = %v: data of another request", addr, regs)
								break
							}
						}
					}
				}()
			}
			wg.Wait()
			t.Logf("%d ok, %d failed, %d connections", ok.Load(), failed.Load(), peer.accepted.Load())
			if ok.Load() == 0 {
				t.Fatal("no call succeeded")
			}
			_ = c.Close()
			peer.stop()
			chaosLeakCheck(t, baseline)
		})
	}
}

// TestHostileClient_CancelWhileTrickled: a context cancelled while the peer
// trickles a never-ending frame ends the call promptly with context.Canceled,
// and the client works afterwards.
func TestHostileClient_CancelWhileTrickled(t *testing.T) {
	baseline := chaosGoroutines()
	peer := hostileStartPeer(t, func(conn net.Conn, ex hostileExchange) bool {
		if ex.n == 0 {
			// A header announcing the longest frame, then its body a byte at a time.
			if !hostileWrite(conn, hostileFrame(ex.txn, 0, 254, ex.unit, nil)) {
				return false
			}
			for range 250 {
				if !hostileWrite(conn, []byte{0}) {
					return false
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
		return hostileWrite(conn, hostileGoodAnswer(ex))
	})
	c := hostileOpenClient(t, Config{URL: peer.url(), Timeout: 10 * time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, err := c.ReadRegisters(ctx, 1, 0, 1, HoldingRegister)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("cancel took %v", d)
	}
	hostileExpectGood(t, c, 1)
	_ = c.Close()
	peer.stop()
	chaosLeakCheck(t, baseline)
}

// TestHostileClient_CloseWhileTrickled: Close while a request is being
// trickled ends that request promptly with ErrClientNotOpen.
func TestHostileClient_CloseWhileTrickled(t *testing.T) {
	baseline := chaosGoroutines()
	peer := hostileStartPeer(t, func(conn net.Conn, ex hostileExchange) bool {
		if !hostileWrite(conn, hostileFrame(ex.txn, 0, 254, ex.unit, nil)) {
			return false
		}
		for range 250 {
			if !hostileWrite(conn, []byte{0}) {
				return false
			}
			time.Sleep(5 * time.Millisecond)
		}
		return false
	})
	c, err := New(Config{URL: peer.url(), Timeout: 10 * time.Second, MaxConns: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Open(); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 6)
	for i := range 6 {
		go func() {
			_, err := c.ReadRegisters(context.Background(), 1, uint16(i), 1, HoldingRegister)
			errs <- err
		}()
	}
	if !chaosEventually(2*time.Second, func() bool { return peer.n.Load() >= 3 }) {
		t.Fatalf("only %d requests reached the peer", peer.n.Load())
	}
	start := time.Now()
	_ = c.Close()
	for range 6 {
		select {
		case err := <-errs:
			if !errors.Is(err, ErrClientNotOpen) {
				t.Errorf("request interrupted by Close: %v, want ErrClientNotOpen", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("a request outlived Close by 2 s")
		}
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Close took %v to end the requests", d)
	}
	peer.stop()
	chaosLeakCheck(t, baseline)
}
