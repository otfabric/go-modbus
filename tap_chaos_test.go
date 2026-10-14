// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// This file holds the PDUTap checks of the chaos and stress suites, and the
// PDUTap concurrency tests.

// chaosTap is a self-checking, adversarial PDUTap. For every exchange it checks
// that the response answers the request (same function code or its exception),
// and, when checkData is set, that a well-formed response to a read of the
// read-only tables holds the data of the addresses that were requested: an
// exchange that pairs a request with another request's response shows up as
// wrong data. It then overwrites both PDUs, so a client that kept using the
// slices it handed to the tap would return garbage to its caller.
type chaosTap struct {
	unit      uint8
	checkData bool

	count     atomic.Int64
	checked   atomic.Int64
	malformed atomic.Int64
	failures  atomic.Int64

	mu   sync.Mutex
	errs []string
}

func chaosNewTap(unit uint8, checkData bool) *chaosTap {
	return &chaosTap{unit: unit, checkData: checkData}
}

func (c *chaosTap) failf(format string, args ...any) {
	if c.failures.Add(1) > 10 {
		return
	}
	c.mu.Lock()
	c.errs = append(c.errs, fmt.Sprintf(format, args...))
	c.mu.Unlock()
}

func (c *chaosTap) OnPDU(_ context.Context, ex PDUExchange) {
	c.count.Add(1)
	c.inspect(ex)
	for i := range ex.Request {
		ex.Request[i] = 0xEE
	}
	for i := range ex.Response {
		ex.Response[i] = 0xEE
	}
}

func (c *chaosTap) inspect(ex PDUExchange) {
	if ex.Received.IsZero() {
		c.failf("exchange without a receive time: % X -> % X", ex.Request, ex.Response)
	}
	if len(ex.Request) == 0 || len(ex.Response) == 0 {
		c.failf("empty PDU: % X -> % X", ex.Request, ex.Response)
		return
	}
	if ex.UnitID != c.unit {
		c.failf("unit %d reported, the workload only addresses unit %d", ex.UnitID, c.unit)
	}
	if ex.Response[0]&^0x80 != ex.Request[0] {
		c.failf("request FC 0x%02X reported with response FC 0x%02X", ex.Request[0], ex.Response[0])
		return
	}
	if !c.checkData || len(ex.Request) != 5 || ex.Response[0] != ex.Request[0] {
		return
	}
	addr := int(binary.BigEndian.Uint16(ex.Request[1:3]))
	qty := int(binary.BigEndian.Uint16(ex.Request[3:5]))
	switch FunctionCode(ex.Request[0]) {
	case FCReadInputRegisters:
		if len(ex.Response) != 2+2*qty || int(ex.Response[1]) != 2*qty {
			c.malformed.Add(1)
			return
		}
		c.checked.Add(1)
		for i := 0; i < qty; i++ {
			got := binary.BigEndian.Uint16(ex.Response[2+2*i:])
			if want := chaosInputValue(addr + i); got != want {
				c.failf("FC04 %d x%d: register %d reported as 0x%04X, holds 0x%04X: response of another request",
					addr, qty, addr+i, got, want)
				return
			}
		}
	case FCReadDiscreteInputs:
		n := (qty + 7) / 8
		if len(ex.Response) != 2+n || int(ex.Response[1]) != n {
			c.malformed.Add(1)
			return
		}
		c.checked.Add(1)
		for i := 0; i < qty; i++ {
			got := ex.Response[2+i/8]&(1<<(i%8)) != 0
			if want := chaosDiscreteValue(addr + i); got != want {
				c.failf("FC02 %d x%d: input %d reported as %v, holds %v: response of another request",
					addr, qty, addr+i, got, want)
				return
			}
		}
	default:
		// Holding registers and coils change under the workload's writes.
	}
}

// check asserts the tap's findings and its counts against the client's metrics
// once every call has returned: each successful call put at least one exchange
// on the wire, and no exchange is reported without a successful transport
// attempt. serverRequests, when not negative, is the number of requests the
// server handled; nothing can be reported that the server did not answer.
func (c *chaosTap) check(t *testing.T, m *chaosClientMetrics, serverRequests int64) {
	t.Helper()
	c.mu.Lock()
	for _, e := range c.errs {
		t.Errorf("PDUTap: %s", e)
	}
	c.mu.Unlock()
	if n := c.failures.Load(); n > 10 {
		t.Errorf("PDUTap: %d failures in total", n)
	}
	n := c.count.Load()
	if ok := m.responses.Load(); n < ok {
		t.Errorf("PDUTap: %d exchanges reported for %d successful calls", n, ok)
	}
	if a := m.attempts.Load(); n > a {
		t.Errorf("PDUTap: %d exchanges reported, the client made %d transport attempts", n, a)
	}
	if serverRequests >= 0 && n > serverRequests {
		t.Errorf("PDUTap: %d exchanges reported, the server handled %d requests", n, serverRequests)
	}
	if c.checkData && c.checked.Load() == 0 {
		t.Errorf("PDUTap: no read of the read-only tables was checked")
	}
}

func (c *chaosTap) String() string {
	return fmt.Sprintf("tap: %d exchanges, %d data-checked, %d malformed", c.count.Load(), c.checked.Load(), c.malformed.Load())
}

// tapCallKey is the context key of tapCorrelator.
type tapCallKey struct{}

// tapCorrelator is a PDUTap that ties each exchange to the call that caused it
// through the call's context, and records the exchanges per call.
type tapCorrelator struct {
	mu    sync.Mutex
	calls map[int][]PDUExchange
	stray atomic.Int64
}

func (r *tapCorrelator) OnPDU(ctx context.Context, ex PDUExchange) {
	id, ok := ctx.Value(tapCallKey{}).(int)
	if !ok {
		r.stray.Add(1)
		return
	}
	r.mu.Lock()
	r.calls[id] = append(r.calls[id], ex)
	r.mu.Unlock()
}

// Many goroutines share one client, in each of its modes. Every call reports
// exactly the request it sent, with the response to that request, to the
// context of that call: no exchange is lost, duplicated or attributed to another
// goroutine, and every receive time lies within its call.
func TestStressPDUTapConcurrentCallers(t *testing.T) {
	modes := []chaosMode{chaosModeSingle, chaosModePool, chaosModePoolRetry}
	for _, kind := range transports {
		for _, mode := range modes {
			kind, mode := kind, mode
			t.Run(kind+"/"+mode.name, func(t *testing.T) {
				stressSoak(t, 80, 0, func(t *testing.T, seed int64) {
					tapRunConcurrentCallers(t, kind, mode, seed)
				})
			})
		}
	}
}

func tapRunConcurrentCallers(t *testing.T, kind string, mode chaosMode, seed int64) {
	workers := stressScale(64, 16)
	ops := stressScale(40, 10)

	baseline := chaosGoroutines()
	wd := chaosNewWatchdog(t)
	defer wd.stop()
	dev := chaosNewDevice(seed)
	srv := chaosStartServer(t, kind, dev, nil)
	defer srv.stop()

	rec := &tapCorrelator{calls: make(map[int][]PDUExchange)}
	metrics := &chaosClientMetrics{}
	conf := chaosClientConfig(t, kind, srv.port(), mode, 5*time.Second, metrics)
	conf.PDUTap = rec
	client, err := New(conf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	type call struct {
		req           []byte
		before, after time.Time
	}
	calls := make([][]call, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		w := w
		calls[w] = make([]call, ops)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				// Distinct ranges per call, so that a response of another
				// call never looks like the right one.
				addr := uint16((w*ops + i) % (refSpace - 16))
				qty := uint16(1 + (w+i)%16)
				ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), tapCallKey{}, w*ops+i), 10*time.Second)
				c := &calls[w][i]
				c.req = []byte{byte(FCReadInputRegisters), byte(addr >> 8), byte(addr), byte(qty >> 8), byte(qty)}
				c.before = time.Now()
				var err error
				wd.guard(fmt.Sprintf("worker %d call %d", w, i), 15*time.Second, func() {
					_, err = client.ReadRegisters(ctx, refUnitID, addr, qty, InputRegister)
				})
				c.after = time.Now()
				cancel()
				if err != nil {
					t.Errorf("worker %d call %d: %v", w, i, err)
					return
				}
			}
		}()
	}
	chaosWait(t, &wg, 60*time.Second, "callers")

	if n := rec.stray.Load(); n != 0 {
		t.Errorf("%d exchanges reported without the caller's context", n)
	}
	for w := range calls {
		for i, c := range calls[w] {
			got := rec.calls[w*ops+i]
			if len(got) != 1 {
				t.Errorf("worker %d call %d: %d exchanges reported, want 1", w, i, len(got))
				continue
			}
			ex := got[0]
			if string(ex.Request) != string(c.req) {
				t.Errorf("worker %d call %d: request % X reported, sent % X", w, i, ex.Request, c.req)
				continue
			}
			addr := int(binary.BigEndian.Uint16(c.req[1:3]))
			qty := int(binary.BigEndian.Uint16(c.req[3:5]))
			want := []byte{byte(FCReadInputRegisters), byte(2 * qty)}
			for k := 0; k < qty; k++ {
				want = binary.BigEndian.AppendUint16(want, chaosInputValue(addr+k))
			}
			if string(ex.Response) != string(want) {
				t.Errorf("worker %d call %d: response % X reported, want % X", w, i, ex.Response, want)
			}
			if ex.Received.Before(c.before) || ex.Received.After(c.after) {
				t.Errorf("worker %d call %d: received %v, outside the call", w, i, ex.Received)
			}
		}
	}
	_ = client.Close()
	srv.stop()
	metrics.check(t, "client")
	if got, want := int64(len(rec.calls)), int64(workers*ops); got != want {
		t.Errorf("%d calls reported, want %d", got, want)
	}
	if got, want := srv.metrics.total(), metrics.attempts.Load(); got != want {
		t.Errorf("server saw %d requests, the client made %d attempts", got, want)
	}
	chaosLeakCheck(t, baseline)
}

// A tap that blocks holds up only the call it reports: the connection was
// released before the tap is called, so other callers keep going, even on a
// single-connection client.
func TestPDUTapBlockingTapDoesNotHoldConnection(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		baseline := chaosGoroutines()
		dev := chaosNewDevice(1)
		srv := chaosStartServer(t, kind, dev, nil)
		defer srv.stop()

		entered := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		conf := chaosClientConfig(t, kind, srv.port(), chaosModeSingle, 5*time.Second, nil)
		conf.PDUTap = PDUTapFunc(func(ctx context.Context, _ PDUExchange) {
			if ctx.Value(tapCallKey{}) == nil {
				return
			}
			once.Do(func() { close(entered) })
			<-release
		})
		client := chaosOpenClient(t, conf)

		blocked := make(chan error, 1)
		go func() {
			ctx := context.WithValue(context.Background(), tapCallKey{}, 1)
			_, err := client.ReadRegisters(ctx, refUnitID, 0, 4, InputRegister)
			blocked <- err
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("the tap was not called")
		}
		for i := 0; i < 20; i++ {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			got, err := client.ReadRegisters(ctx, refUnitID, uint16(i), 2, InputRegister)
			cancel()
			if err != nil {
				close(release)
				t.Fatalf("call %d while the tap blocks another call: %v", i, err)
			}
			if got[0] != chaosInputValue(i) {
				t.Errorf("call %d: register %d = 0x%04X, want 0x%04X", i, i, got[0], chaosInputValue(i))
			}
		}
		select {
		case err := <-blocked:
			t.Fatalf("the call whose tap blocks returned early: %v", err)
		default:
		}
		close(release)
		select {
		case err := <-blocked:
			if err != nil {
				t.Errorf("the call whose tap blocked: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the call whose tap was released did not return")
		}
		_ = client.Close()
		srv.stop()
		chaosLeakCheck(t, baseline)
	})
}

// A tap that panics takes down only the call it reports. The panic reaches the
// caller (as a panicking ClientMetrics does) and the client stays usable, with
// concurrent callers unaffected.
func TestPDUTapPanicLeavesClientUsable(t *testing.T) {
	for _, mode := range []chaosMode{chaosModeSingle, chaosModePool} {
		mode := mode
		t.Run(mode.name, func(t *testing.T) {
			baseline := chaosGoroutines()
			dev := chaosNewDevice(1)
			srv := chaosStartServer(t, "tcp", dev, nil)
			defer srv.stop()

			conf := chaosClientConfig(t, "tcp", srv.port(), mode, 5*time.Second, nil)
			conf.PDUTap = PDUTapFunc(func(ctx context.Context, _ PDUExchange) {
				if ctx.Value(tapCallKey{}) != nil {
					panic("tap panic")
				}
			})
			client := chaosOpenClient(t, conf)

			const workers, ops = 8, 25
			var panics, oks atomic.Int64
			var wg sync.WaitGroup
			for w := 0; w < workers; w++ {
				w := w
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < ops; i++ {
						ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
						if (w+i)%3 == 0 {
							ctx = context.WithValue(ctx, tapCallKey{}, i)
						}
						func() {
							defer func() {
								if recover() != nil {
									panics.Add(1)
								}
							}()
							got, err := client.ReadRegisters(ctx, refUnitID, uint16(w), 1, InputRegister)
							if err != nil {
								t.Errorf("worker %d call %d: %v", w, i, err)
								return
							}
							if got[0] != chaosInputValue(w) {
								t.Errorf("worker %d call %d: 0x%04X, want 0x%04X", w, i, got[0], chaosInputValue(w))
							}
							oks.Add(1)
						}()
						cancel()
					}
				}()
			}
			chaosWait(t, &wg, 30*time.Second, "callers")
			if panics.Load() == 0 || oks.Load() == 0 {
				t.Fatalf("%d panics, %d successful calls: the test did not exercise both", panics.Load(), oks.Load())
			}
			if got := panics.Load() + oks.Load(); got != workers*ops {
				t.Errorf("%d calls accounted for, want %d", got, workers*ops)
			}
			chaosAssertUsable(t, client, "client after tap panics")
			_ = client.Close()
			srv.stop()
			chaosLeakCheck(t, baseline)
		})
	}
}
