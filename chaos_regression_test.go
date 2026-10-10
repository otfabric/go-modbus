// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Focused, deterministic regression tests for the defects the chaos and stress
// suites exposed in v1.2.1 (see RELEASE.md, v1.3.0). Each test states the
// behaviour that is required and how it used to fail.
//
// The desynchronization cases are also covered, fault by fault, by
// TestChaosFaultMatrix.

// TestChaosRegressionNoStaleBytesAfterTimeout: a response stalls in the middle
// of its body for longer than the client timeout. The register contents are
// chosen so that the rest of that frame, when it arrives, forms a valid response
// to the next request on the same connection.
//
// Required: the connection is not used again after the timeout, so the next
// request is answered with its own data. (It used to be answered with the
// leftover bytes: wrong data and a nil error.)
func TestChaosRegressionNoStaleBytesAfterTimeout(t *testing.T) {
	const timeout = 100 * time.Millisecond
	rig := chaosNewRig(t, "tcp", 1, nil, nil)
	client := rig.open(chaosModeSingle, timeout)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Bytes 2..12 of this block spell the MBAP frame "txn=4 proto=0 len=5 unit=1
	// FC03 bytecount=2 0xBEEF": a response to the fourth request on the connection.
	block := []uint16{0xAA00, 0x0400, 0x0000, 0x0501, 0x0302, 0xBEEF}
	const victimAddr, victimValue = 100, 0x1111
	if err := client.WriteRegisters(ctx, refUnitID, 0, block); err != nil {
		t.Fatalf("WriteRegisters: %v", err)
	}
	if err := client.WriteRegister(ctx, refUnitID, victimAddr, victimValue); err != nil {
		t.Fatalf("WriteRegister: %v", err)
	}

	// Third exchange: the response (9 bytes of header, FC and byte count, then 12
	// data bytes) is delivered up to its first data byte, and the rest only after
	// the client has timed out.
	rig.proxy.setPlan(&chaosPlan{
		script:      chaosOnFrame(chaosS2C, 2, chaosFragment),
		splitAt:     func(chaosFrame) int { return 10 },
		fragmentGap: timeout + timeout/2,
	})
	if _, err := client.ReadRegisters(context.Background(), refUnitID, 0, uint16(len(block)), HoldingRegister); !errors.Is(err, ErrRequestTimedOut) {
		t.Fatalf("stalled read: got %v, want ErrRequestTimedOut", err)
	}
	time.Sleep(timeout)
	rig.proxy.setPlan(nil)

	got, err := client.ReadRegister(ctx, refUnitID, victimAddr, HoldingRegister)
	if err != nil {
		t.Fatalf("read after the stalled response: %v", err)
	}
	if got != victimValue {
		t.Fatalf("ReadRegister(%d) = 0x%04x, want 0x%04x: the tail of the timed-out response was taken for the answer", victimAddr, got, victimValue)
	}
	if n := rig.proxy.accepted.Load(); n != 2 {
		t.Errorf("the client used %d connection(s), want 2 (the stalled one, then a new one)", n)
	}
}

// TestChaosRegressionRecoversAfterServerOutage: a request fails while the server
// is down, and so does the attempt to reconnect.
//
// Required: the client stays open and the next request, once the server is back,
// dials a new connection and succeeds, with and without a RetryPolicy. (With a
// RetryPolicy it used to return ErrClientNotOpen forever; without one it kept
// using the dead connection.)
func TestChaosRegressionRecoversAfterServerOutage(t *testing.T) {
	for _, mode := range []chaosMode{chaosModeSingle, chaosModeSingleRetry} {
		mode := mode
		t.Run(mode.name, func(t *testing.T) {
			srv := chaosStartServer(t, "tcp", chaosNewDevice(1), nil)
			client := chaosOpenClient(t, chaosClientConfig(t, "tcp", srv.port(), mode, 500*time.Millisecond, nil))
			if ok, err := chaosProbe(client, 5*time.Second); err != nil || !ok {
				t.Fatalf("warm-up: ok=%v err=%v", ok, err)
			}

			srv.stop()
			for i := 0; i < 2; i++ {
				_, err := chaosProbe(client, 2*time.Second)
				if class := chaosClassify(err); class != chaosClassTransport {
					t.Fatalf("request %d while the server is down: class %q (%v), want a transport error", i, class, err)
				}
			}
			if !chaosEventually(2*time.Second, func() bool { return srv.start() == nil }) {
				t.Skip("environment: the server port was taken during the restart")
			}
			if ok, err := chaosProbe(client, 2*time.Second); err != nil || !ok {
				t.Fatalf("first request after the server came back: ok=%v err=%v", ok, err)
			}
		})
	}
}

// TestChaosRegressionConcurrentCallersDuringRetry: one goroutine's request is
// being retried (connection dropped, backing off, re-dialling) while another
// goroutine calls the same single-connection client.
//
// Required: the second caller waits for its turn and succeeds. (It used to get
// ErrClientNotOpen although the client was open.)
func TestChaosRegressionConcurrentCallersDuringRetry(t *testing.T) {
	rig := chaosNewRig(t, "tcp", 1, nil, nil)
	mode := chaosMode{name: "single+slow-retry", retry: func() RetryPolicy {
		return NewExponentialBackoff(ExponentialBackoffConfig{BaseDelay: 150 * time.Millisecond, MaxDelay: 150 * time.Millisecond, MaxAttempts: 2})
	}}
	client := rig.open(mode, time.Second)
	if ok, err := chaosProbe(client, 5*time.Second); err != nil || !ok {
		t.Fatalf("warm-up: ok=%v err=%v", ok, err)
	}

	// The next request's connection is reset: the caller backs off for 150 ms.
	rig.proxy.setPlan(&chaosPlan{script: chaosOnFrame(chaosC2S, 1, chaosRSTBefore)})
	first := make(chan error, 1)
	go func() {
		_, err := chaosProbe(client, 5*time.Second)
		first <- err
	}()
	if !chaosEventually(2*time.Second, func() bool { return rig.proxy.injectedTotal() == 1 }) {
		t.Fatal("the reset was not injected")
	}
	time.Sleep(30 * time.Millisecond)

	ok, second := chaosProbe(client, 5*time.Second)
	if err := <-first; err != nil {
		t.Errorf("the retried request failed: %v", err)
	}
	if second != nil || !ok {
		t.Errorf("request issued while another one was backing off: ok=%v err=%v", ok, second)
	}
}

// TestChaosRegressionPoolWaitersWoken: all MaxConns connections are in use and
// further callers wait for one; the requests in flight fail and their
// connections are discarded.
//
// Required: the waiters get their turn at once (each dials a new connection).
// (They used to wait until an unrelated request returned a connection to the
// pool, forever if none did.)
func TestChaosRegressionPoolWaitersWoken(t *testing.T) {
	const timeout = 60 * time.Millisecond
	rig := chaosNewRig(t, "tcp", 1, nil, nil)
	client := rig.open(chaosMode{name: "pool2", maxConns: 2}, timeout)
	// Every request is black-holed.
	rig.proxy.setPlan(&chaosPlan{script: func(f chaosFrame) chaosFault {
		if f.dir == chaosC2S {
			return chaosDrop
		}
		return 0
	}})

	results := make(chan error, 4)
	call := func() {
		_, err := chaosReadInputs(context.Background(), client, 0, 4)
		results <- err
	}
	go call()
	go call()
	if !chaosEventually(2*time.Second, func() bool { return rig.proxy.frames[chaosC2S].Load() == 2 }) {
		t.Fatal("the first two requests did not reach the proxy")
	}
	go call()
	go call()

	// The two requests in flight time out; then the two waiters run and time out too.
	for i := 0; i < 4; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, ErrRequestTimedOut) {
				t.Errorf("request %d: got %v, want ErrRequestTimedOut", i, err)
			}
		case <-time.After(2*timeout + 3*time.Second):
			t.Fatalf("%d caller(s) are still blocked after the connections in use were discarded\n%s", 4-i, chaosAllStacks())
		}
	}
	if n := rig.proxy.accepted.Load(); n != 4 {
		t.Errorf("the pool dialled %d connection(s), want 4 (two discarded, two for the waiters)", n)
	}
}

// chaosBlockingHook returns a handler hook that reports each entry on entered and
// then blocks until its context is cancelled (reported on cancelled) or release is
// closed.
func chaosBlockingHook(entered, cancelled chan<- struct{}, release <-chan struct{}) func(context.Context) error {
	return func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-ctx.Done():
			cancelled <- struct{}{}
			return ErrServerDeviceFailure
		case <-release:
			return nil
		}
	}
}

// TestChaosRegressionCancelInFlight: the context of a request that is waiting for
// its response is cancelled.
//
// Required: the call returns promptly with context.Canceled, and the client
// serves the next request. (The call used to return only when the client Timeout
// expired, as a timeout.)
func TestChaosRegressionCancelInFlight(t *testing.T) {
	for _, mode := range []chaosMode{chaosModeSingle, chaosModePool} {
		mode := mode
		t.Run(mode.name, func(t *testing.T) {
			const timeout = 5 * time.Second
			dev := chaosNewDevice(1)
			entered, cancelled, release := make(chan struct{}, 4), make(chan struct{}, 4), make(chan struct{})
			dev.setHook(chaosBlockingHook(entered, cancelled, release))
			srv := chaosStartServer(t, "tcp", dev, nil)
			client := chaosOpenClient(t, chaosClientConfig(t, "tcp", srv.port(), mode, timeout, nil))

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				<-entered
				cancel()
			}()
			start := time.Now()
			_, err := chaosReadInputs(ctx, client, 0, 4)
			elapsed := time.Since(start)
			if !errors.Is(err, context.Canceled) {
				t.Errorf("cancelled request returned %v, want context.Canceled", err)
			}
			if elapsed > timeout/2 {
				t.Errorf("cancelled request returned after %v (client Timeout %v)", elapsed.Round(time.Millisecond), timeout)
			}
			// The client dropped the connection: the server's handler sees its context
			// cancelled, and the next request runs on a new connection.
			select {
			case <-cancelled:
			case <-time.After(3 * time.Second):
				t.Errorf("the handler of the cancelled request was not cancelled")
			}
			dev.setHook(nil)
			close(release)
			if ok, err := chaosProbe(client, 5*time.Second); err != nil || !ok {
				t.Errorf("request after a cancelled one: ok=%v err=%v", ok, err)
			}

			// A context that is already cancelled: nothing is sent.
			before := srv.metrics.total()
			if _, err := chaosReadInputs(ctx, client, 0, 4); !errors.Is(err, context.Canceled) {
				t.Errorf("request with a cancelled context returned %v, want context.Canceled", err)
			}
			if after := srv.metrics.total(); after != before {
				t.Errorf("a request with a cancelled context reached the server")
			}
		})
	}
}

// TestChaosRegressionHandlerContextCancelledOnDisconnect: a client gives up on a
// request whose handler is still running, and closes its connection.
//
// Required (as RequestHandler documents): the handler's context is cancelled, and
// the connection's MaxClients slot is freed when the handler returns. (The
// context used to be cancelled only when the server stopped.)
func TestChaosRegressionHandlerContextCancelledOnDisconnect(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := chaosNewDevice(1)
		entered, cancelled, release := make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{})
		defer close(release)
		dev.setHook(chaosBlockingHook(entered, cancelled, release))
		srv := chaosStartServer(t, kind, dev, nil)
		// Without a RetryPolicy and with a long timeout, so that it is Close that
		// ends the request.
		client := chaosOpenClient(t, chaosClientConfig(t, kind, srv.port(), chaosModeSingle, 10*time.Second, nil))

		done := make(chan error, 1)
		go func() {
			_, err := chaosReadInputs(context.Background(), client, 0, 4)
			done <- err
		}()
		<-entered
		if err := client.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		select {
		case <-cancelled:
		case <-time.After(3 * time.Second):
			t.Fatalf("the handler's context was not cancelled after the client closed its connection (server holds %d connection(s))", srv.conns())
		}
		if !chaosEventually(2*time.Second, func() bool { return srv.conns() == 0 }) {
			t.Errorf("the server still holds the connection of the client that left")
		}
		if err := <-done; !errors.Is(err, ErrClientNotOpen) {
			t.Errorf("request interrupted by Close returned %v, want ErrClientNotOpen", err)
		}
	})
}

// TestChaosRegressionSlowHandlerResponseDelivered: a handler takes longer than
// ServerConfig.Timeout, which is the idle timeout of a connection.
//
// Required: the response is delivered, and the connection stays usable. (The
// read deadline used to cover the response as well: it was dropped and the
// client timed out.)
func TestChaosRegressionSlowHandlerResponseDelivered(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		const serverTimeout = 100 * time.Millisecond
		dev := chaosNewDevice(1)
		dev.setHook(func(context.Context) error {
			time.Sleep(serverTimeout + 50*time.Millisecond)
			return nil
		})
		srv := chaosStartServer(t, kind, dev, func(c *ServerConfig) { c.Timeout = serverTimeout })
		client := chaosOpenClient(t, chaosClientConfig(t, kind, srv.port(), chaosModeSingle, 5*time.Second, nil))

		for i := 0; i < 2; i++ {
			ok, err := chaosReadInputs(context.Background(), client, 8*i, 4)
			if err != nil || !ok {
				t.Fatalf("request %d to a handler slower than the idle timeout: ok=%v err=%v", i, ok, err)
			}
		}
		// The idle timeout still applies once the connection is idle.
		if !chaosEventually(serverTimeout+2*time.Second, func() bool { return srv.conns() == 0 }) {
			t.Errorf("the idle connection was not closed after ServerConfig.Timeout")
		}
	})
}

// TestChaosRegressionHalfOpenConnectionReplaced: the connection of a
// single-connection client goes half-open (requests vanish, nothing comes back),
// while new connections work.
//
// Required: the request that times out is the last one on that connection; the
// next one runs on a new connection and succeeds, with and without a
// RetryPolicy. (The client used to keep the dark connection and time out
// forever.)
func TestChaosRegressionHalfOpenConnectionReplaced(t *testing.T) {
	for _, mode := range []chaosMode{chaosModeSingle, chaosModeSingleRetry} {
		mode := mode
		t.Run(mode.name, func(t *testing.T) {
			const timeout = 60 * time.Millisecond
			rig := chaosNewRig(t, "tcp", 1, nil, nil)
			client := rig.open(mode, timeout)
			if ok, err := chaosProbe(client, 5*time.Second); err != nil || !ok {
				t.Fatalf("warm-up: ok=%v err=%v", ok, err)
			}
			// The first connection goes dark; any later connection is healthy.
			rig.proxy.setPlan(&chaosPlan{script: func(f chaosFrame) chaosFault {
				if f.conn == 0 && f.dir == chaosC2S {
					return chaosDrop
				}
				return 0
			}})
			if _, err := chaosReadInputs(context.Background(), client, 0, 4); !errors.Is(err, ErrRequestTimedOut) {
				t.Fatalf("request on the dark connection: %v, want ErrRequestTimedOut", err)
			}
			if ok, err := chaosProbe(client, 5*time.Second); err != nil || !ok {
				t.Fatalf("request after the timeout: ok=%v err=%v", ok, err)
			}
		})
	}
}
