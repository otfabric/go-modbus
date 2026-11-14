// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"

	"github.com/otfabric/go-modbus/internal/adu"
)

// startEchoPeer starts a loopback listener that sends back whatever its one
// client sends, and reports how many bytes it received once the client has
// closed the connection. An echo is a valid answer to Diagnostics sub-function
// 0x0000 (Return Query Data) in all three framings.
func startEchoPeer(t *testing.T) (addr string, received <-chan int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	total := make(chan int, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			total <- 0
			return
		}
		defer func() { _ = conn.Close() }()
		n := 0
		buf := make([]byte, 2048)
		for {
			m, err := conn.Read(buf)
			n += m
			if m > 0 {
				if _, werr := conn.Write(buf[:m]); werr != nil {
					err = werr
				}
			}
			if err != nil {
				total <- n
				return
			}
		}
	}()
	return ln.Addr().String(), total
}

// Client.Diagnostics takes request data of any length from its caller. What
// fits a frame is sent; what does not is refused by the framer of every
// transport before a byte is written: 250 data bytes are the most (253 bytes of
// PDU less the function code and the sub-function, MODBUS Application Protocol
// V1.1b3, section 4.1).
func TestDiagnostics_RequestDataBoundary(t *testing.T) {
	const maxData = 250
	schemes := []struct {
		scheme string
		// frameLen is the length on the wire of a request with n data bytes.
		frameLen func(n int) int
		// tooLong is the error of a request that does not fit.
		tooLong error
	}{
		{"tcp", func(n int) int { return adu.MBAPHeaderLength + 1 + 2 + n }, ErrInvalidMBAPLength},
		{"rtuovertcp", func(n int) int { return 2 + 2 + n + 2 }, ErrProtocolError},
		{"asciiovertcp", func(n int) int { return 1 + 2*(2+2+n+1) + 2 }, ErrProtocolError},
	}
	sizes := []struct {
		n    int
		fits bool
	}{
		{0, true},
		{1, true},
		{maxData - 1, true},
		{maxData, true},
		{maxData + 1, false},
		{maxData + 2, false},
		{1000, false},
		{0xFFFF, false},
		{0x10000, false},
	}
	for _, sc := range schemes {
		for _, sz := range sizes {
			addr, received := startEchoPeer(t)
			c, err := New(Config{URL: sc.scheme + "://" + addr, Speed: 115200, Timeout: e2eWait})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := c.Open(); err != nil {
				t.Fatalf("%s: Open: %v", sc.scheme, err)
			}
			data := make([]byte, sz.n)
			for i := range data {
				data[i] = byte(i%251) + 1
			}
			dr, err := c.Diagnostics(context.Background(), 1, DiagReturnQueryData, data)
			_ = c.Close()
			got := <-received
			if sz.fits {
				if err != nil || dr == nil || !bytes.Equal(dr.Data, data) {
					t.Errorf("%s, %d data bytes: %v, want the data echoed", sc.scheme, sz.n, err)
				}
				if want := sc.frameLen(sz.n); got != want {
					t.Errorf("%s, %d data bytes: the peer received %d bytes, want a frame of %d", sc.scheme, sz.n, got, want)
				}
				continue
			}
			if !errors.Is(err, sc.tooLong) || dr != nil {
				t.Errorf("%s, %d data bytes: %v, want %v", sc.scheme, sz.n, err, sc.tooLong)
			}
			if got != 0 {
				t.Errorf("%s, %d data bytes: the peer received %d bytes, want none", sc.scheme, sz.n, got)
			}
		}
	}
}

// ReportServerID takes its data from the byte count in the response. Every
// byte count a byte can hold is accepted when the payload has that length,
// and the data returned is that long; a byte count that disagrees with the
// payload is a protocol error. The scripted transport is not bound by a frame
// limit, so the largest byte count can be fed.
func TestReportServerID_ByteCountBoundaries(t *testing.T) {
	cases := []struct {
		name      string
		byteCount int
		dataLen   int
		ok        bool
	}{
		{"zero", 0, 0, true},
		{"one", 1, 1, true},
		{"two: id and run indicator", 2, 2, true},
		{"largest in a frame", 251, 251, true},
		{"one below the type maximum", 254, 254, true},
		{"type maximum", 255, 255, true},
		{"byte count one above the data", 1, 0, false},
		{"byte count one below the data", 0, 1, false},
		{"type maximum with one byte missing", 255, 254, false},
		{"zero with a full payload", 0, 255, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := make([]byte, 1+tc.dataLen)
			payload[0] = byte(tc.byteCount)
			for i := 1; i < len(payload); i++ {
				payload[i] = 0xFF
			}
			c, _, _ := newScriptedClient(t, func(req *adu.Request) (*adu.Response, error) {
				return okRes(req, payload...), nil
			})
			rs, err := c.ReportServerID(context.Background(), 1)
			if !tc.ok {
				if _, isProto := errors.AsType[*ProtocolError](err); !isProto || rs != nil {
					t.Fatalf("byte count %d with %d data bytes: %+v, %v; want a protocol error", tc.byteCount, tc.dataLen, rs, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("byte count %d: %v", tc.byteCount, err)
			}
			if len(rs.Data) != tc.byteCount || !bytes.Equal(rs.Data, payload[1:]) {
				t.Fatalf("byte count %d: %d data bytes returned", tc.byteCount, len(rs.Data))
			}
			if wantStatus := tc.byteCount >= 2; (rs.RunIndicatorStatus != nil) != wantStatus {
				t.Errorf("byte count %d: run indicator present = %v, want %v", tc.byteCount, rs.RunIndicatorStatus != nil, wantStatus)
			}
			// The data is the caller's: it does not share storage with the response.
			if tc.byteCount > 0 {
				rs.Data[0] ^= 0xFF
				if payload[1] != 0xFF {
					t.Error("the returned data aliases the response payload")
				}
			}
		})
	}
}
