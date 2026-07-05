package modbus

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// deviceIDTestHandler implements RequestHandler plus DeviceIdentificationHandler.
type deviceIDTestHandler struct {
	objects    []DeviceIdentificationObject
	conformity uint8
	handlerErr error
}

func (h *deviceIDTestHandler) HandleCoils(ctx context.Context, req *CoilsRequest) ([]bool, error) {
	return nil, ErrIllegalFunction
}
func (h *deviceIDTestHandler) HandleDiscreteInputs(ctx context.Context, req *DiscreteInputsRequest) ([]bool, error) {
	return nil, ErrIllegalFunction
}
func (h *deviceIDTestHandler) HandleHoldingRegisters(ctx context.Context, req *HoldingRegistersRequest) ([]uint16, error) {
	return nil, ErrIllegalFunction
}
func (h *deviceIDTestHandler) HandleInputRegisters(ctx context.Context, req *InputRegistersRequest) ([]uint16, error) {
	return nil, ErrIllegalFunction
}

func (h *deviceIDTestHandler) HandleDeviceIdentification(ctx context.Context, req *DeviceIdentificationRequest) (*DeviceIdentificationResponse, error) {
	if h.handlerErr != nil {
		return nil, h.handlerErr
	}
	return &DeviceIdentificationResponse{
		ConformityLevel: h.conformity,
		Objects:         h.objects,
	}, nil
}

func basicDeviceIDObjects() []DeviceIdentificationObject {
	return []DeviceIdentificationObject{
		{ID: 0x00, Name: "VendorName", Value: "otfabric"},
		{ID: 0x01, Name: "ProductCode", Value: "GW-1000"},
		{ID: 0x02, Name: "MajorMinorRevision", Value: "1.2.3"},
	}
}

func regularDeviceIDObjects() []DeviceIdentificationObject {
	return append(basicDeviceIDObjects(),
		DeviceIdentificationObject{ID: 0x03, Name: "VendorUrl", Value: "https://otfabric.example"},
		DeviceIdentificationObject{ID: 0x04, Name: "ProductName", Value: "Test Gateway"},
		DeviceIdentificationObject{ID: 0x05, Name: "ModelName", Value: "TG-X"},
	)
}

// startDeviceIDServer starts a TCP server with the given handler and returns a
// connected client plus a cleanup function.
func startDeviceIDServer(t *testing.T, url string, handler RequestHandler) (*Client, func()) {
	t.Helper()

	server, err := NewServer(&ServerConfig{URL: url, MaxClients: 4}, handler)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	if err = server.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}

	client, err := New(Config{URL: url, Timeout: 2 * time.Second})
	if err != nil {
		_ = server.Stop()
		t.Fatalf("failed to create client: %v", err)
	}
	if err = client.Open(); err != nil {
		_ = server.Stop()
		t.Fatalf("failed to open client: %v", err)
	}

	cleanup := func() {
		_ = client.Close()
		_ = server.Stop()
	}
	return client, cleanup
}

func objectValue(di *DeviceIdentification, id DeviceIDObjectID) (string, bool) {
	for _, o := range di.Objects {
		if o.ID == id {
			return o.Value, true
		}
	}
	return "", false
}

func TestServerFC43_BasicStream(t *testing.T) {
	h := &deviceIDTestHandler{objects: regularDeviceIDObjects()}
	client, cleanup := startDeviceIDServer(t, "tcp://localhost:5551", h)
	defer cleanup()

	di, err := client.ReadDeviceIdentification(context.Background(), 1, DeviceIDBasic, 0x00)
	if err != nil {
		t.Fatalf("ReadDeviceIdentification(basic): %v", err)
	}
	if di.Category != DeviceIDBasic {
		t.Errorf("category = 0x%02x, want basic", uint8(di.Category))
	}
	// Basic must return exactly the 3 mandatory objects (0x00-0x02).
	if len(di.Objects) != 3 {
		t.Fatalf("basic returned %d objects, want 3", len(di.Objects))
	}
	if v, _ := objectValue(di, 0x00); v != "otfabric" {
		t.Errorf("VendorName = %q, want otfabric", v)
	}
	if _, ok := objectValue(di, 0x03); ok {
		t.Errorf("basic must not include object 0x03")
	}
}

func TestServerFC43_ExtendedStreamAllObjects(t *testing.T) {
	h := &deviceIDTestHandler{objects: regularDeviceIDObjects()}
	client, cleanup := startDeviceIDServer(t, "tcp://localhost:5552", h)
	defer cleanup()

	di, err := client.ReadAllDeviceIdentification(context.Background(), 1)
	if err != nil {
		t.Fatalf("ReadAllDeviceIdentification: %v", err)
	}
	if len(di.Objects) != 6 {
		t.Fatalf("extended returned %d objects, want 6", len(di.Objects))
	}
	if v, _ := objectValue(di, 0x05); v != "TG-X" {
		t.Errorf("ModelName = %q, want TG-X", v)
	}
}

func TestServerFC43_Pagination(t *testing.T) {
	// Two large extended objects that cannot share a single response, forcing
	// MoreFollows pagination. The client transparently pages and concatenates.
	big1 := strings.Repeat("A", 240)
	big2 := strings.Repeat("B", 240)
	objs := append(basicDeviceIDObjects(),
		DeviceIdentificationObject{ID: 0x80, Value: big1},
		DeviceIdentificationObject{ID: 0x81, Value: big2},
	)
	h := &deviceIDTestHandler{objects: objs}
	client, cleanup := startDeviceIDServer(t, "tcp://localhost:5553", h)
	defer cleanup()

	di, err := client.ReadDeviceIdentification(context.Background(), 1, DeviceIDExtended, 0x00)
	if err != nil {
		t.Fatalf("ReadDeviceIdentification(extended): %v", err)
	}
	if len(di.Objects) != 5 {
		t.Fatalf("extended returned %d objects, want 5", len(di.Objects))
	}
	if v, _ := objectValue(di, 0x80); v != big1 {
		t.Errorf("object 0x80 value mismatch (len %d)", len(v))
	}
	if v, _ := objectValue(di, 0x81); v != big2 {
		t.Errorf("object 0x81 value mismatch (len %d)", len(v))
	}
}

func TestServerFC43_IndividualHit(t *testing.T) {
	h := &deviceIDTestHandler{objects: regularDeviceIDObjects()}
	client, cleanup := startDeviceIDServer(t, "tcp://localhost:5554", h)
	defer cleanup()

	di, err := client.ReadDeviceIdentification(context.Background(), 1, DeviceIDIndividual, 0x01)
	if err != nil {
		t.Fatalf("ReadDeviceIdentification(individual): %v", err)
	}
	if len(di.Objects) != 1 {
		t.Fatalf("individual returned %d objects, want 1", len(di.Objects))
	}
	if di.Objects[0].ID != 0x01 || di.Objects[0].Value != "GW-1000" {
		t.Errorf("individual object = %+v, want ProductCode GW-1000", di.Objects[0])
	}
}

func TestServerFC43_IndividualMiss(t *testing.T) {
	h := &deviceIDTestHandler{objects: basicDeviceIDObjects()}
	client, cleanup := startDeviceIDServer(t, "tcp://localhost:5555", h)
	defer cleanup()

	_, err := client.ReadDeviceIdentification(context.Background(), 1, DeviceIDIndividual, 0x42)
	if err == nil {
		t.Fatal("individual access to unknown object should fail")
	}
	if !errors.Is(err, ErrIllegalDataAddress) {
		t.Errorf("got %v, want ErrIllegalDataAddress", err)
	}
}

func TestServerFC43_IllegalCategory(t *testing.T) {
	h := &deviceIDTestHandler{objects: basicDeviceIDObjects()}
	client, cleanup := startDeviceIDServer(t, "tcp://localhost:5556", h)
	defer cleanup()

	// The client validates the category, so send a raw FC43 request with an
	// out-of-range Read Device ID code (0x05) to exercise server validation.
	res, err := client.executeRequest(context.Background(), &adu.Request{
		UnitID:       1,
		FunctionCode: byte(FCEncapsulatedInterface),
		Payload:      []byte{byte(MEIReadDeviceIdentification), 0x05, 0x00},
	})
	if err != nil {
		t.Fatalf("executeRequest: %v", err)
	}
	assertExceptionResponse(t, res, exIllegalDataValue)
}

func TestServerFC43_WrongMEIType(t *testing.T) {
	h := &deviceIDTestHandler{objects: basicDeviceIDObjects()}
	client, cleanup := startDeviceIDServer(t, "tcp://localhost:5557", h)
	defer cleanup()

	// MEI type 0x0D (CANopen) is not supported.
	res, err := client.executeRequest(context.Background(), &adu.Request{
		UnitID:       1,
		FunctionCode: byte(FCEncapsulatedInterface),
		Payload:      []byte{0x0D, byte(DeviceIDBasic), 0x00},
	})
	if err != nil {
		t.Fatalf("executeRequest: %v", err)
	}
	assertExceptionResponse(t, res, exIllegalFunction)
}

func TestServerFC43_HandlerNotImplemented(t *testing.T) {
	// tcpTestHandler does not implement DeviceIdentificationHandler.
	client, cleanup := startDeviceIDServer(t, "tcp://localhost:5558", &tcpTestHandler{})
	defer cleanup()

	res, err := client.executeRequest(context.Background(), &adu.Request{
		UnitID:       1,
		FunctionCode: byte(FCEncapsulatedInterface),
		Payload:      []byte{byte(MEIReadDeviceIdentification), byte(DeviceIDBasic), 0x00},
	})
	if err != nil {
		t.Fatalf("executeRequest: %v", err)
	}
	assertExceptionResponse(t, res, exIllegalFunction)
}

func assertExceptionResponse(t *testing.T, res *adu.Response, want ExceptionCode) {
	t.Helper()
	if res.FunctionCode&0x80 == 0 {
		t.Fatalf("expected exception response, got FC 0x%02x", res.FunctionCode)
	}
	if len(res.Payload) != 1 {
		t.Fatalf("exception payload = %v, want 1 byte", res.Payload)
	}
	if ExceptionCode(res.Payload[0]) != want {
		t.Errorf("exception code = 0x%02x, want 0x%02x", res.Payload[0], uint8(want))
	}
}
