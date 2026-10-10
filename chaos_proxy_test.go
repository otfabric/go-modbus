// SPDX-License-Identifier: MIT

package modbus

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This file implements the chaos monkey: a fault-injecting TCP proxy that sits
// between a real client and the real server on loopback. It understands MBAP
// framing (so that faults can target whole frames and individual header
// fields) and injects faults per direction according to a seeded plan.
//
// In raw mode (used for TLS, where the byte stream is opaque) the unit of
// fault injection is a chunk as read from the socket, and only the faults that
// make sense on an opaque stream are applied.

// chaosDir is the direction a frame travels through the proxy.
type chaosDir uint8

const (
	chaosC2S chaosDir = iota
	chaosS2C
)

func (d chaosDir) String() string {
	if d == chaosC2S {
		return "c2s"
	}
	return "s2c"
}

// chaosFault is a bit set of fault kinds.
type chaosFault uint32

const (
	// chaosLatency delays a whole frame by a jittered amount below the client timeout.
	chaosLatency chaosFault = 1 << iota
	// chaosFragment forwards the frame in writes of one to three bytes.
	chaosFragment
	// chaosDrop black-holes the frame.
	chaosDrop
	// chaosTruncate forwards a strict prefix of the frame, then closes the connection.
	chaosTruncate
	// chaosRSTBefore resets the connection instead of forwarding the frame.
	chaosRSTBefore
	// chaosFINBefore closes the connection gracefully instead of forwarding the frame.
	chaosFINBefore
	// chaosRSTAfter forwards the frame, then resets the connection.
	chaosRSTAfter
	// chaosFINAfter forwards the frame, then closes the connection gracefully.
	chaosFINAfter
	// chaosHalfOpen stops forwarding in this direction for good, keeping both sockets open.
	chaosHalfOpen
	// chaosDuplicate forwards the frame twice.
	chaosDuplicate
	// chaosStale delivers the whole frame only after the plan's staleDelay (beyond the client timeout).
	chaosStale
	// chaosReorder holds the frame back and delivers it right after the next frame in the
	// same direction, coalesced into the same write.
	chaosReorder
	// chaosCorruptTxn changes the MBAP transaction ID.
	chaosCorruptTxn
	// chaosCorruptProto changes the MBAP protocol ID.
	chaosCorruptProto
	// chaosCorruptLen changes the MBAP length field (shorter, longer, or invalid).
	chaosCorruptLen
	// chaosCorruptUnit changes the MBAP unit ID.
	chaosCorruptUnit
	// chaosCorruptFC changes the function code.
	chaosCorruptFC
	// chaosCorruptData flips a payload byte. Modbus TCP has no checksum, so no correct
	// client can detect this: never combine it with a "never wrong data" assertion.
	chaosCorruptData

	chaosFaultKinds = iota
)

var chaosFaultNames = [chaosFaultKinds]string{
	"latency", "fragment", "drop", "truncate", "rst-before", "fin-before", "rst-after",
	"fin-after", "half-open", "duplicate", "stale", "reorder", "corrupt-txn",
	"corrupt-proto", "corrupt-len", "corrupt-unit", "corrupt-fc", "corrupt-data",
}

func (f chaosFault) String() string {
	if f == 0 {
		return "none"
	}
	var parts []string
	for i := 0; i < chaosFaultKinds; i++ {
		if f&(1<<i) != 0 {
			parts = append(parts, chaosFaultNames[i])
		}
	}
	return strings.Join(parts, "|")
}

const (
	// chaosRawFaults are the faults that are meaningful on an opaque byte stream.
	chaosRawFaults = chaosLatency | chaosFragment | chaosTruncate | chaosRSTBefore |
		chaosFINBefore | chaosRSTAfter | chaosFINAfter | chaosHalfOpen
)

// chaosFrame describes one frame (or raw chunk) about to be forwarded.
type chaosFrame struct {
	dir  chaosDir
	conn int
	// index is the zero-based position of the frame in its direction on its connection.
	index int
	data  []byte
}

// chaosPlan selects the fault applied to each frame.
type chaosPlan struct {
	// rate is the probability that a frame is faulted (random mode).
	rate float64
	// c2s and s2c are the fault kinds enabled per direction (random mode).
	c2s, s2c chaosFault
	// maxLatency bounds chaosLatency; staleDelay is the delay of chaosStale.
	maxLatency time.Duration
	staleDelay time.Duration
	// fragmentGap is slept between the writes of a fragmented frame (zero: just yield).
	fragmentGap time.Duration
	// script, when set, replaces the random selection: it returns the single fault to
	// apply to the frame (or 0). It is called from the proxy's pump goroutines.
	script func(f chaosFrame) chaosFault
	// splitAt, when set, forces chaosFragment frames to be split in exactly two writes
	// at the returned offset (clamped to a valid boundary).
	splitAt func(f chaosFrame) int
	// truncateAt, when set, forces the prefix length of chaosTruncate.
	truncateAt func(f chaosFrame) int
	// lenTo, when set, forces the value chaosCorruptLen writes over the length cur.
	lenTo func(cur int) int
}

const (
	chaosAcceptForward int32 = iota
	// chaosAcceptReset accepts new connections and resets them at once.
	chaosAcceptReset
	// chaosAcceptBlackhole accepts new connections and never forwards anything.
	chaosAcceptBlackhole
)

// chaosProxy is the fault-injecting proxy.
type chaosProxy struct {
	tb       testing.TB
	seed     int64
	upstream string
	addr     string
	raw      bool

	plan       atomic.Pointer[chaosPlan]
	acceptMode atomic.Int32

	mu     sync.Mutex
	ln     net.Listener
	conns  map[*chaosConn]struct{}
	nextID int
	closed bool
	wg     sync.WaitGroup

	accepted atomic.Int64
	active   atomic.Int64
	peak     atomic.Int64
	frames   [2]atomic.Int64
	injected [2][chaosFaultKinds]atomic.Int64
}

// chaosStartProxy starts a proxy in front of upstream ("host:port"). raw selects
// opaque-stream mode (TLS).
func chaosStartProxy(tb testing.TB, upstream string, seed int64, raw bool) *chaosProxy {
	tb.Helper()
	p := &chaosProxy{
		tb:       tb,
		seed:     seed,
		upstream: upstream,
		raw:      raw,
		conns:    make(map[*chaosConn]struct{}),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("chaos proxy listen: %v", err)
	}
	p.addr = ln.Addr().String()
	p.startAccept(ln)
	return p
}

func (p *chaosProxy) port() string {
	_, port, _ := net.SplitHostPort(p.addr)
	return port
}

func (p *chaosProxy) startAccept(ln net.Listener) {
	p.mu.Lock()
	p.ln = ln
	p.mu.Unlock()
	p.wg.Add(1)
	go p.acceptLoop(ln)
}

// setPlan installs a plan (nil: forward everything untouched).
func (p *chaosProxy) setPlan(pl *chaosPlan) { p.plan.Store(pl) }

// refuse closes the listener so that new connections are refused.
func (p *chaosProxy) refuse() {
	p.mu.Lock()
	ln := p.ln
	p.ln = nil
	p.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}

// listen re-opens the listener on the proxy's original address after refuse. The port
// may have been taken by another process meanwhile: the error is returned.
func (p *chaosProxy) listen() error {
	var ln net.Listener
	var err error
	deadline := time.Now().Add(2 * time.Second)
	for {
		ln, err = net.Listen("tcp", p.addr)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	p.startAccept(ln)
	return nil
}

// killAll closes every proxied connection (rst: with a TCP reset).
func (p *chaosProxy) killAll(rst bool) {
	for _, c := range p.snapshot() {
		c.close(rst)
	}
}

// heal stops all fault injection and resets the connections that a fault left
// half-open, as a healed network would on their next segment.
func (p *chaosProxy) heal() {
	p.setPlan(nil)
	p.acceptMode.Store(chaosAcceptForward)
	for _, c := range p.snapshot() {
		if c.poisoned.Load() {
			c.close(true)
		}
	}
}

func (p *chaosProxy) snapshot() []*chaosConn {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*chaosConn, 0, len(p.conns))
	for c := range p.conns {
		out = append(out, c)
	}
	return out
}

// stop closes the listener and every connection and waits for the proxy goroutines.
func (p *chaosProxy) stop() {
	p.mu.Lock()
	p.closed = true
	ln := p.ln
	p.ln = nil
	p.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	p.killAll(false)
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		p.tb.Errorf("chaos proxy: goroutines did not exit\n%s", chaosAllStacks())
	}
}

// injectedTotal returns the number of faults injected so far.
func (p *chaosProxy) injectedTotal() int64 {
	var n int64
	for d := 0; d < 2; d++ {
		for i := 0; i < chaosFaultKinds; i++ {
			n += p.injected[d][i].Load()
		}
	}
	return n
}

// summary describes the proxy's activity, for failure logs.
func (p *chaosProxy) summary() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "proxy seed=%d accepted=%d peak=%d frames(c2s=%d s2c=%d) faults:",
		p.seed, p.accepted.Load(), p.peak.Load(), p.frames[chaosC2S].Load(), p.frames[chaosS2C].Load())
	for d := chaosC2S; d <= chaosS2C; d++ {
		for i := 0; i < chaosFaultKinds; i++ {
			if n := p.injected[d][i].Load(); n > 0 {
				fmt.Fprintf(&sb, " %s/%s=%d", d, chaosFaultNames[i], n)
			}
		}
	}
	return sb.String()
}

func (p *chaosProxy) acceptLoop(ln net.Listener) {
	defer p.wg.Done()
	for {
		client, err := ln.Accept()
		if err != nil {
			return
		}
		p.accepted.Add(1)
		switch p.acceptMode.Load() {
		case chaosAcceptReset:
			chaosAbortConn(client)
			continue
		case chaosAcceptBlackhole:
			p.adopt(client, nil)
			continue
		}
		server, err := net.DialTimeout("tcp", p.upstream, 2*time.Second)
		if err != nil {
			// Upstream is down (server restart): the client sees its connection die.
			chaosAbortConn(client)
			continue
		}
		p.adopt(client, server)
	}
}

// adopt registers a connection pair and starts its pumps. A nil server makes the
// connection a black hole.
func (p *chaosProxy) adopt(client, server net.Conn) {
	c := &chaosConn{p: p, client: client, server: server, done: make(chan struct{})}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = client.Close()
		if server != nil {
			_ = server.Close()
		}
		return
	}
	c.id = p.nextID
	p.nextID++
	p.conns[c] = struct{}{}
	p.mu.Unlock()

	n := p.active.Add(1)
	for {
		old := p.peak.Load()
		if n <= old || p.peak.CompareAndSwap(old, n) {
			break
		}
	}

	if server == nil {
		c.poisoned.Store(true)
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			_, _ = io.Copy(io.Discard, client)
			c.close(false)
		}()
		return
	}
	p.wg.Add(2)
	go c.pump(chaosC2S, client, server)
	go c.pump(chaosS2C, server, client)
}

// chaosConn is one proxied client/server connection pair.
type chaosConn struct {
	p      *chaosProxy
	id     int
	client net.Conn
	server net.Conn

	// wmu serializes writes per direction: late (stale) frames are written by their
	// own goroutine.
	wmu [2]sync.Mutex
	// poisoned is set once a fault left the connection half-open.
	poisoned atomic.Bool

	once sync.Once
	done chan struct{}
}

func (c *chaosConn) close(rst bool) {
	c.once.Do(func() {
		close(c.done)
		if rst {
			chaosAbortConn(c.client)
		} else {
			_ = c.client.Close()
		}
		if c.server != nil {
			// Always reset the upstream side: a graceful close would park the proxy's
			// ephemeral port in TIME_WAIT, and a long run would exhaust the port range.
			// (Graceful closes towards the server are covered by the direct tests.)
			chaosAbortConn(c.server)
		}
		c.p.mu.Lock()
		delete(c.p.conns, c)
		c.p.mu.Unlock()
		c.p.active.Add(-1)
	})
}

// sleep waits for d or until the connection is closed; it reports whether the
// connection is still open.
func (c *chaosConn) sleep(d time.Duration) bool {
	if d <= 0 {
		runtime.Gosched()
		select {
		case <-c.done:
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-c.done:
		return false
	case <-timer.C:
		return true
	}
}

func (c *chaosConn) write(dir chaosDir, dst net.Conn, b []byte) bool {
	c.wmu[dir].Lock()
	defer c.wmu[dir].Unlock()
	_, err := dst.Write(b)
	return err == nil
}

// readFrame reads one MBAP frame. ok is false when the stream does not look like
// MBAP (the bytes read so far are returned and the caller falls back to raw mode).
func chaosReadFrame(br *bufio.Reader) (frame []byte, ok bool, err error) {
	header := make([]byte, 7)
	if n, err := io.ReadFull(br, header); err != nil {
		return header[:n], true, err
	}
	length := int(binary.BigEndian.Uint16(header[4:6]))
	if length < 2 || length > 260 {
		return header, false, nil
	}
	frame = make([]byte, 7+length-1)
	copy(frame, header)
	if n, err := io.ReadFull(br, frame[7:]); err != nil {
		return frame[:7+n], true, err
	}
	return frame, true, nil
}

func (c *chaosConn) pump(dir chaosDir, src, dst net.Conn) {
	defer c.p.wg.Done()
	defer c.close(false)

	rng := rand.New(rand.NewSource(c.p.seed*7919 + int64(c.id)*2 + int64(dir)))
	br := bufio.NewReader(src)
	raw := c.p.raw
	dead := false
	var held []byte
	buf := make([]byte, 4096)

	for index := 0; ; index++ {
		var frame []byte
		if raw {
			n, err := br.Read(buf)
			if n == 0 || err != nil {
				return
			}
			frame = append([]byte(nil), buf[:n]...)
		} else {
			var ok bool
			var err error
			frame, ok, err = chaosReadFrame(br)
			if err != nil {
				return
			}
			if !ok {
				// Not MBAP: forward this and everything after it untouched.
				raw = true
			}
		}
		c.p.frames[dir].Add(1)
		if dead {
			continue
		}

		pl := c.p.plan.Load()
		fault := c.pick(pl, dir, index, frame, rng)
		if fault != 0 {
			for i := 0; i < chaosFaultKinds; i++ {
				if fault&(1<<i) != 0 {
					c.p.injected[dir][i].Add(1)
				}
			}
		}

		if fault == chaosReorder && held == nil {
			held = frame
			continue
		}
		out := frame
		if held != nil {
			// Deliver the newer frame first, then the held one, in a single write.
			out = append(append([]byte(nil), frame...), held...)
			held = nil
		}

		switch fault {
		case 0, chaosReorder:
			if !c.write(dir, dst, out) {
				return
			}
		case chaosLatency:
			d := time.Duration(0)
			if pl.maxLatency > 0 {
				d = time.Duration(rng.Int63n(int64(pl.maxLatency)))
			}
			if !c.sleep(d) || !c.write(dir, dst, out) {
				return
			}
		case chaosFragment:
			if !c.fragment(pl, dir, index, dst, out, rng) {
				return
			}
		case chaosDrop:
			// Black hole.
		case chaosTruncate:
			k := 1
			if len(out) > 2 {
				k = 1 + rng.Intn(len(out)-1)
			}
			if pl.truncateAt != nil {
				k = pl.truncateAt(chaosFrame{dir: dir, conn: c.id, index: index, data: out})
			}
			if k >= len(out) {
				k = len(out) - 1
			}
			if k > 0 {
				c.write(dir, dst, out[:k])
			}
			// Let the prefix reach the peer before the connection goes away.
			c.sleep(time.Millisecond)
			c.close(rng.Intn(2) == 0)
			return
		case chaosRSTBefore:
			c.close(true)
			return
		case chaosFINBefore:
			c.close(false)
			return
		case chaosRSTAfter, chaosFINAfter:
			c.write(dir, dst, out)
			c.sleep(time.Millisecond)
			c.close(fault == chaosRSTAfter)
			return
		case chaosHalfOpen:
			dead = true
			c.poisoned.Store(true)
		case chaosDuplicate:
			if rng.Intn(2) == 0 {
				// Coalesced into one write.
				if !c.write(dir, dst, append(append([]byte(nil), out...), out...)) {
					return
				}
			} else if !c.write(dir, dst, out) || !c.sleep(0) || !c.write(dir, dst, out) {
				return
			}
		case chaosStale:
			c.p.wg.Add(1)
			go func(late []byte, d time.Duration) {
				defer c.p.wg.Done()
				if c.sleep(d) {
					c.write(dir, dst, late)
				}
			}(out, pl.staleDelay)
		case chaosCorruptTxn, chaosCorruptProto, chaosCorruptLen, chaosCorruptUnit, chaosCorruptFC, chaosCorruptData:
			if !c.write(dir, dst, chaosCorrupt(fault, out, rng, pl.lenTo)) {
				return
			}
		default:
			c.p.tb.Errorf("chaos proxy: plan selected more than one fault for a frame: %v", fault)
			return
		}
	}
}

// pick selects the fault for a frame.
func (c *chaosConn) pick(pl *chaosPlan, dir chaosDir, index int, frame []byte, rng *rand.Rand) chaosFault {
	if pl == nil {
		return 0
	}
	if pl.script != nil {
		return pl.script(chaosFrame{dir: dir, conn: c.id, index: index, data: frame})
	}
	mask := pl.c2s
	if dir == chaosS2C {
		mask = pl.s2c
	}
	if c.p.raw {
		mask &= chaosRawFaults
	}
	// Always draw, so that the sequence does not depend on the mask.
	roll, choice := rng.Float64(), rng.Intn(1<<16)
	if mask == 0 || roll >= pl.rate {
		return 0
	}
	var enabled []chaosFault
	for i := 0; i < chaosFaultKinds; i++ {
		if mask&(1<<i) != 0 {
			enabled = append(enabled, 1<<i)
		}
	}
	return enabled[choice%len(enabled)]
}

// fragment forwards b in several small writes.
func (c *chaosConn) fragment(pl *chaosPlan, dir chaosDir, index int, dst net.Conn, b []byte, rng *rand.Rand) bool {
	if pl.splitAt != nil {
		k := pl.splitAt(chaosFrame{dir: dir, conn: c.id, index: index, data: b})
		if k < 1 {
			k = 1
		}
		if k >= len(b) {
			k = len(b) - 1
		}
		if k < 1 {
			return c.write(dir, dst, b)
		}
		return c.write(dir, dst, b[:k]) && c.sleep(pl.fragmentGap) && c.write(dir, dst, b[k:])
	}
	for len(b) > 0 {
		n := 1 + rng.Intn(3)
		if n > len(b) {
			n = len(b)
		}
		if !c.write(dir, dst, b[:n]) {
			return false
		}
		b = b[n:]
		if len(b) > 0 && !c.sleep(pl.fragmentGap) {
			return false
		}
	}
	return true
}

// chaosCorrupt returns a corrupted copy of an MBAP frame.
func chaosCorrupt(fault chaosFault, frame []byte, rng *rand.Rand, lenTo func(cur int) int) []byte {
	out := append([]byte(nil), frame...)
	if len(out) < 8 {
		return out
	}
	switch fault {
	case chaosCorruptTxn:
		binary.BigEndian.PutUint16(out[0:2], binary.BigEndian.Uint16(out[0:2])^uint16(1+rng.Intn(0xffff)))
	case chaosCorruptProto:
		binary.BigEndian.PutUint16(out[2:4], uint16(1+rng.Intn(0xffff)))
	case chaosCorruptLen:
		cur := int(binary.BigEndian.Uint16(out[4:6]))
		var next int
		if lenTo != nil {
			binary.BigEndian.PutUint16(out[4:6], uint16(lenTo(cur)))
			break
		}
		switch rng.Intn(5) {
		case 0:
			next = 0
		case 1:
			next = 0xffff
		case 2:
			next = cur + 1 + rng.Intn(4)
		case 3:
			next = 1
		default:
			next = cur - 1 - rng.Intn(3)
			if next < 2 {
				next = cur + 1
			}
		}
		binary.BigEndian.PutUint16(out[4:6], uint16(next))
	case chaosCorruptUnit:
		out[6] ^= byte(1 + rng.Intn(255))
	case chaosCorruptFC:
		out[7] ^= byte(1 + rng.Intn(255))
	case chaosCorruptData:
		if len(out) > 8 {
			out[8+rng.Intn(len(out)-8)] ^= byte(1 + rng.Intn(255))
		} else {
			out[7] ^= byte(1 + rng.Intn(255))
		}
	default:
	}
	return out
}
