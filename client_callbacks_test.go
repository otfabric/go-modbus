// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The application's code the client calls: the ClientMetrics and AttemptMetrics
// methods, the RetryPolicy, the Dialer and the connections it returns, the
// private key of TLSClientCert, the Logger (PDUTap: tap_chaos_test.go). On
// v1.15.3 a call of any of them that did not return normally went on
// unwinding into the caller of the client method, or ended the process when
// it ran on a goroutine of the client. Now each is logged at error level with
// its stack, handled as an error of that call, and the client goes on.

// returnsNormally runs fn and fails the test when fn does not return normally.
func returnsNormally(t *testing.T, what string, fn func()) {
	t.Helper()
	var r any
	ok := func() (ok bool) {
		defer func() {
			if !ok {
				r = recover()
			}
		}()
		fn()
		return true
	}()
	if !ok {
		t.Fatalf("%s did not return normally: %v", what, r)
	}
}

// wantLogged waits for an error-level line of log that contains every one of
// subs and a stack.
func wantLogged(t *testing.T, log *e2eLogger, subs ...string) {
	t.Helper()
	deadline := time.Now().Add(e2eWait)
	for !log.has(append([]string{"E ", "goroutine "}, subs...)...) {
		if time.Now().After(deadline) {
			t.Fatalf("no error-level line with a stack containing %q:\n%s", subs, firstLines(log.dump()))
		}
		time.Sleep(time.Millisecond)
	}
}

// failingMetrics is a ClientMetrics and AttemptMetrics whose method named in
// fail does not return normally.
type failingMetrics struct {
	fail  atomic.Pointer[string]
	calls atomic.Int32
}

func (m *failingMetrics) hook(name string) {
	if f := m.fail.Load(); f != nil && *f == name {
		m.calls.Add(1)
		panic(name + " does not return")
	}
}

func (m *failingMetrics) OnRequest(uint8, FunctionCode) { m.hook("ClientMetrics.OnRequest") }
func (m *failingMetrics) OnResponse(uint8, FunctionCode, time.Duration) {
	m.hook("ClientMetrics.OnResponse")
}
func (m *failingMetrics) OnError(uint8, FunctionCode, time.Duration, error) {
	m.hook("ClientMetrics.OnError")
}
func (m *failingMetrics) OnTimeout(uint8, FunctionCode, time.Duration) {
	m.hook("ClientMetrics.OnTimeout")
}
func (m *failingMetrics) OnAttempt(uint8, FunctionCode, int, time.Duration, error) {
	m.hook("AttemptMetrics.OnAttempt")
}
func (m *failingMetrics) OnRetryDial(int, time.Duration, error) { m.hook("AttemptMetrics.OnRetryDial") }

// callbackDevice serves holding register 1 (0x0101); register 2 is an illegal
// address, and a request for register 3 closes the connection unanswered.
func callbackDevice() *fnHandler {
	return &fnHandler{holding: func(req *HoldingRegistersRequest) ([]uint16, error) {
		switch req.Addr {
		case 1:
			return []uint16{0x0101}, nil
		case 3:
			return nil, ErrProtocolError
		default:
			return nil, ErrIllegalDataAddress
		}
	}}
}

// wantServed checks that the client serves a request for register 1.
func wantServed(t *testing.T, c *Client, what string) {
	t.Helper()
	if v, err := c.ReadHoldingRegister(context.Background(), e2eUnit, 1); err != nil || v != 0x0101 {
		t.Errorf("%s: %#04x, %v", what, v, err)
	}
}

// A ClientMetrics or AttemptMetrics method that does not return normally does
// not change the result of the call it reports: the call returns as if the
// method had returned. The client serves the next request.
func TestClientMetrics_HookThatDoesNotReturnIsRecovered(t *testing.T) {
	expired := func() context.Context {
		ctx, cancel := context.WithDeadline(context.Background(), time.Unix(1, 0))
		t.Cleanup(cancel)
		return ctx
	}
	cases := []struct {
		hook string
		// call makes the request that calls the hook and checks its result.
		call func(t *testing.T, c *Client)
	}{
		{"ClientMetrics.OnRequest", func(t *testing.T, c *Client) { wantServed(t, c, "request") }},
		{"ClientMetrics.OnResponse", func(t *testing.T, c *Client) { wantServed(t, c, "request") }},
		{"ClientMetrics.OnError", func(t *testing.T, c *Client) {
			if _, err := c.ReadHoldingRegister(context.Background(), e2eUnit, 2); !errors.Is(err, ErrIllegalDataAddress) {
				t.Errorf("request for an illegal address: %v, want ErrIllegalDataAddress", err)
			}
		}},
		{"ClientMetrics.OnTimeout", func(t *testing.T, c *Client) {
			if _, err := c.ReadHoldingRegister(expired(), e2eUnit, 1); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("request with an expired deadline: %v, want context.DeadlineExceeded", err)
			}
		}},
		{"AttemptMetrics.OnAttempt", func(t *testing.T, c *Client) { wantServed(t, c, "request") }},
		{"AttemptMetrics.OnRetryDial", func(t *testing.T, c *Client) {
			// The connection is lost; the next request dials a new one.
			if _, err := c.ReadHoldingRegister(context.Background(), e2eUnit, 3); err == nil {
				t.Fatal("request whose connection the server closed succeeded")
			}
			wantServed(t, c, "request on a new connection")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.hook, func(t *testing.T) {
			log := &e2eLogger{}
			m := &failingMetrics{}
			p := e2eStart(t, "tcp", callbackDevice(), e2eOpts{client: func(conf *Config) {
				conf.Metrics, conf.Logger = m, log
			}})
			m.fail.Store(&tc.hook)
			returnsNormally(t, "the call", func() { tc.call(t, p.client) })
			if m.calls.Load() == 0 {
				t.Fatalf("%s was not called", tc.hook)
			}
			wantLogged(t, log, tc.hook+" did not return normally: "+tc.hook+" does not return")
			m.fail.Store(nil)
			wantServed(t, p.client, "next request")
		})
	}
}

// failingRetry is a RetryPolicy whose ShouldRetry does not return normally.
type failingRetry struct{ calls atomic.Int32 }

func (r *failingRetry) ShouldRetry(int, error) (bool, time.Duration) {
	r.calls.Add(1)
	panic("ShouldRetry does not return")
}

// A RetryPolicy whose ShouldRetry does not return normally counts as "do not
// retry": the call returns the error of its attempt.
func TestClientRetryPolicy_ThatDoesNotReturnNormally(t *testing.T) {
	log := &e2eLogger{}
	r := &failingRetry{}
	var attempts atomic.Int32
	dev := callbackDevice()
	holding := dev.holding
	dev.holding = func(req *HoldingRegistersRequest) ([]uint16, error) {
		if req.Addr == 3 {
			attempts.Add(1)
		}
		return holding(req)
	}
	p := e2eStart(t, "tcp", dev, e2eOpts{client: func(conf *Config) { conf.RetryPolicy, conf.Logger = r, log }})
	var err error
	returnsNormally(t, "the call", func() { _, err = p.client.ReadHoldingRegister(context.Background(), e2eUnit, 3) })
	if err == nil || strings.Contains(err.Error(), "did not return normally") {
		t.Errorf("call: %v, want the error of its attempt", err)
	}
	if r.calls.Load() != 1 || attempts.Load() != 1 {
		t.Errorf("ShouldRetry called %d times, %d attempts; want 1 and 1", r.calls.Load(), attempts.Load())
	}
	wantLogged(t, log, "RetryPolicy.ShouldRetry did not return normally: ShouldRetry does not return")
	wantServed(t, p.client, "next request")
}

// unrulyConn is a connection returned by a Dialer whose method named in fail
// does not return normally, once. A failing SetDeadline sets the deadline
// first, as a method that did its work and then did not return normally.
type unrulyConn struct {
	net.Conn
	fail *atomic.Pointer[string]
	// unix1 restricts the SetDeadline failure to the deadline the client's
	// interrupt sets, time.Unix(1, 0).
	unix1 bool
}

func (c *unrulyConn) check(method string) {
	if f := c.fail.Load(); f != nil && *f == method && c.fail.CompareAndSwap(f, nil) {
		panic(method + " does not return")
	}
}

func (c *unrulyConn) Read(b []byte) (int, error)  { c.check("Read"); return c.Conn.Read(b) }
func (c *unrulyConn) Write(b []byte) (int, error) { c.check("Write"); return c.Conn.Write(b) }
func (c *unrulyConn) Close() error                { err := c.Conn.Close(); c.check("Close"); return err }
func (c *unrulyConn) RemoteAddr() net.Addr        { c.check("RemoteAddr"); return c.Conn.RemoteAddr() }
func (c *unrulyConn) LocalAddr() net.Addr         { c.check("LocalAddr"); return c.Conn.LocalAddr() }
func (c *unrulyConn) SetReadDeadline(d time.Time) error {
	c.check("SetReadDeadline")
	return c.Conn.SetReadDeadline(d)
}
func (c *unrulyConn) SetWriteDeadline(d time.Time) error {
	c.check("SetWriteDeadline")
	return c.Conn.SetWriteDeadline(d)
}
func (c *unrulyConn) SetDeadline(d time.Time) error {
	err := c.Conn.SetDeadline(d)
	if !c.unix1 || d.Equal(time.Unix(1, 0)) {
		c.check("SetDeadline")
	}
	return err
}

// unrulyDialer returns a Dialer of unrulyConns, and the pointer that selects
// the method to fail. A dial while "Dialer" is selected does not return
// normally.
func unrulyDialer(unix1 bool) (Dialer, *atomic.Pointer[string]) {
	fail := &atomic.Pointer[string]{}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if f := fail.Load(); f != nil && *f == "Dialer" && fail.CompareAndSwap(f, nil) {
			panic("Dialer does not return")
		}
		var d net.Dialer
		c, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &unrulyConn{Conn: c, fail: fail, unix1: unix1}, nil
	}, fail
}

// A Dialer that does not return normally fails the dial: Open returns an error
// that describes it, and the client stays closed.
func TestClientDialer_ThatDoesNotReturnNormally(t *testing.T) {
	log := &e2eLogger{}
	dial, fail := unrulyDialer(false)
	p := e2eStart(t, "tcp", callbackDevice(), e2eOpts{noOpen: true, client: func(conf *Config) {
		conf.Dialer, conf.Logger = dial, log
	}})
	which := "Dialer"
	fail.Store(&which)
	var err error
	returnsNormally(t, "Open", func() { err = p.client.Open() })
	if err == nil || !strings.Contains(err.Error(), "Config.Dialer did not return normally: Dialer does not return") {
		t.Fatalf("Open: %v, want the dial to fail with what the Dialer did", err)
	}
	if p.client.Info().IsOpen {
		t.Error("the client is open after the dial failed")
	}
	wantLogged(t, log, "Config.Dialer did not return normally: Dialer does not return")
	if err := p.client.Open(); err != nil {
		t.Fatalf("Open once the Dialer returns: %v", err)
	}
	wantServed(t, p.client, "request")
}

// A method of a Dialer's connection that does not return normally fails as if
// it had returned an error: the request fails, the connection is not used
// again, and the next request dials a new one. RemoteAddr, which only names
// the connection in log entries, gives no address; Close returns the error.
func TestClientDialerConn_MethodThatDoesNotReturnNormally(t *testing.T) {
	for _, method := range []string{"Write", "Read", "SetDeadline", "RemoteAddr", "Close"} {
		t.Run(method, func(t *testing.T) {
			log := &e2eLogger{}
			dial, fail := unrulyDialer(false)
			p := e2eStart(t, "tcp", callbackDevice(), e2eOpts{noOpen: true, client: func(conf *Config) {
				conf.Dialer, conf.Logger = dial, log
			}})
			fail.Store(&method)
			want := "net.Conn." + method + " (Config.Dialer) did not return normally: " + method + " does not return"
			switch method {
			case "RemoteAddr":
				// Called when the connection is opened.
				returnsNormally(t, "Open", func() {
					if err := p.client.Open(); err != nil {
						t.Fatalf("Open: %v", err)
					}
				})
				wantServed(t, p.client, "request")
			case "Close":
				if err := p.client.Open(); err != nil {
					t.Fatalf("Open: %v", err)
				}
				var err error
				returnsNormally(t, "Close", func() { err = p.client.Close() })
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("Close: %v, want the error of the connection's Close", err)
				}
				if p.client.Info().IsOpen {
					t.Error("the client is still open")
				}
				if err := p.client.Open(); err != nil {
					t.Fatalf("Open again: %v", err)
				}
				wantServed(t, p.client, "request after re-opening")
			default:
				if err := p.client.Open(); err != nil {
					t.Fatalf("Open: %v", err)
				}
				var err error
				returnsNormally(t, "the request", func() { _, err = p.client.ReadHoldingRegister(context.Background(), e2eUnit, 1) })
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("request: %v, want the error of the connection's %s", err, method)
				}
				wantServed(t, p.client, "next request")
			}
			wantLogged(t, log, want)
		})
	}
}

// The methods of a dialled connection that the client's transports do not call
// today are guarded too, for whatever wraps the connection (crypto/tls does):
// LocalAddr returns nil, the deadlines fail with the error of the call.
func TestClientDialerConn_OtherMethodsThatDoNotReturnNormally(t *testing.T) {
	log := &e2eLogger{}
	c, err := New(Config{URL: "tcp://192.0.2.1:502", Logger: log})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cli, peer := net.Pipe()
	defer func() { _ = cli.Close(); _ = peer.Close() }()
	fail := &atomic.Pointer[string]{}
	conn := &appConn{conn: &unrulyConn{Conn: cli, fail: fail}, mc: c}
	want := func(method string) string {
		return "net.Conn." + method + " (Config.Dialer) did not return normally: " + method + " does not return"
	}

	t.Run("LocalAddr", func(t *testing.T) {
		method := "LocalAddr"
		fail.Store(&method)
		var a net.Addr
		returnsNormally(t, method, func() { a = conn.LocalAddr() })
		if a != nil {
			t.Errorf("LocalAddr = %v, want nil", a)
		}
		wantLogged(t, log, want(method))
		if a := conn.LocalAddr(); a == nil {
			t.Error("LocalAddr once it returns normally: nil")
		}
	})
	for method, call := range map[string]func(time.Time) error{
		"SetReadDeadline":  conn.SetReadDeadline,
		"SetWriteDeadline": conn.SetWriteDeadline,
	} {
		t.Run(method, func(t *testing.T) {
			fail.Store(&method)
			var err error
			returnsNormally(t, method, func() { err = call(time.Now()) })
			if err == nil || !strings.Contains(err.Error(), want(method)) {
				t.Errorf("%s: %v, want the error of the call", method, err)
			}
			wantLogged(t, log, want(method))
			if err := call(time.Now()); err != nil {
				t.Errorf("%s once it returns normally: %v", method, err)
			}
		})
	}
}

// The client interrupts the connection's I/O when a request's context ends,
// with SetDeadline on a goroutine of its own. On v1.15.3 a SetDeadline that did
// not return normally there ended the process; now it is logged, and the
// request returns the context's error.
func TestClientDialerConn_InterruptThatDoesNotReturnNormally(t *testing.T) {
	log := &e2eLogger{}
	dial, fail := unrulyDialer(true)
	entered := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	var once sync.Once
	h := &blockingHandler{fn: func(context.Context) {
		once.Do(func() { close(entered) })
		<-release
	}}
	p := e2eStart(t, "tcp", h, e2eOpts{client: func(conf *Config) { conf.Dialer, conf.Logger = dial, log }})
	which := "SetDeadline"
	fail.Store(&which)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-entered
		cancel()
	}()
	var err error
	returnsNormally(t, "the request", func() { _, err = p.client.ReadHoldingRegister(ctx, e2eUnit, 1) })
	if !errors.Is(err, context.Canceled) {
		t.Errorf("request whose context was cancelled: %v, want context.Canceled", err)
	}
	wantLogged(t, log, "net.Conn.SetDeadline (Config.Dialer) did not return normally: SetDeadline does not return")
}

// The client's TLS handshake signs with the private key of TLSClientCert. One
// that does not return normally fails the dial: Open returns an error that
// describes it, and the next Open succeeds.
func TestClientTLS_PrivateKeyThatDoesNotReturnNormally(t *testing.T) {
	dev := e2eNewDevice()
	dev.setHolding(1, 0x0101)
	log := &e2eLogger{}
	var signer *onceFailingSigner
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{noOpen: true, client: func(c *Config) {
		c.TLSClientCert, signer = withFailingKey(t, *c.TLSClientCert)
		c.Logger = log
	}})
	var err error
	returnsNormally(t, "Open", func() { err = p.client.Open() })
	if err == nil || !strings.Contains(err.Error(), "TLS handshake did not return normally: the private key's Sign") {
		t.Fatalf("Open: %v, want the handshake to fail with what the key did", err)
	}
	if signer.fail.Load() {
		t.Fatal("the client's key was not used")
	}
	wantLogged(t, log, "TLS handshake did not return normally: the private key's Sign")
	if err := p.client.Open(); err != nil {
		t.Fatalf("Open once the key signs: %v", err)
	}
	wantServed(t, p.client, "request")
}

// The client logs every request at debug level, on the goroutine of the call.
// A Logger whose Debugf does not return normally loses those entries; the
// failure is reported through its Errorf, and the call returns its value.
func TestClientLogger_MethodThatDoesNotReturnNormally(t *testing.T) {
	log := &failingLogger{}
	log.fail.Store(true)
	p := e2eStart(t, "tcp", callbackDevice(), e2eOpts{client: func(c *Config) { c.Logger = log }})
	returnsNormally(t, "the request", func() { wantServed(t, p.client, "request") })
	if !log.has("E ", "Logger.Debugf did not return normally: the Logger's D", "goroutine ") {
		t.Errorf("no report of the Logger's failure:\n%s", firstLines(log.dump()))
	}
}
