// SPDX-License-Identifier: MIT

package modbus

import (
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/callback"
)

// The server calls application code in these places, each through call, on
// the goroutine of the accept loop or of a connection:
//
//   - the RequestHandler methods (safeDispatch), and the methods of the error a
//     handler returns, which choose the answer (answerFor);
//   - the methods of ServerConfig.Metrics: ServerMetrics (reportMetrics),
//     ConnectionMetrics (reportRejected), ConnectionLifecycleMetrics
//     (reportOpened, reportClosed), ConnectionCloseMetrics
//     (reportCloseReason), AcceptErrorMetrics (reportAcceptError),
//     TLSHandshakeMetrics (reportTLSHandshake) and ProtocolViolationMetrics
//     (reportViolation);
//   - ServerConfig.AllowClient (allowClient) and ServerConfig.PDUTap
//     (tapServerExchange);
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
		allowed = ms.conf.AllowClient.AllowClient(sock.RemoteAddr())
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

// reportCloseReason calls ConnectionCloseMetrics.OnConnectionCloseReason, when
// ServerConfig.Metrics implements it, through call, as reportRejected does.
func (ms *Server) reportCloseReason(addr string, reason CloseReason, err error) {
	cm, ok := ms.metrics.(ConnectionCloseMetrics)
	if !ok {
		return
	}
	_ = ms.call("ConnectionCloseMetrics.OnConnectionCloseReason", func() string {
		return fmt.Sprintf("ConnectionCloseMetrics.OnConnectionCloseReason for %s did not return normally", addr)
	}, func() error {
		cm.OnConnectionCloseReason(addr, reason, err)
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

// reportAcceptError calls AcceptErrorMetrics.OnAcceptError, when
// ServerConfig.Metrics implements it, through call.
func (ms *Server) reportAcceptError(err error, retryIn time.Duration) {
	am := ms.acceptMetrics
	if am == nil {
		return
	}
	_ = ms.call("AcceptErrorMetrics.OnAcceptError", func() string {
		return "AcceptErrorMetrics.OnAcceptError did not return normally"
	}, func() error {
		am.OnAcceptError(err, retryIn)
		return nil
	})
}

// reportTLSHandshake calls TLSHandshakeMetrics.OnTLSHandshake, when
// ServerConfig.Metrics implements it, through call, for the handshake on sock
// that took d and ended with err. tlsSock is nil when the handshake could not
// be started.
func (ms *Server) reportTLSHandshake(sock net.Conn, tlsSock *tls.Conn, d time.Duration, role string, roleErr, err error) {
	tm := ms.tlsMetrics
	if tm == nil {
		return
	}
	var info TLSHandshakeInfo
	if tlsSock != nil {
		info = newTLSHandshakeInfo(tlsSock, d, err)
	} else {
		info = TLSHandshakeInfo{
			LocalAddr:  addrString(sock.LocalAddr()),
			RemoteAddr: addrString(sock.RemoteAddr()),
			Err:        err,
			Duration:   d,
		}
	}
	if err == nil {
		info.Role, info.RoleErr = role, roleErr
	}
	_ = ms.call("TLSHandshakeMetrics.OnTLSHandshake", func() string {
		return fmt.Sprintf("TLSHandshakeMetrics.OnTLSHandshake for %s did not return normally", info.RemoteAddr)
	}, func() error {
		tm.OnTLSHandshake(info)
		return nil
	})
}

// reportRequestViolation reports req, a framed request that breaks the
// structure of its function code, to ProtocolViolationMetrics (see
// reportViolation). code is the exception answered (0: none).
func (ms *Server) reportRequestViolation(req *adu.Request, txnID uint16, clientAddr, clientRole string, err error, code ExceptionCode, closed bool) {
	if ms.violationMetrics == nil {
		return
	}
	ms.reportViolation(ProtocolViolation{
		ClientAddr:      clientAddr,
		ClientRole:      clientRole,
		TransactionID:   txnID,
		Length:          adu.Uint16(len(req.Payload) + 2),
		UnitID:          req.UnitID,
		FunctionCode:    FunctionCode(req.FunctionCode),
		HasFunctionCode: true,
		Err:             err,
		Exception:       code,
		Closed:          closed,
	})
}

// reportViolation calls ProtocolViolationMetrics.OnProtocolViolation through
// call. ms.violationMetrics must not be nil.
func (ms *Server) reportViolation(v ProtocolViolation) {
	_ = ms.call("ProtocolViolationMetrics.OnProtocolViolation", func() string {
		return fmt.Sprintf("ProtocolViolationMetrics.OnProtocolViolation for %s did not return normally", v.ClientAddr)
	}, func() error {
		ms.violationMetrics.OnProtocolViolation(v)
		return nil
	})
}
