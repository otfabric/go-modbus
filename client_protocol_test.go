package modbus

import (
	"context"
	"errors"
	"testing"
)

// These tests drive the client's response-validation paths using the
// programmable mock server, exercising branches that a spec-compliant server
// would never trigger (wrong FC, bad byte counts, echo mismatches, ...).

func wantProtocolError(t *testing.T, err error, op string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected error, got nil", op)
	}
	var pe *ProtocolError
	if !errors.As(err, &pe) {
		t.Fatalf("%s: expected *ProtocolError, got %T: %v", op, err, err)
	}
}

func TestClient_CheckResponseFC_WrongFC(t *testing.T) {
	client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
		// Reply with an unrelated function code.
		return normalFrame(txid, unitID, byte(FCReadInputRegisters), []byte{0x02, 0x00, 0x01})
	})
	defer cleanup()

	_, err := client.ReadHoldingRegisters(context.Background(), 1, 0, 1)
	wantProtocolError(t, err, "ReadHoldingRegisters wrong FC")
}

func TestClient_CheckResponseFC_ExceptionBadPayloadLen(t *testing.T) {
	client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
		// Exception FC but with an over-long payload (spec requires exactly 1).
		return normalFrame(txid, unitID, fc|0x80, []byte{0x01, 0x02})
	})
	defer cleanup()

	_, err := client.ReadHoldingRegisters(context.Background(), 1, 0, 1)
	wantProtocolError(t, err, "ReadHoldingRegisters bad exception len")
}

func TestClient_ExtractByteCount_Empty(t *testing.T) {
	client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
		return normalFrame(txid, unitID, fc, nil)
	})
	defer cleanup()

	_, err := client.ReadHoldingRegisters(context.Background(), 1, 0, 1)
	wantProtocolError(t, err, "ReadHoldingRegisters empty payload")
}

func TestClient_ExtractByteCount_Mismatch(t *testing.T) {
	client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
		// byte count says 4 but only 2 data bytes follow.
		return normalFrame(txid, unitID, fc, []byte{0x04, 0x00, 0x42})
	})
	defer cleanup()

	_, err := client.ReadHoldingRegisters(context.Background(), 1, 0, 1)
	wantProtocolError(t, err, "ReadHoldingRegisters byte-count mismatch")
}

func TestClient_ReadRegisters_DataLenMismatch(t *testing.T) {
	client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
		// Consistent byte count but wrong number of registers for the request.
		return normalFrame(txid, unitID, fc, []byte{0x02, 0x00, 0x42})
	})
	defer cleanup()

	_, err := client.ReadHoldingRegisters(context.Background(), 1, 0, 2)
	wantProtocolError(t, err, "ReadHoldingRegisters data-len mismatch")
}

func TestClient_ExpectEchoAddrValue_Branches(t *testing.T) {
	t.Run("wrong length", func(t *testing.T) {
		client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
			return normalFrame(txid, unitID, fc, []byte{0x00, 0x01, 0x00})
		})
		defer cleanup()
		err := client.WriteRegister(context.Background(), 1, 0x0001, 0x00FF)
		wantProtocolError(t, err, "WriteRegister short echo")
	})
	t.Run("value mismatch", func(t *testing.T) {
		client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
			return normalFrame(txid, unitID, fc, []byte{0x00, 0x01, 0xDE, 0xAD})
		})
		defer cleanup()
		err := client.WriteRegister(context.Background(), 1, 0x0001, 0x00FF)
		wantProtocolError(t, err, "WriteRegister echo mismatch")
	})
}

func TestClient_ExpectEchoAddrQuantity_Branches(t *testing.T) {
	t.Run("wrong length", func(t *testing.T) {
		client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
			return normalFrame(txid, unitID, fc, []byte{0x00, 0x01, 0x00})
		})
		defer cleanup()
		err := client.WriteRegisters(context.Background(), 1, 0x0001, []uint16{0x1111})
		wantProtocolError(t, err, "WriteRegisters short echo")
	})
	t.Run("quantity mismatch", func(t *testing.T) {
		client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
			return normalFrame(txid, unitID, fc, []byte{0x00, 0x01, 0x00, 0x09})
		})
		defer cleanup()
		err := client.WriteCoils(context.Background(), 1, 0x0001, []bool{true, false})
		wantProtocolError(t, err, "WriteCoils echo mismatch")
	})
}

func TestClient_MaskWriteRegister_Branches(t *testing.T) {
	t.Run("wrong length", func(t *testing.T) {
		client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
			return normalFrame(txid, unitID, fc, []byte{0x00, 0x10, 0x00, 0xF2})
		})
		defer cleanup()
		err := client.MaskWriteRegister(context.Background(), 1, 0x0010, 0x00F2, 0x0025)
		wantProtocolError(t, err, "MaskWriteRegister short echo")
	})
	t.Run("echo mismatch", func(t *testing.T) {
		client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
			return normalFrame(txid, unitID, fc, []byte{0x00, 0x10, 0x00, 0xF2, 0xDE, 0xAD})
		})
		defer cleanup()
		err := client.MaskWriteRegister(context.Background(), 1, 0x0010, 0x00F2, 0x0025)
		wantProtocolError(t, err, "MaskWriteRegister echo mismatch")
	})
}

func TestClient_ReadWriteMultipleRegisters_DataLenMismatch(t *testing.T) {
	client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
		// readQty=2 expects 4 data bytes; server returns only 2.
		return normalFrame(txid, unitID, fc, []byte{0x02, 0x00, 0x42})
	})
	defer cleanup()

	_, err := client.ReadWriteMultipleRegisters(context.Background(), 1, 0, 2, 10, []uint16{0x1234})
	wantProtocolError(t, err, "ReadWriteMultipleRegisters data-len mismatch")
}

func TestClient_ReadFIFOQueue_Branches(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		wantErr bool
		want    []uint16
	}{
		{"short", []byte{0x00, 0x02}, true, nil},
		{"fifo count too large", []byte{0x00, 0x40, 0x00, 0x20}, true, nil},
		{"byte count inconsistent", []byte{0x00, 0x06, 0x00, 0x01}, true, nil},
		{"payload length mismatch", []byte{0x00, 0x04, 0x00, 0x01, 0x11, 0x22, 0x33}, true, nil},
		{"ok", []byte{0x00, 0x04, 0x00, 0x01, 0x11, 0x22}, false, []uint16{0x1122}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
				return normalFrame(txid, unitID, fc, tc.payload)
			})
			defer cleanup()

			vals, err := client.ReadFIFOQueue(context.Background(), 1, 0x0000)
			if tc.wantErr {
				wantProtocolError(t, err, "ReadFIFOQueue "+tc.name)
				return
			}
			if err != nil {
				t.Fatalf("ReadFIFOQueue: unexpected error: %v", err)
			}
			if len(vals) != len(tc.want) || (len(vals) == 1 && vals[0] != tc.want[0]) {
				t.Fatalf("ReadFIFOQueue = %v, want %v", vals, tc.want)
			}
		})
	}
}

func TestClient_ReadFileRecords_Branches(t *testing.T) {
	req := []FileRecordRequest{{FileNumber: 1, RecordNumber: 0, RecordLength: 1}}

	cases := []struct {
		name    string
		payload []byte
		wantErr bool
	}{
		{"empty", nil, true},
		{"data length mismatch", []byte{0x08, 0x03, 0x06, 0x00, 0x42}, true},
		{"wrong ref type", []byte{0x04, 0x03, 0x07, 0x00, 0x42}, true},
		{"expected bytes mismatch", []byte{0x06, 0x05, 0x06, 0x00, 0x42, 0x00, 0x43}, true},
		{"ok", []byte{0x04, 0x03, 0x06, 0x00, 0x42}, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
				return normalFrame(txid, unitID, fc, tc.payload)
			})
			defer cleanup()

			recs, err := client.ReadFileRecords(context.Background(), 1, req)
			if tc.wantErr {
				wantProtocolError(t, err, "ReadFileRecords "+tc.name)
				return
			}
			if err != nil {
				t.Fatalf("ReadFileRecords: unexpected error: %v", err)
			}
			if len(recs) != 1 || len(recs[0]) != 1 || recs[0][0] != 0x0042 {
				t.Fatalf("ReadFileRecords = %v, want [[0x0042]]", recs)
			}
		})
	}
}

func TestClient_WriteRegisterBytes_OddLengthPadded(t *testing.T) {
	client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, payload []byte) []byte {
		// Echo addr + quantity (first 4 bytes) as a valid FC16 response.
		return normalFrame(txid, unitID, fc, payload[0:4])
	})
	defer cleanup()

	// 3 bytes -> padded to 2 registers (4 bytes).
	if err := client.WriteRegisterBytes(context.Background(), 1, 0x0000, []byte{0x11, 0x22, 0x33}); err != nil {
		t.Fatalf("WriteRegisterBytes odd length: %v", err)
	}
}
