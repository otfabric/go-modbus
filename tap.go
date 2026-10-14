// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// PDUExchange is one request the client sent and the response it received for
// it, as raw PDUs: the function code byte followed by the data. Transport
// framing (MBAP header, RTU CRC, ASCII LRC) is not included.
//
// Request and Response are copies owned by the receiver of the exchange; they
// may be kept and modified.
type PDUExchange struct {
	// UnitID is the unit id the request was addressed to (and the response
	// carried).
	UnitID uint8
	// Request is the request PDU as sent.
	Request []byte
	// Response is the response PDU as received. An exception response has the
	// function code with bit 0x80 set.
	Response []byte
	// Received is the time the client took the response from the transport.
	// It is read with time.Now and carries a monotonic clock reading, so the
	// difference between two exchanges is not affected by wall-clock steps.
	Received time.Time
}

// PDUTap is an optional callback that sees the raw PDUs of every answered
// request. It is meant for recording what a device answered (for example to
// replay it later from a virtual device), not for metrics.
//
// OnPDU is called synchronously in the goroutine executing the request, after
// the response arrived and before the calling method validates and decodes it.
// ctx is the context passed to the client method, so a caller can correlate an
// exchange with the call that caused it. Implementations must be non-blocking
// and safe for concurrent use when the client is shared between goroutines.
// The connection is released before OnPDU runs, so a slow tap delays only the
// call it reports. A panic in OnPDU is not recovered: it reaches the caller of
// the client method, and the client stays usable.
//
// OnPDU is called once for every request answered with a response that has the
// request's unit id and either its function code or its exception code
// (function code | 0x80):
//
//   - for every request a method puts on the wire, writes and probes included
//     (ReadAllDeviceIdentification and the read-modify-write helpers send more
//     than one request, and each is reported);
//   - once per request, whatever the RetryPolicy: only the attempt that
//     returned the response is reported;
//   - for exception responses;
//   - also for a response that the calling method then rejects (a wrong byte
//     count, a wrong echo): the tap reports what the device sent, the method's
//     error says whether it was usable.
//
// It is not called for a request that is refused by AllowedFunctionCodes, that
// fails or times out in the transport, or whose response carries another unit
// id or an unrelated function code.
type PDUTap interface {
	OnPDU(ctx context.Context, ex PDUExchange)
}

// PDUTapFunc adapts an ordinary function to the PDUTap interface.
type PDUTapFunc func(ctx context.Context, ex PDUExchange)

// OnPDU calls f(ctx, ex).
func (f PDUTapFunc) OnPDU(ctx context.Context, ex PDUExchange) { f(ctx, ex) }

// tapExchange reports the exchange of req and res to tap, when there is one
// and res answers req. received is the time res was taken from the transport.
func tapExchange(ctx context.Context, tap PDUTap, req *adu.Request, res *adu.Response, received time.Time) {
	if tap == nil {
		return
	}
	if res.FunctionCode != req.FunctionCode && res.FunctionCode != req.FunctionCode|0x80 {
		return
	}
	tap.OnPDU(ctx, PDUExchange{
		UnitID:   req.UnitID,
		Request:  pduBytes(req.FunctionCode, req.Payload),
		Response: pduBytes(res.FunctionCode, res.Payload),
		Received: received,
	})
}

// pduBytes returns a fresh PDU: the function code, then the payload.
func pduBytes(fc uint8, payload []byte) []byte {
	b := make([]byte, 0, 1+len(payload))
	b = append(b, fc)
	return append(b, payload...)
}
