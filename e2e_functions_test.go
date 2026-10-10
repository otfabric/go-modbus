// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// e2eObjectIDs returns the IDs of the objects in di, in order.
func e2eObjectIDs(di *DeviceIdentification) []DeviceIDObjectID {
	ids := make([]DeviceIDObjectID, len(di.Objects))
	for i := range di.Objects {
		ids[i] = di.Objects[i].ID
	}
	return ids
}

func e2eDevIDCalls(t *testing.T, dev *e2eDevice) []e2eCall {
	t.Helper()
	calls := dev.takeCalls()
	for _, c := range calls {
		if c.Kind != "devid" || c.FC != FCEncapsulatedInterface || c.MEIType != MEIReadDeviceIdentification || c.UnitID != e2eUnit {
			t.Errorf("device identification handler saw %+v", c)
		}
	}
	return calls
}

func TestE2E_DeviceIdentification_Categories(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		dev.objects = []DeviceIdentificationObject{
			{ID: 0x82, Value: "ext-82"},
			{ID: 0x00, Value: "ACME"},
			{ID: 0x06, Value: "app"},
			{ID: 0x01, Value: "PC-1"},
			{ID: 0x02, Value: "v1.2"},
			{ID: 0x03, Value: "https://acme.example"},
			{ID: 0x80, Value: "ext-80"},
			{ID: 0x04, Value: "Widget"},
			{ID: 0x05, Value: "W-1000"},
		}
		p := e2eStart(t, kind, dev, e2eOpts{})
		c := p.client
		ctx := context.Background()

		streams := []struct {
			category DeviceIDCategory
			want     []DeviceIDObjectID
		}{
			{DeviceIDBasic, []DeviceIDObjectID{0, 1, 2}},
			{DeviceIDRegular, []DeviceIDObjectID{0, 1, 2, 3, 4, 5, 6}},
			{DeviceIDExtended, []DeviceIDObjectID{0, 1, 2, 3, 4, 5, 6, 0x80, 0x82}},
		}
		for _, s := range streams {
			di, err := c.ReadDeviceIdentification(ctx, e2eUnit, s.category, 0)
			if err != nil {
				t.Fatalf("ReadDeviceIdentification(0x%02X): %v", uint8(s.category), err)
			}
			if got := e2eObjectIDs(di); !reflect.DeepEqual(got, s.want) {
				t.Errorf("category 0x%02X: objects %v, want %v (sorted by ID)", uint8(s.category), got, s.want)
			}
			if di.Category != s.category || di.ConformityLevel != 0x83 || di.MoreFollows || di.NextObjectID != 0 {
				t.Errorf("category 0x%02X: header %+v", uint8(s.category), di)
			}
			if !di.SupportsStreamAccess() || !di.SupportsIndividualAccess() {
				t.Errorf("category 0x%02X: conformity 0x83 should advertise stream and individual access", uint8(s.category))
			}
			calls := e2eDevIDCalls(t, dev)
			if len(calls) != 1 || calls[0].Category != s.category || calls[0].ObjectID != 0 {
				t.Errorf("category 0x%02X: handler saw %+v", uint8(s.category), calls)
			}
		}

		// Values and names of the basic objects.
		di, err := c.ReadDeviceIdentification(ctx, e2eUnit, DeviceIDRegular, 0)
		if err != nil {
			t.Fatal(err)
		}
		wantObjs := []DeviceIdentificationObject{
			{ID: 0, Name: "VendorName", Value: "ACME"},
			{ID: 1, Name: "ProductCode", Value: "PC-1"},
			{ID: 2, Name: "MajorMinorRevision", Value: "v1.2"},
			{ID: 3, Name: "VendorUrl", Value: "https://acme.example"},
			{ID: 4, Name: "ProductName", Value: "Widget"},
			{ID: 5, Name: "ModelName", Value: "W-1000"},
			{ID: 6, Name: "UserApplicationName", Value: "app"},
		}
		if !reflect.DeepEqual(di.Objects, wantObjs) {
			t.Errorf("regular objects = %+v, want %+v", di.Objects, wantObjs)
		}

		// Stream access from a start object returns that object and the rest.
		di, err = c.ReadDeviceIdentification(ctx, e2eUnit, DeviceIDRegular, 0x04)
		if err != nil || !reflect.DeepEqual(e2eObjectIDs(di), []DeviceIDObjectID{4, 5, 6}) {
			t.Errorf("regular from 0x04: %v, %v", di, err)
		}
		// An unknown start object restarts the stream at object 0 (per spec).
		di, err = c.ReadDeviceIdentification(ctx, e2eUnit, DeviceIDBasic, 0x55)
		if err != nil || !reflect.DeepEqual(e2eObjectIDs(di), []DeviceIDObjectID{0, 1, 2}) {
			t.Errorf("basic from unknown 0x55: %v, %v", di, err)
		}
		calls := e2eDevIDCalls(t, dev)
		if len(calls) != 3 || calls[1].ObjectID != 0x04 || calls[2].ObjectID != 0x55 {
			t.Errorf("handler saw %+v", calls)
		}

		// Individual access: every object, including extended ones.
		for _, obj := range dev.objects {
			di, err := c.ReadDeviceIdentification(ctx, e2eUnit, DeviceIDIndividual, obj.ID)
			if err != nil {
				t.Errorf("individual 0x%02X: %v", uint8(obj.ID), err)
				continue
			}
			if len(di.Objects) != 1 || di.Objects[0].ID != obj.ID || di.Objects[0].Value != obj.Value ||
				di.Category != DeviceIDIndividual || di.MoreFollows {
				t.Errorf("individual 0x%02X = %+v", uint8(obj.ID), di)
			}
		}
		calls = e2eDevIDCalls(t, dev)
		if len(calls) != len(dev.objects) || calls[0].Category != DeviceIDIndividual || calls[0].ObjectID != 0x82 {
			t.Errorf("individual access: handler saw %+v", calls)
		}
		_, err = c.ReadDeviceIdentification(ctx, e2eUnit, DeviceIDIndividual, 0x81)
		e2eWantException(t, "individual unknown object", err, FCEncapsulatedInterface, exIllegalDataAddress)
		dev.takeCalls()

		// ReadAllDeviceIdentification is the extended stream from object 0.
		all, err := c.ReadAllDeviceIdentification(ctx, e2eUnit)
		if err != nil || len(all.Objects) != len(dev.objects) || all.Category != DeviceIDExtended {
			t.Errorf("ReadAllDeviceIdentification: %+v, %v", all, err)
		}
		calls = e2eDevIDCalls(t, dev)
		if len(calls) != 1 || calls[0].Category != DeviceIDExtended || calls[0].ObjectID != 0 {
			t.Errorf("ReadAllDeviceIdentification: handler saw %+v", calls)
		}

		// Invalid categories never reach the wire.
		_, err = c.ReadDeviceIdentification(ctx, e2eUnit, 0x00, 0)
		e2eWantParamError(t, "category 0", err, dev)
		_, err = c.ReadDeviceIdentification(ctx, e2eUnit, 0x05, 0)
		e2eWantParamError(t, "category 5", err, dev)
		// ... and the server rejects them when sent anyway, as it does other MEI types.
		res := sendRawFC(t, c, e2eUnit, byte(FCEncapsulatedInterface), []byte{byte(MEIReadDeviceIdentification), 0x05, 0x00})
		assertExceptionResponse(t, res, exIllegalDataValue)
		res = sendRawFC(t, c, e2eUnit, byte(FCEncapsulatedInterface), []byte{0x0D, 0x01, 0x00})
		assertExceptionResponse(t, res, exIllegalFunction)
		if n := dev.callCount(); n != 0 {
			t.Errorf("invalid FC43 requests reached the handler (%d)", n)
		}
	})
}

// The conformity level is derived from the object set when the handler leaves
// it at zero, and an empty object set is served as an empty list.
func TestE2E_DeviceIdentification_DerivedConformity(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		dev.conformity = 0
		p := e2eStart(t, kind, dev, e2eOpts{})
		ctx := context.Background()

		cases := []struct {
			name string
			objs []DeviceIdentificationObject
			want uint8
		}{
			{"basic", basicDeviceIDObjects(), 0x81},
			{"regular", regularDeviceIDObjects(), 0x82},
			{"extended", append(regularDeviceIDObjects(), DeviceIdentificationObject{ID: 0x90, Value: "x"}), 0x83},
		}
		for _, tc := range cases {
			dev.mu.Lock()
			dev.objects = tc.objs
			dev.mu.Unlock()
			di, err := p.client.ReadAllDeviceIdentification(ctx, e2eUnit)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if di.ConformityLevel != tc.want || len(di.Objects) != len(tc.objs) {
				t.Errorf("%s: conformity 0x%02X with %d objects, want 0x%02X with %d",
					tc.name, di.ConformityLevel, len(di.Objects), tc.want, len(tc.objs))
			}
		}

		dev.mu.Lock()
		dev.objects = nil
		dev.conformity = 0x01
		dev.mu.Unlock()
		di, err := p.client.ReadDeviceIdentification(ctx, e2eUnit, DeviceIDBasic, 0)
		if err != nil || len(di.Objects) != 0 || di.ConformityLevel != 0x01 {
			t.Errorf("empty object set: %+v, %v", di, err)
		}
		if di != nil && (!di.SupportsStreamAccess() || di.SupportsIndividualAccess()) {
			t.Error("conformity 0x01 should advertise stream access only")
		}
	})
}

// A large object set is split over several FC43 responses; the client follows
// MoreFollows/NextObjectID and the handler sees one request per page.
func TestE2E_DeviceIdentification_Pagination(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		// 60 objects of 2+98 bytes: two objects per 246-byte page, 30 pages.
		var objs []DeviceIdentificationObject
		for i := 0; i < 60; i++ {
			id := DeviceIDObjectID(i)
			if i >= 7 {
				id = DeviceIDObjectID(0x80 + i)
			}
			objs = append(objs, DeviceIdentificationObject{ID: id, Value: fmt.Sprintf("%02d", i) + strings.Repeat("v", 96)})
		}
		dev.objects = objs
		p := e2eStart(t, kind, dev, e2eOpts{})
		ctx := context.Background()

		di, err := p.client.ReadAllDeviceIdentification(ctx, e2eUnit)
		if err != nil {
			t.Fatalf("ReadAllDeviceIdentification: %v", err)
		}
		if len(di.Objects) != len(objs) || di.MoreFollows || di.NextObjectID != 0 {
			t.Fatalf("got %d objects (more=%v next=0x%02X), want %d", len(di.Objects), di.MoreFollows, uint8(di.NextObjectID), len(objs))
		}
		for i := range objs {
			if di.Objects[i].ID != objs[i].ID || di.Objects[i].Value != objs[i].Value {
				t.Fatalf("object %d = %+v, want %+v", i, di.Objects[i], objs[i])
			}
		}
		calls := e2eDevIDCalls(t, dev)
		if len(calls) != 30 {
			t.Fatalf("handler saw %d page requests, want 30", len(calls))
		}
		for i, call := range calls {
			if call.Category != DeviceIDExtended || call.ObjectID != objs[2*i].ID {
				t.Errorf("page %d requested category 0x%02X from object 0x%02X, want extended from 0x%02X",
					i, uint8(call.Category), uint8(call.ObjectID), uint8(objs[2*i].ID))
			}
		}

		// The regular stream of the same device fits in four pages.
		di, err = p.client.ReadDeviceIdentification(ctx, e2eUnit, DeviceIDRegular, 0)
		if err != nil || len(di.Objects) != 7 {
			t.Fatalf("regular stream: %+v, %v", di, err)
		}
		if n := len(e2eDevIDCalls(t, dev)); n != 4 {
			t.Errorf("regular stream took %d requests, want 4", n)
		}

		// The largest object that fits in one response: 244 value bytes.
		dev.mu.Lock()
		dev.objects = append(basicDeviceIDObjects(), DeviceIdentificationObject{ID: 0x80, Value: strings.Repeat("m", 244)})
		dev.mu.Unlock()
		di, err = p.client.ReadAllDeviceIdentification(ctx, e2eUnit)
		if err != nil || len(di.Objects) != 4 || len(di.Objects[3].Value) != 244 {
			t.Errorf("244-byte object: %v", err)
		}
		if p.conns() != 1 {
			t.Errorf("server connections = %d, want 1", p.conns())
		}
	})
}

// A device may legitimately expose more identification data than fits in 32
// responses; our server serves it, so our client must be able to read it.
func TestE2E_DeviceIdentification_ManyPages(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		var objs []DeviceIdentificationObject
		for i := 0; i < 200; i++ {
			objs = append(objs, DeviceIdentificationObject{ID: DeviceIDObjectID(i), Value: strings.Repeat("y", 40)})
		}
		dev.objects = objs
		p := e2eStart(t, kind, dev, e2eOpts{})

		di, err := p.client.ReadAllDeviceIdentification(context.Background(), e2eUnit)
		if err != nil {
			if errors.Is(err, ErrProtocolError) && strings.Contains(err.Error(), "max page count") {
				t.Fatalf("the server serves 200 objects of 40 bytes in 40 FC43 responses, but the client "+
					"gives up after 32 pages (client_device_id.go maxPages): %v (after %d requests)", err, dev.callCount())
			}
			t.Fatalf("ReadAllDeviceIdentification: %v", err)
		}
		if len(di.Objects) != len(objs) {
			t.Fatalf("got %d objects, want %d", len(di.Objects), len(objs))
		}
	})
}

// An identification object that cannot fit in one response must not make the
// server emit a frame that violates the protocol.
func TestE2E_DeviceIdentification_OversizedObject(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		dev.objects = append(basicDeviceIDObjects(), DeviceIdentificationObject{ID: 0x80, Value: strings.Repeat("x", 245)})
		p := e2eStart(t, kind, dev, e2eOpts{})
		ctx := context.Background()

		_, err := p.client.ReadAllDeviceIdentification(ctx, e2eUnit)
		var exc *ExceptionError
		if !errors.As(err, &exc) {
			t.Fatalf("a 245-byte FC43 object makes the server send a 254-byte PDU (MBAP length 255) "+
				"instead of an exception; the client fails with %v and the connection is left out of sync", err)
		}
		if exc.ExceptionCode != exServerDeviceFailure {
			t.Errorf("oversized object: exception 0x%02X, want Server Device Failure", uint8(exc.ExceptionCode))
		}
		if _, err := p.client.ReadHoldingRegister(ctx, e2eUnit, 0); err != nil {
			t.Errorf("connection unusable after the oversized object: %v", err)
		}
	})
}

func TestE2E_DeviceIdentification_NotImplemented(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, e2eBasicOnly{dev}, e2eOpts{})
		ctx := context.Background()

		for _, cat := range []DeviceIDCategory{DeviceIDBasic, DeviceIDRegular, DeviceIDExtended, DeviceIDIndividual} {
			_, err := p.client.ReadDeviceIdentification(ctx, e2eUnit, cat, 0)
			e2eWantException(t, "ReadDeviceIdentification", err, FCEncapsulatedInterface, exIllegalFunction)
		}
		_, err := p.client.ReadAllDeviceIdentification(ctx, e2eUnit)
		e2eWantException(t, "ReadAllDeviceIdentification", err, FCEncapsulatedInterface, exIllegalFunction)
		if ok, err := p.client.SupportsDeviceIdentification(ctx, e2eUnit); ok || err != nil {
			t.Errorf("SupportsDeviceIdentification = %v, %v; want false, nil", ok, err)
		}
		if _, err := p.client.ReadHoldingRegister(ctx, e2eUnit, 0); err != nil {
			t.Errorf("connection unusable afterwards: %v", err)
		}
		if p.conns() != 1 {
			t.Errorf("server connections = %d, want 1", p.conns())
		}
	})
}

func TestE2E_SerialLineFunctions(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		c := p.client
		ctx := context.Background()

		for _, status := range []uint8{0x00, 0x01, 0xA5, 0xFF} {
			dev.mu.Lock()
			dev.excStatus = status
			dev.mu.Unlock()
			got, err := c.ReadExceptionStatus(ctx, e2eUnit)
			if err != nil || got != status {
				t.Errorf("ReadExceptionStatus = 0x%02X, %v; want 0x%02X", got, err, status)
			}
			if call := e2eOneCall(t, "ReadExceptionStatus", dev); call.Kind != "excstatus" || call.FC != FCReadExceptionStatus || call.UnitID != e2eUnit {
				t.Errorf("ReadExceptionStatus: handler saw %+v", call)
			}
		}

		for _, v := range [][2]uint16{{0xFFFF, 0x0102}, {0x0000, 0x0000}, {0x0000, 0xFFFF}} {
			dev.mu.Lock()
			dev.commStatus, dev.eventCount = v[0], v[1]
			dev.mu.Unlock()
			cr, err := c.GetCommEventCounter(ctx, e2eUnit)
			if err != nil || cr.Status != v[0] || cr.EventCount != v[1] {
				t.Errorf("GetCommEventCounter = %+v, %v; want %04X", cr, err, v)
			}
			if call := e2eOneCall(t, "GetCommEventCounter", dev); call.Kind != "commcounter" || call.FC != FCGetCommEventCounters {
				t.Errorf("GetCommEventCounter: handler saw %+v", call)
			}
		}

		// Event logs of 0, 1 and the spec maximum of 64 events.
		for _, n := range []int{0, 1, 3, 64} {
			events := make([]byte, n)
			for i := range events {
				events[i] = byte(0x80 + i)
			}
			dev.mu.Lock()
			dev.commStatus, dev.eventCount, dev.messageCount, dev.events = 0xFFFF, 0x0108, 0x0121, events
			dev.mu.Unlock()
			cl, err := c.GetCommEventLog(ctx, e2eUnit)
			if err != nil {
				t.Fatalf("GetCommEventLog(%d events): %v", n, err)
			}
			if cl.Status != 0xFFFF || cl.EventCount != 0x0108 || cl.MessageCount != 0x0121 || !bytes.Equal(cl.Events, events) {
				t.Errorf("GetCommEventLog(%d events) = %+v", n, cl)
			}
			if call := e2eOneCall(t, "GetCommEventLog", dev); call.Kind != "commlog" || call.FC != FCGetCommEventLog {
				t.Errorf("GetCommEventLog: handler saw %+v", call)
			}
		}

		// Handler errors map to exceptions.
		dev.setHook(func(context.Context, e2eCall) error { return ErrServerDeviceBusy })
		_, err := c.ReadExceptionStatus(ctx, e2eUnit)
		e2eWantException(t, "ReadExceptionStatus", err, FCReadExceptionStatus, exServerDeviceBusy)
		_, err = c.GetCommEventCounter(ctx, e2eUnit)
		e2eWantException(t, "GetCommEventCounter", err, FCGetCommEventCounters, exServerDeviceBusy)
		_, err = c.GetCommEventLog(ctx, e2eUnit)
		e2eWantException(t, "GetCommEventLog", err, FCGetCommEventLog, exServerDeviceBusy)
	})
}

// An event log beyond what a response can carry must not make the server emit
// a frame that violates the protocol.
func TestE2E_SerialLineFunctions_OversizedEventLog(t *testing.T) {
	dev := e2eNewDevice()
	dev.events = make([]byte, 249)
	p := e2eStart(t, "tcp", dev, e2eOpts{})

	_, err := p.client.GetCommEventLog(context.Background(), e2eUnit)
	var exc *ExceptionError
	if !errors.As(err, &exc) {
		t.Fatalf("a 249-event FC0C log (the spec allows 64) makes the server send a PDU longer than "+
			"253 bytes instead of an exception; the client fails with %v", err)
	}
	if exc.ExceptionCode != exServerDeviceFailure {
		t.Errorf("oversized event log: exception 0x%02X, want Server Device Failure", uint8(exc.ExceptionCode))
	}
}

func TestE2E_SerialLineFunctions_NotImplemented(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, e2eBasicOnly{dev}, e2eOpts{})
		ctx := context.Background()

		_, err := p.client.ReadExceptionStatus(ctx, e2eUnit)
		e2eWantException(t, "ReadExceptionStatus", err, FCReadExceptionStatus, exIllegalFunction)
		_, err = p.client.GetCommEventCounter(ctx, e2eUnit)
		e2eWantException(t, "GetCommEventCounter", err, FCGetCommEventCounters, exIllegalFunction)
		_, err = p.client.GetCommEventLog(ctx, e2eUnit)
		e2eWantException(t, "GetCommEventLog", err, FCGetCommEventLog, exIllegalFunction)
		if n := dev.callCount(); n != 0 {
			t.Errorf("handler received %d requests", n)
		}
		if _, err := p.client.ReadCoil(ctx, e2eUnit, 0); err != nil {
			t.Errorf("connection unusable afterwards: %v", err)
		}
	})
}

// Every client method for a function code our server does not serve reports
// Illegal Function, and the connection stays usable.
func TestE2E_UnservedFunctionCodes(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		ctx := context.Background()

		n := 0
		for _, op := range e2eAllOps() {
			if op.served {
				continue
			}
			n++
			err := op.call(ctx, p.client)
			e2eWantException(t, op.name, err, op.fc, exIllegalFunction)
			if got := p.client.LastObservedTransactionID(); int(got) != 2*n-1 {
				t.Errorf("%s: transaction ID %d, want %d (same connection)", op.name, got, 2*n-1)
			}
			// The same connection serves a normal request right after.
			if err := p.client.WriteRegister(ctx, e2eUnit, 9, uint16(n)); err != nil {
				t.Fatalf("WriteRegister after %s: %v", op.name, err)
			}
			if call := e2eOneCall(t, op.name, dev); call.FC != FCWriteSingleRegister {
				t.Errorf("%s reached a handler: %+v", op.name, call)
			}
		}
		if n < 18 {
			t.Errorf("only %d unserved methods exercised", n)
		}
		if p.conns() != 1 {
			t.Errorf("server connections = %d, want 1", p.conns())
		}
		if got := dev.holdingAt(9, 1)[0]; int(got) != n {
			t.Errorf("register 9 = %d, want %d", got, n)
		}
	})
}

func TestE2E_Probes(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		c := p.client
		ctx := context.Background()

		supported := map[FunctionCode]bool{
			FCReadCoils: true, FCReadDiscreteInputs: true, FCReadHoldingRegisters: true,
			FCReadInputRegisters: true, FCEncapsulatedInterface: true,
			FCDiagnostics: false, FCReportServerID: false, FCReadFIFOQueue: false, FCReadFileRecord: false,
		}
		for fc, want := range supported {
			got, err := c.SupportsFunction(ctx, e2eUnit, fc)
			if err != nil || got != want {
				t.Errorf("SupportsFunction(0x%02X) = %v, %v; want %v", uint8(fc), got, err, want)
			}
			res, err := c.ProbeFunction(ctx, e2eUnit, fc)
			if err != nil {
				t.Errorf("ProbeFunction(0x%02X): %v", uint8(fc), err)
				continue
			}
			if want {
				if res.Outcome != ProbeSupported || !res.Supported || res.ResponseFC != fc || len(res.RawPayload) == 0 {
					t.Errorf("ProbeFunction(0x%02X) = %+v, want supported", uint8(fc), res)
				}
			} else if res.Outcome != ProbeException || res.Supported || res.ExceptionCode != exIllegalFunction {
				t.Errorf("ProbeFunction(0x%02X) = %+v, want Illegal Function exception", uint8(fc), res)
			}
		}
		if ok, err := c.SupportsDeviceIdentification(ctx, e2eUnit); !ok || err != nil {
			t.Errorf("SupportsDeviceIdentification = %v, %v", ok, err)
		}
		// The probes are real requests: the handlers saw the served ones.
		kinds := map[string]int{}
		for _, call := range dev.takeCalls() {
			kinds[call.Kind]++
			if call.Kind != "devid" && (call.Addr != 0 || call.Quantity != 1) {
				t.Errorf("probe reached the handler as %+v", call)
			}
		}
		if want := map[string]int{"coils": 2, "discrete": 2, "holding": 2, "input": 2, "devid": 3}; !reflect.DeepEqual(kinds, want) {
			t.Errorf("handler calls by kind = %v, want %v", kinds, want)
		}

		// Function codes without a probe.
		for _, fc := range []FunctionCode{FCWriteSingleCoil, FCWriteMultipleRegisters, FCReadExceptionStatus, 0x65} {
			if _, err := c.SupportsFunction(ctx, e2eUnit, fc); !errors.Is(err, ErrUnexpectedParameters) {
				t.Errorf("SupportsFunction(0x%02X): %v", uint8(fc), err)
			}
			if _, err := c.ProbeFunction(ctx, e2eUnit, fc); !errors.Is(err, ErrUnexpectedParameters) {
				t.Errorf("ProbeFunction(0x%02X): %v", uint8(fc), err)
			}
		}
		if n := dev.callCount(); n != 0 {
			t.Errorf("probes without a probe definition reached the server (%d)", n)
		}

		// An exception other than Illegal Function shows the function exists.
		dev.setHook(func(context.Context, e2eCall) error { return ErrIllegalDataAddress })
		if ok, err := c.SupportsFunction(ctx, e2eUnit, FCReadInputRegisters); !ok || err != nil {
			t.Errorf("SupportsFunction with Illegal Data Address = %v, %v; want true", ok, err)
		}
		res, err := c.ProbeFunction(ctx, e2eUnit, FCReadInputRegisters)
		if err != nil || res.Outcome != ProbeException || res.ExceptionCode != exIllegalDataAddress || res.Supported {
			t.Errorf("ProbeFunction with Illegal Data Address = %+v, %v", res, err)
		}
		// A unit the device does not serve answers with a gateway exception:
		// nothing can be said about the function.
		dev.setHook(nil)
		if ok, err := c.SupportsFunction(ctx, e2eUnit+1, FCReadHoldingRegisters); ok || err != nil {
			t.Errorf("SupportsFunction on unserved unit = %v, %v; want false", ok, err)
		}
		res, err = c.ProbeFunction(ctx, e2eUnit+1, FCReadHoldingRegisters)
		if err != nil || res.Outcome != ProbeException || res.ExceptionCode != exGWPathUnavailable {
			t.Errorf("ProbeFunction on unserved unit = %+v, %v", res, err)
		}

		// A handler slower than the deadline is a probe timeout, not an error.
		release := make(chan struct{})
		dev.setHook(func(ctx context.Context, _ e2eCall) error {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return ErrServerDeviceFailure
		})
		fast := p.newClient(func(conf *Config) { conf.Timeout = 30 * time.Millisecond })
		if err := fast.Open(); err != nil {
			t.Fatal(err)
		}
		if ok, err := fast.SupportsFunction(ctx, e2eUnit, FCReadCoils); ok || err != nil {
			t.Errorf("SupportsFunction on timeout = %v, %v; want false, nil", ok, err)
		}
		res, err = fast.ProbeFunction(ctx, e2eUnit, FCReadCoils)
		if err != nil || res.Outcome != ProbeTimeout || !errors.Is(res.Err, ErrRequestTimedOut) {
			t.Errorf("ProbeFunction on timeout = %+v, %v", res, err)
		}
		close(release)
		_ = fast.Close()

		// A dropped connection is a transport error.
		dev.setHook(func(context.Context, e2eCall) error { return ErrProtocolError })
		res, err = c.ProbeFunction(ctx, e2eUnit, FCReadCoils)
		if err != nil || res.Outcome != ProbeTransportError || res.Err == nil {
			t.Errorf("ProbeFunction on dropped connection = %+v, %v", res, err)
		}
		if ok, err := c.SupportsFunction(ctx, e2eUnit, FCReadCoils); ok || err == nil {
			t.Errorf("SupportsFunction on dropped connection = %v, %v; want an error", ok, err)
		}
		// A cancelled context is returned as such without a request.
		dev.setHook(nil)
		before := dev.callCount()
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		if _, err := c.SupportsFunction(cancelled, e2eUnit, FCReadCoils); !errors.Is(err, context.Canceled) {
			t.Errorf("SupportsFunction(cancelled ctx): %v", err)
		}
		if _, err := c.ProbeFunction(cancelled, e2eUnit, FCReadCoils); !errors.Is(err, context.Canceled) {
			t.Errorf("ProbeFunction(cancelled ctx): %v", err)
		}
		if dev.callCount() != before {
			t.Error("a probe with a cancelled context reached the server")
		}
	})
}
