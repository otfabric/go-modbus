// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// This file drives the server's request pipeline (dispatch, exception
// mapping, metrics, link handling) through an in-memory transport, and covers
// the listener/lifecycle edge cases that need injected failures.

// fnHandler is a RequestHandler whose behaviour is supplied per test. It
// implements every optional handler interface; unset functions answer with
// ErrIllegalFunction.
type fnHandler struct {
	coils     func(*CoilsRequest) ([]bool, error)
	inputs    func(*DiscreteInputsRequest) ([]bool, error)
	holding   func(*HoldingRegistersRequest) ([]uint16, error)
	inputRegs func(*InputRegistersRequest) ([]uint16, error)
	readWrite func(*ReadWriteRegistersRequest) ([]uint16, error)
	excStatus func(*ExceptionStatusRequest) (uint8, error)
	evCounter func(*CommEventCounterRequest) (*CommEventCounterResponse, error)
	evLog     func(*CommEventLogRequest) (*CommEventLogResponse, error)
	devID     func(*DeviceIdentificationRequest) (*DeviceIdentificationResponse, error)
}

func (h *fnHandler) HandleCoils(_ context.Context, req *CoilsRequest) ([]bool, error) {
	if h.coils == nil {
		return nil, ErrIllegalFunction
	}
	return h.coils(req)
}

func (h *fnHandler) HandleDiscreteInputs(_ context.Context, req *DiscreteInputsRequest) ([]bool, error) {
	if h.inputs == nil {
		return nil, ErrIllegalFunction
	}
	return h.inputs(req)
}

func (h *fnHandler) HandleHoldingRegisters(_ context.Context, req *HoldingRegistersRequest) ([]uint16, error) {
	if h.holding == nil {
		return nil, ErrIllegalFunction
	}
	return h.holding(req)
}

func (h *fnHandler) HandleInputRegisters(_ context.Context, req *InputRegistersRequest) ([]uint16, error) {
	if h.inputRegs == nil {
		return nil, ErrIllegalFunction
	}
	return h.inputRegs(req)
}

func (h *fnHandler) HandleReadWriteRegisters(_ context.Context, req *ReadWriteRegistersRequest) ([]uint16, error) {
	if h.readWrite == nil {
		return nil, ErrIllegalFunction
	}
	return h.readWrite(req)
}

func (h *fnHandler) HandleExceptionStatus(_ context.Context, req *ExceptionStatusRequest) (uint8, error) {
	if h.excStatus == nil {
		return 0, ErrIllegalFunction
	}
	return h.excStatus(req)
}

func (h *fnHandler) HandleCommEventCounter(_ context.Context, req *CommEventCounterRequest) (*CommEventCounterResponse, error) {
	if h.evCounter == nil {
		return nil, ErrIllegalFunction
	}
	return h.evCounter(req)
}

func (h *fnHandler) HandleCommEventLog(_ context.Context, req *CommEventLogRequest) (*CommEventLogResponse, error) {
	if h.evLog == nil {
		return nil, ErrIllegalFunction
	}
	return h.evLog(req)
}

func (h *fnHandler) HandleDeviceIdentification(_ context.Context, req *DeviceIdentificationRequest) (*DeviceIdentificationResponse, error) {
	if h.devID == nil {
		return nil, ErrIllegalFunction
	}
	return h.devID(req)
}

// memServerTransport feeds scripted requests to Server.handleTransport and
// records what the server writes back.
type memServerTransport struct {
	reqs      []*adu.Request
	next      int
	responses []*adu.Response
	writeErr  error
	closed    bool
}

func (m *memServerTransport) ReadRequest() (*adu.Request, uint16, error) {
	if m.closed {
		return nil, 0, net.ErrClosed
	}
	if m.next >= len(m.reqs) {
		return nil, 0, io.EOF
	}
	req := m.reqs[m.next]
	m.next++
	return req, uint16(0x0100 + m.next), nil
}

func (m *memServerTransport) WriteResponse(res *adu.Response) error {
	m.responses = append(m.responses, res)
	return m.writeErr
}

func (m *memServerTransport) ExecuteRequest(context.Context, *adu.Request) (*adu.Response, error) {
	return nil, errors.New("not a client transport")
}

func (m *memServerTransport) Close() error {
	m.closed = true
	return nil
}

// serveOne runs a single request through a server built around handler and
// returns the transport (with the recorded response, if any) and the metrics.
func serveOne(t *testing.T, handler RequestHandler, fc FunctionCode, payload ...byte) (*memServerTransport, *eventMetrics) {
	t.Helper()
	m := &eventMetrics{}
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Metrics: m}, handler)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	tr := &memServerTransport{reqs: []*adu.Request{{UnitID: 0x2A, FunctionCode: byte(fc), Payload: payload}}}
	ms.handleTransport(context.Background(), tr, "192.0.2.7:4242", "operator")
	return tr, m
}

func u16(vals ...uint16) []byte { return uint16sToBytes(BigEndian, vals) }

func TestServerDispatch_ExceptionsAndProtocolErrors(t *testing.T) {
	busy := func(*ReadWriteRegistersRequest) ([]uint16, error) { return nil, ErrServerDeviceBusy }
	okRW := func(req *ReadWriteRegistersRequest) ([]uint16, error) { return make([]uint16, req.ReadQty), nil }

	cases := []struct {
		name    string
		handler RequestHandler
		fc      FunctionCode
		payload []byte
		// wantExc is the expected exception code; 0 means the link must be
		// dropped without a response (protocol error).
		wantExc ExceptionCode
		wantErr error // error reported to metrics
	}{
		{
			name:    "FC03 handler returns too few registers",
			handler: &fnHandler{holding: func(*HoldingRegistersRequest) ([]uint16, error) { return []uint16{1}, nil }},
			fc:      FCReadHoldingRegisters, payload: u16(0, 2),
			wantExc: exServerDeviceFailure, wantErr: ErrServerDeviceFailure,
		},
		{
			name:    "FC04 handler returns too many registers",
			handler: &fnHandler{inputRegs: func(*InputRegistersRequest) ([]uint16, error) { return []uint16{1, 2, 3}, nil }},
			fc:      FCReadInputRegisters, payload: u16(0, 2),
			wantExc: exServerDeviceFailure, wantErr: ErrServerDeviceFailure,
		},
		{
			name:    "FC01 handler returns too few coils",
			handler: &fnHandler{coils: func(*CoilsRequest) ([]bool, error) { return []bool{true}, nil }},
			fc:      FCReadCoils, payload: u16(0, 9),
			wantExc: exServerDeviceFailure, wantErr: ErrServerDeviceFailure,
		},
		{
			name:    "FC02 handler returns too many inputs",
			handler: &fnHandler{inputs: func(*DiscreteInputsRequest) ([]bool, error) { return make([]bool, 4), nil }},
			fc:      FCReadDiscreteInputs, payload: u16(0, 3),
			wantExc: exServerDeviceFailure, wantErr: ErrServerDeviceFailure,
		},
		{
			name:    "FC15 range runs past 0xFFFF",
			handler: &fnHandler{},
			fc:      FCWriteMultipleCoils, payload: append(u16(0xFFFF, 2), 0x01, 0x03),
			wantExc: exIllegalDataAddress, wantErr: ErrIllegalDataAddress,
		},
		{
			name:    "FC16 range runs past 0xFFFF",
			handler: &fnHandler{},
			fc:      FCWriteMultipleRegisters, payload: append(append(u16(0xFFFF, 2), 0x04), u16(1, 2)...),
			wantExc: exIllegalDataAddress, wantErr: ErrIllegalDataAddress,
		},
		{
			name:    "FC23 without ReadWriteHandler",
			handler: &noSerialFCHandler{},
			fc:      FCReadWriteMultipleRegs, payload: append(append(u16(0, 1, 0, 1), 0x02), u16(7)...),
			wantExc: exIllegalFunction, wantErr: ErrIllegalFunction,
		},
		{
			name:    "FC23 zero write quantity",
			handler: &fnHandler{readWrite: okRW},
			fc:      FCReadWriteMultipleRegs, payload: append(u16(0, 1, 0, 0), 0x00, 0x00),
			wantExc: exIllegalDataValue, wantErr: ErrIllegalDataValue,
		},
		{
			name:    "FC23 read range runs past 0xFFFF",
			handler: &fnHandler{readWrite: okRW},
			fc:      FCReadWriteMultipleRegs, payload: append(append(u16(0xFFFF, 2, 0, 1), 0x02), u16(7)...),
			wantExc: exIllegalDataAddress, wantErr: ErrIllegalDataAddress,
		},
		{
			name:    "FC23 write range runs past 0xFFFF",
			handler: &fnHandler{readWrite: okRW},
			fc:      FCReadWriteMultipleRegs, payload: append(append(u16(0, 1, 0xFFFF, 2), 0x04), u16(7, 8)...),
			wantExc: exIllegalDataAddress, wantErr: ErrIllegalDataAddress,
		},
		{
			name:    "FC23 data longer than its byte count",
			handler: &fnHandler{readWrite: okRW},
			fc:      FCReadWriteMultipleRegs, payload: append(append(u16(0, 1, 0, 1), 0x02), 0x00, 0x07, 0xFF),
			wantErr: ErrProtocolError,
		},
		{
			name:    "FC23 byte count disagrees with write quantity",
			handler: &fnHandler{readWrite: okRW},
			fc:      FCReadWriteMultipleRegs, payload: append(append(u16(0, 1, 0, 1), 0x04), u16(7, 8)...),
			wantErr: ErrProtocolError,
		},
		{
			name:    "FC23 handler error",
			handler: &fnHandler{readWrite: busy},
			fc:      FCReadWriteMultipleRegs, payload: append(append(u16(0, 1, 0, 1), 0x02), u16(7)...),
			wantExc: exServerDeviceBusy, wantErr: ErrServerDeviceBusy,
		},
		{
			name:    "FC23 handler returns wrong register count",
			handler: &fnHandler{readWrite: func(*ReadWriteRegistersRequest) ([]uint16, error) { return []uint16{1}, nil }},
			fc:      FCReadWriteMultipleRegs, payload: append(append(u16(0, 2, 0, 1), 0x02), u16(7)...),
			wantExc: exServerDeviceFailure, wantErr: ErrServerDeviceFailure,
		},
		{
			name:    "FC07 handler error",
			handler: &fnHandler{excStatus: func(*ExceptionStatusRequest) (uint8, error) { return 0, ErrServerDeviceBusy }},
			fc:      FCReadExceptionStatus,
			wantExc: exServerDeviceBusy, wantErr: ErrServerDeviceBusy,
		},
		{
			name: "FC0B handler error",
			handler: &fnHandler{evCounter: func(*CommEventCounterRequest) (*CommEventCounterResponse, error) {
				return nil, ErrServerDeviceFailure
			}},
			fc:      FCGetCommEventCounters,
			wantExc: exServerDeviceFailure, wantErr: ErrServerDeviceFailure,
		},
		{
			name: "FC0C handler error",
			handler: &fnHandler{evLog: func(*CommEventLogRequest) (*CommEventLogResponse, error) {
				return nil, ErrIllegalDataValue
			}},
			fc:      FCGetCommEventLog,
			wantExc: exIllegalDataValue, wantErr: ErrIllegalDataValue,
		},
		{
			name: "FC43 handler error",
			handler: &fnHandler{devID: func(*DeviceIdentificationRequest) (*DeviceIdentificationResponse, error) {
				return nil, ErrIllegalDataAddress
			}},
			fc: FCEncapsulatedInterface, payload: []byte{0x0E, 0x01, 0x00},
			wantExc: exIllegalDataAddress, wantErr: ErrIllegalDataAddress,
		},
		{
			name: "FC43 handler returns nil response",
			handler: &fnHandler{devID: func(*DeviceIdentificationRequest) (*DeviceIdentificationResponse, error) {
				return nil, nil
			}},
			fc: FCEncapsulatedInterface, payload: []byte{0x0E, 0x01, 0x00},
			wantExc: exServerDeviceFailure, wantErr: ErrServerDeviceFailure,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, m := serveOne(t, tc.handler, tc.fc, tc.payload...)

			if tc.wantExc == 0 {
				if len(tr.responses) != 0 {
					t.Fatalf("server answered a malformed request: %+v", tr.responses[0])
				}
				if !tr.closed {
					t.Fatal("link was not closed after a protocol error")
				}
			} else {
				if tr.closed {
					t.Fatal("link was closed for a request that only warrants an exception")
				}
				want := &adu.Response{
					UnitID:        0x2A,
					FunctionCode:  byte(tc.fc) | 0x80,
					Payload:       []byte{byte(tc.wantExc)},
					TransactionID: 0x0101,
				}
				if len(tr.responses) != 1 || !reflect.DeepEqual(tr.responses[0], want) {
					t.Fatalf("responses = %+v, want one %+v", tr.responses, want)
				}
			}
			checkEvents(t, m, []metricEvent{
				{kind: "request", unit: 0x2A, fc: tc.fc},
				{kind: "error", unit: 0x2A, fc: tc.fc},
			}, tc.wantErr)
		})
	}
}

// Optional function codes the handler does not implement are answered with
// Illegal Function rather than being silently dropped, and are reported to the
// metrics as errors, exactly like an unimplemented FC23.
func TestServerDispatch_OptionalHandlersNotImplemented(t *testing.T) {
	requests := map[FunctionCode][]byte{
		FCReadExceptionStatus:   nil,
		FCGetCommEventCounters:  nil,
		FCGetCommEventLog:       nil,
		FCEncapsulatedInterface: {byte(MEIReadDeviceIdentification), byte(DeviceIDBasic), 0x00},
		FCReadWriteMultipleRegs: append(append(u16(0, 1, 0, 1), 0x02), u16(7)...),
	}
	for fc, payload := range requests {
		tr, m := serveOne(t, &noSerialFCHandler{}, fc, payload...)
		checkEvents(t, m, []metricEvent{
			{kind: "request", unit: 0x2A, fc: fc},
			{kind: "error", unit: 0x2A, fc: fc},
		}, ErrIllegalFunction)
		want := &adu.Response{UnitID: 0x2A, FunctionCode: byte(fc) | 0x80, Payload: []byte{byte(exIllegalFunction)}, TransactionID: 0x0101}
		if len(tr.responses) != 1 || !reflect.DeepEqual(tr.responses[0], want) {
			t.Errorf("fc 0x%02X: responses = %+v, want one %+v", uint8(fc), tr.responses, want)
		}
		if tr.closed {
			t.Errorf("fc 0x%02X: link closed", uint8(fc))
		}
	}
}

func TestServerDispatch_FC23Success(t *testing.T) {
	var got *ReadWriteRegistersRequest
	h := &fnHandler{readWrite: func(req *ReadWriteRegistersRequest) ([]uint16, error) {
		got = req
		return []uint16{0xAAAA, 0xBBBB}, nil
	}}
	tr, m := serveOne(t, h, FCReadWriteMultipleRegs, append(append(u16(0x0010, 2, 0x0020, 3), 0x06), u16(7, 8, 9)...)...)

	wantReq := &ReadWriteRegistersRequest{
		ClientAddr:   "192.0.2.7:4242",
		ClientRole:   "operator",
		UnitID:       0x2A,
		FunctionCode: FCReadWriteMultipleRegs,
		ReadAddr:     0x0010,
		ReadQty:      2,
		WriteAddr:    0x0020,
		WriteValues:  []uint16{7, 8, 9},
	}
	if !reflect.DeepEqual(got, wantReq) {
		t.Fatalf("handler saw %+v, want %+v", got, wantReq)
	}
	wantRes := &adu.Response{UnitID: 0x2A, FunctionCode: 0x17, Payload: []byte{0x04, 0xAA, 0xAA, 0xBB, 0xBB}, TransactionID: 0x0101}
	if len(tr.responses) != 1 || !reflect.DeepEqual(tr.responses[0], wantRes) {
		t.Fatalf("responses = %+v, want one %+v", tr.responses, wantRes)
	}
	checkEvents(t, m, []metricEvent{
		{kind: "request", unit: 0x2A, fc: FCReadWriteMultipleRegs},
		{kind: "response", unit: 0x2A, fc: FCReadWriteMultipleRegs},
	}, nil)
}

func TestServerDispatch_DeviceIdentificationStreams(t *testing.T) {
	objects := []DeviceIdentificationObject{
		{ID: 0x00, Value: "ACME"},
		{ID: 0x01, Value: "PLC"},
		{ID: 0x02, Value: "1.0"},
		{ID: 0x05, Value: "M5"},
		{ID: 0x80, Value: "ext"},
	}
	h := &fnHandler{devID: func(*DeviceIdentificationRequest) (*DeviceIdentificationResponse, error) {
		return &DeviceIdentificationResponse{ConformityLevel: 0x83, Objects: objects}, nil
	}}
	// encode builds the expected object list bytes.
	encode := func(objs ...DeviceIdentificationObject) []byte {
		var b []byte
		for _, o := range objs {
			b = append(b, byte(o.ID), byte(len(o.Value)))
			b = append(b, o.Value...)
		}
		return b
	}

	t.Run("unknown start object restarts the stream at object 0", func(t *testing.T) {
		tr, _ := serveOne(t, h, FCEncapsulatedInterface, 0x0E, byte(DeviceIDBasic), 0x55)
		want := append([]byte{0x0E, 0x01, 0x83, 0x00, 0x00, 0x03}, encode(objects[:3]...)...)
		if len(tr.responses) != 1 || !bytes.Equal(tr.responses[0].Payload, want) {
			t.Fatalf("responses = %+v, want payload % X", tr.responses, want)
		}
	})
	t.Run("known start object resumes there", func(t *testing.T) {
		tr, _ := serveOne(t, h, FCEncapsulatedInterface, 0x0E, byte(DeviceIDBasic), 0x01)
		want := append([]byte{0x0E, 0x01, 0x83, 0x00, 0x00, 0x02}, encode(objects[1:3]...)...)
		if len(tr.responses) != 1 || !bytes.Equal(tr.responses[0].Payload, want) {
			t.Fatalf("responses = %+v, want payload % X", tr.responses, want)
		}
	})
	t.Run("regular stream hides extended objects", func(t *testing.T) {
		tr, _ := serveOne(t, h, FCEncapsulatedInterface, 0x0E, byte(DeviceIDRegular), 0x00)
		want := append([]byte{0x0E, 0x02, 0x83, 0x00, 0x00, 0x04}, encode(objects[:4]...)...)
		if len(tr.responses) != 1 || !bytes.Equal(tr.responses[0].Payload, want) {
			t.Fatalf("responses = %+v, want payload % X", tr.responses, want)
		}
	})
}

// A failed write must not stop the server from reading the next request on
// the same link.
func TestServerHandleTransport_ContinuesAfterWriteError(t *testing.T) {
	var calls int
	h := &fnHandler{holding: func(req *HoldingRegistersRequest) ([]uint16, error) {
		calls++
		return make([]uint16, req.Quantity), nil
	}}
	m := &eventMetrics{}
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Metrics: m}, h)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	tr := &memServerTransport{
		reqs: []*adu.Request{
			{UnitID: 1, FunctionCode: 0x03, Payload: u16(0, 1)},
			{UnitID: 1, FunctionCode: 0x03, Payload: u16(4, 2)},
		},
		writeErr: errors.New("peer went away"),
	}
	ms.handleTransport(context.Background(), tr, "client", "")

	if calls != 2 || len(tr.responses) != 2 {
		t.Fatalf("handler calls = %d, write attempts = %d, want 2 and 2", calls, len(tr.responses))
	}
	if tr.responses[0].TransactionID != 0x0101 || tr.responses[1].TransactionID != 0x0102 {
		t.Fatalf("responses carry transaction ids %#x, %#x", tr.responses[0].TransactionID, tr.responses[1].TransactionID)
	}
	if got := len(m.snapshot()); got != 4 {
		t.Fatalf("%d metrics events, want 4", got)
	}
}

// flakyListener fails Accept with the scripted errors, then reports closure.
type flakyListener struct {
	errs  []error
	calls int
}

func (l *flakyListener) Accept() (net.Conn, error) {
	l.calls++
	if len(l.errs) > 0 {
		err := l.errs[0]
		l.errs = l.errs[1:]
		return nil, err
	}
	return nil, net.ErrClosed
}

func (l *flakyListener) Close() error   { return nil }
func (l *flakyListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestServerAccept_SurvivesTransientErrors(t *testing.T) {
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0"}, &fnHandler{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ln := &flakyListener{errs: []error{errors.New("too many open files"), errors.New("connection aborted")}}
	ms.tcpListener = ln

	done := make(chan struct{})
	go func() {
		ms.acceptTCPClients()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("accept loop did not stop when the listener was closed")
	}
	if ln.calls != 3 {
		t.Fatalf("Accept called %d times, want 3 (two transient failures, then closed)", ln.calls)
	}
}

func TestServerHandleTCPClient_UnknownTransportClosesConnection(t *testing.T) {
	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0"}, &fnHandler{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ms.transportType = transportType(0xEE)
	ms.stopCtx = context.Background()

	c1, c2 := net.Pipe()
	defer func() { _ = c2.Close() }()
	ms.tcpClients = []net.Conn{c1}
	ms.wg.Add(1)
	ms.handleTCPClient(c1)

	if len(ms.tcpClients) != 0 {
		t.Fatalf("client still tracked: %d", len(ms.tcpClients))
	}
	_ = c2.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c2.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("connection not closed by the server: read returned %v", err)
	}
}

func TestServerStartTLS_DeadlineErrorOnClosedSocket(t *testing.T) {
	ms := &Server{
		conf:   ServerConfig{TLSHandshakeTimeout: time.Second},
		logger: newLogger("test-server-starttls", nil),
	}
	c1, c2 := net.Pipe()
	_ = c1.Close()
	_ = c2.Close()
	tlsSock, role, err := ms.startTLS(c1)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("want io.ErrClosedPipe, got %v", err)
	}
	if tlsSock != nil || role != "" {
		t.Fatalf("got (%v, %q) alongside the error", tlsSock, role)
	}
}

func TestServerExtractRole_MalformedExtension(t *testing.T) {
	ms := &Server{logger: newLogger("test-server-role-malformed", nil)}
	cases := map[string][]byte{
		"truncated UTF8String":  {0x0c, 0x05, 'a'},
		"not a UTF8String":      {0x13, 0x02, 'o', 'p'},
		"too short to be a TLV": {0x0c},
	}
	for name, value := range cases {
		cert := &x509.Certificate{Extensions: []pkix.Extension{{Id: modbusRoleOID, Value: value}}}
		if role := ms.extractRole(cert); role != "" {
			t.Errorf("%s: role = %q, want empty", name, role)
		}
	}
	// Sanity check: a well-formed extension is accepted.
	cert := &x509.Certificate{Extensions: []pkix.Extension{{Id: modbusRoleOID, Value: []byte{0x0c, 0x02, 'o', 'p'}}}}
	if role := ms.extractRole(cert); role != "op" {
		t.Errorf("well-formed extension: role = %q, want %q", role, "op")
	}
}

func TestServerStart_LifecycleEdgeCases(t *testing.T) {
	t.Run("start twice", func(t *testing.T) {
		ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0"}, &fnHandler{})
		if err != nil {
			t.Fatalf("NewServer: %v", err)
		}
		if err := ms.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		ln := ms.tcpListener
		if err := ms.Start(); err != nil {
			t.Fatalf("second Start: %v", err)
		}
		if ms.tcpListener != ln {
			t.Fatal("second Start replaced the listener")
		}
		if err := ms.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		// Stopping an already stopped server is a no-op.
		if err := ms.Stop(); err != nil {
			t.Fatalf("second Stop: %v", err)
		}
		if _, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second); err == nil {
			t.Fatal("listener still accepting after Stop")
		}
	})
	t.Run("stop before start", func(t *testing.T) {
		ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0"}, &fnHandler{})
		if err != nil {
			t.Fatalf("NewServer: %v", err)
		}
		if err := ms.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown before Start: %v", err)
		}
	})
	t.Run("unknown transport type", func(t *testing.T) {
		ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0"}, &fnHandler{})
		if err != nil {
			t.Fatalf("NewServer: %v", err)
		}
		ms.transportType = transportType(0xEE)
		if err := ms.Start(); !errors.Is(err, ErrConfigurationError) {
			t.Fatalf("Start: want ErrConfigurationError, got %v", err)
		}
		if ms.started {
			t.Fatal("server marked started after a failed Start")
		}
		if err := ms.stopCtx.Err(); !errors.Is(err, context.Canceled) {
			t.Fatalf("stop context not cancelled after failed Start: %v", err)
		}
	})
}

// Shutdown gives up waiting when its context expires while a handler is
// still running, and the handler's context is cancelled.
func TestServerShutdown_ContextExpiresWhileHandlerRuns(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	handlerCtxErr := make(chan error, 1)
	h := &blockingHandler{fn: func(ctx context.Context) {
		once.Do(func() { close(entered) })
		<-release
		handlerCtxErr <- ctx.Err()
	}}

	ms, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0", Timeout: 5 * time.Second}, h)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := ms.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	conn, err := net.Dial("tcp", ms.tcpListener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write(rawRequestFrame(1, 1, byte(FCReadHoldingRegisters), u16(0, 1))); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler was never invoked")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := ms.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown: want context.DeadlineExceeded, got %v", err)
	}

	close(release)
	if err := <-handlerCtxErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("handler context after Shutdown: %v, want context.Canceled", err)
	}
	// With the handler released, the server's goroutines wind down.
	done := make(chan struct{})
	go func() {
		ms.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler goroutines did not exit after Shutdown")
	}
}

// blockingHandler calls fn from HandleHoldingRegisters.
type blockingHandler struct {
	noSerialFCHandler
	fn func(ctx context.Context)
}

func (h *blockingHandler) HandleHoldingRegisters(ctx context.Context, req *HoldingRegistersRequest) ([]uint16, error) {
	h.fn(ctx)
	return make([]uint16, req.Quantity), nil
}
