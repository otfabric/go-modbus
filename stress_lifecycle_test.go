// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Lifecycle races, meant to run under the race detector: Open/Close/requests on
// one client, Close with requests in flight, server Start/Stop/Shutdown against
// connecting clients, restarts on one port, cancellation storms, Close during a
// retry back-off.

// stressLifecycleModes are the client modes the lifecycle tests run in.
var stressLifecycleModes = []chaosMode{chaosModeSingle, chaosModeSingleRetry, chaosModePool, chaosModePoolRetry}

// TestStressClientOpenCloseRace calls Open, Close and requests concurrently on
// one client. Asserts: no call hangs; a request either succeeds with correct data
// or fails as "not open" / transport error; after the final Close the server
// holds no connection of that client and no goroutine is left.
func TestStressClientOpenCloseRace(t *testing.T) {
	for _, mode := range stressLifecycleModes {
		mode := mode
		t.Run(mode.name, func(t *testing.T) {
			// A round opens a connection per Close (several with a pool).
			pace := 500 * time.Millisecond
			if mode.pooled() {
				pace = 1500 * time.Millisecond
			}
			stressSoak(t, 10, pace, func(t *testing.T, seed int64) {
				iterations := stressScale(60, 20)
				baseline := chaosGoroutines()
				wd := chaosNewWatchdog(t)
				defer wd.stop()
				srv := chaosStartServer(t, "tcp", chaosNewDevice(seed), nil)
				defer srv.stop()
				metrics := &chaosClientMetrics{}
				client, err := New(chaosClientConfig(t, "tcp", srv.port(), mode, time.Second, metrics))
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				defer func() { _ = client.Close() }()

				var wg sync.WaitGroup
				var okCalls atomic.Int64
				const bound = 8 * time.Second
				var closing sync.WaitGroup
				var stop atomic.Bool
				for g := 0; g < 6; g++ {
					wg.Add(1)
					go func(g int) {
						defer wg.Done()
						rng := rand.New(rand.NewSource(seed*100 + int64(g)))
						for !stop.Load() && !wd.hasTripped() {
							addr := rng.Intn(refSpace - 8)
							var ok bool
							var err error
							wd.guard("request", bound, func() {
								ok, err = chaosReadInputs(context.Background(), client, addr, 8)
							})
							switch class := chaosClassify(err); class {
							case chaosClassOK:
								okCalls.Add(1)
								if !ok {
									t.Errorf("request returned wrong data")
								}
							case chaosClassNotOpen:
								// Do not spin while the client is closed.
								time.Sleep(20 * time.Microsecond)
							case chaosClassTransport:
							default:
								t.Errorf("request: unexpected error class %q: %v", class, err)
							}
						}
					}(g)
				}
				for g := 0; g < 2; g++ {
					wg.Add(1)
					closing.Add(1)
					go func() {
						defer wg.Done()
						for !stop.Load() && !wd.hasTripped() {
							wd.guard("Open", bound, func() {
								if err := client.Open(); err != nil {
									t.Errorf("Open: %v", err)
								}
							})
						}
					}()
					go func(g int) {
						defer closing.Done()
						rng := rand.New(rand.NewSource(seed*200 + int64(g)))
						for i := 0; i < iterations && !wd.hasTripped(); i++ {
							time.Sleep(time.Duration(rng.Intn(1000)) * time.Microsecond)
							wd.guard("Close", bound, func() { _ = client.Close() })
						}
					}(g)
				}
				chaosWait(t, &closing, 60*time.Second, "Close goroutines")
				stop.Store(true)
				chaosWait(t, &wg, 60*time.Second, "open/close/request goroutines")

				// The client must still be usable, and then close cleanly.
				if err := client.Open(); err != nil {
					t.Fatalf("final Open: %v", err)
				}
				if ok, err := chaosProbe(client, 5*time.Second); err != nil || !ok {
					t.Errorf("request after the race: ok=%v err=%v", ok, err)
				}
				if err := client.Close(); err != nil {
					t.Errorf("final Close: %v", err)
				}
				if !chaosEventually(2*time.Second, func() bool { return srv.conns() == 0 }) {
					t.Errorf("server still holds %d connection(s) after the final Close", srv.conns())
				}
				srv.stop()
				if okCalls.Load() == 0 {
					t.Errorf("no request ever succeeded")
				}
				metrics.check(t, "client")
				chaosLeakCheck(t, baseline)
			})
		})
	}
}

// TestStressCloseWithRequestsInFlight closes a client while requests are waiting
// for their responses and others are queued behind them. Asserts: every call
// returns promptly with an error (never a hang until the request timeout, never
// data), and the server ends up without connections.
func TestStressCloseWithRequestsInFlight(t *testing.T) {
	for _, mode := range []chaosMode{chaosModeSingle, chaosModePool} {
		mode := mode
		t.Run(mode.name, func(t *testing.T) {
			const timeout = 300 * time.Millisecond
			const calls = 6
			dev := chaosNewDevice(1)
			entered, cancelled, release := make(chan struct{}, 64), make(chan struct{}, 64), make(chan struct{})
			dev.setHook(chaosBlockingHook(entered, cancelled, release))
			srv := chaosStartServer(t, "tcp", dev, nil)
			client := chaosOpenClient(t, chaosClientConfig(t, "tcp", srv.port(), mode, timeout, nil))

			inFlight := 1
			if mode.pooled() {
				inFlight = mode.maxConns
			}
			results := make(chan error, calls)
			for i := 0; i < calls; i++ {
				go func() {
					_, err := chaosReadInputs(context.Background(), client, 0, 4)
					results <- err
				}()
			}
			for i := 0; i < inFlight; i++ {
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("requests did not reach the handler")
				}
			}
			// Let the remaining callers queue up behind the requests in flight.
			time.Sleep(20 * time.Millisecond)

			start := time.Now()
			if err := client.Close(); err != nil {
				t.Errorf("Close: %v", err)
			}
			prompt := time.After(timeout / 3)
			late := time.After(timeout + 5*time.Second)
			returned, slow := 0, 0
			for returned < calls {
				select {
				case err := <-results:
					returned++
					if err == nil {
						t.Errorf("a request returned success after Close")
					}
				case <-prompt:
					slow = calls - returned
					prompt = nil
				case <-late:
					t.Fatalf("%d request(s) never returned after Close\n%s", calls-returned, chaosAllStacks())
				}
			}
			elapsed := time.Since(start)
			close(release)
			if !chaosEventually(3*time.Second, func() bool { return srv.conns() == 0 }) {
				t.Errorf("server still holds %d connection(s) after Close", srv.conns())
			}
			if slow > 0 {
				t.Errorf("%d of %d request(s) were still blocked %v after Close; the last returned after %v (request timeout %v)",
					slow, calls, timeout/3, elapsed.Round(time.Millisecond), timeout)
			}
		})
	}
}

// TestStressCloseDuringRetryBackoff closes a client while a request is between two
// retry attempts. Asserts: the request returns within the back-off delay with
// ErrClientNotOpen, the client does not re-dial after Close (no connection is left
// behind), and it can be opened again.
func TestStressCloseDuringRetryBackoff(t *testing.T) {
	stressSoak(t, 20, 0, func(t *testing.T, seed int64) {
		const backoff = 25 * time.Millisecond
		rounds := stressScale(6, 3)
		baseline := chaosGoroutines()
		rng := rand.New(rand.NewSource(seed))
		rig := chaosNewRig(t, "tcp", seed, nil, nil)
		mode := chaosMode{name: "single+retry", retry: func() RetryPolicy {
			return NewExponentialBackoff(ExponentialBackoffConfig{BaseDelay: backoff, MaxDelay: backoff, MaxAttempts: 50})
		}}
		metrics := &chaosClientMetrics{}
		client, err := New(chaosClientConfig(t, "tcp", rig.proxy.port(), mode, time.Second, metrics))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer func() { _ = client.Close() }()

		for round := 0; round < rounds; round++ {
			rig.proxy.heal()
			if err := client.Open(); err != nil {
				t.Fatalf("round %d: Open: %v", round, err)
			}
			if ok, err := chaosProbe(client, 5*time.Second); err != nil || !ok {
				t.Fatalf("round %d: request on a healthy link: ok=%v err=%v", round, ok, err)
			}
			// From now on every connection is reset: the request keeps retrying.
			rig.proxy.acceptMode.Store(chaosAcceptReset)
			rig.proxy.killAll(true)
			done := make(chan error, 1)
			go func() {
				_, err := chaosProbe(client, 10*time.Second)
				done <- err
			}()
			time.Sleep(time.Duration(rng.Int63n(int64(2 * backoff))))
			start := time.Now()
			if err := client.Close(); err != nil {
				t.Errorf("round %d: Close: %v", round, err)
			}
			select {
			case err := <-done:
				if !errors.Is(err, ErrClientNotOpen) && chaosClassify(err) != chaosClassTransport {
					t.Errorf("round %d: request returned %v, want ErrClientNotOpen or a transport error", round, err)
				}
				if err == nil {
					t.Errorf("round %d: request succeeded although every connection was reset", round)
				}
			case <-time.After(backoff + 3*time.Second):
				t.Fatalf("round %d: the retrying request did not return %v after Close\n%s", round, time.Since(start), chaosAllStacks())
			}
			// Close won: nothing may be dialled afterwards (counted on the client side,
			// by its AttemptMetrics), and no connection may be left behind.
			dials := metrics.dials.Load()
			time.Sleep(2 * backoff)
			if n := metrics.dials.Load(); n != dials {
				t.Errorf("round %d: the client re-dialled %d time(s) after Close returned", round, n-dials)
			}
			if !chaosEventually(2*time.Second, func() bool { return rig.proxy.active.Load() == 0 }) {
				t.Errorf("round %d: %d connection(s) left open after Close", round, rig.proxy.active.Load())
			}
		}
		_ = client.Close()
		rig.proxy.stop()
		rig.srv.stop()
		chaosLeakCheck(t, baseline)
	})
}

// TestStressServerStopWithClients stops (Stop or Shutdown) a server while clients
// are connecting and requests are in flight. Asserts: Stop/Shutdown returns, no
// client call hangs, calls fail only with transport, timeout or not-open errors,
// successful calls carry correct data, the stopped server holds no connection and
// no goroutine is left. Every round uses a new Server object (see
// TestStressServerRestartSameObject for restarts of one object).
func TestStressServerStopWithClients(t *testing.T) {
	stressSoak(t, 30, time.Second, func(t *testing.T, seed int64) {
		rounds := stressScale(12, 5)
		baseline := chaosGoroutines()
		wd := chaosNewWatchdog(t)
		defer wd.stop()
		rng := rand.New(rand.NewSource(seed))
		dev := chaosNewDevice(seed)
		for round := 0; round < rounds && !wd.hasTripped(); round++ {
			srv := chaosStartServer(t, "tcp", dev, nil)
			var stop atomic.Bool
			var wg sync.WaitGroup
			for g := 0; g < 6; g++ {
				wg.Add(1)
				mode := stressLifecycleModes[g%len(stressLifecycleModes)]
				go func() {
					defer wg.Done()
					for !stop.Load() {
						c, err := New(chaosClientConfig(t, "tcp", srv.port(), mode, 300*time.Millisecond, nil))
						if err != nil {
							t.Errorf("New: %v", err)
							return
						}
						if err := c.Open(); err == nil {
							for i := 0; i < 4; i++ {
								var ok bool
								wd.guard("request during Stop", 6*time.Second, func() {
									ok, err = chaosReadInputs(context.Background(), c, 8*i, 8)
								})
								switch class := chaosClassify(err); class {
								case chaosClassOK:
									if !ok {
										t.Errorf("request returned wrong data")
									}
								case chaosClassTransport, chaosClassTimeout, chaosClassNotOpen:
								default:
									t.Errorf("request during Stop: unexpected error class %q: %v", class, err)
								}
							}
						}
						_ = c.Close()
					}
				}()
			}
			time.Sleep(time.Duration(rng.Intn(4000)) * time.Microsecond)
			server := srv.server()
			wd.guard("Stop/Shutdown", 10*time.Second, func() {
				if round%2 == 0 {
					if err := server.Stop(); err != nil {
						t.Errorf("Stop: %v", err)
					}
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := server.Shutdown(ctx); err != nil {
					t.Errorf("Shutdown: %v", err)
				}
			})
			if n := chaosServerConns(server); n != 0 {
				t.Errorf("round %d: stopped server still holds %d connection(s)", round, n)
			}
			// A stopped server must refuse connections.
			if conn, err := net.DialTimeout("tcp", srv.addr, 200*time.Millisecond); err == nil {
				_ = conn.Close()
				t.Errorf("round %d: the stopped server still accepts connections", round)
			}
			stop.Store(true)
			chaosWait(t, &wg, 60*time.Second, "clients")
		}
		chaosLeakCheck(t, baseline)
	})
}

// stressRestartSameObject restarts one Server object on its port while clients
// connect.
func stressRestartSameObject(t *testing.T, rounds int) {
	t.Helper()
	srv := chaosStartServer(t, "tcp", chaosNewDevice(1), nil)
	server := srv.server()
	var stop atomic.Bool
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if conn, err := net.DialTimeout("tcp", srv.addr, 200*time.Millisecond); err == nil {
					_ = conn.Close()
				}
			}
		}()
	}
	for i := 0; i < rounds; i++ {
		if err := server.Stop(); err != nil {
			t.Errorf("round %d: Stop: %v", i, err)
		}
		if !chaosEventually(2*time.Second, func() bool { return server.Start() == nil }) {
			stop.Store(true)
			wg.Wait()
			t.Skip("environment: the server port was taken during a restart")
		}
	}
	stop.Store(true)
	chaosWait(t, &wg, 30*time.Second, "dialers")
	client := chaosOpenClient(t, chaosClientConfig(t, "tcp", srv.port(), chaosModeSingle, 5*time.Second, nil))
	chaosAssertUsable(t, client, "client after the restarts")
}

// TestStressServerRestartSameObject restarts one Server object repeatedly on the
// same port while clients keep connecting. Asserts (under -race): no data race,
// and the server works after the last restart.
// (The accept loop of the previous cycle used to race with Start.)
func TestStressServerRestartSameObject(t *testing.T) {
	stressRestartSameObject(t, stressScale(200, 50))
}

// TestStressPoolCloseWithBusyConnections closes a pooled client again and again
// while its connections are busy. Asserts: once Close has returned and every
// request has come back, the server holds no connection of that client.
func TestStressPoolCloseWithBusyConnections(t *testing.T) {
	stressSoak(t, 40, 1500*time.Millisecond, func(t *testing.T, seed int64) {
		rounds := stressScale(100, 30)
		srv := chaosStartServer(t, "tcp", chaosNewDevice(seed), nil)
		defer srv.stop()
		conf := chaosClientConfig(t, "tcp", srv.port(), chaosModePool, time.Second, nil)
		for round := 0; round < rounds; round++ {
			client, err := New(conf)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := client.Open(); err != nil {
				t.Fatalf("Open: %v", err)
			}
			var wg sync.WaitGroup
			for g := 0; g < 2*chaosModePool.maxConns; g++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						ok, err := chaosReadInputs(context.Background(), client, 0, 2)
						if err != nil {
							if class := chaosClassify(err); class != chaosClassNotOpen && class != chaosClassTransport {
								t.Errorf("request during Close: unexpected error class %q: %v", class, err)
							}
							return
						}
						if !ok {
							t.Errorf("request returned wrong data")
							return
						}
					}
				}()
			}
			time.Sleep(time.Duration(round%10) * 100 * time.Microsecond)
			if err := client.Close(); err != nil {
				t.Errorf("round %d: Close: %v", round, err)
			}
			chaosWait(t, &wg, 30*time.Second, "requests")
			if !chaosEventually(2*time.Second, func() bool { return srv.conns() == 0 }) {
				t.Fatalf("round %d: %d connection(s) of a closed client stay open", round, srv.conns())
			}
		}
	})
}

// TestStressContextCancellationStorm issues requests whose contexts are cancelled
// or expire at random moments, against handlers of random duration. Asserts: every
// call returns within the request timeout, with success and correct data, or with
// a timeout or cancellation error; afterwards the client still works.
func TestStressContextCancellationStorm(t *testing.T) {
	for _, mode := range []chaosMode{chaosModeSingle, chaosModePool} {
		mode := mode
		t.Run(mode.name, func(t *testing.T) {
			stressSoak(t, 50, 700*time.Millisecond, func(t *testing.T, seed int64) {
				const timeout = 100 * time.Millisecond
				workers := 12
				ops := stressScale(30, 10)
				baseline := chaosGoroutines()
				wd := chaosNewWatchdog(t)
				defer wd.stop()
				dev := chaosNewDevice(seed)
				dev.plan.Store(&chaosHandlerPlan{rate: 0.5, kinds: chaosHandlerSlow, slowBy: 6 * time.Millisecond})
				srv := chaosStartServer(t, "tcp", dev, nil)
				defer srv.stop()
				metrics := &chaosClientMetrics{}
				client, err := New(chaosClientConfig(t, "tcp", srv.port(), mode, timeout, metrics))
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				if err := client.Open(); err != nil {
					t.Fatalf("Open: %v", err)
				}
				defer func() { _ = client.Close() }()

				var wg sync.WaitGroup
				var counts [3]atomic.Int64
				for g := 0; g < workers; g++ {
					wg.Add(1)
					go func(g int) {
						defer wg.Done()
						rng := rand.New(rand.NewSource(seed*300 + int64(g)))
						for i := 0; i < ops && !wd.hasTripped(); i++ {
							delay := time.Duration(rng.Intn(8000)) * time.Microsecond
							ctx, cancel := context.WithCancel(context.Background())
							var timer *time.Timer
							if rng.Intn(2) == 0 {
								timer = time.AfterFunc(delay, cancel)
							} else {
								var cancelDeadline context.CancelFunc
								ctx, cancelDeadline = context.WithTimeout(ctx, delay)
								timer = time.AfterFunc(time.Hour, cancelDeadline)
							}
							addr := rng.Intn(refSpace - 8)
							var ok bool
							var err error
							// Single connection: a call may queue behind every other worker.
							wd.guard("request", time.Duration(workers)*timeout+3*time.Second, func() {
								ok, err = chaosReadInputs(ctx, client, addr, 8)
							})
							timer.Stop()
							cancel()
							switch class := chaosClassify(err); class {
							case chaosClassOK:
								counts[0].Add(1)
								if !ok {
									t.Errorf("request returned wrong data")
								}
							case chaosClassTimeout:
								counts[1].Add(1)
							case chaosClassCanceled:
								counts[2].Add(1)
							default:
								t.Errorf("request: unexpected error class %q: %v", class, err)
							}
						}
					}(g)
				}
				chaosWait(t, &wg, 120*time.Second, "storm")
				dev.plan.Store(nil)

				// Late responses of expired requests may still be on their way.
				time.Sleep(10 * time.Millisecond)
				for i := 0; i < 2*workers; i++ {
					if ok, err := chaosProbe(client, 2*time.Second); err != nil || !ok {
						t.Errorf("request %d after the storm: ok=%v err=%v", i, ok, err)
						break
					}
				}
				if counts[0].Load() == 0 || counts[1].Load()+counts[2].Load() == 0 {
					t.Errorf("the storm was not a storm: ok=%d timeout=%d canceled=%d", counts[0].Load(), counts[1].Load(), counts[2].Load())
				}
				_ = client.Close()
				if !chaosEventually(3*time.Second, func() bool { return srv.conns() == 0 }) {
					t.Errorf("server still holds %d connection(s) after Close", srv.conns())
				}
				srv.stop()
				metrics.check(t, "client")
				srv.metrics.check(t, "server")
				chaosLeakCheck(t, baseline)
			})
		})
	}
}

// TestStressServerRestartNewObject stops a server and starts a new Server object
// on the same port, repeatedly, under load from a pooled client. Asserts: no call
// hangs, calls fail only with transport or timeout errors, successful calls carry
// correct data, the client works again after the last restart without being
// recreated, and nothing leaks.
func TestStressServerRestartNewObject(t *testing.T) {
	stressSoak(t, 70, 500*time.Millisecond, func(t *testing.T, seed int64) {
		restarts := stressScale(12, 4)
		baseline := chaosGoroutines()
		wd := chaosNewWatchdog(t)
		defer wd.stop()
		srv := chaosStartServer(t, "tcp", chaosNewDevice(seed), nil)
		defer srv.stop()
		metrics := &chaosClientMetrics{}
		client, err := New(chaosClientConfig(t, "tcp", srv.port(), chaosModePoolRetry, 300*time.Millisecond, metrics))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := client.Open(); err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer func() { _ = client.Close() }()

		var stop atomic.Bool
		var okCalls atomic.Int64
		var wg sync.WaitGroup
		for g := 0; g < 2*chaosModePoolRetry.maxConns; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				rng := rand.New(rand.NewSource(seed*400 + int64(g)))
				for !stop.Load() && !wd.hasTripped() {
					addr := rng.Intn(refSpace - 8)
					var ok bool
					var err error
					wd.guard("request during restarts", 10*time.Second, func() {
						ok, err = chaosReadInputs(context.Background(), client, addr, 8)
					})
					switch class := chaosClassify(err); class {
					case chaosClassOK:
						okCalls.Add(1)
						if !ok {
							t.Errorf("request returned wrong data")
						}
					case chaosClassTransport, chaosClassTimeout:
					default:
						t.Errorf("request during restarts: unexpected error class %q: %v", class, err)
					}
				}
			}(g)
		}
		for i := 0; i < restarts; i++ {
			time.Sleep(2 * time.Millisecond)
			if !srv.restart(time.Millisecond) {
				stop.Store(true)
				chaosWait(t, &wg, 60*time.Second, "requests")
				t.Skip("environment: the server port was taken during a restart")
			}
		}
		// Let the workers see the last server, then stop them.
		time.Sleep(5 * time.Millisecond)
		stop.Store(true)
		chaosWait(t, &wg, 60*time.Second, "requests")

		failures := 0
		for streak := 0; streak < 2*chaosModePoolRetry.maxConns; {
			if ok, err := chaosProbe(client, 2*time.Second); err != nil || !ok {
				streak = 0
				if failures++; failures > 2 {
					t.Fatalf("the client does not recover after the last restart: ok=%v err=%v", ok, err)
				}
				continue
			}
			streak++
		}
		if okCalls.Load() == 0 {
			t.Errorf("no request succeeded between the restarts")
		}
		_ = client.Close()
		if !chaosEventually(3*time.Second, func() bool { return srv.conns() == 0 }) {
			t.Errorf("server still holds %d connection(s) after Close", srv.conns())
		}
		srv.stop()
		metrics.check(t, "client")
		srv.metrics.check(t, "server")
		chaosLeakCheck(t, baseline)
	})
}

// TestStressShutdownWithBusyHandlers shuts a server down while handlers are
// running. Asserts: handlers that honour their context are cancelled and Shutdown
// returns nil; with a handler that ignores its context Shutdown returns the
// context's error when its deadline expires, without hanging; in both cases the
// client's call returns promptly with an error instead of waiting for its
// timeout, and no goroutine is left once the handlers return.
func TestStressShutdownWithBusyHandlers(t *testing.T) {
	for _, cooperative := range []bool{true, false} {
		cooperative := cooperative
		name := "handler ignores context"
		if cooperative {
			name = "handler honours context"
		}
		t.Run(name, func(t *testing.T) {
			const clients = 3
			baseline := chaosGoroutines()
			dev := chaosNewDevice(1)
			entered, cancelled, release := make(chan struct{}, 16), make(chan struct{}, 16), make(chan struct{})
			if cooperative {
				dev.setHook(chaosBlockingHook(entered, cancelled, release))
			} else {
				dev.setHook(func(context.Context) error {
					entered <- struct{}{}
					<-release
					return nil
				})
			}
			srv := chaosStartServer(t, "tcp", dev, nil)
			results := make(chan error, clients)
			var open []*Client
			for i := 0; i < clients; i++ {
				c, err := New(chaosClientConfig(t, "tcp", srv.port(), chaosModeSingle, 10*time.Second, nil))
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				if err := c.Open(); err != nil {
					t.Fatalf("Open: %v", err)
				}
				open = append(open, c)
				go func() {
					_, err := chaosReadInputs(context.Background(), c, 0, 4)
					results <- err
				}()
			}
			for i := 0; i < clients; i++ {
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("requests did not reach the handlers")
				}
			}

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			err := srv.server().Shutdown(ctx)
			elapsed := time.Since(start)
			switch {
			case cooperative && err != nil:
				t.Errorf("Shutdown: %v, want nil (the handlers return when their context is cancelled)", err)
			case !cooperative && !errors.Is(err, context.DeadlineExceeded):
				t.Errorf("Shutdown: %v, want context.DeadlineExceeded (the handlers are stuck)", err)
			}
			if elapsed > 2*time.Second {
				t.Errorf("Shutdown took %v", elapsed)
			}
			// Shutdown closed the sockets: the clients must notice at once.
			for i := 0; i < clients; i++ {
				select {
				case err := <-results:
					// A cancelled handler's error may still be answered (as an exception)
					// before the socket is closed.
					class := chaosClassify(err)
					if class != chaosClassTransport && (!cooperative || class != chaosClassException) {
						t.Errorf("request cut off by Shutdown: error class %q (%v), want a transport error", class, err)
					}
				case <-time.After(3 * time.Second):
					t.Fatalf("a request did not return after Shutdown\n%s", chaosAllStacks())
				}
			}
			if cooperative && len(cancelled) != clients {
				t.Errorf("%d handler context(s) were cancelled, want %d", len(cancelled), clients)
			}
			close(release)
			for _, c := range open {
				_ = c.Close()
			}
			if err := srv.server().Stop(); err != nil {
				t.Errorf("Stop after Shutdown: %v", err)
			}
			chaosLeakCheck(t, baseline)
		})
	}
}
