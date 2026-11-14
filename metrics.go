// SPDX-License-Identifier: MIT

package modbus

import "time"

// ClientMetrics is an optional callback interface for observing client-side request
// outcomes. All methods are called synchronously in the goroutine executing the
// request; implementations must be non-blocking (e.g. increment an atomic counter,
// send on a buffered channel). A nil ClientMetrics is valid and disables collection.
//
// The methods must not panic. The client does not recover a panic in a
// ClientMetrics (or AttemptMetrics) method: it unwinds through the client into
// the goroutine that called the client method, like a panic in that caller's
// own code. A method may call the client, Close included.
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
// panic in a method is not recovered.
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
// any value from 0 to 255 can arrive. A panic in a method is recovered and
// logged at error level with a stack trace, as for a handler method; the
// request is then served and answered as if the method had returned, and the
// other methods are still called for it.
//
// A method must not call Stop, or Shutdown and wait for it, on the server that
// calls it; see RequestHandler.
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
