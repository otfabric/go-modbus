// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/sunspec"
)

// Tests of Config.AllowedFunctionCodes: the client-side function code allow-list.

// readOnlyRegisterFCs is the allow-list of a client that may only read registers.
var readOnlyRegisterFCs = []FunctionCode{FCReadHoldingRegisters, FCReadInputRegisters}

// fcAllowedByReadOnly reports whether fc is on readOnlyRegisterFCs.
func fcAllowedByReadOnly(fc FunctionCode) bool {
	return fc == FCReadHoldingRegisters || fc == FCReadInputRegisters
}

// countingRetry is a RetryPolicy that counts how often it is consulted.
type countingRetry struct{ calls atomic.Int32 }

func (r *countingRetry) ShouldRetry(int, error) (bool, time.Duration) {
	r.calls.Add(1)
	return false, 0
}

// With only FC03 and FC04 allowed, every other exported client method fails
// with ErrFunctionCodeNotAllowed, the server receives nothing for it, and the
// reads keep working on the same connection. Metrics: a refused request is
// reported as OnRequest followed by OnError carrying the sentinel, with no
// transport attempt.
func TestAllowedFunctionCodes_EveryClientMethod(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		cm, sm := &e2eAttemptMetrics{}, &e2eMetrics{}
		retry := &countingRetry{}
		p := e2eStart(t, kind, dev, e2eOpts{
			server: func(c *ServerConfig) { c.Metrics = sm },
			client: func(c *Config) {
				c.AllowedFunctionCodes = readOnlyRegisterFCs
				c.Metrics = cm
				c.RetryPolicy = retry
			},
		})
		ctx := context.Background()
		e2eEventually(t, "one connection", func() bool { return p.conns() == 1 })
		cm.take()

		var blocked, allowed int
		for _, op := range e2eAllOps() {
			err := op.call(ctx, p.client)
			cevents, sevents, calls := cm.take(), sm.take(), dev.takeCalls()

			if fcAllowedByReadOnly(op.fc) && op.requests == 1 {
				allowed++
				if err != nil {
					t.Errorf("%s: allowed read failed: %v", op.name, err)
				}
				if len(calls) != 1 {
					t.Errorf("%s: device saw %d requests, want 1", op.name, len(calls))
				}
				continue
			}

			blocked++
			if !errors.Is(err, ErrFunctionCodeNotAllowed) {
				t.Errorf("%s: error %v, want ErrFunctionCodeNotAllowed", op.name, err)
				continue
			}

			// WriteRegisterBit and UpdateRegisterMask read (allowed) before they
			// write (refused): the device sees the read and nothing else.
			wantCalls := op.requests - 1
			if len(calls) != wantCalls || len(sevents) != 2*wantCalls {
				t.Errorf("%s: device saw %d requests (%d server metric events), want %d", op.name, len(calls), len(sevents), wantCalls)
			}
			for _, c := range calls {
				if c.IsWrite || !fcAllowedByReadOnly(c.FC) {
					t.Errorf("%s: device received %+v", op.name, c)
				}
			}

			// The last two client events are the refused request and its outcome.
			if len(cevents) < 2 {
				t.Errorf("%s: client metric events %v", op.name, cevents)
				continue
			}
			req, out := cevents[len(cevents)-2], cevents[len(cevents)-1]
			if req.Kind != "request" || out.Kind != "error" || req.FC != out.FC || fcAllowedByReadOnly(req.FC) ||
				req.Unit != e2eUnit || !errors.Is(out.Err, ErrFunctionCodeNotAllowed) {
				t.Errorf("%s: refused request reported as %v %v", op.name, req, out)
			}
			var attempts int
			for _, e := range cevents {
				if e.Kind == "attempt" || e.Kind == "retrydial" {
					attempts++
				}
			}
			if attempts != wantCalls {
				t.Errorf("%s: %d transport attempts reported, want %d: %v", op.name, attempts, wantCalls, cevents)
			}
			if !strings.Contains(err.Error(), "0x") {
				t.Errorf("%s: error %q does not name the function code", op.name, err)
			}
		}
		if blocked == 0 || allowed == 0 {
			t.Fatalf("blocked %d and allowed %d operations: the operation list is not what this test expects", blocked, allowed)
		}
		if n := retry.calls.Load(); n != 0 {
			t.Errorf("retry policy consulted %d times", n)
		}

		// The client is still open on the connection it started with.
		dev.setHolding(100, 0xBEEF)
		dev.setInput(100, 0xCAFE)
		if v, err := p.client.ReadHoldingRegister(ctx, e2eUnit, 100); err != nil || v != 0xBEEF {
			t.Errorf("ReadHoldingRegister after refused requests: %#04x, %v", v, err)
		}
		if v, err := p.client.ReadInputRegister(ctx, e2eUnit, 100); err != nil || v != 0xCAFE {
			t.Errorf("ReadInputRegister after refused requests: %#04x, %v", v, err)
		}
		if n := p.conns(); n != 1 {
			t.Errorf("server tracks %d connections, want 1", n)
		}
		for _, e := range cm.take() {
			if e.Kind == "retrydial" {
				t.Errorf("client re-dialled: %v", e)
			}
		}
	})
}

// countingConn counts the bytes read from a connection.
type countingConn struct {
	net.Conn
	n *atomic.Int64
}

func (c countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.n.Add(int64(n))
	return n, err
}

// startCountingMBAPServer starts a raw Modbus/TCP listener that answers every
// request with one register (0x1234) and counts accepted connections and
// received bytes. With closeAfterReply it closes a connection after one answer.
func startCountingMBAPServer(t *testing.T, closeAfterReply bool) (addr string, accepts *atomic.Int32, received *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepts, received = &atomic.Int32{}, &atomic.Int64{}
	go func() {
		for {
			sock, err := ln.Accept()
			if err != nil {
				return
			}
			accepts.Add(1)
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				for {
					frame, err := readMBAPFrame(countingConn{Conn: c, n: received})
					if err != nil || len(frame) < 8 {
						return
					}
					if _, err := c.Write(normalFrame(frame[0:2], frame[6], frame[7], []byte{0x02, 0x12, 0x34})); err != nil {
						return
					}
					if closeAfterReply {
						return
					}
				}
			}(sock)
		}
	}()
	return ln.Addr().String(), accepts, received
}

// refusedOps returns the operations of e2eAllOps that put nothing on the wire
// when only FC03 and FC04 are allowed.
func refusedOps() []e2eOp {
	var out []e2eOp
	for _, op := range e2eAllOps() {
		if !fcAllowedByReadOnly(op.fc) {
			out = append(out, op)
		}
	}
	return out
}

// Wire level, Modbus/TCP: a refused request writes no byte and opens no
// connection, whether the client is open, not open yet, or has lost its
// connection.
func TestAllowedFunctionCodes_NothingOnTheWire_TCP(t *testing.T) {
	ctx := context.Background()
	// mbapReadRequestLen is the size of one FC03/FC04 request: MBAP header (7) + PDU (5).
	const mbapReadRequestLen = 12

	t.Run("open client", func(t *testing.T) {
		addr, accepts, received := startCountingMBAPServer(t, false)
		c, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second, AllowedFunctionCodes: readOnlyRegisterFCs})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := c.Open(); err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer func() { _ = c.Close() }()

		for _, op := range refusedOps() {
			if err := op.call(ctx, c); !errors.Is(err, ErrFunctionCodeNotAllowed) {
				t.Errorf("%s: error %v, want ErrFunctionCodeNotAllowed", op.name, err)
			}
		}
		// A read after the refused requests: had any of them been written, the
		// server would have counted its bytes before those of the read.
		if v, err := c.ReadHoldingRegister(ctx, 1, 0); err != nil || v != 0x1234 {
			t.Fatalf("ReadHoldingRegister: %#04x, %v", v, err)
		}
		if n := received.Load(); n != mbapReadRequestLen {
			t.Errorf("server received %d bytes, want %d (one read request)", n, mbapReadRequestLen)
		}
		if n := accepts.Load(); n != 1 {
			t.Errorf("server accepted %d connections, want 1", n)
		}
	})

	t.Run("client not open", func(t *testing.T) {
		addr, accepts, received := startCountingMBAPServer(t, false)
		c, err := New(Config{URL: "tcp://" + addr, AllowedFunctionCodes: readOnlyRegisterFCs})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		// The refusal does not depend on the state of the client.
		if err := c.WriteRegister(ctx, 1, 0, 1); !errors.Is(err, ErrFunctionCodeNotAllowed) {
			t.Errorf("WriteRegister: error %v, want ErrFunctionCodeNotAllowed", err)
		}
		if _, err := c.ReadHoldingRegister(ctx, 1, 0); !errors.Is(err, ErrClientNotOpen) {
			t.Errorf("ReadHoldingRegister: error %v, want ErrClientNotOpen", err)
		}
		time.Sleep(50 * time.Millisecond)
		if a, n := accepts.Load(), received.Load(); a != 0 || n != 0 {
			t.Errorf("server accepted %d connections and received %d bytes, want none", a, n)
		}
	})

	t.Run("no redial", func(t *testing.T) {
		addr, accepts, received := startCountingMBAPServer(t, true)
		retry := &countingRetry{}
		c, err := New(Config{URL: "tcp://" + addr, Timeout: 2 * time.Second, RetryPolicy: retry,
			AllowedFunctionCodes: readOnlyRegisterFCs})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := c.Open(); err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer func() { _ = c.Close() }()
		// The server answers this read and closes the connection.
		if _, err := c.ReadHoldingRegister(ctx, 1, 0); err != nil {
			t.Fatalf("ReadHoldingRegister: %v", err)
		}
		for _, op := range refusedOps() {
			if err := op.call(ctx, c); !errors.Is(err, ErrFunctionCodeNotAllowed) {
				t.Errorf("%s: error %v, want ErrFunctionCodeNotAllowed", op.name, err)
			}
		}
		time.Sleep(50 * time.Millisecond)
		if a, n := accepts.Load(), received.Load(); a != 1 || n != mbapReadRequestLen {
			t.Errorf("server accepted %d connections and received %d bytes, want 1 and %d", a, n, mbapReadRequestLen)
		}
		if n := retry.calls.Load(); n != 0 {
			t.Errorf("retry policy consulted %d times for refused requests", n)
		}
	})
}

// Wire level, serial framing (RTU over TCP): the allow-list sits above the
// transports, so it holds for the serial-framed ones as well.
func TestAllowedFunctionCodes_NothingOnTheWire_RTUOverTCP(t *testing.T) {
	addr, frames := startRTUOverTCPSlave(t, func(frame []byte) []byte {
		return adu.AssembleRTUFrame(frame[0], frame[1], []byte{0x02, 0x12, 0x34})
	})
	c, err := New(Config{URL: "rtuovertcp://" + addr, Speed: 115200, Timeout: 2 * time.Second,
		AllowedFunctionCodes: readOnlyRegisterFCs})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = c.Close() }()
	ctx := context.Background()

	for _, op := range refusedOps() {
		if err := op.call(ctx, c); !errors.Is(err, ErrFunctionCodeNotAllowed) {
			t.Errorf("%s: error %v, want ErrFunctionCodeNotAllowed", op.name, err)
		}
	}
	if v, err := c.ReadHoldingRegister(ctx, e2eUnit, 0); err != nil || v != 0x1234 {
		t.Fatalf("ReadHoldingRegister: %#04x, %v", v, err)
	}
	if err := c.WriteRegister(ctx, e2eUnit, 0, 1); !errors.Is(err, ErrFunctionCodeNotAllowed) {
		t.Errorf("WriteRegister: error %v, want ErrFunctionCodeNotAllowed", err)
	}
	if v, err := c.ReadInputRegister(ctx, e2eUnit, 0); err != nil || v != 0x1234 {
		t.Fatalf("ReadInputRegister: %#04x, %v", v, err)
	}
	// The slave reads fixed 8-byte frames: a refused request on the wire would
	// show up here as a frame, or shift the two below.
	got := frames()
	if len(got) != 2 || got[0][1] != byte(FCReadHoldingRegisters) || got[1][1] != byte(FCReadInputRegisters) {
		t.Errorf("frames on the wire % X, want one FC03 and one FC04 request", got)
	}
}

// A nil or empty list means no restriction.
func TestAllowedFunctionCodes_NilAndEmptyAreUnrestricted(t *testing.T) {
	for name, list := range map[string][]FunctionCode{"nil": nil, "empty": {}} {
		list := list
		t.Run(name, func(t *testing.T) {
			dev := e2eNewDevice()
			p := e2eStart(t, "tcp", dev, e2eOpts{client: func(c *Config) { c.AllowedFunctionCodes = list }})
			if p.client.allowedFCs != nil {
				t.Fatal("an allow-list was built")
			}
			for _, op := range e2eAllOps() {
				err := op.call(context.Background(), p.client)
				if errors.Is(err, ErrFunctionCodeNotAllowed) {
					t.Errorf("%s: %v", op.name, err)
				}
				if err != nil {
					t.Errorf("%s: %v", op.name, err)
				}
			}
			if got := dev.holdingAt(923, 1); got[0] != 0x1234 {
				t.Errorf("WriteRegister did not reach the device: %#04x", got[0])
			}
		})
	}
}

// The list is not limited to reads, and each code stands for itself.
func TestAllowedFunctionCodes_ArbitraryList(t *testing.T) {
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp", dev, e2eOpts{client: func(c *Config) {
		c.AllowedFunctionCodes = []FunctionCode{FCWriteSingleRegister, FCWriteSingleRegister, FCEncapsulatedInterface}
	}})
	ctx := context.Background()
	if err := p.client.WriteRegister(ctx, e2eUnit, 5, 0x0A0B); err != nil {
		t.Fatalf("WriteRegister: %v", err)
	}
	if _, err := p.client.ReadAllDeviceIdentification(ctx, e2eUnit); err != nil {
		t.Fatalf("ReadAllDeviceIdentification: %v", err)
	}
	if _, err := p.client.ReadHoldingRegister(ctx, e2eUnit, 5); !errors.Is(err, ErrFunctionCodeNotAllowed) {
		t.Errorf("ReadHoldingRegister: error %v, want ErrFunctionCodeNotAllowed", err)
	}
	if err := p.client.WriteRegisters(ctx, e2eUnit, 5, []uint16{1}); !errors.Is(err, ErrFunctionCodeNotAllowed) {
		t.Errorf("WriteRegisters: error %v, want ErrFunctionCodeNotAllowed", err)
	}
	if got := dev.holdingAt(5, 1); got[0] != 0x0A0B {
		t.Errorf("holding register 5 = %#04x, want 0x0A0B", got[0])
	}
}

// New rejects a list with a value that is not a request function code.
func TestAllowedFunctionCodes_InvalidListRejected(t *testing.T) {
	for _, fc := range []FunctionCode{0x00, 0x80, 0x83, 0xFF} {
		conf := Config{URL: "tcp://127.0.0.1:502", AllowedFunctionCodes: []FunctionCode{FCReadHoldingRegisters, fc}}
		c, err := New(conf)
		if !errors.Is(err, ErrConfigurationError) {
			t.Errorf("0x%02X: New returned %v, %v, want ErrConfigurationError", uint8(fc), c, err)
			continue
		}
		var ce *ConfigurationError
		if !errors.As(err, &ce) || ce.Field != "AllowedFunctionCodes" {
			t.Errorf("0x%02X: error %v does not name the field", uint8(fc), err)
		}
		if err := ValidateConfig(conf); !errors.Is(err, ErrConfigurationError) {
			t.Errorf("0x%02X: ValidateConfig returned %v", uint8(fc), err)
		}
	}
	// Any request function code is accepted, the ones the client has no method for included.
	if _, err := New(Config{URL: "tcp://127.0.0.1:502", AllowedFunctionCodes: []FunctionCode{0x01, 0x41, 0x7F}}); err != nil {
		t.Errorf("New: %v", err)
	}
}

// The list is copied by New, and NewConfig carries it.
func TestAllowedFunctionCodes_CopiedAndGrouped(t *testing.T) {
	list := []FunctionCode{FCReadHoldingRegisters}
	conf := NewConfig(
		TransportConfig{URL: "tcp://127.0.0.1:1"},
		ExecutionConfig{AllowedFunctionCodes: list},
		ObservabilityConfig{},
	)
	if len(conf.AllowedFunctionCodes) != 1 || conf.AllowedFunctionCodes[0] != FCReadHoldingRegisters {
		t.Fatalf("NewConfig dropped the list: %v", conf.AllowedFunctionCodes)
	}
	c, err := New(conf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	list[0] = FCWriteSingleRegister
	if err := c.WriteRegister(context.Background(), 1, 0, 1); !errors.Is(err, ErrFunctionCodeNotAllowed) {
		t.Errorf("WriteRegister after changing the caller's slice: %v", err)
	}
	if !c.allowedFCs[FCReadHoldingRegisters] {
		t.Error("FC03 no longer allowed after changing the caller's slice")
	}
}

// The probe helpers build their requests themselves; they are refused like any
// other request, with an error and not with a probe result.
func TestAllowedFunctionCodes_Probes(t *testing.T) {
	dev := e2eNewDevice()
	cm := &e2eMetrics{}
	p := e2eStart(t, "tcp", dev, e2eOpts{client: func(c *Config) {
		c.AllowedFunctionCodes = readOnlyRegisterFCs
		c.Metrics = cm
	}})
	ctx := context.Background()

	if ok, err := p.client.SupportsFunction(ctx, e2eUnit, FCReadCoils); ok || !errors.Is(err, ErrFunctionCodeNotAllowed) {
		t.Errorf("SupportsFunction(FC01) = %v, %v", ok, err)
	}
	if ok, err := p.client.SupportsDeviceIdentification(ctx, e2eUnit); ok || !errors.Is(err, ErrFunctionCodeNotAllowed) {
		t.Errorf("SupportsDeviceIdentification = %v, %v", ok, err)
	}
	res, err := p.client.ProbeFunction(ctx, e2eUnit, FCDiagnostics)
	if !errors.Is(err, ErrFunctionCodeNotAllowed) || res.Supported || res.Err != nil {
		t.Errorf("ProbeFunction(FC08) = %+v, %v", res, err)
	}
	if n := dev.callCount(); n != 0 {
		t.Errorf("device saw %d requests", n)
	}
	events := cm.take()
	e2eCheckPairs(t, "refused probes", events)
	if len(events) != 6 {
		t.Errorf("%d metric events, want 3 request/error pairs: %v", len(events), events)
	}
	for i := 1; i < len(events); i += 2 {
		if events[i].Kind != "error" || !errors.Is(events[i].Err, ErrFunctionCodeNotAllowed) {
			t.Errorf("outcome %v, want an error carrying ErrFunctionCodeNotAllowed", events[i])
		}
	}

	if ok, err := p.client.SupportsFunction(ctx, e2eUnit, FCReadHoldingRegisters); !ok || err != nil {
		t.Errorf("SupportsFunction(FC03) = %v, %v", ok, err)
	}
	if res, err := p.client.ProbeFunction(ctx, e2eUnit, FCReadInputRegisters); err != nil || !res.Supported {
		t.Errorf("ProbeFunction(FC04) = %+v, %v", res, err)
	}
}

// The sunspec package reads through the client and is bound by its list.
func TestAllowedFunctionCodes_SunSpec(t *testing.T) {
	dev := e2eNewDevice()
	regs, _ := e2eSunSpecMap(40000, []e2eSunSpecModel{{id: 1, length: 66}})
	dev.setHolding(40000, regs...)
	dev.setInput(40000, regs...)
	p := e2eStart(t, "tcp", dev, e2eOpts{client: func(c *Config) {
		c.AllowedFunctionCodes = []FunctionCode{FCReadHoldingRegisters}
	}})
	ctx := context.Background()
	r := e2eSunSpecReader{c: p.client}

	if _, err := sunspec.Discover(ctx, r, &sunspec.Options{UnitID: e2eUnit, RegType: sunspec.InputRegister}); !errors.Is(err, ErrFunctionCodeNotAllowed) {
		t.Errorf("Discover in input registers: error %v, want ErrFunctionCodeNotAllowed", err)
	}
	if n := dev.callCount(); n != 0 {
		t.Errorf("device saw %d requests", n)
	}
	res, err := sunspec.Discover(ctx, r, &sunspec.Options{UnitID: e2eUnit, RegType: sunspec.HoldingRegister})
	if err != nil || res == nil || len(res.Models) == 0 {
		t.Errorf("Discover in holding registers: %+v, %v", res, err)
	}
}
