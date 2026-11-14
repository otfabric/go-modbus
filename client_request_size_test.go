// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/otfabric/go-modbus/internal/adu"
	intsession "github.com/otfabric/go-modbus/internal/session"
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
			if pe, ok := errors.AsType[*ParameterError](err); !ok || pe.Method != "Diagnostics" || pe.Param != "data" {
				t.Errorf("%s, %d data bytes: %v, want a *ParameterError for Diagnostics, data", sc.scheme, sz.n, err)
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

// diagnosticsSchemes are the transports of the client, with the error their
// framer gave a request too long for a frame on v1.15.3.
var diagnosticsSchemes = []struct {
	url    string
	framer error
}{
	{"tcp://scripted.invalid:502", ErrInvalidMBAPLength},
	{"tcp+tls://scripted.invalid:802", ErrInvalidMBAPLength},
	{"udp://scripted.invalid:502", ErrInvalidMBAPLength},
	{"rtu:///dev/scripted", ErrProtocolError},
	{"rtuovertcp://scripted.invalid:502", ErrProtocolError},
	{"rtuoverudp://scripted.invalid:502", ErrProtocolError},
	{"ascii:///dev/scripted", ErrProtocolError},
	{"asciiovertcp://scripted.invalid:502", ErrProtocolError},
}

// newScriptedClientFor is newScriptedClient for any URL: the client's engine
// runs on a scripted transport, so nothing is dialled. mut adjusts the
// configuration; open false leaves the client closed.
func newScriptedClientFor(t *testing.T, url string, mut func(*Config), open bool, fn scriptFn) (*Client, *scriptedTransport, *eventMetrics) {
	t.Helper()
	m := &eventMetrics{}
	conf := Config{URL: url, Speed: 19200, Metrics: m}
	if strings.HasPrefix(url, "tcp+tls") {
		pki := e2ePKI(t)
		conf.TLSClientCert, conf.TLSRootCAs = &pki.clientCert, pki.ca.pool
	}
	if mut != nil {
		mut(&conf)
	}
	c, err := New(conf)
	if err != nil {
		t.Fatalf("New(%s): %v", url, err)
	}
	tr := &scriptedTransport{fn: fn}
	if open {
		eng := intsession.NewEngine(intsession.Config{
			Dial: func(context.Context) (intsession.Transport[*adu.Request, *adu.Response], error) { return tr, nil },
		})
		if err := eng.Open(context.Background()); err != nil {
			t.Fatalf("engine Open: %v", err)
		}
		c.lock.Lock()
		c.state.engine = eng
		c.lock.Unlock()
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, tr, m
}

// More than 250 bytes of Diagnostics data are refused before anything is
// sent, on every transport, with a *ParameterError that also matches what the
// transport's framer returned for them on v1.15.3. 250 bytes are still sent.
// A function code that AllowedFunctionCodes refuses, and a client that is not
// open, are reported first; ClientMetrics sees the request and its error.
func TestDiagnostics_TooMuchDataIsAParameterError(t *testing.T) {
	echo := func(req *adu.Request) (*adu.Response, error) { return okRes(req, req.Payload...), nil }
	for _, sc := range diagnosticsSchemes {
		scheme, _, _ := strings.Cut(sc.url, ":")
		other := ErrProtocolError
		if sc.framer == ErrProtocolError {
			other = ErrInvalidMBAPLength
		}
		t.Run(scheme, func(t *testing.T) {
			for _, n := range []int{251, 252, 0xFFFF, 0x10000} {
				c, tr, m := newScriptedClientFor(t, sc.url, nil, true, echo)
				dr, err := c.Diagnostics(context.Background(), 1, DiagReturnQueryData, make([]byte, n))
				pe, ok := errors.AsType[*ParameterError](err)
				if !ok || pe.Method != "Diagnostics" || pe.Param != "data" || dr != nil {
					t.Fatalf("%d bytes: %+v, %v; want a *ParameterError for Diagnostics, data", n, dr, err)
				}
				if err.Error() != pe.Error() {
					t.Errorf("%d bytes: message %q, want the ParameterError's %q", n, err.Error(), pe.Error())
				}
				if !errors.Is(err, ErrUnexpectedParameters) || !errors.Is(err, sc.framer) || errors.Is(err, other) {
					t.Errorf("%d bytes: %v matches ErrUnexpectedParameters %v, %v %v, %v %v",
						n, err, errors.Is(err, ErrUnexpectedParameters), sc.framer, errors.Is(err, sc.framer), other, errors.Is(err, other))
				}
				if reqs := tr.requests(); len(reqs) != 0 {
					t.Errorf("%d bytes: %d requests reached the transport, want none", n, len(reqs))
				}
				checkEvents(t, m, []metricEvent{
					{kind: "request", unit: 1, fc: FCDiagnostics},
					{kind: "error", unit: 1, fc: FCDiagnostics},
				}, err)
			}

			c, tr, _ := newScriptedClientFor(t, sc.url, nil, true, echo)
			data := make([]byte, 250)
			if dr, err := c.Diagnostics(context.Background(), 1, DiagReturnQueryData, data); err != nil || len(dr.Data) != 250 {
				t.Errorf("250 bytes: %v, want them echoed", err)
			}
			if reqs := tr.requests(); len(reqs) != 1 || len(reqs[0].Payload) != 252 {
				t.Errorf("250 bytes: requests %d, want one of 252 payload bytes", len(reqs))
			}

			c, _, _ = newScriptedClientFor(t, sc.url, nil, false, echo)
			if _, err := c.Diagnostics(context.Background(), 1, DiagReturnQueryData, make([]byte, 251)); !errors.Is(err, ErrClientNotOpen) || errors.Is(err, ErrUnexpectedParameters) {
				t.Errorf("closed client: %v, want ErrClientNotOpen first", err)
			}
			allowed := func(conf *Config) { conf.AllowedFunctionCodes = []FunctionCode{FCReadHoldingRegisters} }
			c, _, _ = newScriptedClientFor(t, sc.url, allowed, false, echo)
			if _, err := c.Diagnostics(context.Background(), 1, DiagReturnQueryData, make([]byte, 251)); !errors.Is(err, ErrFunctionCodeNotAllowed) || errors.Is(err, ErrUnexpectedParameters) {
				t.Errorf("FC08 not allowed: %v, want ErrFunctionCodeNotAllowed first", err)
			}
		})
	}
}
