// SPDX-License-Identifier: MIT

package modbus

import (
	"fmt"
	"net"

	"github.com/otfabric/go-modbus/internal/callback"
)

// The server calls application code in these places, each through call, on
// the goroutine of the accept loop or of a connection:
//
//   - the RequestHandler methods (safeDispatch), and the methods of the error a
//     handler returns, which choose the answer (answerFor);
//   - the methods of ServerConfig.Metrics: ServerMetrics (reportMetrics) and
//     ConnectionMetrics (reportRejected);
//   - the TLS handshake of tcp+tls (startTLS), which runs the private key of
//     ServerConfig.TLSServerCert, a crypto.Signer that may be the
//     application's.
//
// ServerConfig.Logger is guarded by internal/logging. The server holds none of
// its locks while it makes these calls, so a failure is logged where it
// happens.

// call runs fn, application code that name names, and returns fn's error.
// When fn does not return normally, call logs that at error level, as line()
// followed by the value fn ended with and the stack, and returns an error that
// describes it ("<name> did not return normally: <value>"), which the caller
// handles as the error of that call. line is only called then.
func (ms *Server) call(name string, line func() string, fn func() error) error {
	fail, err := callback.Run(name, fn)
	if fail != nil {
		ms.logger.Errorf("%s: %v\n%s", line(), fail.Value, fail.Stack)
	}
	return err
}

// reportRejected calls ConnectionMetrics.OnConnectionRejected, when
// ServerConfig.Metrics implements it, through call: a method that does not
// return normally is logged, and the server goes on as if it had returned.
func (ms *Server) reportRejected(addr string, reason RejectReason) {
	cm, ok := ms.metrics.(ConnectionMetrics)
	if !ok {
		return
	}
	_ = ms.call("ConnectionMetrics.OnConnectionRejected", func() string {
		return fmt.Sprintf("ConnectionMetrics.OnConnectionRejected for %s did not return normally", addr)
	}, func() error {
		cm.OnConnectionRejected(addr, reason)
		return nil
	})
}

// allowClient asks ServerConfig.AllowClient, through call, whether to serve an
// accepted connection: true when it is not set, false when it does not return
// normally.
func (ms *Server) allowClient(sock net.Conn) bool {
	if ms.conf.AllowClient == nil {
		return true
	}
	allowed := false
	_ = ms.call("ServerConfig.AllowClient", func() string {
		return fmt.Sprintf("ServerConfig.AllowClient for %s did not return normally", sock.RemoteAddr())
	}, func() error {
		allowed = ms.conf.AllowClient(sock.RemoteAddr())
		return nil
	})
	return allowed
}

// reportOpened and reportClosed call ConnectionLifecycleMetrics.OnConnectionOpened
// and OnConnectionClosed, when ServerConfig.Metrics implements it, through
// call, as reportRejected does.
func (ms *Server) reportOpened(addr, role string) {
	lm, ok := ms.metrics.(ConnectionLifecycleMetrics)
	if !ok {
		return
	}
	_ = ms.call("ConnectionLifecycleMetrics.OnConnectionOpened", func() string {
		return fmt.Sprintf("ConnectionLifecycleMetrics.OnConnectionOpened for %s did not return normally", addr)
	}, func() error {
		lm.OnConnectionOpened(addr, role)
		return nil
	})
}

func (ms *Server) reportClosed(addr string) {
	lm, ok := ms.metrics.(ConnectionLifecycleMetrics)
	if !ok {
		return
	}
	_ = ms.call("ConnectionLifecycleMetrics.OnConnectionClosed", func() string {
		return fmt.Sprintf("ConnectionLifecycleMetrics.OnConnectionClosed for %s did not return normally", addr)
	}, func() error {
		lm.OnConnectionClosed(addr)
		return nil
	})
}
