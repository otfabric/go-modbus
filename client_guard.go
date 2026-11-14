// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"net"
	"time"

	"github.com/otfabric/go-modbus/internal/callback"
)

// The client calls application code in these places, each through call:
//
//   - the methods of Config.Metrics: ClientMetrics (guardedMetrics) and
//     AttemptMetrics (attemptBridge);
//   - Config.PDUTap (tapExchange);
//   - Config.RetryPolicy (guardedRetry);
//   - Config.Dialer (dialConn) and the methods of the connections it returns
//     (appConn);
//   - the TLS handshake of tcp+tls (dialTLS), which runs the private key of
//     Config.TLSClientCert, a crypto.Signer that may be the application's.
//
// Config.Logger is guarded by internal/logging. The library holds none of the
// client's locks while it makes these calls, so a failure is logged where it
// happens.

// call runs fn, application code that name names. When fn returns, call
// returns fn's error. When fn does not return normally, call logs that at
// error level, with the value it ended with and the stack, and returns an
// error that describes it ("<name> did not return normally: <value>"), which
// the caller handles as the error of that call.
func (mc *Client) call(name string, fn func() error) error {
	fail, err := callback.Run(name, fn)
	if fail != nil {
		mc.logger.Errorf("%s did not return normally: %v\n%s", fail.Name, fail.Value, fail.Stack)
	}
	return err
}

// guardedMetrics is Config.Metrics with every method called through call: a
// method that does not return normally is logged, and the request goes on as
// if it had returned.
type guardedMetrics struct {
	m  ClientMetrics
	mc *Client
}

func (g guardedMetrics) OnRequest(unitID uint8, fc FunctionCode) {
	_ = g.mc.call("ClientMetrics.OnRequest", func() error { g.m.OnRequest(unitID, fc); return nil })
}

func (g guardedMetrics) OnResponse(unitID uint8, fc FunctionCode, d time.Duration) {
	_ = g.mc.call("ClientMetrics.OnResponse", func() error { g.m.OnResponse(unitID, fc, d); return nil })
}

func (g guardedMetrics) OnError(unitID uint8, fc FunctionCode, d time.Duration, err error) {
	_ = g.mc.call("ClientMetrics.OnError", func() error { g.m.OnError(unitID, fc, d, err); return nil })
}

func (g guardedMetrics) OnTimeout(unitID uint8, fc FunctionCode, d time.Duration) {
	_ = g.mc.call("ClientMetrics.OnTimeout", func() error { g.m.OnTimeout(unitID, fc, d); return nil })
}

// attemptBridge adapts the public AttemptMetrics interface to the internal
// session.AttemptObserver interface, calling each method through call.
type attemptBridge struct {
	am AttemptMetrics
	mc *Client
}

func (ab *attemptBridge) OnAttempt(unitID uint8, fc byte, attempt int, d time.Duration, err error) {
	_ = ab.mc.call("AttemptMetrics.OnAttempt", func() error {
		ab.am.OnAttempt(unitID, FunctionCode(fc), attempt, d, err)
		return nil
	})
}

func (ab *attemptBridge) OnRetryDial(attempt int, d time.Duration, err error) {
	_ = ab.mc.call("AttemptMetrics.OnRetryDial", func() error {
		ab.am.OnRetryDial(attempt, d, err)
		return nil
	})
}

// guardedRetry is Config.RetryPolicy called through call. A ShouldRetry that
// does not return normally counts as (false, 0): the request is not retried
// and returns the error of its attempt.
type guardedRetry struct {
	p  RetryPolicy
	mc *Client
}

func (g guardedRetry) ShouldRetry(attempt int, err error) (retry bool, delay time.Duration) {
	if g.mc.call("RetryPolicy.ShouldRetry", func() error {
		retry, delay = g.p.ShouldRetry(attempt, err)
		return nil
	}) != nil {
		return false, 0
	}
	return retry, delay
}

// retryPolicy returns Config.RetryPolicy guarded, nil when there is none.
func (mc *Client) retryPolicy() RetryPolicy {
	if mc.conf.RetryPolicy == nil {
		return nil
	}
	return guardedRetry{p: mc.conf.RetryPolicy, mc: mc}
}

// dialApp calls Config.Dialer through call and returns its connection wrapped
// in an appConn. A dialer that does not return normally fails the dial.
func (mc *Client) dialApp(ctx context.Context, dial Dialer, network string) (net.Conn, error) {
	var conn net.Conn
	err := mc.call("Config.Dialer", func() error {
		c, err := dial(ctx, network, mc.endpoint)
		conn = c
		return err
	})
	if conn != nil {
		conn = &appConn{conn: conn, mc: mc}
	}
	return conn, err
}

// appConn is a connection returned by Config.Dialer, with every method called
// through call. A method that does not return normally fails with that error,
// as a failed read or write does: the request fails and the connection is not
// used again. LocalAddr and RemoteAddr return nil then.
type appConn struct {
	conn net.Conn
	mc   *Client
}

func (c *appConn) Read(b []byte) (n int, err error) {
	err = c.mc.call("net.Conn.Read (Config.Dialer)", func() error {
		m, err := c.conn.Read(b)
		n = m
		return err
	})
	return n, err
}

func (c *appConn) Write(b []byte) (n int, err error) {
	err = c.mc.call("net.Conn.Write (Config.Dialer)", func() error {
		m, err := c.conn.Write(b)
		n = m
		return err
	})
	return n, err
}

func (c *appConn) Close() error {
	return c.mc.call("net.Conn.Close (Config.Dialer)", c.conn.Close)
}

func (c *appConn) LocalAddr() (a net.Addr) {
	_ = c.mc.call("net.Conn.LocalAddr (Config.Dialer)", func() error { a = c.conn.LocalAddr(); return nil })
	return a
}

func (c *appConn) RemoteAddr() (a net.Addr) {
	_ = c.mc.call("net.Conn.RemoteAddr (Config.Dialer)", func() error { a = c.conn.RemoteAddr(); return nil })
	return a
}

func (c *appConn) SetDeadline(t time.Time) error {
	return c.mc.call("net.Conn.SetDeadline (Config.Dialer)", func() error { return c.conn.SetDeadline(t) })
}

func (c *appConn) SetReadDeadline(t time.Time) error {
	return c.mc.call("net.Conn.SetReadDeadline (Config.Dialer)", func() error { return c.conn.SetReadDeadline(t) })
}

func (c *appConn) SetWriteDeadline(t time.Time) error {
	return c.mc.call("net.Conn.SetWriteDeadline (Config.Dialer)", func() error { return c.conn.SetWriteDeadline(t) })
}
