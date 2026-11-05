// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/otfabric/go-modbus/internal/adu"
)

// Response fields one byte wide must not wrap: data longer than the field can
// count is answered with Server Device Failure before any frame is built.

func TestServerFC0C_EventsBeyondByteCount(t *testing.T) {
	h := &serialFCHandler{}
	ms, err := NewServer(&ServerConfig{URL: "tcp://localhost:5599"}, h)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	req := &adu.Request{UnitID: 1, FunctionCode: byte(FCGetCommEventLog)}

	h.events = make([]byte, 249) // byte count 6 + 249 = 255
	res, err := ms.handleCommEventLog(context.Background(), req, 1, "", "")
	if err != nil || res.Payload[0] != 255 {
		t.Fatalf("249 events: res %v, err %v, want byte count 255", res, err)
	}

	h.events = make([]byte, 250) // byte count 256 would wrap to 0
	if _, err := ms.handleCommEventLog(context.Background(), req, 1, "", ""); !errors.Is(err, ErrServerDeviceFailure) {
		t.Errorf("250 events: err %v, want ErrServerDeviceFailure", err)
	}
}

func TestServerFC43_ValueBeyondLengthField(t *testing.T) {
	ms, err := NewServer(&ServerConfig{URL: "tcp://localhost:5599"}, &deviceIDTestHandler{})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	req := &adu.Request{UnitID: 1, FunctionCode: byte(FCEncapsulatedInterface)}

	fits := []DeviceIdentificationObject{{ID: 0x80, Value: strings.Repeat("v", 255)}}
	res, err := ms.buildIndividualDeviceIDResponse(req, 1, DeviceIDIndividual, 0x83, 0x80, fits)
	if err != nil || res.Payload[7] != 255 {
		t.Fatalf("255-byte value: res %v, err %v, want length byte 255", res, err)
	}

	// 256 would go out with a length byte of 0.
	tooLong := []DeviceIdentificationObject{{ID: 0x80, Value: strings.Repeat("v", 256)}}
	if _, err := ms.buildIndividualDeviceIDResponse(req, 1, DeviceIDIndividual, 0x83, 0x80, tooLong); !errors.Is(err, ErrServerDeviceFailure) {
		t.Errorf("individual, 256-byte value: err %v, want ErrServerDeviceFailure", err)
	}
	if _, err := ms.buildStreamDeviceIDResponse(req, 1, DeviceIDExtended, 0x83, 0x80, tooLong); !errors.Is(err, ErrServerDeviceFailure) {
		t.Errorf("stream, 256-byte value: err %v, want ErrServerDeviceFailure", err)
	}
}
