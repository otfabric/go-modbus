package modbus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// This file provides the shared harness for the back-to-back client-vs-server
// conformance suite: a reference in-memory device implementing every server
// handler interface, helpers to spin up a connected client/server pair over
// both TCP and TCP+TLS, and small invariant assertion helpers.

const (
	// refUnitID is the only unit ID the reference device serves; requests for
	// other unit IDs return ErrIllegalFunction (used to exercise that path).
	refUnitID uint8 = 1
	// refSpace is the number of coils / discrete inputs / registers the
	// reference device exposes at addresses [0, refSpace).
	refSpace int = 512
)

// refDevice is a reference in-memory Modbus device implementing RequestHandler
// plus every optional handler interface. It is the known-correct peer that our
// own client is tested against. It is safe for concurrent use.
type refDevice struct {
	mu             sync.RWMutex
	coils          []bool
	discreteInputs []bool
	holding        []uint16
	input          []uint16

	// FC43 device identification.
	devObjects []DeviceIdentificationObject
	conformity uint8

	// Serial-line FC state (FC07/0B/0C).
	exceptionStatus uint8
	commStatus      uint16
	eventCount      uint16
	messageCount    uint16
	events          []byte
}

// newRefDevice returns a reference device with allocated address space and a
// default device-identification object set.
func newRefDevice() *refDevice {
	return &refDevice{
		coils:           make([]bool, refSpace),
		discreteInputs:  make([]bool, refSpace),
		holding:         make([]uint16, refSpace),
		input:           make([]uint16, refSpace),
		devObjects:      regularDeviceIDObjects(),
		conformity:      0x83,
		exceptionStatus: 0x5A,
		commStatus:      0xFFFF,
		eventCount:      42,
		messageCount:    99,
		events:          []byte{0x20, 0x00},
	}
}

func (d *refDevice) HandleCoils(_ context.Context, req *CoilsRequest) ([]bool, error) {
	if req.UnitID != refUnitID {
		return nil, ErrIllegalFunction
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if int(req.Addr)+int(req.Quantity) > len(d.coils) {
		return nil, ErrIllegalDataAddress
	}
	if req.IsWrite {
		for i := 0; i < int(req.Quantity); i++ {
			d.coils[int(req.Addr)+i] = req.Args[i]
		}
	}
	res := make([]bool, req.Quantity)
	for i := 0; i < int(req.Quantity); i++ {
		res[i] = d.coils[int(req.Addr)+i]
	}
	return res, nil
}

func (d *refDevice) HandleDiscreteInputs(_ context.Context, req *DiscreteInputsRequest) ([]bool, error) {
	if req.UnitID != refUnitID {
		return nil, ErrIllegalFunction
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if int(req.Addr)+int(req.Quantity) > len(d.discreteInputs) {
		return nil, ErrIllegalDataAddress
	}
	res := make([]bool, req.Quantity)
	for i := 0; i < int(req.Quantity); i++ {
		res[i] = d.discreteInputs[int(req.Addr)+i]
	}
	return res, nil
}

func (d *refDevice) HandleHoldingRegisters(_ context.Context, req *HoldingRegistersRequest) ([]uint16, error) {
	if req.UnitID != refUnitID {
		return nil, ErrIllegalFunction
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if int(req.Addr)+int(req.Quantity) > len(d.holding) {
		return nil, ErrIllegalDataAddress
	}
	if req.IsWrite {
		for i := 0; i < int(req.Quantity); i++ {
			d.holding[int(req.Addr)+i] = req.Args[i]
		}
	}
	res := make([]uint16, req.Quantity)
	for i := 0; i < int(req.Quantity); i++ {
		res[i] = d.holding[int(req.Addr)+i]
	}
	return res, nil
}

func (d *refDevice) HandleInputRegisters(_ context.Context, req *InputRegistersRequest) ([]uint16, error) {
	if req.UnitID != refUnitID {
		return nil, ErrIllegalFunction
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if int(req.Addr)+int(req.Quantity) > len(d.input) {
		return nil, ErrIllegalDataAddress
	}
	res := make([]uint16, req.Quantity)
	for i := 0; i < int(req.Quantity); i++ {
		res[i] = d.input[int(req.Addr)+i]
	}
	return res, nil
}

func (d *refDevice) HandleMaskWrite(_ context.Context, req *MaskWriteRequest) error {
	if req.UnitID != refUnitID {
		return ErrIllegalFunction
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if int(req.Addr) >= len(d.holding) {
		return ErrIllegalDataAddress
	}
	// Modbus FC22 semantics: result = (value AND andMask) OR (orMask AND (NOT andMask)).
	cur := d.holding[req.Addr]
	d.holding[req.Addr] = (cur & req.AndMask) | (req.OrMask &^ req.AndMask)
	return nil
}

func (d *refDevice) HandleReadWriteRegisters(_ context.Context, req *ReadWriteRegistersRequest) ([]uint16, error) {
	if req.UnitID != refUnitID {
		return nil, ErrIllegalFunction
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if int(req.WriteAddr)+len(req.WriteValues) > len(d.holding) {
		return nil, ErrIllegalDataAddress
	}
	if int(req.ReadAddr)+int(req.ReadQty) > len(d.holding) {
		return nil, ErrIllegalDataAddress
	}
	// Spec: the write is performed before the read.
	for i := range req.WriteValues {
		d.holding[int(req.WriteAddr)+i] = req.WriteValues[i]
	}
	res := make([]uint16, req.ReadQty)
	for i := 0; i < int(req.ReadQty); i++ {
		res[i] = d.holding[int(req.ReadAddr)+i]
	}
	return res, nil
}

func (d *refDevice) HandleDeviceIdentification(_ context.Context, req *DeviceIdentificationRequest) (*DeviceIdentificationResponse, error) {
	if req.UnitID != refUnitID {
		return nil, ErrIllegalFunction
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	objs := make([]DeviceIdentificationObject, len(d.devObjects))
	copy(objs, d.devObjects)
	return &DeviceIdentificationResponse{ConformityLevel: d.conformity, Objects: objs}, nil
}

func (d *refDevice) HandleExceptionStatus(_ context.Context, req *ExceptionStatusRequest) (uint8, error) {
	if req.UnitID != refUnitID {
		return 0, ErrIllegalFunction
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.exceptionStatus, nil
}

func (d *refDevice) HandleCommEventCounter(_ context.Context, req *CommEventCounterRequest) (*CommEventCounterResponse, error) {
	if req.UnitID != refUnitID {
		return nil, ErrIllegalFunction
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return &CommEventCounterResponse{Status: d.commStatus, EventCount: d.eventCount}, nil
}

func (d *refDevice) HandleCommEventLog(_ context.Context, req *CommEventLogRequest) (*CommEventLogResponse, error) {
	if req.UnitID != refUnitID {
		return nil, ErrIllegalFunction
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	ev := make([]byte, len(d.events))
	copy(ev, d.events)
	return &CommEventLogResponse{
		Status:       d.commStatus,
		EventCount:   d.eventCount,
		MessageCount: d.messageCount,
		Events:       ev,
	}, nil
}

// transports is the transport matrix every conformance test runs over.
var transports = []string{"tcp", "tcp+tls"}

// forEachTransport runs run as a subtest for each transport in the matrix.
func forEachTransport(t *testing.T, run func(t *testing.T, kind string)) {
	t.Helper()
	for _, kind := range transports {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			run(t, kind)
		})
	}
}

// startPair starts a server backed by handler and returns a connected client
// plus a cleanup function. kind is "tcp" or "tcp+tls".
func startPair(t *testing.T, kind string, handler RequestHandler) (*Client, func()) {
	t.Helper()

	// Grab a free ephemeral port, then hand the address to the server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		_ = ln.Close()
		t.Fatalf("SplitHostPort: %v", err)
	}
	_ = ln.Close()

	var serverConf ServerConfig
	var clientConf Config

	switch kind {
	case "tcp":
		serverConf = ServerConfig{URL: "tcp://127.0.0.1:" + port, MaxClients: 16}
		clientConf = Config{URL: "tcp://127.0.0.1:" + port, Timeout: 2 * time.Second}
	case "tcp+tls":
		serverKeyPair, err := tls.X509KeyPair([]byte(serverCert), []byte(serverKey))
		if err != nil {
			t.Fatalf("server keypair: %v", err)
		}
		clientKeyPair, err := tls.X509KeyPair([]byte(clientCert), []byte(clientKey))
		if err != nil {
			t.Fatalf("client keypair: %v", err)
		}
		serverPool := x509.NewCertPool()
		if !serverPool.AppendCertsFromPEM([]byte(clientCert)) {
			t.Fatal("append client cert to server pool")
		}
		clientPool := x509.NewCertPool()
		if !clientPool.AppendCertsFromPEM([]byte(serverCert)) {
			t.Fatal("append server cert to client pool")
		}
		serverConf = ServerConfig{
			URL:           "tcp+tls://127.0.0.1:" + port,
			MaxClients:    16,
			TLSServerCert: &serverKeyPair,
			TLSClientCAs:  serverPool,
		}
		// Connect via "localhost" so the server certificate SAN validates.
		clientConf = Config{
			URL:           "tcp+tls://localhost:" + port,
			Timeout:       2 * time.Second,
			TLSClientCert: &clientKeyPair,
			TLSRootCAs:    clientPool,
		}
	default:
		t.Fatalf("unknown transport %q", kind)
	}

	server, err := NewServer(&serverConf, handler)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	client, err := New(clientConf)
	if err != nil {
		_ = server.Stop()
		t.Fatalf("New client: %v", err)
	}
	if err := client.Open(); err != nil {
		_ = server.Stop()
		t.Fatalf("Open: %v", err)
	}

	cleanup := func() {
		_ = client.Close()
		_ = server.Stop()
	}
	return client, cleanup
}

// sendRawFC sends an arbitrary function code and payload via the client's
// low-level request path, returning the raw response for invariant checks.
func sendRawFC(t *testing.T, client *Client, unitID uint8, fc byte, payload []byte) *adu.Response {
	t.Helper()
	res, err := client.executeRequest(context.Background(), &adu.Request{
		UnitID:       unitID,
		FunctionCode: fc,
		Payload:      payload,
	})
	if err != nil {
		t.Fatalf("executeRequest(fc=0x%02x): %v", fc, err)
	}
	return res
}

// assertMBAPResponseWellFormed checks the invariants every server response must
// satisfy: matching unit ID, and either an echoed FC or an exception FC with a
// single valid exception-code byte.
func assertMBAPResponseWellFormed(t *testing.T, res *adu.Response, reqUnitID, reqFC byte) {
	t.Helper()
	if res.UnitID != reqUnitID {
		t.Errorf("response unit ID = 0x%02x, want 0x%02x", res.UnitID, reqUnitID)
	}
	if res.FunctionCode&0x80 != 0 {
		// Exception response FC is the request FC with the exception bit set.
		if res.FunctionCode != reqFC|0x80 {
			t.Errorf("exception FC = 0x%02x, want 0x%02x", res.FunctionCode, reqFC|0x80)
		}
		if len(res.Payload) != 1 {
			t.Errorf("exception payload = %v, want exactly 1 byte", res.Payload)
			return
		}
		if !validExceptionCode(res.Payload[0]) {
			t.Errorf("invalid exception code 0x%02x", res.Payload[0])
		}
		return
	}
	if res.FunctionCode != reqFC {
		t.Errorf("response FC = 0x%02x, want echoed 0x%02x", res.FunctionCode, reqFC)
	}
}

// validExceptionCode reports whether b is a defined Modbus exception code.
func validExceptionCode(b byte) bool {
	switch ExceptionCode(b) {
	case exIllegalFunction, exIllegalDataAddress, exIllegalDataValue,
		exServerDeviceFailure, exAcknowledge, exServerDeviceBusy,
		exMemoryParityError, exGWPathUnavailable, exGWTargetFailedToRespond:
		return true
	default:
		return false
	}
}

// readMBAPResponse reads one full MBAP response frame from conn and parses it.
// Used by the fuzz harness to validate raw server responses.
func readMBAPResponse(conn net.Conn) (*adu.Response, error) {
	header := make([]byte, adu.MBAPHeaderLength)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	txnID, unitID, mbapLen, err := adu.ParseMBAPHeader(header)
	if err != nil {
		return nil, err
	}
	body := make([]byte, mbapLen-1)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	return &adu.Response{
		UnitID:        unitID,
		FunctionCode:  body[0],
		Payload:       body[1:],
		TransactionID: txnID,
	}, nil
}
