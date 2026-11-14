// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Lifecycle contract tests of the client for the rows of the matrix in
// TESTING.md that had none: L4 (Close during a write), L5 (Close during Open),
// L6 for a cancellation that arrives while the request deadline is set, L9 (a
// callback calls the client) and L10 (a callback calls Close). Each pins the
// documented outcome and is driven by events, not by sleeps.

// pipeClient returns a tcp client whose connection is one end of a net.Pipe,
// wrapped by wrap, and the other end.
func pipeClient(t *testing.T, timeout time.Duration, wrap func(net.Conn) net.Conn) (*Client, net.Conn) {
	t.Helper()
	cli, srv := net.Pipe()
	t.Cleanup(func() { _ = srv.Close(); _ = cli.Close() })
	c, err := New(Config{
		URL:     "tcp://192.0.2.1:502",
		Timeout: timeout,
		Dialer:  func(context.Context, string, string) (net.Conn, error) { return wrap(cli), nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, srv
}

// writeSignalConn closes writing when its first Write begins.
type writeSignalConn struct {
	net.Conn
	writing chan struct{}
	once    sync.Once
}

func (c *writeSignalConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.writing) })
	return c.Conn.Write(p)
}

// L4. Close while a request is being written: the peer does not read, so the
// write cannot complete. The request returns with ErrClientNotOpen, and the
// peer finds the connection closed without a byte of the request.
func TestClientL4_CloseDuringWrite(t *testing.T) {
	writing := make(chan struct{})
	c, peer := pipeClient(t, time.Minute, func(conn net.Conn) net.Conn {
		return &writeSignalConn{Conn: conn, writing: writing}
	})
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.ReadHoldingRegister(context.Background(), 1, 0)
		done <- err
	}()
	<-writing
	if err := mustReturn(t, "Close during a write", c.Close); err != nil {
		t.Errorf("Close: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrClientNotOpen) {
			t.Errorf("request closed while writing: %v, want ErrClientNotOpen", err)
		}
	case <-time.After(e2eWait):
		t.Fatalf("the request did not return within %v of Close", e2eWait)
	}
	_ = peer.SetReadDeadline(time.Now().Add(e2eWait))
	if n, err := peer.Read(make([]byte, 16)); n != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("peer read %d bytes, error %v; want the connection closed with nothing delivered", n, err)
	}
	if c.Info().IsOpen {
		t.Error("the client is open after Close")
	}
}

// L5. Close while Open is still dialling closes nothing and returns nil; Open
// then completes and the client is open, on the connection that dial returned.
// A Close after Open has returned closes it.
func TestClientL5_CloseDuringOpen(t *testing.T) {
	dialling := make(chan struct{})
	proceed := make(chan struct{})
	cli, peer := net.Pipe()
	defer func() { _ = peer.Close(); _ = cli.Close() }()
	c, err := New(Config{
		URL:     "tcp://192.0.2.1:502",
		Timeout: time.Minute,
		Dialer: func(context.Context, string, string) (net.Conn, error) {
			close(dialling)
			<-proceed
			return cli, nil
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	opened := make(chan error, 1)
	go func() { opened <- c.Open() }()
	<-dialling

	if err := mustReturn(t, "Close during Open", c.Close); err != nil {
		t.Errorf("Close during Open: %v, want nil", err)
	}
	if c.Info().IsOpen {
		t.Error("the client is open while Open is still dialling")
	}
	close(proceed)
	select {
	case err := <-opened:
		if err != nil {
			t.Fatalf("Open after a Close that came during it: %v, want nil", err)
		}
	case <-time.After(e2eWait):
		t.Fatalf("Open did not return within %v", e2eWait)
	}
	if !c.Info().IsOpen {
		t.Fatal("the client is not open after Open returned")
	}

	// The connection the dial returned is the client's: a request uses it.
	served := make(chan error, 1)
	go func() {
		frame, err := readMBAPFrame(peer)
		if err != nil {
			served <- err
			return
		}
		_, err = peer.Write(normalFrame(frame[0:2], frame[6], frame[7], []byte{0x02, 0x12, 0x34}))
		served <- err
	}()
	if v, err := c.ReadHoldingRegister(context.Background(), 1, 0); err != nil || v != 0x1234 {
		t.Fatalf("ReadHoldingRegister on the connection opened across the Close: %#04x, %v", v, err)
	}
	if err := <-served; err != nil {
		t.Fatalf("peer: %v", err)
	}

	// A Close after Open has returned does close it.
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	_ = peer.SetReadDeadline(time.Now().Add(e2eWait))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("peer read after Close: %v, want io.EOF", err)
	}
	if c.Info().IsOpen {
		t.Error("the client is open after Close")
	}
}

// interleavedConn forces one order of events on the connection it wraps: when
// armed, the first request deadline set on it cancels the request's context and
// is applied only after the client's reaction to the cancellation (a deadline
// in the past) has been applied.
type interleavedConn struct {
	net.Conn
	mu          sync.Mutex
	cancel      context.CancelFunc
	interrupted chan struct{}
	once        sync.Once
	written     atomic.Int64
}

func (c *interleavedConn) arm(cancel context.CancelFunc) {
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
}

func (c *interleavedConn) SetDeadline(t time.Time) error {
	if !t.IsZero() && t.Before(time.Now()) {
		// The client interrupting the request.
		err := c.Conn.SetDeadline(t)
		c.once.Do(func() { close(c.interrupted) })
		return err
	}
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
		<-c.interrupted
	}
	return c.Conn.SetDeadline(t)
}

func (c *interleavedConn) Write(p []byte) (int, error) {
	c.written.Add(int64(len(p)))
	return c.Conn.Write(p)
}

// L6. A context cancelled just before the transport sets the request deadline:
// the request is not sent and the call returns context.Canceled at once, not
// after Timeout.
func TestClientL6_CancelWhileRequestDeadlineIsSet(t *testing.T) {
	var ic *interleavedConn
	c, peer := pipeClient(t, time.Minute, func(conn net.Conn) net.Conn {
		ic = &interleavedConn{Conn: conn, interrupted: make(chan struct{})}
		return ic
	})
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	// A peer that takes what it is sent and never answers.
	received := make(chan int, 1)
	go func() {
		total := 0
		buf := make([]byte, 512)
		for {
			n, err := peer.Read(buf)
			total += n
			if err != nil {
				received <- total
				return
			}
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ic.arm(cancel)
	err := mustReturn(t, "the cancelled request", func() error {
		_, err := c.ReadHoldingRegister(ctx, 1, 0)
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("request: %v, want context.Canceled", err)
	}
	if n := ic.written.Load(); n != 0 {
		t.Errorf("%d bytes written after the cancellation, want none", n)
	}
	_ = c.Close()
	if n := <-received; n != 0 {
		t.Errorf("the peer received %d bytes, want none", n)
	}
}

// reentrantMetrics is a ClientMetrics and AttemptMetrics whose methods call the
// functions set on it.
type reentrantMetrics struct {
	onResponse atomic.Pointer[func()]
	onAttempt  atomic.Pointer[func()]
}

func (m *reentrantMetrics) run(p *atomic.Pointer[func()]) {
	if fn := p.Swap(nil); fn != nil {
		(*fn)()
	}
}

func (m *reentrantMetrics) OnRequest(uint8, FunctionCode)                     {}
func (m *reentrantMetrics) OnResponse(uint8, FunctionCode, time.Duration)     { m.run(&m.onResponse) }
func (m *reentrantMetrics) OnError(uint8, FunctionCode, time.Duration, error) {}
func (m *reentrantMetrics) OnTimeout(uint8, FunctionCode, time.Duration)      {}
func (m *reentrantMetrics) OnAttempt(uint8, FunctionCode, int, time.Duration, error) {
	m.run(&m.onAttempt)
}
func (m *reentrantMetrics) OnRetryDial(int, time.Duration, error) {}

// L9. A PDUTap and the ClientMetrics outcome hooks run with no lock held and
// after the request has given up its connection: a request they make on the
// same client, single connection included, is served.
func TestClientL9_CallbackMakesARequest(t *testing.T) {
	for _, site := range []string{"PDUTap", "ClientMetrics.OnResponse"} {
		t.Run(site, func(t *testing.T) {
			dev := e2eNewDevice()
			dev.setHolding(1, 0x0101)
			dev.setHolding(2, 0x0202)
			var c *Client
			var nested atomic.Pointer[error]
			var innerValue atomic.Uint32
			inner := func(ctx context.Context) {
				v, err := c.ReadHoldingRegister(ctx, e2eUnit, 2)
				innerValue.Store(uint32(v))
				nested.Store(&err)
			}
			m := &reentrantMetrics{}
			// The tap sees the nested exchange too: it reacts to the first only.
			var tapped atomic.Bool
			p := e2eStart(t, "tcp", dev, e2eOpts{client: func(conf *Config) {
				conf.Metrics = m
				if site == "PDUTap" {
					conf.PDUTap = PDUTapFunc(func(ctx context.Context, _ PDUExchange) {
						if tapped.CompareAndSwap(false, true) {
							inner(ctx)
						}
					})
				}
			}})
			c = p.client
			if site != "PDUTap" {
				fn := func() { inner(context.Background()) }
				m.onResponse.Store(&fn)
			}

			var outer uint16
			err := mustReturn(t, "a request whose "+site+" makes a request", func() error {
				var err error
				outer, err = c.ReadHoldingRegister(context.Background(), e2eUnit, 1)
				return err
			})
			if err != nil || outer != 0x0101 {
				t.Errorf("outer request: %#04x, %v; want 0x0101", outer, err)
			}
			if e := nested.Load(); e == nil || *e != nil || innerValue.Load() != 0x0202 {
				t.Errorf("nested request: %#04x, %v; want 0x0202", innerValue.Load(), e)
			}
			if p.conns() != 1 {
				t.Errorf("server connections = %d, want both requests on the one connection", p.conns())
			}
		})
	}
}

// L10. Close called from a callback returns. From a PDUTap or an outcome hook
// the request has its response already and returns it; from OnAttempt, which
// runs before the request returns to the client, the request reports
// ErrClientNotOpen. In every case the client is closed afterwards and can be
// opened again.
func TestClientL10_CallbackCallsClose(t *testing.T) {
	cases := []struct {
		site string
		// wantErr is what the request whose callback closed the client returns.
		wantErr error
	}{
		{"PDUTap", nil},
		{"ClientMetrics.OnResponse", nil},
		{"AttemptMetrics.OnAttempt", ErrClientNotOpen},
	}
	for _, tc := range cases {
		t.Run(tc.site, func(t *testing.T) {
			dev := e2eNewDevice()
			dev.setHolding(1, 0x0101)
			var c *Client
			var closeErr atomic.Pointer[error]
			closeIt := func() {
				err := c.Close()
				closeErr.Store(&err)
			}
			m := &reentrantMetrics{}
			var tapped atomic.Bool
			p := e2eStart(t, "tcp", dev, e2eOpts{client: func(conf *Config) {
				conf.Metrics = m
				if tc.site == "PDUTap" {
					conf.PDUTap = PDUTapFunc(func(context.Context, PDUExchange) {
						if tapped.CompareAndSwap(false, true) {
							closeIt()
						}
					})
				}
			}})
			c = p.client
			switch tc.site {
			case "ClientMetrics.OnResponse":
				m.onResponse.Store(&closeIt)
			case "AttemptMetrics.OnAttempt":
				m.onAttempt.Store(&closeIt)
			}

			var v uint16
			err := mustReturn(t, "a request whose "+tc.site+" calls Close", func() error {
				var err error
				v, err = c.ReadHoldingRegister(context.Background(), e2eUnit, 1)
				return err
			})
			if tc.wantErr == nil {
				if err != nil || v != 0x0101 {
					t.Errorf("request: %#04x, %v; want its response, 0x0101", v, err)
				}
			} else if !errors.Is(err, tc.wantErr) {
				t.Errorf("request: %v, want %v", err, tc.wantErr)
			}
			if e := closeErr.Load(); e == nil || *e != nil {
				t.Errorf("Close from %s: %v, want nil", tc.site, e)
			}
			if c.Info().IsOpen {
				t.Error("the client is open after the callback closed it")
			}
			if _, err := c.ReadHoldingRegister(context.Background(), e2eUnit, 1); !errors.Is(err, ErrClientNotOpen) {
				t.Errorf("request on the closed client: %v, want ErrClientNotOpen", err)
			}
			e2eEventually(t, "the server to release the connection", func() bool { return p.conns() == 0 })

			if err := c.Open(); err != nil {
				t.Fatalf("Open after the callback closed the client: %v", err)
			}
			if v, err := c.ReadHoldingRegister(context.Background(), e2eUnit, 1); err != nil || v != 0x0101 {
				t.Errorf("request after re-opening: %#04x, %v", v, err)
			}
		})
	}
}

// closeHookConn is a connection whose Close runs onClose first.
type closeHookConn struct {
	net.Conn
	onClose func()
}

func (c *closeHookConn) Close() error {
	c.onClose()
	return c.Conn.Close()
}

// Close closes the connections without the client's lock, so the Close of a
// Config.Dialer connection may call the client: Info returns during it and the
// client is no longer open, and a second Close returns once the first has. On
// v1.15.3 Close held the lock while it closed them, and such a call never
// returned.
func TestClientClose_ConnectionCloseRunsWithoutTheLock(t *testing.T) {
	entered := make(chan ClientInfo, 1)
	release := make(chan struct{})
	var c atomic.Pointer[Client]
	var once sync.Once
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		conn, err := d.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &closeHookConn{Conn: conn, onClose: func() {
			once.Do(func() {
				entered <- c.Load().Info()
				<-release
			})
		}}, nil
	}
	p := e2eStart(t, "tcp", e2eNewDevice(), e2eOpts{client: func(conf *Config) { conf.Dialer = dial }})
	c.Store(p.client)

	closed := func() <-chan error {
		ch := make(chan error, 1)
		go func() { ch <- p.client.Close() }()
		return ch
	}
	first := closed()
	select {
	case info := <-entered:
		if info.IsOpen {
			t.Error("Info in the connection's Close: the client is open")
		}
	case <-time.After(e2eWait):
		close(release)
		t.Fatalf("Info called in the connection's Close did not return within %v", e2eWait)
	}
	info := make(chan ClientInfo, 1)
	go func() { info <- p.client.Info() }()
	select {
	case i := <-info:
		if i.IsOpen {
			t.Error("Info during Close: the client is open")
		}
	case <-time.After(e2eWait):
		t.Fatalf("Info during Close did not return within %v", e2eWait)
	}
	second := closed()
	close(release)
	for _, ch := range []<-chan error{first, second} {
		if err := wantResult(t, "Close", ch); err != nil {
			t.Errorf("Close: %v", err)
		}
	}
	if err := p.client.Open(); err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
}
