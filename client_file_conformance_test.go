// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// fileRecordResponder answers FC20 with RecordLength zero registers for each
// sub-request and FC21 with the echo, and counts the requests it saw.
func fileRecordResponder(n *atomic.Int32) mockResponder {
	return func(txid []byte, unitID, fc byte, payload []byte) []byte {
		n.Add(1)
		switch fc {
		case byte(FCReadFileRecord):
			var out []byte
			for off := 1; off+7 <= len(payload); off += 7 {
				regs := int(payload[off+5])<<8 | int(payload[off+6])
				out = append(out, byte(1+2*regs), 0x06)
				out = append(out, make([]byte, 2*regs)...)
			}
			return normalFrame(txid, unitID, fc, append([]byte{byte(len(out))}, out...))
		case byte(FCWriteFileRecord):
			return normalFrame(txid, unitID, fc, payload)
		default:
			return exceptionFrame(txid, unitID, fc, byte(exIllegalFunction))
		}
	}
}

func fileSubs(n int, length uint16) []FileRecordRequest {
	subs := make([]FileRecordRequest, n)
	for i := range subs {
		subs[i] = FileRecordRequest{FileNumber: 4, RecordNumber: uint16(i), RecordLength: length}
	}
	return subs
}

// TestReadFileRecords_ResponseSize: the response every sub-request asks for
// (2 + 2*RecordLength bytes each) must fit in a response data length of at
// most 0xF5 (V1.1b3 section 6.14). Requests that ask for more are refused
// with a *ParameterError and nothing is sent.
func TestReadFileRecords_ResponseSize(t *testing.T) {
	var sent atomic.Int32
	client, cleanup := startMockServer(t, fileRecordResponder(&sent))
	defer cleanup()
	ctx := context.Background()
	cases := []struct {
		name  string
		subs  []FileRecordRequest
		ok    bool
		param string
	}{
		{"one sub-request of 121 registers (244 bytes)", fileSubs(1, 121), true, ""},
		{"one sub-request of 122 registers (246 bytes)", fileSubs(1, 122), false, "RecordLength"},
		{"one sub-request of 125 registers", fileSubs(1, 125), false, "RecordLength"},
		{"one sub-request of 65535 registers", fileSubs(1, 0xFFFF), false, "RecordLength"},
		{"35 sub-requests of 1 register (140 bytes)", fileSubs(35, 1), true, ""},
		{"35 sub-requests of 4 registers (350 bytes)", fileSubs(35, 4), false, "requests"},
		{"35 sub-requests of 65535 registers", fileSubs(35, 0xFFFF), false, "RecordLength"},
		// Every sub-response is 2 + 2N bytes, even, so the sum cannot be
		// 0xF5 (245): 244 is the largest that fits and 246 the first beyond.
		{"two sub-requests summing to 244 bytes", []FileRecordRequest{
			{FileNumber: 1, RecordLength: 60}, {FileNumber: 2, RecordLength: 60},
		}, true, ""},
		{"two sub-requests summing to 246 bytes", []FileRecordRequest{
			{FileNumber: 1, RecordLength: 60}, {FileNumber: 2, RecordLength: 61},
		}, false, "requests"},
		{"three sub-requests summing to 244 bytes", []FileRecordRequest{
			{FileNumber: 1, RecordLength: 39}, {FileNumber: 2, RecordLength: 40}, {FileNumber: 3, RecordLength: 40},
		}, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := sent.Load()
			recs, err := client.ReadFileRecords(ctx, 1, tc.subs)
			if tc.ok {
				if err != nil || len(recs) != len(tc.subs) {
					t.Fatalf("got %d records, %v", len(recs), err)
				}
				return
			}
			var pe *ParameterError
			if !errors.As(err, &pe) || pe.Param != tc.param || !errors.Is(err, ErrUnexpectedParameters) {
				t.Fatalf("err = %v, want a *ParameterError for %q", err, tc.param)
			}
			if sent.Load() != before {
				t.Fatal("a refused request reached the wire")
			}
		})
	}
}

// TestFileRecords_RecordNumberBounds: records are 0..9999 (0x270F) and file 0
// does not exist (V1.1b3 sections 6.14 and 6.15), for reads and writes alike.
func TestFileRecords_RecordNumberBounds(t *testing.T) {
	var sent atomic.Int32
	client, cleanup := startMockServer(t, fileRecordResponder(&sent))
	defer cleanup()
	ctx := context.Background()

	if _, err := client.ReadFileRecords(ctx, 1, []FileRecordRequest{{FileNumber: 1, RecordNumber: 0x270F, RecordLength: 1}}); err != nil {
		t.Errorf("FC20 record 0x270F: %v", err)
	}
	if err := client.WriteFileRecords(ctx, 1, []FileRecord{{FileNumber: 1, RecordNumber: 0x270F, Data: []uint16{1}}}); err != nil {
		t.Errorf("FC21 record 0x270F: %v", err)
	}
	if _, err := client.ReadFileRecords(ctx, 1, []FileRecordRequest{{FileNumber: 0xFFFF, RecordNumber: 0, RecordLength: 1}}); err != nil {
		t.Errorf("FC20 file 0xFFFF record 0: %v", err)
	}
	before := sent.Load()
	for _, tc := range []struct {
		name  string
		call  func() error
		param string
	}{
		{"FC20 record 0x2710", func() error {
			_, err := client.ReadFileRecords(ctx, 1, []FileRecordRequest{{FileNumber: 1, RecordNumber: 0x2710, RecordLength: 1}})
			return err
		}, "RecordNumber"},
		{"FC20 file 0", func() error {
			_, err := client.ReadFileRecords(ctx, 1, []FileRecordRequest{{FileNumber: 0, RecordNumber: 0, RecordLength: 1}})
			return err
		}, "FileNumber"},
		{"FC21 record 0x2710", func() error {
			return client.WriteFileRecords(ctx, 1, []FileRecord{{FileNumber: 1, RecordNumber: 0x2710, Data: []uint16{1}}})
		}, "RecordNumber"},
		{"FC21 file 0", func() error {
			return client.WriteFileRecords(ctx, 1, []FileRecord{{FileNumber: 0, RecordNumber: 0, Data: []uint16{1}}})
		}, "FileNumber"},
	} {
		var pe *ParameterError
		if err := tc.call(); !errors.As(err, &pe) || pe.Param != tc.param {
			t.Errorf("%s: err = %v, want a *ParameterError for %q", tc.name, err, tc.param)
		}
	}
	if sent.Load() != before {
		t.Fatal("a refused request reached the wire")
	}
}

// TestReadFileRecords_SpecExample replays the worked example of V1.1b3 section
// 6.14 byte for byte: two groups, file 4 records 1-2 and file 3 records 9-10.
// Its file response lengths are 0x05, below the 0x07 of the response table;
// the client accepts them, as the example requires.
func TestReadFileRecords_SpecExample(t *testing.T) {
	wantReq := []byte{0x0E, 0x06, 0x00, 0x04, 0x00, 0x01, 0x00, 0x02, 0x06, 0x00, 0x03, 0x00, 0x09, 0x00, 0x02}
	resp := []byte{0x0C, 0x05, 0x06, 0x0D, 0xFE, 0x00, 0x20, 0x05, 0x06, 0x33, 0xCD, 0x00, 0x40}
	var gotReq []byte
	client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, payload []byte) []byte {
		gotReq = append([]byte{fc}, payload...)
		return normalFrame(txid, unitID, fc, resp)
	})
	defer cleanup()
	recs, err := client.ReadFileRecords(context.Background(), 1, []FileRecordRequest{
		{FileNumber: 4, RecordNumber: 1, RecordLength: 2},
		{FileNumber: 3, RecordNumber: 9, RecordLength: 2},
	})
	if err != nil {
		t.Fatalf("ReadFileRecords: %v", err)
	}
	if !bytes.Equal(gotReq, append([]byte{0x14}, wantReq...)) {
		t.Errorf("request % X, want 14 % X", gotReq, wantReq)
	}
	if len(recs) != 2 || recs[0][0] != 0x0DFE || recs[0][1] != 0x0020 || recs[1][0] != 0x33CD || recs[1][1] != 0x0040 {
		t.Errorf("records %04X", recs)
	}
}
