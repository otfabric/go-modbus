// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/otfabric/go-modbus/internal/adu"
)

// wantProtocolErrorText asserts err is a *ProtocolError whose text mentions substr.
func wantProtocolErrorText(t *testing.T, err error, substr string) {
	t.Helper()
	var pe *ProtocolError
	if !errors.As(err, &pe) || !errors.Is(err, ErrProtocolError) {
		t.Fatalf("want *ProtocolError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("error %q does not mention %q", err, substr)
	}
}

// fixedPayload answers every request with a success response carrying payload.
func fixedPayload(payload ...byte) scriptFn {
	return func(req *adu.Request) (*adu.Response, error) { return okRes(req, payload...), nil }
}

func TestReadBits_ByteCountDoesNotMatchQuantity(t *testing.T) {
	// 9 bits need 2 data bytes; the device sends a self-consistent 1-byte answer.
	c, _, _ := newScriptedClient(t, fixedPayload(0x01, 0xFF))
	_, err := c.ReadCoils(context.Background(), 1, 0, 9)
	wantProtocolErrorText(t, err, "byte count 1 does not match expected 2")

	_, err = c.ReadDiscreteInputs(context.Background(), 1, 0, 9)
	wantProtocolErrorText(t, err, "byte count 1 does not match expected 2")

	// Too many bytes is just as wrong as too few.
	c, _, _ = newScriptedClient(t, fixedPayload(0x02, 0xFF, 0xFF))
	_, err = c.ReadCoils(context.Background(), 1, 0, 8)
	wantProtocolErrorText(t, err, "byte count 2 does not match expected 1")
}

func TestReadRegisters_DataLengthDoesNotMatchQuantity(t *testing.T) {
	c, _, _ := newScriptedClient(t, fixedPayload(0x02, 0xAA, 0xBB))
	_, err := c.ReadRegisters(context.Background(), 1, 0, 2, HoldingRegister)
	wantProtocolErrorText(t, err, "expected 4 data bytes for 2 registers, got 2")

	_, err = c.ReadWriteMultipleRegisters(context.Background(), 1, 0, 2, 0, []uint16{1})
	wantProtocolErrorText(t, err, "expected 4 data bytes for readQty=2, got 2")
}

func TestReadFileRecords_MalformedResponses(t *testing.T) {
	one := []FileRecordRequest{{FileNumber: 4, RecordNumber: 1, RecordLength: 1}}
	two := []FileRecordRequest{
		{FileNumber: 4, RecordNumber: 1, RecordLength: 1},
		{FileNumber: 4, RecordNumber: 2, RecordLength: 1},
	}
	cases := []struct {
		name     string
		requests []FileRecordRequest
		payload  []byte
		want     string
	}{
		{"second sub-response missing", two, []byte{0x04, 0x03, 0x06, 0xAA, 0xBB}, "sub-response 1: truncated payload (no length byte)"},
		{"zero file response length", one, []byte{0x01, 0x00}, "sub-response 0: file response length is 0"},
		{"reference type missing", one, []byte{0x01, 0x03}, "sub-response 0: truncated payload (no reference type)"},
		{"odd register data length", one, []byte{0x03, 0x02, 0x06, 0xAA}, "sub-response 0: odd data length 1"},
		{"data length differs from request", one, []byte{0x06, 0x05, 0x06, 0xAA, 0xBB, 0xCC, 0xDD}, "sub-response 0: expected 2 data bytes, got 4"},
		{"register data truncated", one, []byte{0x02, 0x03, 0x06}, "sub-response 0: data truncated at offset 3"},
		{"trailing bytes", one, []byte{0x06, 0x03, 0x06, 0xAA, 0xBB, 0x00, 0x00}, "trailing data: consumed 5 bytes, payload has 7"},
		{"outer length mismatch", one, []byte{0x09, 0x03, 0x06, 0xAA, 0xBB}, "data length 9 does not match payload length 4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _, m := newScriptedClient(t, fixedPayload(tc.payload...))
			_, err := c.ReadFileRecords(context.Background(), 9, tc.requests)
			wantProtocolErrorText(t, err, tc.want)
			checkEvents(t, m, []metricEvent{
				{kind: "request", unit: 9, fc: FCReadFileRecord},
				{kind: "error", unit: 9, fc: FCReadFileRecord},
			}, ErrProtocolError)
		})
	}
}

func TestWriteFileRecords_ResponseLengthMismatch(t *testing.T) {
	c, _, _ := newScriptedClient(t, func(req *adu.Request) (*adu.Response, error) {
		return okRes(req, req.Payload[:len(req.Payload)-1]...), nil
	})
	err := c.WriteFileRecords(context.Background(), 1, []FileRecord{{FileNumber: 4, RecordNumber: 7, Data: []uint16{1, 2}}})
	wantProtocolErrorText(t, err, "response payload length 11 does not match request 12")
}

// devIDPage builds one FC43/0x0E response payload.
func devIDPage(category DeviceIDCategory, conformity, more, next byte, objs ...DeviceIdentificationObject) []byte {
	p := []byte{0x0E, byte(category), conformity, more, next, byte(len(objs))}
	for _, o := range objs {
		p = append(p, byte(o.ID), byte(len(o.Value)))
		p = append(p, o.Value...)
	}
	return p
}

// pagedDevice serves the given payloads in order, one per request.
func pagedDevice(pages ...[]byte) scriptFn {
	i := 0
	return func(req *adu.Request) (*adu.Response, error) {
		if i >= len(pages) {
			return nil, errors.New("pagedDevice: no more pages scripted")
		}
		p := pages[i]
		i++
		return okRes(req, p...), nil
	}
}

func TestReadDeviceIdentification_Pagination(t *testing.T) {
	vendor := DeviceIdentificationObject{ID: 0x00, Value: "ACME"}
	product := DeviceIdentificationObject{ID: 0x01, Value: "PLC-9"}
	revision := DeviceIdentificationObject{ID: 0x02, Value: "1.2"}
	userApp := DeviceIdentificationObject{ID: 0x06, Value: "pump-ctl"}

	c, tr, m := newScriptedClient(t, pagedDevice(
		devIDPage(DeviceIDRegular, 0x82, 0xFF, 0x02, vendor, product),
		// The device repeats an already delivered object on the next page.
		devIDPage(DeviceIDRegular, 0x82, 0xFF, 0x06, product, revision),
		devIDPage(DeviceIDRegular, 0x82, 0x00, 0x00, userApp),
	))
	di, err := c.ReadDeviceIdentification(context.Background(), 7, DeviceIDRegular, 0)
	if err != nil {
		t.Fatalf("ReadDeviceIdentification: %v", err)
	}
	want := &DeviceIdentification{
		Category:        DeviceIDRegular,
		ConformityLevel: 0x82,
		Objects: []DeviceIdentificationObject{
			{ID: 0x00, Name: "VendorName", Value: "ACME"},
			{ID: 0x01, Name: "ProductCode", Value: "PLC-9"},
			{ID: 0x02, Name: "MajorMinorRevision", Value: "1.2"},
			{ID: 0x06, Name: "UserApplicationName", Value: "pump-ctl"},
		},
	}
	if !reflect.DeepEqual(di, want) {
		t.Fatalf("got %+v\nwant %+v", di, want)
	}
	// Each follow-up request must resume at the NextObjectID of the page before.
	wantReqs := []adu.Request{
		{UnitID: 7, FunctionCode: 0x2B, Payload: []byte{0x0E, 0x02, 0x00}},
		{UnitID: 7, FunctionCode: 0x2B, Payload: []byte{0x0E, 0x02, 0x02}},
		{UnitID: 7, FunctionCode: 0x2B, Payload: []byte{0x0E, 0x02, 0x06}},
	}
	if got := tr.requests(); !reflect.DeepEqual(got, wantReqs) {
		t.Fatalf("requests:\n got %+v\nwant %+v", got, wantReqs)
	}
	// Paging is one logical request for metrics purposes.
	checkEvents(t, m, []metricEvent{
		{kind: "request", unit: 7, fc: FCEncapsulatedInterface},
		{kind: "response", unit: 7, fc: FCEncapsulatedInterface},
	}, nil)
}

func TestReadDeviceIdentification_MalformedResponses(t *testing.T) {
	vendor := DeviceIdentificationObject{ID: 0x00, Value: "ACME"}
	product := DeviceIdentificationObject{ID: 0x01, Value: "PLC-9"}

	cases := []struct {
		name  string
		pages [][]byte
		want  string
	}{
		{
			"category changes between pages",
			[][]byte{
				devIDPage(DeviceIDRegular, 0x02, 0xFF, 0x01, vendor),
				devIDPage(DeviceIDBasic, 0x02, 0x00, 0x00, product),
			},
			"category changed across pages: 0x02 → 0x01",
		},
		{
			"conformity level changes between pages",
			[][]byte{
				devIDPage(DeviceIDRegular, 0x02, 0xFF, 0x01, vendor),
				devIDPage(DeviceIDRegular, 0x82, 0x00, 0x00, product),
			},
			"conformity level changed across pages: 0x02 → 0x82",
		},
		{
			"object count exceeds payload",
			[][]byte{{0x0E, 0x02, 0x02, 0x00, 0x00, 0x02, 0x00, 0x01, 'A'}},
			"truncated object header at offset 9 (object 1/2)",
		},
		{
			"object header cut in half",
			[][]byte{{0x0E, 0x02, 0x02, 0x00, 0x00, 0x01, 0x00}},
			"truncated object header at offset 6 (object 0/1)",
		},
		{
			"object value shorter than its length",
			[][]byte{{0x0E, 0x02, 0x02, 0x00, 0x00, 0x01, 0x00, 0x05, 'A', 'B'}},
			"truncated object body at offset 8: need 5 bytes, have 2",
		},
		{
			"same object id with different values",
			[][]byte{
				devIDPage(DeviceIDRegular, 0x02, 0xFF, 0x01, vendor),
				devIDPage(DeviceIDRegular, 0x02, 0x00, 0x00, DeviceIdentificationObject{ID: 0x00, Value: "EVIL"}),
			},
			"duplicate object ID 0x00 with conflicting value",
		},
		{
			"bytes after the last object",
			[][]byte{append(devIDPage(DeviceIDRegular, 0x02, 0x00, 0x00, vendor), 0xDE, 0xAD)},
			"trailing data: consumed 12 bytes, payload has 14",
		},
		{
			"more-follows is neither 0x00 nor 0xFF",
			[][]byte{devIDPage(DeviceIDRegular, 0x02, 0x01, 0x00, vendor)},
			"invalid MoreFollows value 0x01",
		},
		{
			"next object id does not advance",
			[][]byte{devIDPage(DeviceIDRegular, 0x02, 0xFF, 0x00, vendor)},
			"pagination stuck: NextObjectID not advancing (0x00)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, tr, m := newScriptedClient(t, pagedDevice(tc.pages...))
			di, err := c.ReadDeviceIdentification(context.Background(), 7, DeviceIDRegular, 0)
			wantProtocolErrorText(t, err, tc.want)
			if di != nil {
				t.Errorf("identification returned alongside the error: %+v", di)
			}
			if n := len(tr.requests()); n != len(tc.pages) {
				t.Errorf("%d requests, want %d", n, len(tc.pages))
			}
			checkEvents(t, m, []metricEvent{
				{kind: "request", unit: 7, fc: FCEncapsulatedInterface},
				{kind: "error", unit: 7, fc: FCEncapsulatedInterface},
			}, ErrProtocolError)
		})
	}
}

// A device that keeps announcing more pages must not keep the client busy forever.
func TestReadDeviceIdentification_PageLimit(t *testing.T) {
	c, tr, _ := newScriptedClient(t, func(req *adu.Request) (*adu.Response, error) {
		// Alternate NextObjectID so the "not advancing" check does not fire.
		next := req.Payload[2] ^ 0x01
		obj := DeviceIdentificationObject{ID: DeviceIDObjectID(0x80 + req.Payload[2]), Value: "x"}
		return okRes(req, devIDPage(DeviceIDExtended, 0x83, 0xFF, next, obj)...), nil
	})
	di, err := c.ReadAllDeviceIdentification(context.Background(), 1)
	wantProtocolErrorText(t, err, "pagination exceeded max page count (32)")
	if di != nil {
		t.Errorf("identification returned alongside the error: %+v", di)
	}
	if n := len(tr.requests()); n != 32 {
		t.Fatalf("%d requests, want exactly 32", n)
	}
}

func TestSupportsFunction_Outcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("valid response", func(t *testing.T) {
		c, tr, m := newScriptedClient(t, goodDevice)
		ok, err := c.SupportsFunction(ctx, 3, FCReadHoldingRegisters)
		if err != nil || !ok {
			t.Fatalf("got (%v, %v), want (true, nil)", ok, err)
		}
		want := []adu.Request{{UnitID: 3, FunctionCode: 0x03, Payload: []byte{0, 0, 0, 1}}}
		if got := tr.requests(); !reflect.DeepEqual(got, want) {
			t.Fatalf("probe request %+v, want %+v", got, want)
		}
		checkEvents(t, m, []metricEvent{
			{kind: "request", unit: 3, fc: FCReadHoldingRegisters},
			{kind: "response", unit: 3, fc: FCReadHoldingRegisters},
		}, nil)
	})
	t.Run("exception counts as supported", func(t *testing.T) {
		c, _, _ := newScriptedClient(t, func(req *adu.Request) (*adu.Response, error) { return excRes(req, 0x02), nil })
		ok, err := c.SupportsDeviceIdentification(ctx, 3)
		if err != nil || !ok {
			t.Fatalf("got (%v, %v), want (true, nil)", ok, err)
		}
	})
	t.Run("structurally invalid response", func(t *testing.T) {
		c, _, m := newScriptedClient(t, fixedPayload(0x04, 0x00, 0x01))
		ok, err := c.SupportsFunction(ctx, 3, FCReadCoils)
		if err != nil || ok {
			t.Fatalf("got (%v, %v), want (false, nil)", ok, err)
		}
		ev := m.snapshot()
		if len(ev) != 2 || ev[1].kind != "error" || ev[1].fc != FCReadCoils {
			t.Fatalf("metrics = %v, want request followed by error", ev)
		}
	})
	t.Run("gateway target not responding", func(t *testing.T) {
		c, _, _ := newScriptedClient(t, func(*adu.Request) (*adu.Response, error) { return nil, ErrGWTargetFailedToRespond })
		ok, err := c.SupportsFunction(ctx, 3, FCReadCoils)
		if err != nil || ok {
			t.Fatalf("got (%v, %v), want (false, nil)", ok, err)
		}
	})
	t.Run("transport error is returned", func(t *testing.T) {
		c, _, m := newScriptedClient(t, func(*adu.Request) (*adu.Response, error) { return nil, io.ErrUnexpectedEOF })
		ok, err := c.SupportsFunction(ctx, 3, FCReadCoils)
		if !errors.Is(err, io.ErrUnexpectedEOF) || ok {
			t.Fatalf("got (%v, %v), want (false, unexpected EOF)", ok, err)
		}
		checkEvents(t, m, []metricEvent{
			{kind: "request", unit: 3, fc: FCReadCoils},
			{kind: "error", unit: 3, fc: FCReadCoils},
		}, io.ErrUnexpectedEOF)
	})
	t.Run("cancellation reported by the transport is returned", func(t *testing.T) {
		c, _, m := newScriptedClient(t, func(*adu.Request) (*adu.Response, error) { return nil, context.Canceled })
		ok, err := c.SupportsFunction(ctx, 3, FCReadCoils)
		if !errors.Is(err, context.Canceled) || ok {
			t.Fatalf("got (%v, %v), want (false, context.Canceled)", ok, err)
		}
		// Every OnRequest is followed by exactly one outcome, cancellation included.
		checkEvents(t, m, []metricEvent{
			{kind: "request", unit: 3, fc: FCReadCoils},
			{kind: "error", unit: 3, fc: FCReadCoils},
		}, context.Canceled)
	})
	t.Run("deadline reported by the transport is a timeout", func(t *testing.T) {
		// The session layer maps every timeout-flavoured error, including
		// context.DeadlineExceeded, to ErrRequestTimedOut.
		c, _, m := newScriptedClient(t, func(*adu.Request) (*adu.Response, error) { return nil, context.DeadlineExceeded })
		ok, err := c.SupportsFunction(ctx, 3, FCReadCoils)
		if err != nil || ok {
			t.Fatalf("got (%v, %v), want (false, nil)", ok, err)
		}
		checkEvents(t, m, []metricEvent{
			{kind: "request", unit: 3, fc: FCReadCoils},
			{kind: "timeout", unit: 3, fc: FCReadCoils},
		}, nil)
	})
	t.Run("cancelled context sends nothing", func(t *testing.T) {
		c, tr, m := newScriptedClient(t, goodDevice)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		ok, err := c.SupportsFunction(cctx, 3, FCReadCoils)
		if !errors.Is(err, context.Canceled) || ok {
			t.Fatalf("got (%v, %v), want (false, context.Canceled)", ok, err)
		}
		if len(tr.requests()) != 0 || len(m.snapshot()) != 0 {
			t.Fatalf("probe was sent or reported despite cancellation")
		}
	})
	t.Run("function code without a probe", func(t *testing.T) {
		c, tr, _ := newScriptedClient(t, goodDevice)
		ok, err := c.SupportsFunction(ctx, 3, FCWriteSingleCoil)
		var pe *ParameterError
		if !errors.As(err, &pe) || ok {
			t.Fatalf("got (%v, %v), want (false, *ParameterError)", ok, err)
		}
		if len(tr.requests()) != 0 {
			t.Fatal("a request was sent for a function code that has no probe")
		}
	})
}

func TestProbeFunction_ErrorOutcomes(t *testing.T) {
	ctx := context.Background()

	t.Run("transport error", func(t *testing.T) {
		c, _, m := newScriptedClient(t, func(*adu.Request) (*adu.Response, error) { return nil, io.EOF })
		res, err := c.ProbeFunction(ctx, 3, FCReportServerID)
		if err != nil {
			t.Fatalf("ProbeFunction: %v", err)
		}
		if res.Outcome != ProbeTransportError || res.Supported || !errors.Is(res.Err, io.EOF) || res.Reason != io.EOF.Error() {
			t.Fatalf("unexpected result %+v", res)
		}
		checkEvents(t, m, []metricEvent{
			{kind: "request", unit: 3, fc: FCReportServerID},
			{kind: "error", unit: 3, fc: FCReportServerID},
		}, io.EOF)
	})
	t.Run("gateway target not responding is a timeout", func(t *testing.T) {
		c, _, _ := newScriptedClient(t, func(*adu.Request) (*adu.Response, error) { return nil, ErrGWTargetFailedToRespond })
		res, err := c.ProbeFunction(ctx, 3, FCReportServerID)
		if err != nil {
			t.Fatalf("ProbeFunction: %v", err)
		}
		if res.Outcome != ProbeTimeout || !errors.Is(res.Err, ErrGWTargetFailedToRespond) {
			t.Fatalf("unexpected result %+v", res)
		}
	})
	t.Run("cancellation reported by the transport", func(t *testing.T) {
		c, _, m := newScriptedClient(t, func(*adu.Request) (*adu.Response, error) { return nil, context.Canceled })
		res, err := c.ProbeFunction(ctx, 3, FCReportServerID)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
		checkEvents(t, m, []metricEvent{
			{kind: "request", unit: 3, fc: FCReportServerID},
			{kind: "error", unit: 3, fc: FCReportServerID},
		}, context.Canceled)
		if !reflect.DeepEqual(res, ProbeResult{}) {
			t.Fatalf("want zero result, got %+v", res)
		}
	})
	t.Run("deadline reported by the transport is a timeout", func(t *testing.T) {
		c, _, _ := newScriptedClient(t, func(*adu.Request) (*adu.Response, error) { return nil, context.DeadlineExceeded })
		res, err := c.ProbeFunction(ctx, 3, FCReportServerID)
		if err != nil {
			t.Fatalf("ProbeFunction: %v", err)
		}
		if res.Outcome != ProbeTimeout || !errors.Is(res.Err, ErrRequestTimedOut) || res.Reason != "request timed out" {
			t.Fatalf("unexpected result %+v", res)
		}
	})
	t.Run("cancelled context sends nothing", func(t *testing.T) {
		c, tr, m := newScriptedClient(t, goodDevice)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		res, err := c.ProbeFunction(cctx, 3, FCReportServerID)
		if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(res, ProbeResult{}) {
			t.Fatalf("got (%+v, %v), want (zero, context.Canceled)", res, err)
		}
		if len(tr.requests()) != 0 || len(m.snapshot()) != 0 {
			t.Fatalf("probe was sent or reported despite cancellation")
		}
	})
	t.Run("validation failure keeps the raw response", func(t *testing.T) {
		c, _, _ := newScriptedClient(t, fixedPayload(0x09, 0x42))
		res, err := c.ProbeFunction(ctx, 3, FCReportServerID)
		if err != nil {
			t.Fatalf("ProbeFunction: %v", err)
		}
		if res.Outcome != ProbeValidationFailed || res.Supported || res.ResponseFC != FCReportServerID ||
			!reflect.DeepEqual(res.RawPayload, []byte{0x09, 0x42}) {
			t.Fatalf("unexpected result %+v", res)
		}
	})
}
