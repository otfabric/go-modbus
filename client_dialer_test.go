// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// dialRecord is one call of a recording Dialer.
type dialRecord struct {
	network  string
	addr     string
	deadline time.Time
	hasDL    bool
}

// recordingDialer records every call and answers with fn.
type recordingDialer struct {
	mu    sync.Mutex
	calls []dialRecord
	fn    func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (d *recordingDialer) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	dl, ok := ctx.Deadline()
	d.mu.Lock()
	d.calls = append(d.calls, dialRecord{network: network, addr: addr, deadline: dl, hasDL: ok})
	d.mu.Unlock()
	return d.fn(ctx, network, addr)
}

func (d *recordingDialer) records() []dialRecord {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]dialRecord(nil), d.calls...)
}

// closeTrackingConn records whether Close was called.
type closeTrackingConn struct {
	net.Conn
	closed atomic.Bool
}

func (c *closeTrackingConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

var errDialRefused = errors.New("destination not allowed")

// dialerTestConfig returns a client configuration for kind reaching hostPort
// through dial.
func dialerTestConfig(t *testing.T, kind, hostPort string, dial Dialer) Config {
	t.Helper()
	conf := Config{URL: kind + "://" + hostPort, Timeout: time.Second, Dialer: dial}
	if kind == "tcp+tls" {
		pki := e2ePKI(t)
		conf.TLSClientCert = &pki.clientCert
		conf.TLSRootCAs = pki.ca.pool
	}
	return conf
}

// Every network transport dials through Config.Dialer, with the network it
// needs and the address of the URL as written, and the dialer's error reaches
// the caller wrapped.
func TestDialer_NetworkAndAddressPerTransport(t *testing.T) {
	cases := []struct {
		kind    string
		network string
	}{
		{"tcp", "tcp"},
		{"tcp+tls", "tcp"},
		{"rtuovertcp", "tcp"},
		{"asciiovertcp", "tcp"},
		{"udp", "udp"},
		{"rtuoverudp", "udp"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			d := &recordingDialer{fn: func(context.Context, string, string) (net.Conn, error) {
				return nil, errDialRefused
			}}
			// A name that does not resolve: the dialer must get it unresolved.
			const addr = "plc.invalid:1502"
			c, err := New(dialerTestConfig(t, tc.kind, addr, d.dial))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			err = c.Open()
			if !errors.Is(err, errDialRefused) {
				t.Fatalf("Open: want errDialRefused, got %v", err)
			}
			want := "modbus: dial " + tc.network + " " + addr + ": "
			if !strings.HasPrefix(err.Error(), want) {
				t.Errorf("error %q does not start with %q", err, want)
			}
			recs := d.records()
			if len(recs) != 1 {
				t.Fatalf("dialer called %d times, want 1", len(recs))
			}
			if recs[0].network != tc.network || recs[0].addr != addr {
				t.Errorf("dialer got (%q, %q), want (%q, %q)", recs[0].network, recs[0].addr, tc.network, addr)
			}
		})
	}
}

// The serial transports do not use the dialer.
func TestDialer_NotUsedBySerial(t *testing.T) {
	for _, kind := range []string{"rtu", "ascii"} {
		t.Run(kind, func(t *testing.T) {
			var called atomic.Bool
			c, err := New(Config{URL: kind + ":///dev/does-not-exist-modbus", Dialer: func(context.Context, string, string) (net.Conn, error) {
				called.Store(true)
				return nil, errDialRefused
			}})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := c.Open(); err == nil {
				_ = c.Close()
				t.Fatal("Open of a missing serial device succeeded")
			}
			if called.Load() {
				t.Fatal("dialer called for a serial transport")
			}
		})
	}
}

// The context the dialer gets carries the dial timeout: Config.DialTimeout,
// or the default (5s, 15s for tcp+tls).
func TestDialer_ContextCarriesDialTimeout(t *testing.T) {
	cases := []struct {
		kind    string
		timeout time.Duration
		want    time.Duration
	}{
		{"tcp", 0, 5 * time.Second},
		{"udp", 0, 5 * time.Second},
		{"tcp+tls", 0, 15 * time.Second},
		{"tcp", 750 * time.Millisecond, 750 * time.Millisecond},
		{"tcp+tls", 2 * time.Second, 2 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.kind+"/"+tc.want.String(), func(t *testing.T) {
			d := &recordingDialer{fn: func(context.Context, string, string) (net.Conn, error) {
				return nil, errDialRefused
			}}
			conf := dialerTestConfig(t, tc.kind, "127.0.0.1:1", d.dial)
			conf.DialTimeout = tc.timeout
			c, err := New(conf)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			start := time.Now()
			_ = c.Open()
			end := time.Now()
			recs := d.records()
			if len(recs) != 1 || !recs[0].hasDL {
				t.Fatalf("dialer records %+v: want one call with a deadline", recs)
			}
			if dl := recs[0].deadline; dl.Before(start.Add(tc.want)) || dl.After(end.Add(tc.want)) {
				t.Errorf("deadline %v after Open began, want %v", dl.Sub(start), tc.want)
			}
		})
	}
}

// A dialer that honours its context stops at the dial timeout, and the error
// still reports a timeout and wraps the context's.
func TestDialer_TimeoutIsHonouredAndReported(t *testing.T) {
	conf := Config{URL: "tcp://127.0.0.1:1", DialTimeout: 50 * time.Millisecond,
		Dialer: func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}}
	c, err := New(conf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	start := time.Now()
	err = c.Open()
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("Open took %v", el)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Open: want context.DeadlineExceeded, got %v", err)
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("Open: want a net.Error reporting a timeout, got %T %v", err, err)
	}
}

// A dialer that returns neither a connection nor an error fails the dial.
func TestDialer_NilConnWithoutError(t *testing.T) {
	for _, kind := range []string{"tcp", "tcp+tls", "udp"} {
		t.Run(kind, func(t *testing.T) {
			c, err := New(dialerTestConfig(t, kind, "127.0.0.1:1", func(context.Context, string, string) (net.Conn, error) {
				return nil, nil
			}))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := c.Open(); !errors.Is(err, errDialNoConn) {
				t.Fatalf("Open: want errDialNoConn, got %v", err)
			}
			if c.Info().IsOpen {
				t.Fatal("client open after a dial without connection")
			}
		})
	}
}

// A connection returned together with an error is closed.
func TestDialer_ConnWithErrorIsClosed(t *testing.T) {
	for _, kind := range []string{"tcp", "tcp+tls", "udp", "rtuovertcp"} {
		t.Run(kind, func(t *testing.T) {
			c1, c2 := net.Pipe()
			defer func() { _ = c2.Close() }()
			tc := &closeTrackingConn{Conn: c1}
			c, err := New(dialerTestConfig(t, kind, "127.0.0.1:1", func(context.Context, string, string) (net.Conn, error) {
				return tc, errDialRefused
			}))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := c.Open(); !errors.Is(err, errDialRefused) {
				t.Fatalf("Open: want errDialRefused, got %v", err)
			}
			if !tc.closed.Load() {
				t.Fatal("connection returned with an error was not closed")
			}
		})
	}
}

// mbapServeOne answers Modbus/TCP read-holding-register requests on conn with
// value until conn is closed. Each answer is written in one Write, so it also
// serves as one datagram.
func mbapServe(conn net.Conn, value uint16) {
	defer func() { _ = conn.Close() }()
	buf := make([]byte, 260)
	for {
		n, err := conn.Read(buf)
		if err != nil || n < 12 {
			return
		}
		qty := binary.BigEndian.Uint16(buf[10:12])
		res := make([]byte, 9+2*int(qty))
		copy(res[0:2], buf[0:2]) // transaction id
		binary.BigEndian.PutUint16(res[4:6], uint16(3+2*int(qty)))
		res[6] = buf[6] // unit id
		res[7] = 0x03
		res[8] = byte(2 * qty)
		for i := 0; i < int(qty); i++ {
			binary.BigEndian.PutUint16(res[9+2*i:], value)
		}
		if _, err := conn.Write(res); err != nil {
			return
		}
	}
}

// A dialer can hand the client an in-memory connection, for tcp and for udp.
func TestDialer_InMemoryConnection(t *testing.T) {
	for _, kind := range []string{"tcp", "udp"} {
		t.Run(kind, func(t *testing.T) {
			var dials atomic.Int32
			c, err := New(Config{URL: kind + "://device.test:502", Timeout: time.Second,
				Dialer: func(context.Context, string, string) (net.Conn, error) {
					dials.Add(1)
					a, b := net.Pipe()
					go mbapServe(b, 0x1234)
					return a, nil
				}})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := c.Open(); err != nil {
				t.Fatalf("Open: %v", err)
			}
			defer func() { _ = c.Close() }()
			for i := 0; i < 3; i++ {
				v, err := c.ReadHoldingRegister(context.Background(), 1, 7)
				if err != nil || v != 0x1234 {
					t.Fatalf("read %d = 0x%04X, %v", i, v, err)
				}
			}
			if n := dials.Load(); n != 1 {
				t.Errorf("dialer called %d times, want 1", n)
			}
		})
	}
}

// The pool pre-warms and grows through the dialer.
func TestDialer_Pool(t *testing.T) {
	var dials atomic.Int32
	c, err := New(Config{URL: "tcp://device.test:502", Timeout: time.Second, MinConns: 2, MaxConns: 3,
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			dials.Add(1)
			a, b := net.Pipe()
			go mbapServe(b, 7)
			return a, nil
		}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = c.Close() }()
	if n := dials.Load(); n != 2 {
		t.Fatalf("pre-warm dialled %d times, want 2", n)
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Go(func() {
			if v, err := c.ReadHoldingRegister(context.Background(), 1, 0); err != nil || v != 7 {
				t.Errorf("read = %d, %v", v, err)
			}
		})
	}
	wg.Wait()
	if n := dials.Load(); n < 2 || n > 3 {
		t.Fatalf("dialled %d times, want 2..3", n)
	}
}

// A pool whose pre-warm dial gets no connection fails Open.
func TestDialer_PoolNilConn(t *testing.T) {
	c, err := New(Config{URL: "tcp://device.test:502", MinConns: 1, MaxConns: 2,
		Dialer: func(context.Context, string, string) (net.Conn, error) { return nil, nil }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Open(); !errors.Is(err, errDialNoConn) {
		t.Fatalf("Open: want errDialNoConn, got %v", err)
	}
}

// When a lost connection is replaced during a request, the dial runs under
// that request's context: cancelling the request ends the dial, and a dial
// that runs out of the request's deadline reports a request timeout that
// still wraps the dial's error.
func TestDialer_RedialHonoursRequestContext(t *testing.T) {
	var (
		dials atomic.Int32
		first net.Conn
	)
	c, err := New(Config{URL: "tcp://device.test:502", Timeout: time.Second,
		Dialer: func(ctx context.Context, _, _ string) (net.Conn, error) {
			if dials.Add(1) == 1 {
				a, b := net.Pipe()
				first = b
				go mbapServe(b, 1)
				return a, nil
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.ReadHoldingRegister(context.Background(), 1, 0); err != nil {
		t.Fatalf("first read: %v", err)
	}
	// The peer goes away: the next request fails and drops the connection.
	_ = first.Close()
	if _, err := c.ReadHoldingRegister(context.Background(), 1, 0); err == nil {
		t.Fatal("read on a closed connection succeeded")
	}

	// Cancelled while re-dialling.
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err = c.ReadHoldingRegister(ctx, 1, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled re-dial: want context.Canceled, got %v", err)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("cancelled re-dial took %v", el)
	}

	// Out of time while re-dialling.
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = c.ReadHoldingRegister(ctx, 1, 0)
	if !errors.Is(err, ErrRequestTimedOut) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed-out re-dial: want ErrRequestTimedOut wrapping context.DeadlineExceeded, got %v", err)
	}
	if !c.Info().IsOpen {
		t.Fatal("client closed after a failed re-dial")
	}
}

// A pooled request that has to dial does so under the request's context.
func TestDialer_PoolDialHonoursRequestContext(t *testing.T) {
	c, err := New(Config{URL: "tcp://device.test:502", Timeout: time.Second, MaxConns: 2,
		Dialer: func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = c.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = c.ReadHoldingRegister(ctx, 1, 0)
	if !errors.Is(err, ErrRequestTimedOut) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want ErrRequestTimedOut wrapping context.DeadlineExceeded, got %v", err)
	}
}

// tcp+tls dials through the dialer and runs the TLS handshake on the
// connection it returns, verifying the server against the URL's host name.
func TestDialer_TLSThroughCustomDialer(t *testing.T) {
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{noOpen: true})
	_, port, _ := net.SplitHostPort(p.hostPort)

	var calls []string
	var mu sync.Mutex
	c := p.newClient(func(conf *Config) {
		// The URL names localhost (the name in the server certificate); the
		// dialer sends the connection to 127.0.0.1, as an allowlist that pins
		// resolved addresses would.
		conf.Dialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			calls = append(calls, network+" "+addr)
			mu.Unlock()
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
		}
	})
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	dev.setHolding(3, 0xBEEF)
	if v, err := c.ReadHoldingRegister(context.Background(), e2eUnit, 3); err != nil || v != 0xBEEF {
		t.Fatalf("read = 0x%04X, %v", v, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0] != "tcp localhost:"+port {
		t.Fatalf("dialer calls %v, want [tcp localhost:%s]", calls, port)
	}
}

// A TLS handshake that fails on the dialer's connection closes it and reports
// a wrapped handshake error; one that stalls ends at the dial timeout.
func TestDialer_TLSHandshakeFailure(t *testing.T) {
	t.Run("peer closes", func(t *testing.T) {
		a, b := net.Pipe()
		tc := &closeTrackingConn{Conn: a}
		go func() {
			// Read the ClientHello, then hang up.
			_, _ = b.Read(make([]byte, 512))
			_ = b.Close()
		}()
		c, err := New(dialerTestConfig(t, "tcp+tls", "plc.test:802", func(context.Context, string, string) (net.Conn, error) {
			return tc, nil
		}))
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		err = c.Open()
		if err == nil || !strings.HasPrefix(err.Error(), "modbus: tls handshake tcp plc.test:802: ") {
			t.Fatalf("Open: want a TLS handshake error, got %v", err)
		}
		if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
			t.Errorf("Open: cause not reachable with errors.Is: %v", err)
		}
		if !tc.closed.Load() {
			t.Fatal("connection not closed after a failed handshake")
		}
	})
	t.Run("peer stalls", func(t *testing.T) {
		a, b := net.Pipe()
		defer func() { _ = b.Close() }()
		go func() { _, _ = io.Copy(io.Discard, b) }()
		conf := dialerTestConfig(t, "tcp+tls", "plc.test:802", func(context.Context, string, string) (net.Conn, error) {
			return a, nil
		})
		conf.DialTimeout = 100 * time.Millisecond
		c, err := New(conf)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		start := time.Now()
		err = c.Open()
		if el := time.Since(start); el > 3*time.Second {
			t.Fatalf("stalled handshake took %v", el)
		}
		var ne net.Error
		if !errors.As(err, &ne) || !ne.Timeout() {
			t.Fatalf("Open: want a timeout, got %v", err)
		}
	})
}

// The ServerName of the TLS configuration is the host of the endpoint, as
// tls.DialWithDialer sets it.
func TestTLSConfigServerName(t *testing.T) {
	pki := e2ePKI(t)
	for endpoint, want := range map[string]string{
		"plc.example:802": "plc.example",
		"10.0.0.1:802":    "10.0.0.1",
		"[::1]:802":       "::1",
		"no-port":         "no-port",
	} {
		c := &Client{endpoint: endpoint, conf: Config{TLSClientCert: &pki.clientCert, TLSRootCAs: pki.ca.pool}}
		if got := c.tlsConfig().ServerName; got != want {
			t.Errorf("%s: ServerName %q, want %q", endpoint, got, want)
		}
	}
}

// NewConfig carries TransportConfig.Dialer over.
func TestNewConfig_Dialer(t *testing.T) {
	d := func(context.Context, string, string) (net.Conn, error) { return nil, errDialRefused }
	conf := NewConfig(TransportConfig{URL: "tcp://x:502", Dialer: d}, ExecutionConfig{}, ObservabilityConfig{})
	if conf.Dialer == nil {
		t.Fatal("NewConfig dropped the Dialer")
	}
	c, err := New(conf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Open(); !errors.Is(err, errDialRefused) {
		t.Fatalf("Open: want errDialRefused, got %v", err)
	}
}

// The default dialer is net.Dialer: the error of a refused connection is the
// same *net.OpError as before, now wrapped.
func TestDialer_DefaultIsNetDialer(t *testing.T) {
	addr := e2eFreeAddr(t)
	c, err := New(Config{URL: "tcp://" + addr, DialTimeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = c.Open()
	var opErr *net.OpError
	if !errors.As(err, &opErr) || opErr.Op != "dial" {
		t.Fatalf("Open: want a dial *net.OpError, got %T: %v", err, err)
	}
	// The *net.OpError already names the dial: the address appears once.
	if want := "modbus: " + opErr.Error(); err.Error() != want {
		t.Errorf("error %q, want %q", err, want)
	}
	if n := strings.Count(err.Error(), addr); n != 1 {
		t.Errorf("address appears %d times in %q", n, err)
	}
}

// The prefix is left out only when the cause already names the same dial.
func TestDialError_Message(t *testing.T) {
	tcpAddr := func(s string) net.Addr {
		a, err := net.ResolveTCPAddr("tcp", s)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	refused := errors.New("connect: connection refused")
	cases := []struct {
		name string
		e    *dialError
		want string
	}{
		{"same dial", &dialError{op: "dial", network: "tcp", addr: "10.0.0.5:502",
			err: &net.OpError{Op: "dial", Net: "tcp", Addr: tcpAddr("10.0.0.5:502"), Err: refused}},
			"modbus: dial tcp 10.0.0.5:502: connect: connection refused"},
		{"same dial, IPv6", &dialError{op: "dial", network: "tcp", addr: "[::1]:502",
			err: &net.OpError{Op: "dial", Net: "tcp", Addr: tcpAddr("[::1]:502"), Err: refused}},
			"modbus: dial tcp [::1]:502: connect: connection refused"},
		{"same dial wrapped by a dialer", &dialError{op: "dial", network: "tcp", addr: "10.0.0.5:502",
			err: fmt.Errorf("allowlist dialer: %w", &net.OpError{Op: "dial", Net: "tcp", Addr: tcpAddr("10.0.0.5:502"), Err: refused})},
			"modbus: allowlist dialer: dial tcp 10.0.0.5:502: connect: connection refused"},
		{"host name resolved to an IP", &dialError{op: "dial", network: "tcp", addr: "plc.test:502",
			err: &net.OpError{Op: "dial", Net: "tcp", Addr: tcpAddr("10.0.0.5:502"), Err: refused}},
			"modbus: dial tcp plc.test:502: dial tcp 10.0.0.5:502: connect: connection refused"},
		{"lookup failed", &dialError{op: "dial", network: "tcp", addr: "plc.invalid:502",
			err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("lookup plc.invalid: no such host")}},
			"modbus: dial tcp plc.invalid:502: dial tcp: lookup plc.invalid: no such host"},
		{"other network", &dialError{op: "dial", network: "udp", addr: "10.0.0.5:502",
			err: &net.OpError{Op: "dial", Net: "tcp", Addr: tcpAddr("10.0.0.5:502"), Err: refused}},
			"modbus: dial udp 10.0.0.5:502: dial tcp 10.0.0.5:502: connect: connection refused"},
		{"dialer's own error", &dialError{op: "dial", network: "tcp", addr: "10.0.0.5:502", err: errDialRefused},
			"modbus: dial tcp 10.0.0.5:502: destination not allowed"},
		{"no connection", &dialError{op: "dial", network: "tcp", addr: "10.0.0.5:502", err: errDialNoConn},
			"modbus: dial tcp 10.0.0.5:502: dialer returned no connection"},
		{"tls handshake", &dialError{op: "tls handshake", network: "tcp", addr: "10.0.0.5:802",
			err: &net.OpError{Op: "dial", Net: "tcp", Addr: tcpAddr("10.0.0.5:802"), Err: refused}},
			"modbus: tls handshake tcp 10.0.0.5:802: dial tcp 10.0.0.5:802: connect: connection refused"},
	}
	for _, tc := range cases {
		if got := tc.e.Error(); got != tc.want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}
