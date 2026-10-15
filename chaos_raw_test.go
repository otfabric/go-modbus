// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// chaosVendorFC is the vendor function code chaosRawDevice serves.
const chaosVendorFC FunctionCode = 0x41

// chaosRawDevice is the chaos reference device with a raw request handler that
// answers chaosVendorFC with the request payload reversed.
type chaosRawDevice struct{ *chaosDevice }

func (d chaosRawDevice) HandleRawRequest(ctx context.Context, req *RawRequest) ([]byte, error) {
	if err := d.fault(ctx); err != nil {
		return nil, err
	}
	if req.FunctionCode != chaosVendorFC {
		return nil, ErrIllegalFunction
	}
	return reversed(req.Payload), nil
}

// chaosRawCall sends chaosVendorFC with a 16-byte payload derived from n and
// checks the answer.
func chaosRawCall(ctx context.Context, c *Client, n int) (bool, error) {
	in := make([]byte, 16)
	for i := range in {
		in[i] = byte(n + 3*i)
	}
	res, err := c.SendRawRequest(ctx, refUnitID, chaosVendorFC, in)
	if err != nil {
		return false, err
	}
	return res.FunctionCode == chaosVendorFC && bytes.Equal(res.Payload, reversed(in)), nil
}

// TestChaosSendRawRequest runs the fault catalogue of TestChaosFaultMatrix with
// SendRawRequest and a vendor function code as the faulted and follow-up call.
//
// Every fault has the same outcome as for a standard function code but one: an
// MBAP length one byte short. For FC04 both sides compare it with the PDU and
// drop the connection. A vendor PDU has no length of its own, so the shortened
// frame is a valid PDU: the server answers the request without its last byte,
// or the client takes the response without its last byte, and the call
// succeeds with that data. The byte left behind breaks the next exchange (the
// server closes the connection, or the client times out and drops it), and the
// calls after that succeed.
func TestChaosSendRawRequest(t *testing.T) {
	const timeout = 150 * time.Millisecond
	for _, mode := range []chaosMode{chaosModeSingle, chaosModeSingleRetry, chaosModePool} {
		mode := mode
		t.Run(mode.name, func(t *testing.T) {
			for i, fc := range chaosFaultCases(timeout) {
				fc := fc
				if strings.HasSuffix(fc.name, "/corrupt-len/shorter") {
					fc.want, fc.wrongData, fc.lostFollowUps = chaosClassOK, true, 1
				}
				seed := int64(i)
				t.Run(fc.name, func(t *testing.T) {
					t.Parallel()
					chaosRunFaultCaseWith(t, mode, fc, timeout, seed, chaosRawDevice{chaosNewDevice(seed)}, chaosRawCall)
				})
			}
		})
	}
}
