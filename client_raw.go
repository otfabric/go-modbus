// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"fmt"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// maxRawRequestPayload is the largest request PDU after the function code
// that SendRawRequest accepts: a PDU is at most 253 bytes.
const maxRawRequestPayload = 252

// RawResponse is the response to SendRawRequest: the response PDU, split into
// the function code and the bytes after it, as received.
type RawResponse struct {
	// FunctionCode is the function code of the response. It is always the
	// request's function code: an exception is returned as an error instead.
	FunctionCode FunctionCode
	// Payload is the response PDU after the function code. The library does
	// not parse or validate it.
	Payload []byte
}

// SendRawRequest sends a request PDU made of fc and payload (the bytes after
// the function code) to unitID and returns the response PDU unparsed. It is
// meant for vendor and user-defined function codes, and for recording or
// forwarding standard ones without decoding them.
//
// The request takes the same path as every other method:
// Config.AllowedFunctionCodes (ErrFunctionCodeNotAllowed, nothing is sent),
// metrics, retries, the PDU tap, the unit id and transaction id checks, and the
// timeouts. An exception response is returned as an *ExceptionError, and a
// response with another function code as a *ProtocolError.
//
// fc must be a request function code (0x01..0x7F) and payload at most 252
// bytes; otherwise the error wraps ErrUnexpectedParameters and nothing is sent.
//
// On rtu://, rtuovertcp:// and rtuoverudp:// the frame carries no length, so
// the transport has to know where the response ends. For the function codes the
// library implements it reads the length from the response as usual. For any
// other function code it reads until the line has been silent for 3.5
// characters, as it does for FC08 and FC43: a device or adapter that pauses
// longer than that inside a frame cuts the response short (ErrBadCRC or
// ErrShortFrame). TCP, TLS, UDP and ASCII frames carry their length or an end
// delimiter and need nothing of the kind.
func (mc *Client) SendRawRequest(ctx context.Context, unitID uint8, fc FunctionCode, payload []byte) (rr *RawResponse, err error) {
	if fc == 0 || fc.IsException() {
		return nil, newParameterError("SendRawRequest", "fc",
			fmt.Sprintf("0x%02X is not a request function code (valid: 0x01..0x7F)", uint8(fc)))
	}
	if len(payload) > maxRawRequestPayload {
		return nil, newParameterError("SendRawRequest", "payload",
			fmt.Sprintf("%d bytes, the most a PDU can carry is %d", len(payload), maxRawRequestPayload))
	}
	req := &adu.Request{
		UnitID:       unitID,
		FunctionCode: byte(fc),
		Payload:      append([]byte(nil), payload...),
	}

	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, fc)
	}
	defer func() { reportOutcome(m, unitID, fc, start, err) }()

	res, err := mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	if err = checkResponseFC(res, req.FunctionCode); err != nil {
		return
	}
	rr = &RawResponse{
		FunctionCode: FunctionCode(res.FunctionCode),
		Payload:      append([]byte(nil), res.Payload...),
	}
	return
}
