// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"testing"
)

// TestClientReadBools_PaddingAndByteCount: the byte count of an FC01/FC02
// response must be exactly ceil(quantity/8). The unused high bits of the last
// byte are to be zero filled (V1.1b3 sections 6.1 and 6.2); a device that
// leaves them set is still read, and those bits are ignored.
func TestClientReadBools_PaddingAndByteCount(t *testing.T) {
	ctx := context.Background()
	for _, fc := range []FunctionCode{FCReadCoils, FCReadDiscreteInputs} {
		read := func(c *Client, qty uint16) ([]bool, error) {
			if fc == FCReadCoils {
				return c.ReadCoils(ctx, 1, 0, qty)
			}
			return c.ReadDiscreteInputs(ctx, 1, 0, qty)
		}
		reply := func(data []byte) mockResponder {
			return func(txid []byte, unitID, f byte, _ []byte) []byte {
				return normalFrame(txid, unitID, f, data)
			}
		}

		// Three bits 1, 0, 1 with the five padding bits set: 0xFD.
		c, cleanup := startMockServer(t, reply([]byte{0x01, 0xFD}))
		got, err := read(c, 3)
		cleanup()
		if err != nil || len(got) != 3 || !got[0] || got[1] || !got[2] {
			t.Errorf("%v padding bits set: %v, %v; want [true false true]", fc, got, err)
		}

		// Byte count one too large and one too small.
		for _, data := range [][]byte{{0x02, 0x05, 0x00}, {0x00}} {
			c, cleanup := startMockServer(t, reply(data))
			_, err := read(c, 3)
			cleanup()
			wantProtocolError(t, err, fc.String()+" wrong byte count")
		}
		// 16 coils need exactly two bytes.
		c, cleanup = startMockServer(t, reply([]byte{0x03, 0xFF, 0xFF, 0x00}))
		_, err = read(c, 16)
		cleanup()
		wantProtocolError(t, err, fc.String()+" byte count 3 for 16 bits")
	}
}

// TestProbeFunction_ExceptionShape: only an exception to the probed function
// (its code plus 0x80, one byte, code 0x01..0x0B) is reported as an exception;
// any other exception-shaped response is a response that failed validation.
func TestProbeFunction_ExceptionShape(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		frame   func(txid []byte, unit byte) []byte
		outcome ProbeOutcome
		code    ExceptionCode
	}{
		{"exception to the probed FC", func(txid []byte, unit byte) []byte {
			return exceptionFrame(txid, unit, byte(FCDiagnostics), byte(exIllegalDataValue))
		}, ProbeException, exIllegalDataValue},
		{"exception to another FC (0x83 for FC08)", func(txid []byte, unit byte) []byte {
			return exceptionFrame(txid, unit, byte(FCReadHoldingRegisters), byte(exIllegalFunction))
		}, ProbeValidationFailed, 0},
		{"exception with two bytes", func(txid []byte, unit byte) []byte {
			return normalFrame(txid, unit, 0x88, []byte{0x01, 0x00})
		}, ProbeValidationFailed, 0},
		{"exception without a code", func(txid []byte, unit byte) []byte {
			return normalFrame(txid, unit, 0x88, nil)
		}, ProbeValidationFailed, 0},
		{"exception code 0x00", func(txid []byte, unit byte) []byte {
			return exceptionFrame(txid, unit, byte(FCDiagnostics), 0x00)
		}, ProbeValidationFailed, 0},
		{"exception code 0xFF", func(txid []byte, unit byte) []byte {
			return exceptionFrame(txid, unit, byte(FCDiagnostics), 0xFF)
		}, ProbeValidationFailed, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, cleanup := startMockServer(t, func(txid []byte, unitID, _ byte, _ []byte) []byte {
				return tc.frame(txid, unitID)
			})
			defer cleanup()
			res, err := client.ProbeFunction(ctx, 1, FCDiagnostics)
			if err != nil || res.Outcome != tc.outcome || res.ExceptionCode != tc.code || res.Supported {
				t.Fatalf("ProbeFunction = %+v, %v; want outcome %v code %v", res, err, tc.outcome, tc.code)
			}
			// SupportsFunction agrees: none of these shows support, apart
			// from Illegal Data Value to the probed function.
			ok, err := client.SupportsFunction(ctx, 1, FCDiagnostics)
			if err != nil || ok != (tc.outcome == ProbeException) {
				t.Fatalf("SupportsFunction = %v, %v", ok, err)
			}
		})
	}
}
