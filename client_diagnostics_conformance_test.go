// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/otfabric/go-modbus/internal/adu"
)

// Conformance tests for Diagnostics (FC08, V1.1b3 section 6.8), Report Server
// ID (FC17, section 6.13) and the event field of Get Comm Event Log (FC12,
// section 6.10), on the client and on the server.

// pduRecorder is a mockResponder that records the request PDUs it sees and
// answers with answer.
type pduRecorder struct {
	mu     sync.Mutex
	pdus   [][]byte
	answer func(fc byte, payload []byte) []byte
}

func (r *pduRecorder) respond(txid []byte, unitID, fc byte, payload []byte) []byte {
	r.mu.Lock()
	r.pdus = append(r.pdus, append([]byte{fc}, payload...))
	r.mu.Unlock()
	return normalFrame(txid, unitID, fc, r.answer(fc, payload))
}

func (r *pduRecorder) take() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.pdus
	r.pdus = nil
	return out
}

// TestDiagnostics_TypedWrapperRequestPDUs pins the request PDU of every typed
// FC08 wrapper byte for byte against the data fields of section 6.8.1: the
// counter and register sub-functions carry 00 00 (DiagnosticRegister and
// BusMessageCount sent no data field up to v1.19.0), the loopback its value.
func TestDiagnostics_TypedWrapperRequestPDUs(t *testing.T) {
	rec := &pduRecorder{answer: func(_ byte, p []byte) []byte {
		// Echo everything: valid for the echo sub-functions, and two data
		// bytes for the counters.
		return append([]byte(nil), p...)
	}}
	client, cleanup := startMockServer(t, rec.respond)
	defer cleanup()
	ctx := context.Background()
	u16call := func(f func(context.Context, uint8) (uint16, error)) func() error {
		return func() error { _, err := f(ctx, 1); return err }
	}
	cases := []struct {
		name string
		call func() error
		pdu  []byte
	}{
		{"DiagnosticLoopback", func() error { _, err := client.DiagnosticLoopback(ctx, 1, 0xA537); return err }, []byte{0x08, 0x00, 0x00, 0xA5, 0x37}},
		{"DiagnosticRegister", u16call(client.DiagnosticRegister), []byte{0x08, 0x00, 0x02, 0x00, 0x00}},
		{"DiagnosticForceListenOnlyMode", func() error { return client.DiagnosticForceListenOnlyMode(ctx, 1) }, []byte{0x08, 0x00, 0x04, 0x00, 0x00}},
		{"DiagnosticClearCounters", func() error { return client.DiagnosticClearCounters(ctx, 1) }, []byte{0x08, 0x00, 0x0A, 0x00, 0x00}},
		{"BusMessageCount", u16call(client.BusMessageCount), []byte{0x08, 0x00, 0x0B, 0x00, 0x00}},
		{"DiagnosticBusCommunicationErrorCount", u16call(client.DiagnosticBusCommunicationErrorCount), []byte{0x08, 0x00, 0x0C, 0x00, 0x00}},
		{"DiagnosticBusExceptionErrorCount", u16call(client.DiagnosticBusExceptionErrorCount), []byte{0x08, 0x00, 0x0D, 0x00, 0x00}},
		{"DiagnosticServerMessageCount", u16call(client.DiagnosticServerMessageCount), []byte{0x08, 0x00, 0x0E, 0x00, 0x00}},
		{"DiagnosticServerNoResponseCount", u16call(client.DiagnosticServerNoResponseCount), []byte{0x08, 0x00, 0x0F, 0x00, 0x00}},
		{"DiagnosticServerNAKCount", u16call(client.DiagnosticServerNAKCount), []byte{0x08, 0x00, 0x10, 0x00, 0x00}},
		{"DiagnosticServerBusyCount", u16call(client.DiagnosticServerBusyCount), []byte{0x08, 0x00, 0x11, 0x00, 0x00}},
		{"DiagnosticBusCharacterOverrunCount", u16call(client.DiagnosticBusCharacterOverrunCount), []byte{0x08, 0x00, 0x12, 0x00, 0x00}},
		{"DiagnosticClearOverrunCounterAndFlag", func() error { return client.DiagnosticClearOverrunCounterAndFlag(ctx, 1) }, []byte{0x08, 0x00, 0x14, 0x00, 0x00}},
	}
	for _, tc := range cases {
		if err := tc.call(); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		got := rec.take()
		if len(got) != 1 || !bytes.Equal(got[0], tc.pdu) {
			t.Errorf("%s: request PDUs % X, want % X", tc.name, got, tc.pdu)
		}
	}
}

// TestDiagnostics_EchoChecked: the wrappers of the sub-functions whose
// response is "Echo Request Data" reject a response that is not the echo.
func TestDiagnostics_EchoChecked(t *testing.T) {
	for _, data := range [][]byte{{0xFF, 0x00}, {0x00}, {0x00, 0x00, 0x00, 0x00}, nil} {
		client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, p []byte) []byte {
			return normalFrame(txid, unitID, fc, append(append([]byte(nil), p[:2]...), data...))
		})
		ctx := context.Background()
		if _, err := client.DiagnosticLoopback(ctx, 1, 0x1234); err == nil {
			t.Errorf("Loopback answered with % X: no error", data)
		} else {
			wantProtocolErrorText(t, err, "DiagnosticLoopback")
		}
		wantProtocolErrorText(t, client.DiagnosticClearCounters(ctx, 1), "DiagnosticClearCounters")
		wantProtocolErrorText(t, client.DiagnosticClearOverrunCounterAndFlag(ctx, 1), "DiagnosticClearOverrunCounterAndFlag")
		cleanup()
	}
}

// TestDiagnostics_OddRequestData: the generic Diagnostics refuses data that
// is not a whole number of 16-bit items, for vendor sub-functions too, sends
// nothing, and reports OnRequest and OnError; even lengths are sent.
func TestDiagnostics_OddRequestData(t *testing.T) {
	rec := &pduRecorder{answer: func(_ byte, p []byte) []byte { return append([]byte(nil), p...) }}
	m := &eventMetrics{}
	client, cleanup := startMockServer(t, rec.respond)
	defer cleanup()
	client.metrics = m
	for _, n := range []int{1, 3, 249} {
		for _, sub := range []DiagnosticSubFunction{DiagReturnQueryData, 0xABCD} {
			_, err := client.Diagnostics(context.Background(), 1, sub, make([]byte, n))
			pe, ok := errors.AsType[*ParameterError](err)
			if !ok || pe.Method != "Diagnostics" || pe.Param != "data" || !errors.Is(err, ErrUnexpectedParameters) {
				t.Errorf("%d bytes, sub 0x%04X: %v, want a *ParameterError for data", n, uint16(sub), err)
			}
		}
	}
	if got := rec.take(); len(got) != 0 {
		t.Errorf("odd data reached the wire: % X", got)
	}
	if ev := m.snapshot(); len(ev) != 12 || ev[0].kind != "request" || ev[1].kind != "error" {
		t.Errorf("metrics events %v, want OnRequest and OnError for each call", ev)
	}
	// A vendor sub-function with even data is sent, and its response data is
	// returned as received, whatever its length.
	rec.answer = func(_ byte, p []byte) []byte { return append(append([]byte(nil), p[:2]...), 1, 2, 3) }
	dr, err := client.Diagnostics(context.Background(), 1, 0xABCD, []byte{9, 9})
	if err != nil || !bytes.Equal(dr.Data, []byte{1, 2, 3}) {
		t.Errorf("vendor sub-function: %+v, %v; want the raw data 01 02 03", dr, err)
	}
	if got := rec.take(); len(got) != 1 || !bytes.Equal(got[0], []byte{0x08, 0xAB, 0xCD, 9, 9}) {
		t.Errorf("vendor request PDUs % X", got)
	}
}

// TestDiagnostics_ServerDataLength: the server answers a request with odd
// data with Illegal Data Value without calling the handler, refuses to send
// handler data of odd length (Server Device Failure), and answers the worked
// example of section 6.8.2 (08 00 00 A5 37) with its echo.
func TestDiagnostics_ServerDataLength(t *testing.T) {
	calls := 0
	echo := &fnFunctions{diag: func(req *DiagnosticsRequest) ([]byte, error) {
		calls++
		return req.Data, nil
	}}
	exc := func(fc FunctionCode, code ExceptionCode) *adu.Response {
		return &adu.Response{UnitID: 0x2A, FunctionCode: byte(fc) | 0x80, Payload: []byte{byte(code)}, TransactionID: 0x0101}
	}
	for _, p := range [][]byte{{0, 0, 1}, {0, 0, 1, 2, 3}, {0xAB, 0xCD, 0xEE}} {
		tr, m := serveOne(t, echo, FCDiagnostics, p...)
		if tr.closed || len(tr.responses) != 1 || !reflect.DeepEqual(tr.responses[0], exc(FCDiagnostics, exIllegalDataValue)) {
			t.Errorf("request % X: %+v closed %v, want Illegal Data Value", p, tr.responses, tr.closed)
		}
		checkEvents(t, m, []metricEvent{{kind: "request", unit: 0x2A, fc: FCDiagnostics}, {kind: "error", unit: 0x2A, fc: FCDiagnostics}}, ErrIllegalDataValue)
	}
	if calls != 0 {
		t.Errorf("handler called %d times for odd request data", calls)
	}

	tr, _ := serveOne(t, echo, FCDiagnostics, 0x00, 0x00, 0xA5, 0x37)
	want := &adu.Response{UnitID: 0x2A, FunctionCode: 0x08, Payload: []byte{0x00, 0x00, 0xA5, 0x37}, TransactionID: 0x0101}
	if len(tr.responses) != 1 || !reflect.DeepEqual(tr.responses[0], want) {
		t.Errorf("section 6.8.2 example: %+v, want % X", tr.responses, want.Payload)
	}

	for _, n := range []int{1, 3, 249} {
		odd := &fnFunctions{diag: func(*DiagnosticsRequest) ([]byte, error) { return make([]byte, n), nil }}
		tr, _ := serveOne(t, odd, FCDiagnostics, 0x00, 0x02, 0x00, 0x00)
		if len(tr.responses) != 1 || !reflect.DeepEqual(tr.responses[0], exc(FCDiagnostics, exServerDeviceFailure)) {
			t.Errorf("handler data of %d bytes: %+v, want Server Device Failure", n, tr.responses)
		}
	}
	for _, n := range []int{0, 2, 250} {
		even := &fnFunctions{diag: func(*DiagnosticsRequest) ([]byte, error) { return make([]byte, n), nil }}
		tr, _ := serveOne(t, even, FCDiagnostics, 0x00, 0x0B, 0x00, 0x00)
		if len(tr.responses) != 1 || tr.responses[0].FunctionCode != 0x08 || len(tr.responses[0].Payload) != 2+n {
			t.Errorf("handler data of %d bytes: %+v, want a normal response", n, tr.responses)
		}
	}
}

// TestReportServerID_Server: the run indicator is mandatory, so the server
// does not send Byte Count 0; one byte and Additional Data are sent as given.
func TestReportServerID_Server(t *testing.T) {
	for _, tc := range []struct {
		data []byte
		want []byte // response payload; nil: Server Device Failure
	}{
		{nil, nil},
		{[]byte{}, nil},
		{[]byte{0xFF}, []byte{0x01, 0xFF}},
		{[]byte{0x2A, 0xFF}, []byte{0x02, 0x2A, 0xFF}},
		{[]byte{0x2A, 0x00, 'v', '1'}, []byte{0x04, 0x2A, 0x00, 'v', '1'}},
		{make([]byte, 251), append([]byte{251}, make([]byte, 251)...)},
		{make([]byte, 252), nil},
	} {
		h := &fnFunctions{serverID: func(*ReportServerIDRequest) ([]byte, error) { return tc.data, nil }}
		tr, _ := serveOne(t, h, FCReportServerID)
		if len(tr.responses) != 1 {
			t.Fatalf("%d bytes: %d responses", len(tc.data), len(tr.responses))
		}
		res := tr.responses[0]
		if tc.want == nil {
			if res.FunctionCode != 0x91 || !bytes.Equal(res.Payload, []byte{byte(exServerDeviceFailure)}) {
				t.Errorf("%d bytes: % X % X, want Server Device Failure", len(tc.data), res.FunctionCode, res.Payload)
			}
			continue
		}
		if res.FunctionCode != 0x11 || !bytes.Equal(res.Payload, tc.want) {
			t.Errorf("%d bytes: % X % X, want 11 % X", len(tc.data), res.FunctionCode, res.Payload, tc.want)
		}
	}
}

// TestReportServerID_ClientRunIndicator: the raw data is always returned;
// RunIndicatorStatus is guessed from the last byte whenever Data has 2 bytes
// or more, as up to v1.19.0; RunIndicator reads it at a known Server ID length.
func TestReportServerID_ClientRunIndicator(t *testing.T) {
	on, off := true, false
	for _, tc := range []struct {
		name    string
		payload []byte
		data    []byte
		guess   *bool
	}{
		{"byte count 0", []byte{0x00}, []byte{}, nil},
		{"run indicator only", []byte{0x01, 0xFF}, []byte{0xFF}, nil},
		{"id and ON", []byte{0x02, 0x2A, 0xFF}, []byte{0x2A, 0xFF}, &on},
		{"id and OFF", []byte{0x02, 0x2A, 0x00}, []byte{0x2A, 0x00}, &off},
		{"additional data ending in 0x42", []byte{0x03, 0x2A, 0xFF, 0x42}, []byte{0x2A, 0xFF, 0x42}, &off},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
				return normalFrame(txid, unitID, fc, tc.payload)
			})
			defer cleanup()
			rs, err := client.ReportServerID(context.Background(), 1)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(rs.Data, tc.data) {
				t.Errorf("Data % X, want % X", rs.Data, tc.data)
			}
			if !reflect.DeepEqual(rs.RunIndicatorStatus, tc.guess) {
				t.Errorf("RunIndicatorStatus %v, want %v", rs.RunIndicatorStatus, tc.guess)
			}
		})
	}
	rs := &ReportServerIDResponse{Data: []byte{0x2A, 0xFF, 0x00, 0x42}}
	for _, tc := range []struct {
		idLen  int
		on, ok bool
	}{{-1, false, false}, {0, false, false}, {1, true, true}, {2, false, true}, {3, false, false}, {4, false, false}} {
		if on, ok := rs.RunIndicator(tc.idLen); on != tc.on || ok != tc.ok {
			t.Errorf("RunIndicator(%d) = %v, %v; want %v, %v", tc.idLen, on, ok, tc.on, tc.ok)
		}
	}
	if _, ok := (&ReportServerIDResponse{}).RunIndicator(0); ok {
		t.Error("RunIndicator of empty data reported ok")
	}
}

// TestGetCommEventLog_ClientEventLimit: the event field holds 0-64 bytes, so a
// Byte Count above 70 is a protocol error; 70 is accepted.
func TestGetCommEventLog_ClientEventLimit(t *testing.T) {
	for _, tc := range []struct {
		events int
		ok     bool
	}{{0, true}, {64, true}, {65, false}, {180, false}} {
		payload := append([]byte{byte(6 + tc.events), 0, 0, 0, 1, 0, 2}, make([]byte, tc.events)...)
		client, cleanup := startMockServer(t, func(txid []byte, unitID, fc byte, _ []byte) []byte {
			return normalFrame(txid, unitID, fc, payload)
		})
		cl, err := client.GetCommEventLog(context.Background(), 1)
		cleanup()
		if tc.ok {
			if err != nil || len(cl.Events) != tc.events {
				t.Errorf("%d events: %+v, %v", tc.events, cl, err)
			}
			continue
		}
		wantProtocolErrorText(t, err, "too large")
	}
}
