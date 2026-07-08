// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"testing"
)

// diagEchoResponder answers FC08 by echoing the request sub-function plus
// dataLen fabricated data bytes. A well-behaved device returns 2 data bytes for
// the counter/register sub-functions.
func diagEchoResponder(dataLen int) mockResponder {
	return func(txid []byte, unitID, fc byte, payload []byte) []byte {
		if fc != byte(FCDiagnostics) || len(payload) < 2 {
			return exceptionFrame(txid, unitID, fc, byte(exIllegalFunction))
		}
		data := append([]byte{payload[0], payload[1]}, make([]byte, dataLen)...)
		for i := 0; i < dataLen; i++ {
			data[2+i] = byte(0x10 + i)
		}
		return normalFrame(txid, unitID, fc, data)
	}
}

func diagWrappers() []struct {
	name   string
	call   func(*Client) error
	hasLen bool
} {
	ctx := context.Background()
	return []struct {
		name   string
		call   func(*Client) error
		hasLen bool
	}{
		{"Loopback", func(c *Client) error { _, e := c.DiagnosticLoopback(ctx, 1, 0x1234); return e }, true},
		{"Register", func(c *Client) error { _, e := c.DiagnosticRegister(ctx, 1); return e }, true},
		{"BusMessageCount", func(c *Client) error { _, e := c.BusMessageCount(ctx, 1); return e }, true},
		{"BusCommErr", func(c *Client) error { _, e := c.DiagnosticBusCommunicationErrorCount(ctx, 1); return e }, true},
		{"BusExcErr", func(c *Client) error { _, e := c.DiagnosticBusExceptionErrorCount(ctx, 1); return e }, true},
		{"ServerMsg", func(c *Client) error { _, e := c.DiagnosticServerMessageCount(ctx, 1); return e }, true},
		{"ServerNoResp", func(c *Client) error { _, e := c.DiagnosticServerNoResponseCount(ctx, 1); return e }, true},
		{"ServerNAK", func(c *Client) error { _, e := c.DiagnosticServerNAKCount(ctx, 1); return e }, true},
		{"ServerBusy", func(c *Client) error { _, e := c.DiagnosticServerBusyCount(ctx, 1); return e }, true},
		{"CharOverrun", func(c *Client) error { _, e := c.DiagnosticBusCharacterOverrunCount(ctx, 1); return e }, true},
		{"ClearCounters", func(c *Client) error { return c.DiagnosticClearCounters(ctx, 1) }, true},
		{"ClearOverrun", func(c *Client) error { return c.DiagnosticClearOverrunCounterAndFlag(ctx, 1) }, true},
		{"ForceListenOnly", func(c *Client) error { return c.DiagnosticForceListenOnlyMode(ctx, 1) }, false},
	}
}

func TestClient_DiagnosticWrappers_Happy(t *testing.T) {
	client, cleanup := startMockServer(t, diagEchoResponder(2))
	defer cleanup()
	for _, w := range diagWrappers() {
		if err := w.call(client); err != nil {
			t.Errorf("%s: unexpected error: %v", w.name, err)
		}
	}
}

func TestClient_DiagnosticWrappers_BadLength(t *testing.T) {
	client, cleanup := startMockServer(t, diagEchoResponder(1))
	defer cleanup()
	for _, w := range diagWrappers() {
		if !w.hasLen {
			continue
		}
		if err := w.call(client); err == nil {
			t.Errorf("%s: expected error for bad data length, got nil", w.name)
		}
	}
}

func TestClient_Diagnostics_SubFunctionMismatch(t *testing.T) {
	client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
		// Reply with a different sub-function than requested.
		return normalFrame(txid, unitID, fc, []byte{0xFF, 0xFF, 0x00, 0x00})
	})
	defer cleanup()
	_, err := client.Diagnostics(context.Background(), 1, DiagReturnQueryData, []byte{0x00, 0x00})
	wantProtocolError(t, err, "Diagnostics sub-function mismatch")
}

func TestClient_Diagnostics_PayloadTooShort(t *testing.T) {
	client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
		return normalFrame(txid, unitID, fc, []byte{0x00})
	})
	defer cleanup()
	_, err := client.Diagnostics(context.Background(), 1, DiagReturnQueryData, nil)
	wantProtocolError(t, err, "Diagnostics payload too short")
}

func TestClient_ReadExceptionStatus(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
			return normalFrame(txid, unitID, fc, []byte{0x5A})
		})
		defer cleanup()
		status, err := client.ReadExceptionStatus(context.Background(), 1)
		if err != nil || status != 0x5A {
			t.Fatalf("ReadExceptionStatus = 0x%02X, %v; want 0x5A, nil", status, err)
		}
	})
	t.Run("wrong length", func(t *testing.T) {
		client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
			return normalFrame(txid, unitID, fc, []byte{0x01, 0x02})
		})
		defer cleanup()
		_, err := client.ReadExceptionStatus(context.Background(), 1)
		wantProtocolError(t, err, "ReadExceptionStatus wrong length")
	})
}

func TestClient_GetCommEventCounter(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
			return normalFrame(txid, unitID, fc, []byte{0xFF, 0xFF, 0x00, 0x2A})
		})
		defer cleanup()
		cr, err := client.GetCommEventCounter(context.Background(), 1)
		if err != nil || cr.EventCount != 0x2A {
			t.Fatalf("GetCommEventCounter = %+v, %v", cr, err)
		}
	})
	t.Run("wrong length", func(t *testing.T) {
		client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
			return normalFrame(txid, unitID, fc, []byte{0x00, 0x2A})
		})
		defer cleanup()
		_, err := client.GetCommEventCounter(context.Background(), 1)
		wantProtocolError(t, err, "GetCommEventCounter wrong length")
	})
}

func TestClient_GetCommEventLog_Branches(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		wantErr bool
	}{
		{"empty", nil, true},
		{"byte count mismatch", []byte{0x08, 0x00, 0x00}, true},
		{"too small", []byte{0x02, 0x00, 0x00}, true},
		{"ok", []byte{0x06, 0x00, 0x00, 0x00, 0x0A, 0x00, 0x14}, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
				return normalFrame(txid, unitID, fc, tc.payload)
			})
			defer cleanup()
			cl, err := client.GetCommEventLog(context.Background(), 1)
			if tc.wantErr {
				wantProtocolError(t, err, "GetCommEventLog "+tc.name)
				return
			}
			if err != nil || cl.MessageCount != 0x14 {
				t.Fatalf("GetCommEventLog = %+v, %v", cl, err)
			}
		})
	}
}

func TestClient_ReportServerID_Branches(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		wantErr bool
		wantRun bool
	}{
		{"empty", nil, true, false},
		{"byte count mismatch", []byte{0x08, 0x01}, true, false},
		{"ok with run indicator", []byte{0x02, 0x11, 0xFF}, false, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
				return normalFrame(txid, unitID, fc, tc.payload)
			})
			defer cleanup()
			rs, err := client.ReportServerID(context.Background(), 1)
			if tc.wantErr {
				wantProtocolError(t, err, "ReportServerID "+tc.name)
				return
			}
			if err != nil {
				t.Fatalf("ReportServerID: %v", err)
			}
			if tc.wantRun && (rs.RunIndicatorStatus == nil || !*rs.RunIndicatorStatus) {
				t.Fatalf("ReportServerID run indicator = %v, want true", rs.RunIndicatorStatus)
			}
		})
	}
}

func TestClient_ProbeFunction_Outcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("unsupported fc", func(t *testing.T) {
		client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
			return normalFrame(txid, unitID, fc, nil)
		})
		defer cleanup()
		_, err := client.ProbeFunction(ctx, 1, FCWriteSingleCoil)
		if err == nil {
			t.Fatal("ProbeFunction unsupported FC: expected error")
		}
		if _, err := client.SupportsFunction(ctx, 1, FCWriteSingleCoil); err == nil {
			t.Fatal("SupportsFunction unsupported FC: expected error")
		}
	})

	t.Run("supported", func(t *testing.T) {
		client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
			return normalFrame(txid, unitID, fc, []byte{0x02, 0x00, 0x01})
		})
		defer cleanup()
		res, err := client.ProbeFunction(ctx, 1, FCReadHoldingRegisters)
		if err != nil || res.Outcome != ProbeSupported || !res.Supported {
			t.Fatalf("ProbeFunction supported = %+v, %v", res, err)
		}
		ok, err := client.SupportsFunction(ctx, 1, FCReadHoldingRegisters)
		if err != nil || !ok {
			t.Fatalf("SupportsFunction = %v, %v; want true, nil", ok, err)
		}
	})

	t.Run("exception", func(t *testing.T) {
		client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
			return exceptionFrame(txid, unitID, fc, byte(exIllegalFunction))
		})
		defer cleanup()
		res, err := client.ProbeFunction(ctx, 1, FCReadHoldingRegisters)
		if err != nil || res.Outcome != ProbeException {
			t.Fatalf("ProbeFunction exception = %+v, %v", res, err)
		}
		// A structurally valid exception counts as "function supported".
		ok, err := client.SupportsFunction(ctx, 1, FCReadHoldingRegisters)
		if err != nil || !ok {
			t.Fatalf("SupportsFunction exception = %v, %v; want true, nil", ok, err)
		}
	})

	t.Run("validation failed", func(t *testing.T) {
		client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
			// Structurally valid frame but wrong byte-count prefix for the probe.
			return normalFrame(txid, unitID, fc, []byte{0x04, 0x00, 0x01})
		})
		defer cleanup()
		res, err := client.ProbeFunction(ctx, 1, FCReadHoldingRegisters)
		if err != nil || res.Outcome != ProbeValidationFailed {
			t.Fatalf("ProbeFunction validation failed = %+v, %v", res, err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		client, cleanup := startMockServer(t, func(_ []byte, _ byte, _ byte, _ []byte) []byte {
			return nil // never respond
		})
		defer cleanup()
		res, err := client.ProbeFunction(ctx, 1, FCReadHoldingRegisters)
		if err != nil || res.Outcome != ProbeTimeout {
			t.Fatalf("ProbeFunction timeout = %+v, %v", res, err)
		}
		ok, err := client.SupportsFunction(ctx, 1, FCReadHoldingRegisters)
		if err != nil || ok {
			t.Fatalf("SupportsFunction timeout = %v, %v; want false, nil", ok, err)
		}
	})
}
