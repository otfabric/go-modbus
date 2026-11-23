// SPDX-License-Identifier: MIT

package transport

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/logging"
	"github.com/otfabric/go-modbus/internal/protocol"
)

// readStep is one Read of a scriptConn: the bytes it returns and its error.
type readStep struct {
	data []byte
	err  error
}

// scriptConn is a net.Conn whose Reads follow a script; after it, Read
// returns io.EOF. Only Read and the deadline methods are used.
type scriptConn struct {
	net.Conn
	steps []readStep
}

func (c *scriptConn) Read(p []byte) (int, error) {
	if len(c.steps) == 0 {
		return 0, io.EOF
	}
	s := &c.steps[0]
	n := copy(p, s.data)
	s.data = s.data[n:]
	if len(s.data) > 0 {
		return n, nil
	}
	c.steps = c.steps[1:]
	return n, s.err
}

func (c *scriptConn) SetReadDeadline(time.Time) error { return nil }

// An MBAP request for FC03, unit 1, transaction ID 7.
var fc03Request = adu.AssembleMBAP(7, 1, 3, []byte{0, 0, 0, 1})

// With OnFrameStart set, the request is read as without it, and the
// function is called once per request, after its first byte and before the
// rest has been read.
func TestTCPReadHeader_OnFrameStart(t *testing.T) {
	for _, tc := range []struct {
		name  string
		steps []readStep
		// unread is the number of script steps not yet read in full when
		// OnFrameStart runs: the body, and the rest of the header if any.
		unread int
	}{
		{"one read", []readStep{{data: fc03Request}}, 1},
		{"first byte alone", []readStep{{data: fc03Request[:1]}, {data: fc03Request[1:]}}, 1},
		{"empty read first", []readStep{{data: nil}, {data: fc03Request}}, 1},
		{"first byte with an error", []readStep{{data: fc03Request[:1], err: errors.New("transient")}, {data: fc03Request[1:]}}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := &scriptConn{steps: tc.steps}
			tt := NewTCP(conn, time.Second, logging.NopLogger())
			starts := 0
			tt.OnFrameStart = func() {
				starts++
				if len(conn.steps) != tc.unread {
					t.Errorf("OnFrameStart with %d reads to come, want %d", len(conn.steps), tc.unread)
				}
			}
			req, txnID, err := tt.ReadFrame()
			if err != nil {
				t.Fatal(err)
			}
			if starts != 1 || txnID != 7 || req.UnitID != 1 || req.FunctionCode != 3 {
				t.Errorf("starts %d, txn %d, request %+v", starts, txnID, req)
			}
		})
	}
}

// A connection that ends before the first byte reports its own error and no
// frame start; one that ends inside the header reports io.ErrUnexpectedEOF,
// or the read error, after the frame start.
func TestTCPReadHeader_Errors(t *testing.T) {
	reset := errors.New("connection reset")
	for _, tc := range []struct {
		name       string
		steps      []readStep
		want       error
		wantStarts int
	}{
		{"closed before the first byte", nil, io.EOF, 0},
		{"reset before the first byte", []readStep{{err: reset}}, reset, 0},
		{"closed inside the header", []readStep{{data: fc03Request[:3]}}, io.ErrUnexpectedEOF, 1},
		{"reset inside the header", []readStep{{data: fc03Request[:3]}, {err: reset}}, reset, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tt := NewTCP(&scriptConn{steps: tc.steps}, time.Second, logging.NopLogger())
			starts := 0
			tt.OnFrameStart = func() { starts++ }
			_, _, err := tt.ReadFrame()
			if !errors.Is(err, tc.want) {
				t.Errorf("error %v, want %v", err, tc.want)
			}
			if starts != tc.wantStarts {
				t.Errorf("%d frame starts, want %d", starts, tc.wantStarts)
			}
		})
	}
}

// LastBadHeader holds the last header that could not be framed.
func TestTCPLastBadHeader(t *testing.T) {
	good := &scriptConn{steps: []readStep{{data: fc03Request}}}
	tt := NewTCP(good, time.Second, logging.NopLogger())
	if _, _, err := tt.ReadFrame(); err != nil {
		t.Fatal(err)
	}
	if _, ok := tt.LastBadHeader(); ok {
		t.Error("a bad header after a good frame")
	}
	for _, tc := range []struct {
		name   string
		header []byte
		want   error
	}{
		{"length 0", []byte{0, 9, 0, 0, 0, 0, 1}, protocol.ErrInvalidMBAPLength},
		{"protocol id 5", []byte{0, 9, 0, 5, 0, 1, 1}, protocol.ErrUnknownProtocolID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tt := NewTCP(&scriptConn{steps: []readStep{{data: tc.header}}}, time.Second, logging.NopLogger())
			if _, _, err := tt.ReadFrame(); !errors.Is(err, tc.want) {
				t.Fatalf("error %v, want %v", err, tc.want)
			}
			h, ok := tt.LastBadHeader()
			if !ok || string(h[:]) != string(tc.header) {
				t.Errorf("bad header % X (%v), want % X", h, ok, tc.header)
			}
		})
	}
}

// A response with another transaction ID is skipped and reported, with the
// request it was skipped for; the request then gets its own response.
func TestTCPReadResponse_TransactionIDAnomaly(t *testing.T) {
	late := adu.AssembleMBAP(6, 1, 3, []byte{2, 0, 1})
	own := adu.AssembleMBAP(7, 1, 3, []byte{2, 0, 2})
	tt := NewTCP(&scriptConn{steps: []readStep{{data: late}, {data: own}}}, time.Second, logging.NopLogger())
	tt.lastTxnID = 7
	req := &adu.Request{UnitID: 1, FunctionCode: 3}
	var got []Anomaly
	tt.OnAnomaly = func(r *adu.Request, a Anomaly) {
		if r != req {
			t.Errorf("anomaly for %+v, want the request", r)
		}
		got = append(got, a)
	}
	res, err := tt.readResponse(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.TransactionID != 7 || res.Payload[2] != 2 {
		t.Errorf("response %+v, want its own", res)
	}
	want := Anomaly{Kind: AnomalyTransactionID, Expected: 7, TransactionID: 6}
	if len(got) != 1 || got[0] != want {
		t.Errorf("anomalies %+v, want [%+v]", got, want)
	}
}
