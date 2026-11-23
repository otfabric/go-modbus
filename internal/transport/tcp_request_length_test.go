// SPDX-License-Identifier: MIT

package transport

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/logging"
)

// TestTCPReadFrame_RequestPDULength: with RequestPDULength set, a request of
// a fixed-size function code whose MBAP length disagrees is reported as a
// RequestLengthError with its unit ID and function code as soon as the
// function code is read; the bytes after it stay unread. A request of the
// right length, and one of a function code without a fixed size, are read
// as usual; so are all requests without the hook.
func TestTCPReadFrame_RequestPDULength(t *testing.T) {
	fixed := func(fc byte) int {
		if fc == 0x03 {
			return 5
		}
		return 0
	}
	next := []byte{0x00, 0x02, 0x00, 0x00, 0x00, 0x06, 0x01, 0x03, 0x00, 0x00, 0x00, 0x01}
	for _, tc := range []struct {
		name    string
		hook    func(byte) int
		frame   []byte
		wantErr *RequestLengthError
		wantPDU []byte
		unread  []byte // what must still be in the stream
	}{
		{"right length", fixed, []byte{0, 1, 0, 0, 0, 6, 0x21, 0x03, 0, 0, 0, 1}, nil, []byte{0x03, 0, 0, 0, 1}, next},
		{"overcount", fixed, []byte{0, 1, 0, 0, 0, 8, 0x21, 0x03}, &RequestLengthError{Want: 5, Have: 7}, []byte{0x03}, next},
		{"undercount", fixed, []byte{0, 1, 0, 0, 0, 4, 0x21, 0x03}, &RequestLengthError{Want: 5, Have: 3}, []byte{0x03}, append([]byte{0, 0, 1}, next...)},
		{"variable function code", fixed, []byte{0, 1, 0, 0, 0, 3, 0x21, 0x41, 0xAA}, nil, []byte{0x41, 0xAA}, next},
		{"no hook", nil, []byte{0, 1, 0, 0, 0, 4, 0x21, 0x03, 0, 0}, nil, []byte{0x03, 0, 0}, next},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer func() { _ = client.Close() }()
			tt := NewTCP(server, time.Second, logging.NopLogger())
			tt.RequestPDULength = tc.hook
			stream := append(append([]byte{}, tc.frame...), tc.unread...)
			go func() { _, _ = client.Write(stream) }()

			req, txnID, err := tt.ReadFrame()
			if txnID != 1 || req == nil || req.UnitID != 0x21 {
				t.Fatalf("txn %d, request %+v, err %v", txnID, req, err)
			}
			pdu := append([]byte{req.FunctionCode}, req.Payload...)
			if !bytes.Equal(pdu, tc.wantPDU) {
				t.Errorf("PDU % X, want % X", pdu, tc.wantPDU)
			}
			var lenErr *RequestLengthError
			switch {
			case tc.wantErr == nil && err != nil:
				t.Fatalf("err %v", err)
			case tc.wantErr != nil && (!errors.As(err, &lenErr) || *lenErr != *tc.wantErr):
				t.Fatalf("err %v, want %v", err, tc.wantErr)
			case tc.wantErr != nil && lenErr.Error() == "":
				t.Error("empty message")
			}
			got := make([]byte, len(tc.unread))
			_ = server.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := io.ReadFull(server, got); err != nil || !bytes.Equal(got, tc.unread) {
				t.Errorf("left in the stream % X (%v), want % X", got, err, tc.unread)
			}
		})
	}
}

// TestTCPReadFrame_RequestPDULengthReadErrors: a stream that ends before the
// function code, or before the rest of a request of the right length, is a
// read error, not a RequestLengthError.
func TestTCPReadFrame_RequestPDULengthReadErrors(t *testing.T) {
	for _, frame := range [][]byte{
		{0, 1, 0, 0, 0, 6, 0x21},
		{0, 1, 0, 0, 0, 6, 0x21, 0x03, 0},
	} {
		client, server := net.Pipe()
		tt := NewTCP(server, time.Second, logging.NopLogger())
		tt.RequestPDULength = func(byte) int { return 5 }
		go func() { _, _ = client.Write(frame); _ = client.Close() }()
		req, _, err := tt.ReadFrame()
		var lenErr *RequestLengthError
		if err == nil || errors.As(err, &lenErr) || req != nil {
			t.Errorf("% X: request %+v, err %v; want a read error", frame, req, err)
		}
	}
}
