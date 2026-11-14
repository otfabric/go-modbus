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
)

// String returns the name of the reason ("max clients", "tls handshake
// evicted"), or "RejectReason(n)" for a value without one.
func (r RejectReason) String() string {
	switch r {
	case RejectMaxClients:
		return "max clients"
	case RejectTLSHandshakeEvicted:
		return "tls handshake evicted"
	default:
		return "RejectReason(" + strconv.Itoa(int(r)) + ")"
	}
}
