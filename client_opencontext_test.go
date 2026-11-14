// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// Row L5 of the lifecycle matrix in TESTING.md, OpenContext: its context ends
// the dials; Close during it behaves as during Open.

// gatedDialer is a Dialer that reports each dial on dialling and returns the
// client end of a pipe once proceed is closed, or ctx's error once ctx ends.
type gatedDialer struct {
	dialling chan struct{}
	proceed  chan struct{}
	dials    atomic.Int32
	// conns are the pipes it returned: client end, peer end.
	conns chan [2]net.Conn
}

func newGatedDialer() *gatedDialer {
	return &gatedDialer{
		dialling: make(chan struct{}, 8),
		proceed:  make(chan struct{}),
		conns:    make(chan [2]net.Conn, 8),
	}
}

func (g *gatedDialer) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	g.dials.Add(1)
	g.dialling <- struct{}{}
	select {
	case <-g.proceed:
		cli, peer := net.Pipe()
		g.conns <- [2]net.Conn{cli, peer}
		return cli, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func newGatedClient(t *testing.T, g *gatedDialer, mut func(*Config)) *Client {
	t.Helper()
	conf := Config{URL: "tcp://192.0.2.1:502", Timeout: time.Minute, DialTimeout: time.Hour, Dialer: g.dial}
	if mut != nil {
		mut(&conf)
	}
	c, err := New(conf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// openAsync starts OpenContext and returns the channel of its result.
func openAsync(c *Client, ctx context.Context) <-chan error {
	opened := make(chan error, 1)
	go func() { opened <- c.OpenContext(ctx) }()
	return opened
}

func wantResult(t *testing.T, what string, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(e2eWait):
		t.Fatalf("%s did not return within %v", what, e2eWait)
		return nil
	}
}

// L5. Cancelling the context of OpenContext ends the dial in progress: it
// returns context.Canceled, nothing is left open, and a later Open dials again.
func TestClientL5_OpenContextCancelledWhileDialling(t *testing.T) {
	for _, pool := range []bool{false, true} {
		name := "single"
		if pool {
			name = "pool"
		}
		t.Run(name, func(t *testing.T) {
			g := newGatedDialer()
			c := newGatedClient(t, g, func(conf *Config) {
				if pool {
					conf.MaxConns, conf.MinConns = 2, 1
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			opened := openAsync(c, ctx)
			<-g.dialling
			cancel()
			if err := wantResult(t, "OpenContext after its context was cancelled", opened); !errors.Is(err, context.Canceled) {
				t.Fatalf("OpenContext: %v, want context.Canceled", err)
			}
			if c.Info().IsOpen {
				t.Fatal("the client is open after OpenContext failed")
			}
			if _, err := c.ReadHoldingRegister(context.Background(), 1, 0); !errors.Is(err, ErrClientNotOpen) {
				t.Errorf("request after the failed OpenContext: %v, want ErrClientNotOpen", err)
			}

			// Open is not bounded by anything but DialTimeout, and dials again.
			close(g.proceed)
			if err := mustReturn(t, "Open", c.Open); err != nil {
				t.Fatalf("Open after the cancelled OpenContext: %v", err)
			}
			if !c.Info().IsOpen {
				t.Error("the client is not open after Open")
			}
		})
	}
}

// L5. A context whose deadline passes while OpenContext dials ends the dial
// with context.DeadlineExceeded, which is also a request timeout.
func TestClientL5_OpenContextDeadline(t *testing.T) {
	g := newGatedDialer()
	c := newGatedClient(t, g, nil)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(50*time.Millisecond))
	defer cancel()
	err := wantResult(t, "OpenContext with a deadline", openAsync(c, ctx))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("OpenContext: %v, want context.DeadlineExceeded", err)
	}
	if g.dials.Load() != 1 {
		t.Errorf("%d dials, want 1", g.dials.Load())
	}
	if c.Info().IsOpen {
		t.Error("the client is open")
	}
}

// L5. A context that is already done returns its error before anything is
// dialled.
func TestClientL5_OpenContextAlreadyDone(t *testing.T) {
	g := newGatedDialer()
	c := newGatedClient(t, g, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.OpenContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenContext with a cancelled context: %v, want context.Canceled", err)
	}
	if n := g.dials.Load(); n != 0 {
		t.Errorf("%d dials, want none", n)
	}
	// On an open client it is a no-op, like Open, whatever its context.
	close(g.proceed)
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := c.OpenContext(ctx); err != nil {
		t.Errorf("OpenContext on an open client: %v, want nil", err)
	}
	if n := g.dials.Load(); n != 1 {
		t.Errorf("%d dials, want the one of Open", n)
	}
}

// L5. When the context ends although the dial succeeded (a Dialer that does
// not honour it), OpenContext returns the context's error, closes the
// connection and leaves the client closed.
func TestClientL5_OpenContextEndsAfterTheDial(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var peer net.Conn
	c, err := New(Config{URL: "tcp://192.0.2.1:502", Dialer: func(context.Context, string, string) (net.Conn, error) {
		cli, p := net.Pipe()
		peer = p
		cancel()
		return cli, nil
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = c.Close() }()
	if err := c.OpenContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenContext: %v, want context.Canceled", err)
	}
	if c.Info().IsOpen {
		t.Error("the client is open")
	}
	_ = peer.SetReadDeadline(time.Now().Add(e2eWait))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("peer read: %v, want io.EOF: the connection was not closed", err)
	}
}

// L5. A dial that fails with an error of its own while the context ends gives
// an error that matches both.
func TestClientL5_OpenContextDialErrorWhileCancelled(t *testing.T) {
	errDial := errors.New("dial failed")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, err := New(Config{URL: "tcp://192.0.2.1:502", Dialer: func(context.Context, string, string) (net.Conn, error) {
		cancel()
		return nil, errDial
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = c.OpenContext(ctx)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, errDial) {
		t.Errorf("OpenContext: %v, want an error that matches context.Canceled and the dial's error", err)
	}
}

// L5. Close during OpenContext does what it does during Open: it finds nothing
// to close and returns nil, and OpenContext then completes and leaves the
// client open.
func TestClientL5_CloseDuringOpenContext(t *testing.T) {
	g := newGatedDialer()
	c := newGatedClient(t, g, nil)
	opened := openAsync(c, context.Background())
	<-g.dialling
	if err := mustReturn(t, "Close during OpenContext", c.Close); err != nil {
		t.Errorf("Close during OpenContext: %v, want nil", err)
	}
	close(g.proceed)
	if err := wantResult(t, "OpenContext", opened); err != nil {
		t.Fatalf("OpenContext after a Close that came during it: %v, want nil", err)
	}
	if !c.Info().IsOpen {
		t.Fatal("the client is not open after OpenContext returned")
	}
	pipes := <-g.conns
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	_ = pipes[1].SetReadDeadline(time.Now().Add(e2eWait))
	if _, err := pipes[1].Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Errorf("peer read after Close: %v, want io.EOF", err)
	}
}

// L5. Two OpenContext calls at once leave one connection: the one that
// finishes second closes what it dialled.
func TestClientL5_ConcurrentOpenContext(t *testing.T) {
	g := newGatedDialer()
	c := newGatedClient(t, g, nil)
	first := openAsync(c, context.Background())
	second := openAsync(c, context.Background())
	<-g.dialling
	<-g.dialling
	close(g.proceed)
	for _, ch := range []<-chan error{first, second} {
		if err := wantResult(t, "OpenContext", ch); err != nil {
			t.Fatalf("OpenContext: %v", err)
		}
	}
	a, b := <-g.conns, <-g.conns
	// Exactly one of the two connections was closed.
	closed := 0
	for _, p := range [][2]net.Conn{a, b} {
		_ = p[1].SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if _, err := p[1].Read(make([]byte, 1)); errors.Is(err, io.EOF) {
			closed++
		}
	}
	if closed != 1 {
		t.Errorf("%d of the two connections closed, want 1", closed)
	}
}
