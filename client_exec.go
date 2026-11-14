// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/session"
)

// executeRequest sends req via the session engine and returns the response.
// Retry and transport management are handled by the engine. The method is
// self-locking: it grabs the engine reference under lock, runs the request
// without holding the lock, and updates lastResponseTransactionID under lock.
//
// It is the only path from a client method to the transport, and it is where
// Config.AllowedFunctionCodes is enforced: a request whose function code is not
// on the list is refused here, before the engine is looked at, so nothing is
// dialled, sent or retried, whether the client is open or not. The error wraps
// ErrFunctionCodeNotAllowed. Callers have already called OnRequest and report
// the refusal as OnError, like any other failure.
//
// It is also where Config.PDUTap sees the exchange: once per request whose
// response carries the request's unit id, before any protocol validation.
//
// This method does NOT report to ClientMetrics. Outcome metrics are reported
// by the calling public/internal method after all protocol-level validation
// (checkResponseFC, payload checks, echo mismatches) completes, so that
// metrics accurately reflect the logical API outcome.
func (mc *Client) executeRequest(ctx context.Context, req *adu.Request) (res *adu.Response, err error) {
	engine, err := mc.requestEngine(req)
	if err != nil {
		return nil, err
	}

	res, err = engine.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	var received time.Time
	if mc.tap != nil {
		received = time.Now()
	}

	mc.lock.Lock()
	mc.state.lastResponseTransactionID = res.TransactionID
	mc.lock.Unlock()

	if res.UnitID != req.UnitID {
		return nil, ErrBadUnitID
	}
	mc.tapExchange(ctx, req, res, received)

	return res, nil
}

// requestEngine returns the engine req is to run on: the checks executeRequest
// makes before the request reaches the session, in its order. A function code
// that Config.AllowedFunctionCodes refuses fails first (an error wrapping
// ErrFunctionCodeNotAllowed), then a client that is not open
// (ErrClientNotOpen).
func (mc *Client) requestEngine(req *adu.Request) (*session.Engine, error) {
	if mc.allowedFCs != nil && !mc.allowedFCs[req.FunctionCode] {
		return nil, fmt.Errorf("%w: %s", ErrFunctionCodeNotAllowed, FunctionCode(req.FunctionCode))
	}

	mc.lock.Lock()
	engine := mc.state.engine
	mc.lock.Unlock()
	if engine == nil {
		return nil, ErrClientNotOpen
	}
	return engine, nil
}

// requestSizeError is the error of a call whose arguments make a request that
// does not fit one frame. It is a *ParameterError (errors.As reaches it, and
// errors.Is ErrUnexpectedParameters), and it also matches framer, the error
// the transport's framer gives such a request, which is what the call returned
// before its arguments were checked.
type requestSizeError struct {
	param  *ParameterError
	framer error
}

func (e *requestSizeError) Error() string   { return e.param.Error() }
func (e *requestSizeError) Unwrap() []error { return []error{e.param, e.framer} }

// requestSizeError returns the error of method for an argument param that
// makes a request too long for one frame of this client's transport:
// ErrInvalidMBAPLength is what the MBAP framer (tcp, tcp+tls, udp) refuses
// such a request with, ErrProtocolError what the RTU and ASCII framers do.
func (mc *Client) requestSizeError(method, param, reason string) error {
	framer := ErrProtocolError
	switch mc.transportType {
	case modbusTCP, modbusTCPOverTLS, modbusTCPOverUDP:
		framer = ErrInvalidMBAPLength
	default:
	}
	return &requestSizeError{
		param:  &ParameterError{Method: method, Param: param, Reason: reason},
		framer: framer,
	}
}

// getMetrics returns Config.Metrics, guarded (see guardedMetrics), or nil.
func (mc *Client) getMetrics() ClientMetrics {
	return mc.metrics
}

// reportOutcome reports the logical outcome of a completed request to
// ClientMetrics. It classifies errors into timeout vs general failure.
func reportOutcome(m ClientMetrics, unitID uint8, fc FunctionCode, start time.Time, err error) {
	if m == nil {
		return
	}
	d := time.Since(start)
	if err == nil {
		m.OnResponse(unitID, fc, d)
	} else if errors.Is(err, ErrRequestTimedOut) || errors.Is(err, context.DeadlineExceeded) {
		m.OnTimeout(unitID, fc, d)
	} else {
		m.OnError(unitID, fc, d, err)
	}
}
