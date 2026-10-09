// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	intsession "github.com/otfabric/go-modbus/internal/session"
)

// This file drives every public Client call through an in-memory scripted
// transport. Unlike the socket-based mock server it can inject arbitrary
// transport errors deterministically and records the exact PDUs the client
// emits, which makes it suitable for table tests across the whole API.

// scriptFn answers one request PDU.
type scriptFn func(req *adu.Request) (*adu.Response, error)

// scriptedTransport is a session transport backed by a scriptFn.
type scriptedTransport struct {
	mu   sync.Mutex
	fn   scriptFn
	reqs []adu.Request
}

func (s *scriptedTransport) ExecuteRequest(_ context.Context, req *adu.Request) (*adu.Response, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, adu.Request{
		UnitID:       req.UnitID,
		FunctionCode: req.FunctionCode,
		Payload:      append([]byte(nil), req.Payload...),
	})
	fn := s.fn
	s.mu.Unlock()
	return fn(req)
}

func (s *scriptedTransport) Close() error { return nil }

func (s *scriptedTransport) requests() []adu.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]adu.Request(nil), s.reqs...)
}

// metricEvent is one ClientMetrics callback.
type metricEvent struct {
	kind string // "request", "response", "error" or "timeout"
	unit uint8
	fc   FunctionCode
	err  error
}

func (e metricEvent) String() string {
	return fmt.Sprintf("%s(unit=%d fc=0x%02X err=%v)", e.kind, e.unit, uint8(e.fc), e.err)
}

// eventMetrics records ClientMetrics callbacks in order.
type eventMetrics struct {
	mu     sync.Mutex
	events []metricEvent
}

func (m *eventMetrics) add(e metricEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
}

func (m *eventMetrics) OnRequest(unitID uint8, fc FunctionCode) {
	m.add(metricEvent{kind: "request", unit: unitID, fc: fc})
}

func (m *eventMetrics) OnResponse(unitID uint8, fc FunctionCode, _ time.Duration) {
	m.add(metricEvent{kind: "response", unit: unitID, fc: fc})
}

func (m *eventMetrics) OnError(unitID uint8, fc FunctionCode, _ time.Duration, err error) {
	m.add(metricEvent{kind: "error", unit: unitID, fc: fc, err: err})
}

func (m *eventMetrics) OnTimeout(unitID uint8, fc FunctionCode, _ time.Duration) {
	m.add(metricEvent{kind: "timeout", unit: unitID, fc: fc})
}

func (m *eventMetrics) snapshot() []metricEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]metricEvent(nil), m.events...)
}

// newScriptedClient returns an open client whose requests are answered by fn.
func newScriptedClient(t *testing.T, fn scriptFn) (*Client, *scriptedTransport, *eventMetrics) {
	t.Helper()
	m := &eventMetrics{}
	c, err := New(Config{URL: "tcp://scripted.invalid:502", Metrics: m})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tr := &scriptedTransport{fn: fn}
	eng := intsession.NewEngine(intsession.Config{
		Dial: func() (intsession.Transport[*adu.Request, *adu.Response], error) { return tr, nil },
	})
	if err := eng.Open(); err != nil {
		t.Fatalf("engine Open: %v", err)
	}
	c.lock.Lock()
	c.state.engine = eng
	c.lock.Unlock()
	t.Cleanup(func() { _ = c.Close() })
	return c, tr, m
}

func okRes(req *adu.Request, payload ...byte) *adu.Response {
	return &adu.Response{UnitID: req.UnitID, FunctionCode: req.FunctionCode, Payload: payload}
}

func excRes(req *adu.Request, code byte) *adu.Response {
	return &adu.Response{UnitID: req.UnitID, FunctionCode: req.FunctionCode | 0x80, Payload: []byte{code}}
}

// scriptedRegValue is the value of every register served by goodDevice.
const scriptedRegValue = 0x1234

// scriptedBit is the value of the i-th bit (relative to the request address)
// served by goodDevice.
func scriptedBit(i int) bool { return i%3 == 0 }

func scriptedBits(n int) []bool {
	out := make([]bool, n)
	for i := range out {
		out[i] = scriptedBit(i)
	}
	return out
}

func scriptedRegs(n int) []uint16 {
	out := make([]uint16, n)
	for i := range out {
		out[i] = scriptedRegValue
	}
	return out
}

// goodDevice answers every supported function code with a well-formed
// success response.
func goodDevice(req *adu.Request) (*adu.Response, error) {
	p := req.Payload
	switch FunctionCode(req.FunctionCode) {
	case FCReadCoils, FCReadDiscreteInputs:
		data := encodeBools(scriptedBits(int(bytesToUint16(BigEndian, p[2:4]))))
		return okRes(req, append([]byte{byte(len(data))}, data...)...), nil
	case FCReadHoldingRegisters, FCReadInputRegisters:
		data := uint16sToBytes(BigEndian, scriptedRegs(int(bytesToUint16(BigEndian, p[2:4]))))
		return okRes(req, append([]byte{byte(len(data))}, data...)...), nil
	case FCWriteSingleCoil, FCWriteSingleRegister, FCMaskWriteRegister, FCWriteFileRecord:
		return okRes(req, p...), nil
	case FCWriteMultipleCoils, FCWriteMultipleRegisters:
		return okRes(req, p[:4]...), nil
	case FCReadWriteMultipleRegs:
		data := uint16sToBytes(BigEndian, scriptedRegs(int(bytesToUint16(BigEndian, p[2:4]))))
		return okRes(req, append([]byte{byte(len(data))}, data...)...), nil
	case FCReadFIFOQueue:
		return okRes(req, 0x00, 0x06, 0x00, 0x02, 0x11, 0x22, 0x33, 0x44), nil
	case FCReadExceptionStatus:
		return okRes(req, 0x6D), nil
	case FCGetCommEventCounters:
		return okRes(req, 0xFF, 0xFF, 0x01, 0x08), nil
	case FCGetCommEventLog:
		return okRes(req, 0x08, 0x00, 0x00, 0x01, 0x08, 0x01, 0x21, 0x20, 0x00), nil
	case FCDiagnostics:
		return okRes(req, p[0], p[1], 0xAB, 0xCD), nil
	case FCReportServerID:
		return okRes(req, 0x03, 'I', 'D', 0xFF), nil
	case FCReadFileRecord:
		var body []byte
		for off := 1; off+7 <= len(p); off += 7 {
			n := int(bytesToUint16(BigEndian, p[off+5:off+7]))
			body = append(body, byte(1+2*n), 0x06)
			body = append(body, uint16sToBytes(BigEndian, scriptedRegs(n))...)
		}
		return okRes(req, append([]byte{byte(len(body))}, body...)...), nil
	case FCEncapsulatedInterface:
		return okRes(req, 0x0E, p[1], 0x81, 0x00, 0x00, 0x01, 0x00, 0x03, 'a', 'b', 'c'), nil
	default:
		return excRes(req, 0x01), nil
	}
}

// clientCall is one public Client method invocation with fixed arguments.
type clientCall struct {
	name string
	// fcs lists the function codes of the requests a successful call puts on
	// the wire, in order. A failing call only issues the first one.
	fcs []FunctionCode
	// call invokes the method and, on success, checks the decoded result
	// against what goodDevice serves.
	call func(ctx context.Context, c *Client, unit uint8) error
}

func wantValue(got, want any) error {
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("decoded result %#v, want %#v", got, want)
	}
	return nil
}

func allClientCalls() []clientCall {
	one := func(fc FunctionCode) []FunctionCode { return []FunctionCode{fc} }
	diag := one(FCDiagnostics)
	diagCounter := func(f func(*Client, context.Context, uint8) (uint16, error)) func(context.Context, *Client, uint8) error {
		return func(ctx context.Context, c *Client, u uint8) error {
			v, err := f(c, ctx, u)
			if err != nil {
				return err
			}
			return wantValue(v, uint16(0xABCD))
		}
	}
	devID := &DeviceIdentification{
		ConformityLevel: 0x81,
		Objects:         []DeviceIdentificationObject{{ID: 0x00, Name: "VendorName", Value: "abc"}},
	}
	devIDFor := func(cat DeviceIDCategory) *DeviceIdentification {
		d := *devID
		d.Category = cat
		return &d
	}

	return []clientCall{
		{"ReadCoil", one(FCReadCoils), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReadCoil(ctx, u, 10)
			if err != nil {
				return err
			}
			return wantValue(v, true)
		}},
		{"ReadCoils", one(FCReadCoils), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReadCoils(ctx, u, 10, 11)
			if err != nil {
				return err
			}
			return wantValue(v, scriptedBits(11))
		}},
		{"ReadDiscreteInput", one(FCReadDiscreteInputs), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReadDiscreteInput(ctx, u, 10)
			if err != nil {
				return err
			}
			return wantValue(v, true)
		}},
		{"ReadDiscreteInputs", one(FCReadDiscreteInputs), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReadDiscreteInputs(ctx, u, 10, 16)
			if err != nil {
				return err
			}
			return wantValue(v, scriptedBits(16))
		}},
		{"WriteCoil", one(FCWriteSingleCoil), func(ctx context.Context, c *Client, u uint8) error {
			return c.WriteCoil(ctx, u, 10, true)
		}},
		{"WriteCoilRaw", one(FCWriteSingleCoil), func(ctx context.Context, c *Client, u uint8) error {
			return c.WriteCoilRaw(ctx, u, 10, 0x5500)
		}},
		{"WriteCoils", one(FCWriteMultipleCoils), func(ctx context.Context, c *Client, u uint8) error {
			return c.WriteCoils(ctx, u, 10, []bool{true, false, true})
		}},
		{"ReadRegister/holding", one(FCReadHoldingRegisters), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReadRegister(ctx, u, 10, HoldingRegister)
			if err != nil {
				return err
			}
			return wantValue(v, uint16(scriptedRegValue))
		}},
		{"ReadRegisters/input", one(FCReadInputRegisters), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReadRegisters(ctx, u, 10, 3, InputRegister)
			if err != nil {
				return err
			}
			return wantValue(v, scriptedRegs(3))
		}},
		{"ReadRegisterBytes", one(FCReadHoldingRegisters), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReadRegisterBytes(ctx, u, 10, 3, HoldingRegister)
			if err != nil {
				return err
			}
			return wantValue(v, []byte{0x12, 0x34, 0x12})
		}},
		{"ReadHoldingRegister", one(FCReadHoldingRegisters), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReadHoldingRegister(ctx, u, 10)
			if err != nil {
				return err
			}
			return wantValue(v, uint16(scriptedRegValue))
		}},
		{"ReadHoldingRegisters", one(FCReadHoldingRegisters), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReadHoldingRegisters(ctx, u, 10, 2)
			if err != nil {
				return err
			}
			return wantValue(v, scriptedRegs(2))
		}},
		{"ReadInputRegister", one(FCReadInputRegisters), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReadInputRegister(ctx, u, 10)
			if err != nil {
				return err
			}
			return wantValue(v, uint16(scriptedRegValue))
		}},
		{"ReadInputRegisters", one(FCReadInputRegisters), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReadInputRegisters(ctx, u, 10, 2)
			if err != nil {
				return err
			}
			return wantValue(v, scriptedRegs(2))
		}},
		{"ReadRegisterBit", one(FCReadInputRegisters), func(ctx context.Context, c *Client, u uint8) error {
			// 0x1234: bit 2 is set, bit 0 is clear.
			v, err := c.ReadRegisterBit(ctx, u, 10, 2, InputRegister)
			if err != nil {
				return err
			}
			return wantValue(v, true)
		}},
		{"ReadRegisterBits", one(FCReadHoldingRegisters), func(ctx context.Context, c *Client, u uint8) error {
			// 0x1234 bits 2..5 are 1,0,1,1.
			v, err := c.ReadRegisterBits(ctx, u, 10, 2, 4, HoldingRegister)
			if err != nil {
				return err
			}
			return wantValue(v, []bool{true, false, true, true})
		}},
		{"WriteRegisterBit", []FunctionCode{FCReadHoldingRegisters, FCWriteMultipleRegisters}, func(ctx context.Context, c *Client, u uint8) error {
			return c.WriteRegisterBit(ctx, u, 10, 0, true)
		}},
		{"UpdateRegisterMask", []FunctionCode{FCReadHoldingRegisters, FCWriteMultipleRegisters}, func(ctx context.Context, c *Client, u uint8) error {
			return c.UpdateRegisterMask(ctx, u, 10, 0x00F0, 0xFFFF)
		}},
		{"MaskWriteRegister", one(FCMaskWriteRegister), func(ctx context.Context, c *Client, u uint8) error {
			return c.MaskWriteRegister(ctx, u, 10, 0x00F2, 0x0025)
		}},
		{"WriteRegister", one(FCWriteSingleRegister), func(ctx context.Context, c *Client, u uint8) error {
			return c.WriteRegister(ctx, u, 10, 0xBEEF)
		}},
		{"WriteRegisters", one(FCWriteMultipleRegisters), func(ctx context.Context, c *Client, u uint8) error {
			return c.WriteRegisters(ctx, u, 10, []uint16{1, 2, 3})
		}},
		{"WriteRegisterBytes", one(FCWriteMultipleRegisters), func(ctx context.Context, c *Client, u uint8) error {
			return c.WriteRegisterBytes(ctx, u, 10, []byte{1, 2, 3})
		}},
		{"ReadWriteMultipleRegisters", one(FCReadWriteMultipleRegs), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReadWriteMultipleRegisters(ctx, u, 10, 2, 20, []uint16{7, 8, 9})
			if err != nil {
				return err
			}
			return wantValue(v, scriptedRegs(2))
		}},
		{"ReadFIFOQueue", one(FCReadFIFOQueue), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReadFIFOQueue(ctx, u, 10)
			if err != nil {
				return err
			}
			return wantValue(v, []uint16{0x1122, 0x3344})
		}},
		{"ReadFileRecords", one(FCReadFileRecord), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReadFileRecords(ctx, u, []FileRecordRequest{
				{FileNumber: 4, RecordNumber: 1, RecordLength: 2},
				{FileNumber: 3, RecordNumber: 9, RecordLength: 1},
			})
			if err != nil {
				return err
			}
			return wantValue(v, [][]uint16{scriptedRegs(2), scriptedRegs(1)})
		}},
		{"WriteFileRecords", one(FCWriteFileRecord), func(ctx context.Context, c *Client, u uint8) error {
			return c.WriteFileRecords(ctx, u, []FileRecord{{FileNumber: 4, RecordNumber: 7, Data: []uint16{0x06AF, 0x04BE}}})
		}},
		{"ReadExceptionStatus", one(FCReadExceptionStatus), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReadExceptionStatus(ctx, u)
			if err != nil {
				return err
			}
			return wantValue(v, uint8(0x6D))
		}},
		{"GetCommEventCounter", one(FCGetCommEventCounters), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.GetCommEventCounter(ctx, u)
			if err != nil {
				return err
			}
			return wantValue(v, &CommEventCounterResponse{Status: 0xFFFF, EventCount: 0x0108})
		}},
		{"GetCommEventLog", one(FCGetCommEventLog), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.GetCommEventLog(ctx, u)
			if err != nil {
				return err
			}
			return wantValue(v, &CommEventLogResponse{Status: 0, EventCount: 0x0108, MessageCount: 0x0121, Events: []byte{0x20, 0x00}})
		}},
		{"ReportServerID", one(FCReportServerID), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReportServerID(ctx, u)
			if err != nil {
				return err
			}
			running := true
			return wantValue(v, &ReportServerIDResponse{Data: []byte{'I', 'D', 0xFF}, RunIndicatorStatus: &running})
		}},
		{"Diagnostics", diag, func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.Diagnostics(ctx, u, DiagRestartCommunications, []byte{0xFF, 0x00})
			if err != nil {
				return err
			}
			return wantValue(v, &DiagnosticResponse{SubFunction: DiagRestartCommunications, Data: []byte{0xAB, 0xCD}})
		}},
		{"DiagnosticLoopback", diag, func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.DiagnosticLoopback(ctx, u, 0xABCD)
			if err != nil {
				return err
			}
			return wantValue(v, uint16(0xABCD))
		}},
		{"DiagnosticRegister", diag, diagCounter((*Client).DiagnosticRegister)},
		{"BusMessageCount", diag, diagCounter((*Client).BusMessageCount)},
		{"DiagnosticBusCommunicationErrorCount", diag, diagCounter((*Client).DiagnosticBusCommunicationErrorCount)},
		{"DiagnosticBusExceptionErrorCount", diag, diagCounter((*Client).DiagnosticBusExceptionErrorCount)},
		{"DiagnosticServerMessageCount", diag, diagCounter((*Client).DiagnosticServerMessageCount)},
		{"DiagnosticServerNoResponseCount", diag, diagCounter((*Client).DiagnosticServerNoResponseCount)},
		{"DiagnosticServerNAKCount", diag, diagCounter((*Client).DiagnosticServerNAKCount)},
		{"DiagnosticServerBusyCount", diag, diagCounter((*Client).DiagnosticServerBusyCount)},
		{"DiagnosticBusCharacterOverrunCount", diag, diagCounter((*Client).DiagnosticBusCharacterOverrunCount)},
		{"DiagnosticForceListenOnlyMode", diag, func(ctx context.Context, c *Client, u uint8) error {
			return c.DiagnosticForceListenOnlyMode(ctx, u)
		}},
		{"DiagnosticClearCounters", diag, func(ctx context.Context, c *Client, u uint8) error {
			return c.DiagnosticClearCounters(ctx, u)
		}},
		{"DiagnosticClearOverrunCounterAndFlag", diag, func(ctx context.Context, c *Client, u uint8) error {
			return c.DiagnosticClearOverrunCounterAndFlag(ctx, u)
		}},
		{"ReadDeviceIdentification/basic", one(FCEncapsulatedInterface), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReadDeviceIdentification(ctx, u, DeviceIDBasic, 0)
			if err != nil {
				return err
			}
			return wantValue(v, devIDFor(DeviceIDBasic))
		}},
		{"ReadDeviceIdentification/individual", one(FCEncapsulatedInterface), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReadDeviceIdentification(ctx, u, DeviceIDIndividual, 0)
			if err != nil {
				return err
			}
			return wantValue(v, devIDFor(DeviceIDIndividual))
		}},
		{"ReadAllDeviceIdentification", one(FCEncapsulatedInterface), func(ctx context.Context, c *Client, u uint8) error {
			v, err := c.ReadAllDeviceIdentification(ctx, u)
			if err != nil {
				return err
			}
			return wantValue(v, devIDFor(DeviceIDExtended))
		}},
	}
}

func checkEvents(t *testing.T, m *eventMetrics, want []metricEvent, wantErr error) {
	t.Helper()
	got := m.snapshot()
	if len(got) != len(want) {
		t.Fatalf("metrics events = %v, want %v", got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.kind != w.kind || g.unit != w.unit || g.fc != w.fc {
			t.Fatalf("metrics event %d = %v, want %v (all: %v)", i, g, w, got)
		}
		if w.kind == "error" && wantErr != nil && !errors.Is(g.err, wantErr) {
			t.Fatalf("metrics event %d carries error %v, want %v", i, g.err, wantErr)
		}
	}
}

// TestClientCalls_Success checks, for every call, the decoded result and that
// each request on the wire is bracketed by exactly one OnRequest/OnResponse.
func TestClientCalls_Success(t *testing.T) {
	const unit = 0x11
	for _, cc := range allClientCalls() {
		t.Run(cc.name, func(t *testing.T) {
			c, tr, m := newScriptedClient(t, goodDevice)
			if err := cc.call(context.Background(), c, unit); err != nil {
				t.Fatalf("call failed: %v", err)
			}
			reqs := tr.requests()
			if len(reqs) != len(cc.fcs) {
				t.Fatalf("%d requests on the wire, want %d", len(reqs), len(cc.fcs))
			}
			var want []metricEvent
			for i, fc := range cc.fcs {
				if reqs[i].UnitID != unit || FunctionCode(reqs[i].FunctionCode) != fc {
					t.Errorf("request %d: unit=%d fc=0x%02X, want unit=%d fc=0x%02X",
						i, reqs[i].UnitID, reqs[i].FunctionCode, unit, uint8(fc))
				}
				want = append(want, metricEvent{kind: "request", unit: unit, fc: fc}, metricEvent{kind: "response", unit: unit, fc: fc})
			}
			checkEvents(t, m, want, nil)
		})
	}
}

// TestClientCalls_Failures runs every call against a set of misbehaving
// devices and transports. Each failure must surface as the right error, stop
// after the first request, and be reported to metrics exactly once.
func TestClientCalls_Failures(t *testing.T) {
	const unit = 0x11
	linkErr := errors.New("link down")

	modes := []struct {
		name    string
		fn      scriptFn
		wantErr error
		kind    string // expected outcome metric
		check   func(t *testing.T, err error, fc FunctionCode)
	}{
		{
			name:    "exception",
			fn:      func(req *adu.Request) (*adu.Response, error) { return excRes(req, 0x02), nil },
			wantErr: ErrIllegalDataAddress,
			kind:    "error",
			check: func(t *testing.T, err error, fc FunctionCode) {
				var exc *ExceptionError
				if !errors.As(err, &exc) {
					t.Fatalf("error %v is not an *ExceptionError", err)
				}
				if exc.ExceptionCode != 0x02 || exc.FunctionCode != fc {
					t.Fatalf("ExceptionError = %+v, want code 0x02 for fc 0x%02X", exc, uint8(fc))
				}
			},
		},
		{
			name:    "transport error",
			fn:      func(*adu.Request) (*adu.Response, error) { return nil, linkErr },
			wantErr: linkErr,
			kind:    "error",
		},
		{
			name:    "timeout",
			fn:      func(*adu.Request) (*adu.Response, error) { return nil, os.ErrDeadlineExceeded },
			wantErr: ErrRequestTimedOut,
			kind:    "timeout",
		},
		{
			name: "response from another unit",
			fn: func(req *adu.Request) (*adu.Response, error) {
				res, err := goodDevice(req)
				res.UnitID++
				return res, err
			},
			wantErr: ErrBadUnitID,
			kind:    "error",
		},
		{
			name: "response for another function code",
			fn: func(req *adu.Request) (*adu.Response, error) {
				res, err := goodDevice(req)
				res.FunctionCode = 0x41
				return res, err
			},
			wantErr: ErrProtocolError,
			kind:    "error",
		},
		{
			name: "malformed exception",
			fn: func(req *adu.Request) (*adu.Response, error) {
				res := excRes(req, 0x02)
				res.Payload = append(res.Payload, 0x00)
				return res, nil
			},
			wantErr: ErrProtocolError,
			kind:    "error",
		},
		{
			name:    "empty success payload",
			fn:      func(req *adu.Request) (*adu.Response, error) { return okRes(req), nil },
			wantErr: ErrProtocolError,
			kind:    "error",
		},
	}

	for _, cc := range allClientCalls() {
		fc := cc.fcs[0]
		t.Run(cc.name, func(t *testing.T) {
			t.Run("client not open", func(t *testing.T) {
				m := &eventMetrics{}
				c, err := New(Config{URL: "tcp://scripted.invalid:502", Metrics: m})
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				if err := cc.call(context.Background(), c, unit); !errors.Is(err, ErrClientNotOpen) {
					t.Fatalf("want ErrClientNotOpen, got %v", err)
				}
				checkEvents(t, m, []metricEvent{{kind: "request", unit: unit, fc: fc}, {kind: "error", unit: unit, fc: fc}}, ErrClientNotOpen)
			})
			for _, mode := range modes {
				t.Run(mode.name, func(t *testing.T) {
					c, tr, m := newScriptedClient(t, mode.fn)
					err := cc.call(context.Background(), c, unit)
					if !errors.Is(err, mode.wantErr) {
						t.Fatalf("want %v, got %v", mode.wantErr, err)
					}
					if mode.check != nil {
						mode.check(t, err, fc)
					}
					if n := len(tr.requests()); n != 1 {
						t.Fatalf("%d requests on the wire after a failure, want 1", n)
					}
					checkEvents(t, m, []metricEvent{{kind: "request", unit: unit, fc: fc}, {kind: mode.kind, unit: unit, fc: fc}}, mode.wantErr)
				})
			}
		})
	}
}

// TestClientCalls_RequestPDUs pins the request bytes of the calls whose
// encoding is not covered by a dedicated wire-level test elsewhere.
func TestClientCalls_RequestPDUs(t *testing.T) {
	cases := []struct {
		name string
		call func(ctx context.Context, c *Client) error
		want []adu.Request
	}{
		{
			"WriteRegisterBit sets one bit and writes the register back",
			func(ctx context.Context, c *Client) error { return c.WriteRegisterBit(ctx, 1, 0x0010, 0, true) },
			[]adu.Request{
				{UnitID: 1, FunctionCode: 0x03, Payload: []byte{0x00, 0x10, 0x00, 0x01}},
				{UnitID: 1, FunctionCode: 0x10, Payload: []byte{0x00, 0x10, 0x00, 0x01, 0x02, 0x12, 0x35}},
			},
		},
		{
			"WriteRegisterBit clears one bit",
			func(ctx context.Context, c *Client) error { return c.WriteRegisterBit(ctx, 1, 0x0010, 12, false) },
			[]adu.Request{
				{UnitID: 1, FunctionCode: 0x03, Payload: []byte{0x00, 0x10, 0x00, 0x01}},
				{UnitID: 1, FunctionCode: 0x10, Payload: []byte{0x00, 0x10, 0x00, 0x01, 0x02, 0x02, 0x34}},
			},
		},
		{
			"UpdateRegisterMask only touches masked bits",
			func(ctx context.Context, c *Client) error {
				return c.UpdateRegisterMask(ctx, 1, 0x0010, 0x00F0, 0xFFFF)
			},
			[]adu.Request{
				{UnitID: 1, FunctionCode: 0x03, Payload: []byte{0x00, 0x10, 0x00, 0x01}},
				{UnitID: 1, FunctionCode: 0x10, Payload: []byte{0x00, 0x10, 0x00, 0x01, 0x02, 0x12, 0xF4}},
			},
		},
		{
			"WriteRegisterBytes pads odd input with a zero byte",
			func(ctx context.Context, c *Client) error {
				return c.WriteRegisterBytes(ctx, 1, 0x0010, []byte{0xAA, 0xBB, 0xCC})
			},
			[]adu.Request{
				{UnitID: 1, FunctionCode: 0x10, Payload: []byte{0x00, 0x10, 0x00, 0x02, 0x04, 0xAA, 0xBB, 0xCC, 0x00}},
			},
		},
		{
			"ReadRegisterBytes rounds odd byte counts up to whole registers",
			func(ctx context.Context, c *Client) error {
				_, err := c.ReadRegisterBytes(ctx, 1, 0x0010, 5, InputRegister)
				return err
			},
			[]adu.Request{{UnitID: 1, FunctionCode: 0x04, Payload: []byte{0x00, 0x10, 0x00, 0x03}}},
		},
		{
			"WriteCoil true is 0xFF00",
			func(ctx context.Context, c *Client) error { return c.WriteCoil(ctx, 1, 0x00AC, true) },
			[]adu.Request{{UnitID: 1, FunctionCode: 0x05, Payload: []byte{0x00, 0xAC, 0xFF, 0x00}}},
		},
		{
			"WriteCoil false is 0x0000",
			func(ctx context.Context, c *Client) error { return c.WriteCoil(ctx, 1, 0x00AC, false) },
			[]adu.Request{{UnitID: 1, FunctionCode: 0x05, Payload: []byte{0x00, 0xAC, 0x00, 0x00}}},
		},
		{
			"WriteCoils packs bits LSB first",
			func(ctx context.Context, c *Client) error {
				return c.WriteCoils(ctx, 1, 0x0013, []bool{true, false, true, true, false, false, true, true, true, false})
			},
			[]adu.Request{{UnitID: 1, FunctionCode: 0x0F, Payload: []byte{0x00, 0x13, 0x00, 0x0A, 0x02, 0xCD, 0x01}}},
		},
		{
			"DiagnosticRegister sends no data field",
			func(ctx context.Context, c *Client) error { _, err := c.DiagnosticRegister(ctx, 1); return err },
			[]adu.Request{{UnitID: 1, FunctionCode: 0x08, Payload: []byte{0x00, 0x02}}},
		},
		{
			"DiagnosticForceListenOnlyMode",
			func(ctx context.Context, c *Client) error { return c.DiagnosticForceListenOnlyMode(ctx, 1) },
			[]adu.Request{{UnitID: 1, FunctionCode: 0x08, Payload: []byte{0x00, 0x04, 0x00, 0x00}}},
		},
		{
			"ReadAllDeviceIdentification requests the extended stream from object 0",
			func(ctx context.Context, c *Client) error {
				_, err := c.ReadAllDeviceIdentification(ctx, 1)
				return err
			},
			[]adu.Request{{UnitID: 1, FunctionCode: 0x2B, Payload: []byte{0x0E, 0x03, 0x00}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, tr, _ := newScriptedClient(t, goodDevice)
			if err := tc.call(context.Background(), c); err != nil {
				t.Fatalf("call failed: %v", err)
			}
			if got := tr.requests(); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("requests on the wire:\n got %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

// A failed write in a read-modify-write helper is reported with the write's
// function code, after the read was reported as successful.
func TestClientReadModifyWrite_WriteFails(t *testing.T) {
	calls := map[string]func(ctx context.Context, c *Client) error{
		"WriteRegisterBit":   func(ctx context.Context, c *Client) error { return c.WriteRegisterBit(ctx, 5, 1, 3, true) },
		"UpdateRegisterMask": func(ctx context.Context, c *Client) error { return c.UpdateRegisterMask(ctx, 5, 1, 0xFF, 0x12) },
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			c, tr, m := newScriptedClient(t, func(req *adu.Request) (*adu.Response, error) {
				if FunctionCode(req.FunctionCode) == FCWriteMultipleRegisters {
					return excRes(req, 0x03), nil
				}
				return goodDevice(req)
			})
			if err := call(context.Background(), c); !errors.Is(err, ErrIllegalDataValue) {
				t.Fatalf("want ErrIllegalDataValue, got %v", err)
			}
			if n := len(tr.requests()); n != 2 {
				t.Fatalf("%d requests, want 2", n)
			}
			checkEvents(t, m, []metricEvent{
				{kind: "request", unit: 5, fc: FCReadHoldingRegisters},
				{kind: "response", unit: 5, fc: FCReadHoldingRegisters},
				{kind: "request", unit: 5, fc: FCWriteMultipleRegisters},
				{kind: "error", unit: 5, fc: FCWriteMultipleRegisters},
			}, ErrIllegalDataValue)
		})
	}
}

// Invalid arguments are rejected before anything is sent or reported.
func TestClientCalls_ParameterErrorsSendNothing(t *testing.T) {
	cases := map[string]func(ctx context.Context, c *Client) error{
		"ReadWriteMultipleRegisters read range overflow": func(ctx context.Context, c *Client) error {
			_, err := c.ReadWriteMultipleRegisters(ctx, 1, 0xFFFF, 2, 0, []uint16{1})
			return err
		},
		"ReadWriteMultipleRegisters write range overflow": func(ctx context.Context, c *Client) error {
			_, err := c.ReadWriteMultipleRegisters(ctx, 1, 0, 1, 0xFFFF, []uint16{1, 2})
			return err
		},
		"ReadRegisters unknown register type": func(ctx context.Context, c *Client) error {
			_, err := c.ReadRegisters(ctx, 1, 0, 1, RegType(77))
			return err
		},
		"ReadRegisterBit bit index": func(ctx context.Context, c *Client) error {
			_, err := c.ReadRegisterBit(ctx, 1, 0, 16, HoldingRegister)
			return err
		},
		"ReadRegisterBits range": func(ctx context.Context, c *Client) error {
			_, err := c.ReadRegisterBits(ctx, 1, 0, 10, 7, HoldingRegister)
			return err
		},
		"WriteRegisterBit bit index": func(ctx context.Context, c *Client) error {
			return c.WriteRegisterBit(ctx, 1, 0, 16, true)
		},
		"WriteCoils empty": func(ctx context.Context, c *Client) error {
			return c.WriteCoils(ctx, 1, 0, nil)
		},
		"WriteCoils range overflow": func(ctx context.Context, c *Client) error {
			return c.WriteCoils(ctx, 1, 0xFFFF, []bool{true, true})
		},
		"WriteFileRecords too much data": func(ctx context.Context, c *Client) error {
			// 7 header bytes + 2*123 data bytes = 253 > 0xFB.
			return c.WriteFileRecords(ctx, 1, []FileRecord{{FileNumber: 1, Data: make([]uint16, 123)}})
		},
		"register payload with odd byte count": func(ctx context.Context, c *Client) error {
			return c.writeRegisterPayload(ctx, 1, 0, []byte{1, 2, 3})
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			c, tr, m := newScriptedClient(t, goodDevice)
			err := call(context.Background(), c)
			var pe *ParameterError
			if !errors.As(err, &pe) || !errors.Is(err, ErrUnexpectedParameters) {
				t.Fatalf("want *ParameterError, got %T: %v", err, err)
			}
			if n := len(tr.requests()); n != 0 {
				t.Fatalf("%d requests were sent for invalid arguments", n)
			}
			if ev := m.snapshot(); len(ev) != 0 {
				t.Fatalf("metrics reported for a call that never started: %v", ev)
			}
		})
	}
}
