// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Slow-loris and resource-exhaustion tests against the server: peers that
// connect and stay silent, trickle bytes, announce frames that never arrive, or
// send invalid MBAP headers.

// stressAwaitClose reads from conn until the peer closes it and returns how long
// that took and how many bytes arrived before. ok is false if the connection is
// still open after limit.
func stressAwaitClose(conn net.Conn, limit time.Duration) (elapsed time.Duration, received int, ok bool) {
	start := time.Now()
	_ = conn.SetReadDeadline(start.Add(limit))
	buf := make([]byte, 512)
	for {
		n, err := conn.Read(buf)
		received += n
		if err != nil {
			var netErr net.Error
			timedOut := errors.As(err, &netErr) && netErr.Timeout()
			return time.Since(start), received, !timedOut
		}
	}
}

// TestStressSlowLoris holds server connections open without completing a request.
// Asserts: the server closes each such connection when ServerConfig.Timeout
// expires (trickling bytes does not extend it), sends them nothing, never holds
// more than MaxClients connections, keeps serving a well-behaved client all
// along, and admits new clients once the stalled ones are gone.
func TestStressSlowLoris(t *testing.T) {
	stressSoak(t, 60, 0, func(t *testing.T, seed int64) {
		const serverTimeout = 250 * time.Millisecond
		const maxClients = 4
		baseline := chaosGoroutines()
		srv := chaosStartServer(t, "tcp", chaosNewDevice(seed), func(c *ServerConfig) {
			c.Timeout = serverTimeout
			c.MaxClients = maxClients
		})
		defer srv.stop()
		sampler := chaosSampleConns(srv.conns, maxClients)
		defer sampler.stop()

		// A well-behaved client that must be served all along.
		good := chaosOpenClient(t, chaosClientConfig(t, "tcp", srv.port(), chaosModeSingle, 5*time.Second, nil))
		var stop atomic.Bool
		var served atomic.Int64
		var goodWG sync.WaitGroup
		goodWG.Add(1)
		go func() {
			defer goodWG.Done()
			for i := 0; !stop.Load(); i++ {
				start := time.Now()
				ok, err := chaosReadInputs(context.Background(), good, i%256, 8)
				if err != nil || !ok {
					t.Errorf("well-behaved client: request failed while others stall: ok=%v err=%v", ok, err)
					return
				}
				if d := time.Since(start); d > 2*time.Second {
					t.Errorf("well-behaved client: request took %v while others stall", d)
				}
				served.Add(1)
				time.Sleep(time.Millisecond)
			}
		}()

		// Three stalling peers take the remaining slots.
		type loris struct {
			name string
			feed func(conn net.Conn)
		}
		request := chaosRawRequest(1, 0, 4)
		lorises := []loris{
			{name: "silent", feed: func(net.Conn) {}},
			{name: "one byte per interval", feed: func(conn net.Conn) {
				// 12 bytes, 40 ms apart: the request would be complete after 440 ms.
				for _, b := range request {
					if _, err := conn.Write([]byte{b}); err != nil {
						return
					}
					time.Sleep(40 * time.Millisecond)
				}
			}},
			{name: "announced frame never sent", feed: func(conn net.Conn) {
				header := make([]byte, 7)
				binary.BigEndian.PutUint16(header[4:6], 200)
				header[6] = refUnitID
				_, _ = conn.Write(header)
			}},
		}
		var wg sync.WaitGroup
		for _, l := range lorises {
			l := l
			conn, err := net.DialTimeout("tcp", srv.addr, 2*time.Second)
			if err != nil {
				t.Fatalf("%s: dial: %v", l.name, err)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { _ = conn.Close() }()
				go l.feed(conn)
				elapsed, received, closed := stressAwaitClose(conn, serverTimeout+2*time.Second)
				switch {
				case !closed:
					t.Errorf("%s: still connected %v after connecting (ServerConfig.Timeout is %v)", l.name, elapsed, serverTimeout)
				case elapsed < serverTimeout/2:
					t.Errorf("%s: closed after only %v (ServerConfig.Timeout is %v)", l.name, elapsed, serverTimeout)
				}
				if received != 0 {
					t.Errorf("%s: the server sent %d byte(s) to a peer that never completed a request", l.name, received)
				}
			}()
		}
		if !chaosEventually(2*time.Second, func() bool { return srv.conns() == maxClients }) {
			t.Fatalf("server holds %d connections, want %d", srv.conns(), maxClients)
		}

		// All slots are taken: one more client is turned away...
		late, err := New(chaosClientConfig(t, "tcp", srv.port(), chaosModeSingle, 5*time.Second, nil))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer func() { _ = late.Close() }()
		if err := late.Open(); err == nil {
			_, err = chaosProbe(late, 5*time.Second)
			if class := chaosClassify(err); class != chaosClassTransport {
				t.Errorf("client beyond MaxClients: error class %q (%v), want a transport error", class, err)
			}
		}
		_ = late.Close()

		// ...until the stalled peers are timed out.
		chaosWait(t, &wg, 30*time.Second, "stalled peers")
		if !chaosEventually(2*time.Second, func() bool { return srv.conns() == 1 }) {
			t.Errorf("server holds %d connections after the stalled peers timed out, want 1", srv.conns())
		}
		if err := late.Open(); err != nil {
			t.Fatalf("Open after the stalled peers left: %v", err)
		}
		chaosAssertUsable(t, late, "client admitted after the stalled peers left")

		stop.Store(true)
		chaosWait(t, &goodWG, 30*time.Second, "well-behaved client")
		if served.Load() == 0 {
			t.Errorf("the well-behaved client was never served")
		}
		_ = late.Close()
		_ = good.Close()
		if !chaosEventually(2*time.Second, func() bool { return srv.conns() == 0 }) {
			t.Errorf("server still holds %d connection(s)", srv.conns())
		}
		srv.stop()
		sampler.stop()
		if peak := sampler.peak.Load(); peak > maxClients {
			t.Errorf("the server held %d connections, MaxClients is %d", peak, maxClients)
		}
		chaosLeakCheck(t, baseline)
	})
}

// TestStressInvalidMBAPHeader sends headers the server must reject at once.
// Asserts: an invalid or oversized MBAP length and an unknown protocol ID close
// the connection immediately (no waiting for, or allocating, the announced body,
// well before ServerConfig.Timeout), nothing is sent back, and the server keeps
// working.
func TestStressInvalidMBAPHeader(t *testing.T) {
	const serverTimeout = 3 * time.Second
	srv := chaosStartServer(t, "tcp", chaosNewDevice(1), func(c *ServerConfig) { c.Timeout = serverTimeout })
	header := func(proto, length uint16) []byte {
		h := make([]byte, 7)
		binary.BigEndian.PutUint16(h[0:2], 1)
		binary.BigEndian.PutUint16(h[2:4], proto)
		binary.BigEndian.PutUint16(h[4:6], length)
		h[6] = refUnitID
		return h
	}
	cases := []struct {
		name  string
		bytes []byte
	}{
		{"length 0", header(0, 0)},
		{"length 1", header(0, 1)},
		{"length 255", header(0, 255)},
		{"length 65535", header(0, 0xffff)},
		{"length 65535 with body", append(header(0, 0xffff), make([]byte, 4096)...)},
		{"protocol 1", append(header(1, 6), byte(FCReadInputRegisters), 0, 0, 0, 1)},
		{"protocol 65535, length 65535", header(0xffff, 0xffff)},
	}
	var wg sync.WaitGroup
	for _, tc := range cases {
		tc := tc
		for rep := 0; rep < 4; rep++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				conn, err := net.DialTimeout("tcp", srv.addr, 2*time.Second)
				if err != nil {
					t.Errorf("%s: dial: %v", tc.name, err)
					return
				}
				defer func() { _ = conn.Close() }()
				if _, err := conn.Write(tc.bytes); err != nil {
					t.Errorf("%s: write: %v", tc.name, err)
					return
				}
				elapsed, received, closed := stressAwaitClose(conn, serverTimeout/2)
				if !closed {
					t.Errorf("%s: connection still open after %v: the server waits for the announced frame", tc.name, elapsed)
				}
				if received != 0 {
					t.Errorf("%s: the server answered an invalid header with %d byte(s)", tc.name, received)
				}
			}()
		}
	}
	chaosWait(t, &wg, 30*time.Second, "invalid headers")
	if !chaosEventually(2*time.Second, func() bool { return srv.conns() == 0 }) {
		t.Errorf("server still holds %d connection(s)", srv.conns())
	}
	client := chaosOpenClient(t, chaosClientConfig(t, "tcp", srv.port(), chaosModeSingle, 5*time.Second, nil))
	chaosAssertUsable(t, client, "client after the invalid headers")
	srv.metrics.check(t, "server")
}

// TestStressSlowLorisTLSHandshake connects to a TLS server without ever starting
// the handshake. Asserts: the server gives up after TLSHandshakeTimeout, the
// stalled peers never hold more than MaxClients slots, and a real client gets in
// once they are gone.
func TestStressSlowLorisTLSHandshake(t *testing.T) {
	const handshakeTimeout = 250 * time.Millisecond
	const maxClients = 2
	baseline := chaosGoroutines()
	srv := chaosStartServer(t, "tcp+tls", chaosNewDevice(1), func(c *ServerConfig) {
		c.TLSHandshakeTimeout = handshakeTimeout
		c.MaxClients = maxClients
	})
	defer srv.stop()

	var wg sync.WaitGroup
	for i := 0; i < maxClients; i++ {
		conn, err := net.DialTimeout("tcp", srv.addr, 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { _ = conn.Close() }()
			if i == 0 {
				// Half a TLS record header, then silence.
				_, _ = conn.Write([]byte{0x16, 0x03, 0x01})
			}
			elapsed, _, closed := stressAwaitClose(conn, handshakeTimeout+2*time.Second)
			switch {
			case !closed:
				t.Errorf("peer %d: still connected %v after connecting (TLSHandshakeTimeout is %v)", i, elapsed, handshakeTimeout)
			case elapsed < handshakeTimeout/2:
				t.Errorf("peer %d: closed after only %v (TLSHandshakeTimeout is %v)", i, elapsed, handshakeTimeout)
			}
		}(i)
	}
	if !chaosEventually(2*time.Second, func() bool { return srv.conns() == maxClients }) {
		t.Fatalf("server holds %d connections, want %d", srv.conns(), maxClients)
	}

	// Every slot is held by a peer that is not handshaking: a real client is turned away.
	conf := chaosClientConfig(t, "tcp+tls", srv.port(), chaosModeSingle, 5*time.Second, nil)
	client, err := New(conf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Open(); err == nil {
		t.Errorf("Open succeeded although every MaxClients slot is taken")
		_ = client.Close()
	} else if class := chaosClassify(err); class != chaosClassTransport {
		t.Errorf("Open beyond MaxClients: error class %q (%v), want a transport error", class, err)
	}

	chaosWait(t, &wg, 30*time.Second, "stalled peers")
	if !chaosEventually(2*time.Second, func() bool { return srv.conns() == 0 }) {
		t.Errorf("server holds %d connections after the stalled peers timed out", srv.conns())
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open after the stalled peers left: %v", err)
	}
	chaosAssertUsable(t, client, "TLS client admitted after the stalled peers left")
	_ = client.Close()
	if !chaosEventually(2*time.Second, func() bool { return srv.conns() == 0 }) {
		t.Errorf("server still holds %d connection(s)", srv.conns())
	}
	srv.stop()
	chaosLeakCheck(t, baseline)
}
