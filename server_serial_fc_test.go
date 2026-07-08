// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"testing"
	"time"
)

// serialFCHandler implements all four required methods plus the optional serial-line FC handlers.
type serialFCHandler struct {
	exceptionStatus uint8
	status          uint16
	eventCount      uint16
	messageCount    uint16
	events          []byte
}

func (h *serialFCHandler) HandleCoils(_ context.Context, _ *CoilsRequest) ([]bool, error) {
	return nil, ErrIllegalFunction
}
func (h *serialFCHandler) HandleDiscreteInputs(_ context.Context, _ *DiscreteInputsRequest) ([]bool, error) {
	return nil, ErrIllegalFunction
}
func (h *serialFCHandler) HandleHoldingRegisters(_ context.Context, _ *HoldingRegistersRequest) ([]uint16, error) {
	return nil, ErrIllegalFunction
}
func (h *serialFCHandler) HandleInputRegisters(_ context.Context, _ *InputRegistersRequest) ([]uint16, error) {
	return nil, ErrIllegalFunction
}

func (h *serialFCHandler) HandleExceptionStatus(_ context.Context, _ *ExceptionStatusRequest) (uint8, error) {
	return h.exceptionStatus, nil
}

func (h *serialFCHandler) HandleCommEventCounter(_ context.Context, _ *CommEventCounterRequest) (*CommEventCounterResponse, error) {
	return &CommEventCounterResponse{
		Status:     h.status,
		EventCount: h.eventCount,
	}, nil
}

func (h *serialFCHandler) HandleCommEventLog(_ context.Context, _ *CommEventLogRequest) (*CommEventLogResponse, error) {
	return &CommEventLogResponse{
		Status:       h.status,
		EventCount:   h.eventCount,
		MessageCount: h.messageCount,
		Events:       h.events,
	}, nil
}

func TestServerFC07_ExceptionStatus(t *testing.T) {
	h := &serialFCHandler{exceptionStatus: 0x6D}
	server, err := NewServer(&ServerConfig{URL: "tcp://localhost:5520", MaxClients: 1}, h)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = server.Stop() }()

	client, err := New(Config{URL: "tcp://localhost:5520", Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	status, err := client.ReadExceptionStatus(context.Background(), 1)
	if err != nil {
		t.Fatalf("ReadExceptionStatus: %v", err)
	}
	if status != 0x6D {
		t.Errorf("status=0x%02X, want 0x6D", status)
	}
}

func TestServerFC0B_CommEventCounter(t *testing.T) {
	h := &serialFCHandler{status: 0xFFFF, eventCount: 264}
	server, err := NewServer(&ServerConfig{URL: "tcp://localhost:5521", MaxClients: 1}, h)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = server.Stop() }()

	client, err := New(Config{URL: "tcp://localhost:5521", Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	cr, err := client.GetCommEventCounter(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetCommEventCounter: %v", err)
	}
	if cr.Status != 0xFFFF || cr.EventCount != 264 {
		t.Errorf("Status=0x%04X EventCount=%d", cr.Status, cr.EventCount)
	}
}

func TestServerFC0C_CommEventLog(t *testing.T) {
	h := &serialFCHandler{
		status:       0x0000,
		eventCount:   264,
		messageCount: 289,
		events:       []byte{0x20, 0x00},
	}
	server, err := NewServer(&ServerConfig{URL: "tcp://localhost:5522", MaxClients: 1}, h)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = server.Stop() }()

	client, err := New(Config{URL: "tcp://localhost:5522", Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	cl, err := client.GetCommEventLog(context.Background(), 1)
	if err != nil {
		t.Fatalf("GetCommEventLog: %v", err)
	}
	if cl.Status != 0x0000 || cl.EventCount != 264 || cl.MessageCount != 289 {
		t.Errorf("Status=0x%04X EventCount=%d MessageCount=%d", cl.Status, cl.EventCount, cl.MessageCount)
	}
	if len(cl.Events) != 2 || cl.Events[0] != 0x20 || cl.Events[1] != 0x00 {
		t.Errorf("Events=%v, want [0x20 0x00]", cl.Events)
	}
}

// noSerialFCHandler does NOT implement the optional serial FC interfaces.
type noSerialFCHandler struct{}

func (h *noSerialFCHandler) HandleCoils(_ context.Context, _ *CoilsRequest) ([]bool, error) {
	return nil, ErrIllegalFunction
}
func (h *noSerialFCHandler) HandleDiscreteInputs(_ context.Context, _ *DiscreteInputsRequest) ([]bool, error) {
	return nil, ErrIllegalFunction
}
func (h *noSerialFCHandler) HandleHoldingRegisters(_ context.Context, _ *HoldingRegistersRequest) ([]uint16, error) {
	return nil, ErrIllegalFunction
}
func (h *noSerialFCHandler) HandleInputRegisters(_ context.Context, _ *InputRegistersRequest) ([]uint16, error) {
	return nil, ErrIllegalFunction
}

func TestServerFC07_IllegalFunction_WhenNotImplemented(t *testing.T) {
	server, err := NewServer(&ServerConfig{URL: "tcp://localhost:5523", MaxClients: 1}, &noSerialFCHandler{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = server.Stop() }()

	client, err := New(Config{URL: "tcp://localhost:5523", Timeout: time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()

	_, err = client.ReadExceptionStatus(context.Background(), 1)
	if !errors.Is(err, ErrIllegalFunction) {
		t.Errorf("want ErrIllegalFunction, got %v", err)
	}
}
