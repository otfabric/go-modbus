// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Parallel stress tests on a healthy link: many goroutines on one pooled client,
// and many clients on one server, up to and beyond MaxClients. They reuse the
// self-checking workload of the chaos suite (see chaosWorkload): on a healthy
// link every call must succeed and return exactly the caller's own data.

// stressSoak runs round once, or in soak mode (MODBUS_STRESS_DURATION) until this
// test's share of the duration has elapsed. pace is the minimum duration of a soak
// round, for tests that open many connections per round (see chaosSoak).
func stressSoak(t *testing.T, seed int64, pace time.Duration, round func(t *testing.T, seed int64)) {
	t.Helper()
	chaosSoak(t, stressEnvDuration, stressSoakShare, pace, seed, round)
}

// stressScale returns full, or short under -short.
func stressScale(full, short int) int {
	if testing.Short() {
		return short
	}
	return full
}

// TestStressSharedPooledClient hammers one pooled client from many goroutines with
// mixed function codes. Asserts: every call succeeds and returns the caller's own
// data, the pool never opens more than MaxConns connections, writes are applied
// exactly as sent, metrics are consistent, nothing leaks.
func TestStressSharedPooledClient(t *testing.T) {
	stressSoak(t, 1, 0, func(t *testing.T, seed int64) {
		const maxConns = 8
		workers := stressScale(128, 32)
		ops := stressScale(60, 10)

		baseline := chaosGoroutines()
		wd := chaosNewWatchdog(t)
		defer wd.stop()
		dev := chaosNewDevice(seed)
		srv := chaosStartServer(t, "tcp", dev, nil)
		defer srv.stop()
		sampler := chaosSampleConns(srv.conns, maxConns)
		defer sampler.stop()

		metrics := &chaosClientMetrics{}
		mode := chaosMode{name: "pool", maxConns: maxConns}
		client, err := New(chaosClientConfig(t, "tcp", srv.port(), mode, 5*time.Second, metrics))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := client.Open(); err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer func() { _ = client.Close() }()

		wl := chaosNewWorkload(t, wd, dev, workers, seed)
		start := time.Now()
		chaosWait(t, wl.run(func(int) *Client { return client }, nil, ops), 60*time.Second, "workload")
		elapsed := time.Since(start)
		wl.verify(func(int) *Client { return client })

		_ = client.Close()
		sampler.stop()
		if !chaosEventually(3*time.Second, func() bool { return srv.conns() == 0 }) {
			t.Errorf("server still holds %d connection(s) after the client was closed", srv.conns())
		}
		srv.stop()

		if peak := sampler.peak.Load(); peak > maxConns {
			t.Errorf("the pool opened %d connections, MaxConns is %d", peak, maxConns)
		}
		calls := wl.stats.count(chaosClassOK)
		if calls < workers*ops {
			t.Errorf("throughput: %d successful calls, want at least %d (%s)", calls, workers*ops, wl.stats.String())
		}
		wl.audit(true)
		metrics.check(t, "client")
		srv.metrics.check(t, "server")
		if got, want := srv.metrics.total(), metrics.attempts.Load(); got != want {
			t.Errorf("server saw %d requests, the client made %d attempts", got, want)
		}
		chaosLeakCheck(t, baseline)
		if testing.Verbose() {
			t.Logf("%d goroutines, %d calls in %v (%.0f calls/s), peak connections %d",
				workers, calls, elapsed.Round(time.Millisecond), float64(calls)/elapsed.Seconds(), sampler.peak.Load())
		}
	})
}

// TestStressManyClients runs more clients than the server's MaxClients. Asserts:
// the server never serves more than MaxClients connections; the clients within
// the limit all work, concurrently, with correct data; the ones beyond it fail
// with a transport error (never hang, never get data); once slots free up they
// connect and work.
func TestStressManyClients(t *testing.T) {
	stressSoak(t, 2, 150*time.Millisecond, func(t *testing.T, seed int64) {
		const maxClients = 16
		const extra = 8
		ops := stressScale(150, 20)

		baseline := chaosGoroutines()
		wd := chaosNewWatchdog(t)
		defer wd.stop()
		dev := chaosNewDevice(seed)
		srv := chaosStartServer(t, "tcp", dev, func(c *ServerConfig) { c.MaxClients = maxClients })
		defer srv.stop()
		sampler := chaosSampleConns(srv.conns, maxClients)
		defer sampler.stop()

		conf := chaosClientConfig(t, "tcp", srv.port(), chaosModeSingle, 5*time.Second, nil)
		open := func() *Client {
			c, err := New(conf)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := c.Open(); err != nil {
				t.Fatalf("Open: %v", err)
			}
			return c
		}
		var clients []*Client
		defer func() {
			for _, c := range clients {
				_ = c.Close()
			}
		}()
		for i := 0; i < maxClients; i++ {
			c := open()
			clients = append(clients, c)
			if ok, err := chaosProbe(c, 5*time.Second); err != nil || !ok {
				t.Fatalf("client %d within MaxClients: ok=%v err=%v", i, ok, err)
			}
		}

		// The clients beyond the limit keep trying while the others work.
		var stop atomic.Bool
		var rejected, admitted atomic.Int64
		var extras sync.WaitGroup
		for i := 0; i < extra; i++ {
			extras.Add(1)
			go func() {
				defer extras.Done()
				for !stop.Load() {
					c, err := New(conf)
					if err != nil {
						t.Errorf("New: %v", err)
						return
					}
					// The kernel completes the TCP handshake before the server rejects the
					// connection, so Open may succeed; the request must then fail.
					if err := c.Open(); err == nil {
						var ok bool
						wd.guard("request beyond MaxClients", 7*time.Second, func() {
							ok, err = chaosProbe(c, 5*time.Second)
						})
						switch class := chaosClassify(err); {
						case err == nil:
							admitted.Add(1)
							if !ok {
								t.Errorf("client beyond MaxClients got wrong data")
							}
						case class != chaosClassTransport:
							t.Errorf("client beyond MaxClients: error class %q (%v), want a transport error", class, err)
						default:
							rejected.Add(1)
						}
					}
					_ = c.Close()
					time.Sleep(time.Millisecond)
				}
			}()
		}

		wl := chaosNewWorkload(t, wd, dev, maxClients, seed)
		clientFor := func(i int) *Client { return clients[i] }
		chaosWait(t, wl.run(clientFor, nil, ops), 60*time.Second, "workload")
		stop.Store(true)
		chaosWait(t, &extras, 30*time.Second, "clients beyond MaxClients")
		wl.verify(clientFor)

		if n := admitted.Load(); n != 0 {
			t.Errorf("%d request(s) of clients beyond MaxClients were served while all %d slots were taken", n, maxClients)
		}
		if rejected.Load() == 0 {
			t.Errorf("no client beyond MaxClients was ever rejected")
		}
		if calls := wl.stats.count(chaosClassOK); calls < maxClients*ops {
			t.Errorf("throughput: %d successful calls, want at least %d", calls, maxClients*ops)
		}

		// Free half of the slots: new clients must get in.
		for i := 0; i < extra; i++ {
			_ = clients[i].Close()
		}
		if !chaosEventually(3*time.Second, func() bool { return srv.conns() <= maxClients-extra }) {
			t.Fatalf("server still holds %d connections after %d clients left", srv.conns(), extra)
		}
		for i := 0; i < extra; i++ {
			c := open()
			clients[i] = c
			if ok, err := chaosProbe(c, 5*time.Second); err != nil || !ok {
				t.Errorf("client %d after slots were freed: ok=%v err=%v", i, ok, err)
			}
		}
		wl.verify(clientFor)

		for _, c := range clients {
			_ = c.Close()
		}
		if !chaosEventually(3*time.Second, func() bool { return srv.conns() == 0 }) {
			t.Errorf("server still holds %d connection(s) after every client was closed", srv.conns())
		}
		srv.stop()
		sampler.stop()
		if peak := sampler.peak.Load(); peak > maxClients {
			t.Errorf("the server held %d connections, MaxClients is %d", peak, maxClients)
		}
		wl.audit(true)
		srv.metrics.check(t, "server")
		chaosLeakCheck(t, baseline)
	})
}

// TestStressPoolExhaustionCancelledWaiters fills the pool, then cancels callers
// that are waiting for a connection. Asserts: a cancelled or expired waiter
// returns promptly with its context's error, the requests in flight are not
// disturbed, the pool neither loses nor gains connections.
func TestStressPoolExhaustionCancelledWaiters(t *testing.T) {
	stressSoak(t, 3, 0, func(t *testing.T, seed int64) {
		const maxConns = 2
		const waiters = 12
		baseline := chaosGoroutines()
		dev := chaosNewDevice(seed)
		entered, cancelled, release := make(chan struct{}, 64), make(chan struct{}, 64), make(chan struct{})
		dev.setHook(chaosBlockingHook(entered, cancelled, release))
		srv := chaosStartServer(t, "tcp", dev, nil)
		defer srv.stop()
		sampler := chaosSampleConns(srv.conns, maxConns)
		defer sampler.stop()
		client, err := New(chaosClientConfig(t, "tcp", srv.port(), chaosMode{maxConns: maxConns}, 10*time.Second, nil))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := client.Open(); err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer func() { _ = client.Close() }()

		// Two requests occupy both connections.
		inFlight := make(chan error, maxConns)
		for i := 0; i < maxConns; i++ {
			go func(i int) {
				ok, err := chaosReadInputs(context.Background(), client, 10*i, 4)
				if err == nil && !ok {
					t.Errorf("request in flight returned wrong data")
				}
				inFlight <- err
			}(i)
		}
		for i := 0; i < maxConns; i++ {
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("requests did not reach the handler")
			}
		}

		// Waiters: half are cancelled, half run into their deadline.
		type result struct {
			err     error
			elapsed time.Duration
			cancel  bool
		}
		results := make(chan result, waiters)
		for i := 0; i < waiters; i++ {
			go func(i int) {
				delay := time.Duration(5+i*3) * time.Millisecond
				var ctx context.Context
				var cancel context.CancelFunc
				byCancel := i%2 == 0
				if byCancel {
					ctx, cancel = context.WithCancel(context.Background())
					timer := time.AfterFunc(delay, cancel)
					defer timer.Stop()
				} else {
					ctx, cancel = context.WithTimeout(context.Background(), delay)
				}
				defer cancel()
				start := time.Now()
				_, err := chaosReadInputs(ctx, client, 0, 1)
				results <- result{err: err, elapsed: time.Since(start), cancel: byCancel}
			}(i)
		}
		for i := 0; i < waiters; i++ {
			select {
			case r := <-results:
				want := chaosClassTimeout
				if r.cancel {
					want = chaosClassCanceled
				}
				if got := chaosClassify(r.err); got != want {
					t.Errorf("waiter: error class %q (%v), want %q", got, r.err, want)
				}
				if r.elapsed > 2*time.Second {
					t.Errorf("waiter returned after %v", r.elapsed)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("a cancelled waiter did not return\n%s", chaosAllStacks())
			}
		}

		// The requests in flight were not disturbed and complete once released.
		close(release)
		for i := 0; i < maxConns; i++ {
			select {
			case err := <-inFlight:
				if err != nil {
					t.Errorf("request in flight failed: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("a request in flight did not complete")
			}
		}
		// The pool still works, at full width.
		var wg sync.WaitGroup
		for i := 0; i < 4*maxConns; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if ok, err := chaosProbe(client, 5*time.Second); err != nil || !ok {
					t.Errorf("request after the waiters left: ok=%v err=%v", ok, err)
				}
			}(i)
		}
		chaosWait(t, &wg, 30*time.Second, "requests after the waiters left")
		if n := srv.conns(); n != maxConns {
			t.Errorf("the pool holds %d connection(s), want %d", n, maxConns)
		}
		_ = client.Close()
		if !chaosEventually(3*time.Second, func() bool { return srv.conns() == 0 }) {
			t.Errorf("server still holds %d connection(s) after Close", srv.conns())
		}
		srv.stop()
		sampler.stop()
		if peak := sampler.peak.Load(); peak > maxConns {
			t.Errorf("the pool opened %d connections, MaxConns is %d", peak, maxConns)
		}
		chaosLeakCheck(t, baseline)
	})
}
