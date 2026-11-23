// SPDX-License-Identifier: MIT

package modbus

import (
	"strconv"
	"time"
)

// ClientMetrics is an optional callback interface for observing client-side request
// outcomes. All methods are called synchronously in the goroutine executing the
// request; implementations must be non-blocking (e.g. increment an atomic counter,
// send on a buffered channel). A nil ClientMetrics is valid and disables collection.
//
// A method that does not return normally (of ClientMetrics or AttemptMetrics)
// is stopped there and logged at error level with its stack, through
// Config.Logger; the request goes on as if the method had returned and its
// result is not affected. A method may call the client, Close included.
//
// Metrics are request-level, not attempt-level: individual retry attempts within
// a single request are not observable through this interface. OnRequest fires once
// before the first attempt; the outcome callback (OnResponse/OnError/OnTimeout)
// fires once after all retries are exhausted. Duration always reflects total
// elapsed time from first attempt through final outcome, including retry delays.
type ClientMetrics interface {
	// OnRequest is called immediately before the first attempt to send a request.
	// unitID and functionCode identify the target device and operation.
	// This is called exactly once per logical request, regardless of retries.
	// It is also called for a request that Config.AllowedFunctionCodes refuses:
	// that request is not sent, and its outcome is OnError with an error that
	// wraps ErrFunctionCodeNotAllowed.
	OnRequest(unitID uint8, functionCode FunctionCode)

	// OnResponse is called after a successful round-trip (possibly after retries).
	// duration covers the total elapsed time including any retry delays.
	OnResponse(unitID uint8, functionCode FunctionCode, duration time.Duration)

	// OnError is called when a request ultimately fails with a non-timeout error
	// (after all retry attempts are exhausted).
	// duration covers the total elapsed time including any retry delays.
	OnError(unitID uint8, functionCode FunctionCode, duration time.Duration, err error)

	// OnTimeout is called when a request ultimately fails because it exceeded
	// its deadline (errors.Is(err, ErrRequestTimedOut) or context deadline exceeded).
	// duration covers the total elapsed time including any retry delays.
	OnTimeout(unitID uint8, functionCode FunctionCode, duration time.Duration)
}

// AttemptMetrics is an optional extension of ClientMetrics that provides per-attempt
// visibility into retries and reconnects. If the value assigned to Config.Metrics
// also implements AttemptMetrics, the library calls its methods for every individual
// attempt and dial within a retried request. Implementations must be non-blocking,
// and must not make requests on the same client: on a single connection the
// methods run while the request holds that connection. As for ClientMetrics, a
// method that does not return normally is logged, and the request goes on.
type AttemptMetrics interface {
	// OnAttempt is called after each individual transport attempt (including the first).
	// attempt is the zero-based attempt index (0 = first try). err is nil on success.
	OnAttempt(unitID uint8, functionCode FunctionCode, attempt int, duration time.Duration, err error)

	// OnRetryDial is called when the engine re-dials the single connection: between
	// retry attempts, and when a request finds the client without a usable
	// connection (after the previous one was lost or dropped). attempt is the
	// zero-based index of the attempt the dial is for: 0 for a request's first
	// attempt. err is nil on successful dial. Pooled connections are not reported.
	OnRetryDial(attempt int, duration time.Duration, err error)
}

// ServerMetrics is an optional callback interface for observing server-side request
// outcomes. All methods are called synchronously; implementations must be non-blocking.
// A nil ServerMetrics is valid and disables collection.
//
// Metrics are request-level: each incoming request triggers exactly one OnRequest
// call and exactly one outcome call (OnResponse or OnError). There is no retry
// concept on the server side.
//
// The methods run on the goroutine that serves the client's connection, with
// the unit ID and the function code as the client sent them: OnRequest is
// called before the server has checked that it serves the function code, so
// any value from 0 to 255 can arrive. A method that does not return normally
// is stopped there and logged at error level with its stack, as for a handler
// method; the request is then served and answered as if the method had
// returned, and the other methods are still called for it.
//
// A method may call Stop or Shutdown on the server that calls it; see
// RequestHandler.
type ServerMetrics interface {
	// OnRequest is called immediately before the handler is invoked.
	// unitID and functionCode identify the incoming request.
	OnRequest(unitID uint8, functionCode FunctionCode)

	// OnResponse is called after the handler returns without error.
	// duration is the handler execution time.
	OnResponse(unitID uint8, functionCode FunctionCode, duration time.Duration)

	// OnError is called when the handler returns an error, and when the server
	// answers with an exception of its own: ErrIllegalFunction for a function
	// code it does not serve, ErrServerDeviceFailure for a response too large
	// to send. duration is the handler execution time.
	OnError(unitID uint8, functionCode FunctionCode, duration time.Duration, err error)
}

// ConnectionMetrics is an optional extension of ServerMetrics. If the value
// assigned to ServerConfig.Metrics also implements ConnectionMetrics, the
// server calls OnConnectionRejected for every connection it closes to keep
// within ServerConfig.MaxClients:
//
//   - a connection it accepted when every slot was taken, which it closes at
//     once (RejectMaxClients);
//   - on tcp+tls, a connection whose TLS handshake had not completed, which it
//     closed to make room for a newer one (RejectTLSHandshakeEvicted).
//
// Connections accepted while the server is stopping, and connections that
// close for any other reason, are not reported.
//
// addr is the remote address of the connection (host:port). The method is
// called on the server's accept loop, after the connection was closed and
// with no lock held, so it delays the next Accept: it must be non-blocking.
// Like the ServerMetrics methods, a method that does not return normally is
// logged at error level with the stack, and the server goes on as if it had
// returned. It may call Stop or Shutdown (see RequestHandler).
type ConnectionMetrics interface {
	OnConnectionRejected(addr string, reason RejectReason)
}

// ConnectionLifecycleMetrics is an optional extension of ServerMetrics. A
// ServerConfig.Metrics that implements it is told when the server starts
// serving a tcp or tcp+tls connection (on tcp+tls, once its TLS handshake has
// completed), with the client's address and role, and when it stops serving
// it. Every opened connection is closed once, except one a Shutdown called
// from one of its own callbacks stopped: no callback runs on it after that.
type ConnectionLifecycleMetrics interface {
	OnConnectionOpened(addr, role string)
	OnConnectionClosed(addr string)
}

// ConnectionCloseMetrics is an optional extension of ServerMetrics. A
// ServerConfig.Metrics that implements it is told why the server stopped
// serving a tcp or tcp+tls connection: OnConnectionCloseReason is called once
// for every connection reported to ConnectionLifecycleMetrics.OnConnectionOpened
// (whether or not the value implements that interface), right after
// OnConnectionClosed, with the same exception: a connection a Shutdown called
// from one of its own callbacks stopped is not reported. err is the error that
// ended the connection; it is never nil.
// It runs on the connection's goroutine and must not block; one that does not
// return normally is logged, and the server goes on as if it had returned.
type ConnectionCloseMetrics interface {
	OnConnectionCloseReason(addr string, reason CloseReason, err error)
}

// CloseReason says why the server stopped serving a connection, as reported
// to ConnectionCloseMetrics.OnConnectionCloseReason. When several causes race
// (a client that leaves while its response is being written), the first one
// the server saw is reported.
type CloseReason uint8

const (
	// CloseClientClosed is a connection the client closed: the read ended with
	// io.EOF, or with io.ErrUnexpectedEOF when it closed in the middle of a
	// request.
	CloseClientClosed CloseReason = iota + 1
	// CloseIdleTimeout is a connection on which no complete request arrived
	// within ServerConfig.Timeout while the server had none to answer (err
	// wraps os.ErrDeadlineExceeded).
	CloseIdleTimeout
	// CloseMalformedFrame is a connection closed because the client sent an
	// MBAP header the server cannot frame: err is ErrInvalidMBAPLength or
	// ErrUnknownProtocolID.
	CloseMalformedFrame
	// CloseProtocolError is a connection closed because a request PDU was
	// shorter or longer than the fields of its function code or than its byte
	// count announces (which on a TCP stream is what a wrong MBAP length looks
	// like), or because a handler returned ErrProtocolError: err is
	// ErrProtocolError. The request is reported to ServerMetrics as an OnError
	// with an error that matches ErrProtocolError. Since v1.20.0 a request PDU
	// of the wrong length is answered with Illegal Data Value before the
	// connection is closed.
	CloseProtocolError
	// CloseWriteFailed is a connection closed because a response could not be
	// written in full within ServerConfig.Timeout: err is the write error.
	CloseWriteFailed
	// CloseReadFailed is a connection whose read failed for another reason, a
	// connection reset by the client for example: err is the read error.
	CloseReadFailed
	// CloseServerStopped is a connection closed by Shutdown or Stop: err is the
	// error the closed socket returned to the read or write in progress
	// (typically wrapping net.ErrClosed).
	CloseServerStopped
)

// String returns the name of the reason ("client closed", "idle timeout",
// "malformed frame", "protocol error", "write failed", "read failed", "server
// stopped"), or "CloseReason(n)" for a value without one.
func (r CloseReason) String() string {
	switch r {
	case CloseClientClosed:
		return "client closed"
	case CloseIdleTimeout:
		return "idle timeout"
	case CloseMalformedFrame:
		return "malformed frame"
	case CloseProtocolError:
		return "protocol error"
	case CloseWriteFailed:
		return "write failed"
	case CloseReadFailed:
		return "read failed"
	case CloseServerStopped:
		return "server stopped"
	default:
		return "CloseReason(" + strconv.Itoa(int(r)) + ")"
	}
}

// RejectReason says why the server closed a connection it reports to
// ConnectionMetrics.OnConnectionRejected.
type RejectReason uint8

const (
	// RejectMaxClients is a connection the server closed as soon as it was
	// accepted, because every ServerConfig.MaxClients slot was taken: by
	// connections that completed their TLS handshake on tcp+tls, by any
	// connection on tcp.
	RejectMaxClients RejectReason = iota + 1
	// RejectTLSHandshakeEvicted is a tcp+tls connection whose TLS handshake had
	// not completed, closed to make room for a newer connection (see
	// ServerConfig.MaxClients).
	RejectTLSHandshakeEvicted
	// RejectNotAllowed is a tcp or tcp+tls connection the server closed as soon
	// as it was accepted, because ServerConfig.AllowClient refused it.
	RejectNotAllowed
)

// String returns the name of the reason ("max clients", "tls handshake
// evicted", "not allowed"), or "RejectReason(n)" for a value without one.
func (r RejectReason) String() string {
	switch r {
	case RejectMaxClients:
		return "max clients"
	case RejectTLSHandshakeEvicted:
		return "tls handshake evicted"
	case RejectNotAllowed:
		return "not allowed"
	default:
		return "RejectReason(" + strconv.Itoa(int(r)) + ")"
	}
}
