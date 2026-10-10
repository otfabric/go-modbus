// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// Deterministic single-fault tests: one precisely placed fault per case, with the
// exact error class it must produce and the requirement that the client works
// again right after it.

// chaosRig is a server, a proxy in front of it and their reference device.
type chaosRig struct {
	t     *testing.T
	dev   *chaosDevice
	srv   *chaosServer
	proxy *chaosProxy
}

func chaosNewRig(t *testing.T, kind string, seed int64, handler RequestHandler, mod func(*ServerConfig)) *chaosRig {
	t.Helper()
	r := &chaosRig{t: t, dev: chaosNewDevice(seed)}
	if handler == nil {
		handler = r.dev
	}
	r.srv = chaosStartServer(t, kind, handler, mod)
	r.proxy = chaosStartProxy(t, r.srv.addr, seed, kind == "tcp+tls")
	t.Cleanup(r.proxy.stop)
	return r
}

// open returns an open client that reaches the server through the proxy.
func (r *chaosRig) open(mode chaosMode, timeout time.Duration) *Client {
	r.t.Helper()
	return chaosOpenClient(r.t, chaosClientConfig(r.t, r.srv.kind, r.proxy.port(), mode, timeout, nil))
}

// chaosOnFrame returns a script that applies fault to the frame with the given index
// in direction dir on the first connection, and to nothing else.
func chaosOnFrame(dir chaosDir, index int, fault chaosFault) func(chaosFrame) chaosFault {
	return func(f chaosFrame) chaosFault {
		if f.conn == 0 && f.dir == dir && f.index == index {
			return fault
		}
		return 0
	}
}

// chaosReadInputs reads qty input registers at addr and checks them against the
// reference data. ok is false when the call succeeded with wrong data.
func chaosReadInputs(ctx context.Context, c *Client, addr, qty int) (ok bool, err error) {
	got, err := c.ReadRegisters(ctx, refUnitID, uint16(addr), uint16(qty), InputRegister)
	if err != nil {
		return false, err
	}
	if len(got) != qty {
		return false, nil
	}
	for i, g := range got {
		if g != chaosInputValue(addr+i) {
			return false, nil
		}
	}
	return true, nil
}

type chaosFaultCase struct {
	name  string
	dir   chaosDir
	fault chaosFault
	// want is the error class of the faulted call without a RetryPolicy.
	want string
	// lenTo, splitAt and gap refine the fault.
	lenTo   func(cur int) int
	splitAt int
	gap     time.Duration
}

func chaosFaultCases(timeout time.Duration) []chaosFaultCase {
	cases := []chaosFaultCase{
		{name: "latency", fault: chaosLatency, want: chaosClassOK},
		{name: "fragment", fault: chaosFragment, want: chaosClassOK},
		{name: "drop", fault: chaosDrop, want: chaosClassTimeout},
		{name: "truncate", fault: chaosTruncate, want: chaosClassTransport},
		{name: "rst-before", fault: chaosRSTBefore, want: chaosClassTransport},
		{name: "fin-before", fault: chaosFINBefore, want: chaosClassTransport},
		{name: "rst-after", fault: chaosRSTAfter, want: chaosClassOK},
		{name: "fin-after", fault: chaosFINAfter, want: chaosClassOK},
		{name: "half-open", fault: chaosHalfOpen, want: chaosClassTimeout},
		{name: "stale", fault: chaosStale, want: chaosClassTimeout},
		{name: "reorder", fault: chaosReorder, want: chaosClassTimeout},
		{name: "corrupt-txn", fault: chaosCorruptTxn, want: chaosClassTimeout},
		{name: "corrupt-unit", fault: chaosCorruptUnit, want: chaosClassProtocol},
	}
	var out []chaosFaultCase
	for _, c := range cases {
		for _, dir := range []chaosDir{chaosC2S, chaosS2C} {
			c := c
			c.dir = dir
			c.name = dir.String() + "/" + c.name
			out = append(out, c)
		}
	}
	longer := func(cur int) int { return cur + 2 }
	shorter := func(cur int) int { return cur - 1 }
	fixed := func(v int) func(int) int { return func(int) int { return v } }
	out = append(out,
		chaosFaultCase{name: "s2c/duplicate", dir: chaosS2C, fault: chaosDuplicate, want: chaosClassOK},
		chaosFaultCase{name: "c2s/corrupt-proto", dir: chaosC2S, fault: chaosCorruptProto, want: chaosClassTransport},
		chaosFaultCase{name: "s2c/corrupt-proto", dir: chaosS2C, fault: chaosCorruptProto, want: chaosClassTimeout},
		chaosFaultCase{name: "s2c/corrupt-fc", dir: chaosS2C, fault: chaosCorruptFC, want: chaosClassProtocol},
		// A request whose length field is wrong makes the server drop the connection, or
		// wait for bytes that never come.
		chaosFaultCase{name: "c2s/corrupt-len/zero", dir: chaosC2S, fault: chaosCorruptLen, lenTo: fixed(0), want: chaosClassTransport},
		chaosFaultCase{name: "c2s/corrupt-len/huge", dir: chaosC2S, fault: chaosCorruptLen, lenTo: fixed(0xffff), want: chaosClassTransport},
		chaosFaultCase{name: "c2s/corrupt-len/shorter", dir: chaosC2S, fault: chaosCorruptLen, lenTo: shorter, want: chaosClassTransport},
		chaosFaultCase{name: "c2s/corrupt-len/longer", dir: chaosC2S, fault: chaosCorruptLen, lenTo: longer, want: chaosClassTimeout},
		// A response whose length field is wrong, or that stalls half-way, leaves bytes
		// of the old frame in the stream: the client must drop the connection.
		chaosFaultCase{name: "s2c/corrupt-len/zero", dir: chaosS2C, fault: chaosCorruptLen, lenTo: fixed(0), want: chaosClassProtocol},
		chaosFaultCase{name: "s2c/corrupt-len/one", dir: chaosS2C, fault: chaosCorruptLen, lenTo: fixed(1), want: chaosClassProtocol},
		chaosFaultCase{name: "s2c/corrupt-len/huge", dir: chaosS2C, fault: chaosCorruptLen, lenTo: fixed(0xffff), want: chaosClassProtocol},
		chaosFaultCase{name: "s2c/corrupt-len/shorter", dir: chaosS2C, fault: chaosCorruptLen, lenTo: shorter, want: chaosClassProtocol},
		chaosFaultCase{name: "s2c/corrupt-len/longer", dir: chaosS2C, fault: chaosCorruptLen, lenTo: longer, want: chaosClassTimeout},
		chaosFaultCase{name: "c2s/stall-mid-frame", dir: chaosC2S, fault: chaosFragment, splitAt: 9, gap: timeout + timeout/2, want: chaosClassTimeout},
		chaosFaultCase{name: "s2c/stall-mid-header", dir: chaosS2C, fault: chaosFragment, splitAt: 3, gap: timeout + timeout/2, want: chaosClassTimeout},
		chaosFaultCase{name: "s2c/stall-mid-body", dir: chaosS2C, fault: chaosFragment, splitAt: 12, gap: timeout + timeout/2, want: chaosClassTimeout},
	)
	return out
}

// TestChaosFaultMatrix applies each fault of the catalogue, in each direction, to
// exactly one frame and asserts the class of the resulting error and that the
// client (single connection with and without RetryPolicy, and pooled without)
// serves the next requests correctly, without returning the faulted request's
// data.
func TestChaosFaultMatrix(t *testing.T) {
	// Long enough that a loaded machine does not turn a prompt failure into a
	// timeout; the cases run in parallel, so the timeouts do not add up.
	const timeout = 150 * time.Millisecond
	for _, mode := range []chaosMode{chaosModeSingle, chaosModeSingleRetry, chaosModePool} {
		mode := mode
		t.Run(mode.name, func(t *testing.T) {
			for i, fc := range chaosFaultCases(timeout) {
				fc := fc
				seed := int64(i)
				t.Run(fc.name, func(t *testing.T) {
					t.Parallel()
					chaosRunFaultCase(t, mode, fc, timeout, seed)
				})
			}
		})
	}
}

func chaosRunFaultCase(t *testing.T, mode chaosMode, fc chaosFaultCase, timeout time.Duration, seed int64) {
	rig := chaosNewRig(t, "tcp", seed, nil, nil)
	client := rig.open(mode, timeout)
	wd := chaosNewWatchdog(t)

	long, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if ok, err := chaosReadInputs(long, client, 0, 8); err != nil || !ok {
		t.Fatalf("warm-up read: ok=%v err=%v", ok, err)
	}

	pl := &chaosPlan{
		script:      chaosOnFrame(fc.dir, 1, fc.fault),
		maxLatency:  timeout / 4,
		staleDelay:  timeout + timeout/2,
		lenTo:       fc.lenTo,
		fragmentGap: fc.gap,
	}
	if fc.splitAt > 0 {
		pl.splitAt = func(chaosFrame) int { return fc.splitAt }
	}
	rig.proxy.setPlan(pl)

	// The faulted call runs under the client's own Timeout (no context deadline).
	var ok bool
	var err error
	wd.guard("faulted call", time.Duration(mode.attempts())*timeout+2*time.Second, func() {
		ok, err = chaosReadInputs(context.Background(), client, 100, 8)
	})
	if rig.proxy.injectedTotal() != 1 {
		t.Fatalf("the fault was injected %d times, want once", rig.proxy.injectedTotal())
	}
	want := fc.want
	if mode.retry != nil && want == chaosClassTransport {
		// A transport error is retried on a fresh connection, which is healthy.
		want = chaosClassOK
	}
	got := chaosClassify(err)
	// A connection closed right after the request was forwarded: whether the
	// response still makes it back is a race, and both outcomes are correct.
	closedBehindRequest := fc.dir == chaosC2S && fc.fault&(chaosRSTAfter|chaosFINAfter) != 0
	if closedBehindRequest && got == chaosClassTransport {
		got = want
	}
	if got != want {
		t.Errorf("faulted call: error class %q (%v), want %q", got, err, want)
	}
	if err == nil && !ok {
		t.Errorf("faulted call: succeeded with wrong data")
	}

	rig.proxy.heal()
	// Let a stalled or stale remainder of the faulted exchange arrive (at most 1.5
	// timeouts after the fault), or the peer's close propagate.
	if errors.Is(err, ErrRequestTimedOut) {
		time.Sleep(timeout/2 + 10*time.Millisecond)
	} else {
		time.Sleep(5 * time.Millisecond)
	}

	// A client without RetryPolicy finds out that its idle connection was closed by
	// the peer only when it uses it: that costs one request.
	const followUp = 2 * time.Second
	allowance := 0
	if mode.retry == nil && fc.fault&(chaosRSTAfter|chaosFINAfter) != 0 {
		allowance = 1
	}
	for i := 0; i < 4; i++ {
		addr := 200 + 20*i
		// A healthy link answers in a few milliseconds; the bound is for loaded machines.
		wd.guard("follow-up call", time.Duration(mode.attempts())*followUp+2*time.Second, func() {
			ctx, cancel := context.WithTimeout(context.Background(), followUp)
			defer cancel()
			ok, err = chaosReadInputs(ctx, client, addr, 8)
		})
		switch {
		case err == nil && !ok:
			t.Fatalf("follow-up call %d succeeded with wrong data (response of another request)", i)
		case err == nil:
		case allowance > 0:
			allowance--
		default:
			t.Fatalf("follow-up call %d failed on a healthy link: %v", i, err)
		}
	}
}

// TestChaosFragmentEveryBoundary splits requests and responses in two writes at
// every possible byte boundary; every call must succeed with the right data.
func TestChaosFragmentEveryBoundary(t *testing.T) {
	for _, mode := range []chaosMode{chaosModeSingle, chaosModePool} {
		mode := mode
		t.Run(mode.name, func(t *testing.T) {
			rig := chaosNewRig(t, "tcp", 1, nil, nil)
			client := rig.open(mode, 5*time.Second)
			var split atomic.Int64
			var maxLen [2]atomic.Int64
			rig.proxy.setPlan(&chaosPlan{
				script: func(f chaosFrame) chaosFault {
					if int64(len(f.data)) > maxLen[f.dir].Load() {
						maxLen[f.dir].Store(int64(len(f.data)))
					}
					return chaosFragment
				},
				splitAt: func(chaosFrame) int { return int(split.Load()) },
			})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			// The longest frame is the FC03/FC04 response: 9 + 2*16 bytes.
			const qty = 16
			for k := 1; k < 9+2*qty; k++ {
				split.Store(int64(k))
				values := make([]uint16, qty)
				for i := range values {
					values[i] = uint16(k)<<8 | uint16(i)
				}
				if err := client.WriteRegisters(ctx, refUnitID, 32, values); err != nil {
					t.Fatalf("split at %d: WriteRegisters: %v", k, err)
				}
				got, err := client.ReadRegisters(ctx, refUnitID, 32, qty, HoldingRegister)
				if err != nil {
					t.Fatalf("split at %d: ReadRegisters: %v", k, err)
				}
				for i := range values {
					if got[i] != values[i] {
						t.Fatalf("split at %d: register %d = 0x%04x, want 0x%04x", k, i, got[i], values[i])
					}
				}
				if ok, err := chaosReadInputs(ctx, client, k, qty); err != nil || !ok {
					t.Fatalf("split at %d: ReadInputRegisters: ok=%v err=%v", k, ok, err)
				}
			}
			if maxLen[chaosC2S].Load() < 13+2*qty || maxLen[chaosS2C].Load() < 9+2*qty {
				t.Errorf("frames were shorter than expected (c2s %d, s2c %d): not every boundary was exercised",
					maxLen[chaosC2S].Load(), maxLen[chaosS2C].Load())
			}
		})
	}
}

// chaosRawRequest builds an FC04 request frame.
func chaosRawRequest(txn uint16, addr, qty int) []byte {
	frame := make([]byte, 12)
	binary.BigEndian.PutUint16(frame[0:2], txn)
	binary.BigEndian.PutUint16(frame[4:6], 6)
	frame[6] = refUnitID
	frame[7] = byte(FCReadInputRegisters)
	binary.BigEndian.PutUint16(frame[8:10], uint16(addr))
	binary.BigEndian.PutUint16(frame[10:12], uint16(qty))
	return frame
}

// chaosCheckRawResponse reads one response from conn and checks that it answers
// the FC04 request (txn, addr, qty).
func chaosCheckRawResponse(conn net.Conn, txn uint16, addr, qty int) error {
	res, err := readMBAPResponse(conn)
	if err != nil {
		return err
	}
	if res.TransactionID != txn || res.UnitID != refUnitID || res.FunctionCode != byte(FCReadInputRegisters) {
		return fmt.Errorf("response txn=%d unit=%d fc=0x%02x, want txn=%d", res.TransactionID, res.UnitID, res.FunctionCode, txn)
	}
	if len(res.Payload) != 1+2*qty || int(res.Payload[0]) != 2*qty {
		return fmt.Errorf("response to txn %d has a %d-byte payload, want %d", txn, len(res.Payload), 1+2*qty)
	}
	for i := 0; i < qty; i++ {
		if got := binary.BigEndian.Uint16(res.Payload[1+2*i:]); got != chaosInputValue(addr+i) {
			return fmt.Errorf("response to txn %d: register %d = 0x%04x, want 0x%04x", txn, addr+i, got, chaosInputValue(addr+i))
		}
	}
	return nil
}

// TestChaosServerCoalescedAndFragmentedRequests sends the server several requests
// coalesced into one write, and requests cut at every byte boundary (including in
// the middle of a coalesced batch): each must be answered once, in order.
func TestChaosServerCoalescedAndFragmentedRequests(t *testing.T) {
	srv := chaosStartServer(t, "tcp", chaosNewDevice(1), nil)
	conn, err := net.DialTimeout("tcp", srv.addr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))

	// Twenty requests in one write.
	const batch = 20
	var all []byte
	for i := 0; i < batch; i++ {
		all = append(all, chaosRawRequest(uint16(100+i), 3*i, 1+i)...)
	}
	if _, err := conn.Write(all); err != nil {
		t.Fatalf("write: %v", err)
	}
	for i := 0; i < batch; i++ {
		if err := chaosCheckRawResponse(conn, uint16(100+i), 3*i, 1+i); err != nil {
			t.Fatalf("coalesced batch: %v", err)
		}
	}

	// Three coalesced requests, cut in two writes at every boundary.
	var three []byte
	for i := 0; i < 3; i++ {
		three = append(three, chaosRawRequest(uint16(200+i), 7*i, 2+i)...)
	}
	for k := 1; k < len(three); k++ {
		if _, err := conn.Write(three[:k]); err != nil {
			t.Fatalf("split at %d: write: %v", k, err)
		}
		time.Sleep(200 * time.Microsecond)
		if _, err := conn.Write(three[k:]); err != nil {
			t.Fatalf("split at %d: write: %v", k, err)
		}
		for i := 0; i < 3; i++ {
			if err := chaosCheckRawResponse(conn, uint16(200+i), 7*i, 2+i); err != nil {
				t.Fatalf("split at %d: %v", k, err)
			}
		}
	}

	// One byte per write.
	for _, b := range chaosRawRequest(300, 40, 5) {
		if _, err := conn.Write([]byte{b}); err != nil {
			t.Fatalf("byte-wise write: %v", err)
		}
	}
	if err := chaosCheckRawResponse(conn, 300, 40, 5); err != nil {
		t.Fatalf("byte-wise request: %v", err)
	}

	// Nothing else may be pending: the server answers each request exactly once.
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Millisecond))
	if n, err := conn.Read(make([]byte, 1)); n != 0 || err == io.EOF {
		t.Errorf("unexpected data or close after the last response (n=%d, err=%v)", n, err)
	}
}
