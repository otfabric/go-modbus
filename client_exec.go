// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
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
	if mc.allowedFCs != nil && !mc.allowedFCs[req.FunctionCode] {
		return nil, fmt.Errorf("%w: %s", ErrFunctionCodeNotAllowed, FunctionCode(req.FunctionCode))
	}

	mc.lock.Lock()
	engine := mc.state.engine
	if engine == nil {
		mc.lock.Unlock()
		return nil, ErrClientNotOpen
	}
	mc.lock.Unlock()

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
	tapExchange(ctx, mc.tap, req, res, received)

	return res, nil
}

// getMetrics returns the current ClientMetrics under lock.
func (mc *Client) getMetrics() ClientMetrics {
	mc.lock.Lock()
	m := mc.conf.Metrics
	mc.lock.Unlock()
	return m
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
