// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// nilResponseHandler implements every handler interface and answers each
// request with a nil response pointer or slice and no error.
type nilResponseHandler struct{}

func (nilResponseHandler) HandleCoils(context.Context, *CoilsRequest) ([]bool, error) {
	return nil, nil
}
func (nilResponseHandler) HandleDiscreteInputs(context.Context, *DiscreteInputsRequest) ([]bool, error) {
	return nil, nil
}
func (nilResponseHandler) HandleHoldingRegisters(context.Context, *HoldingRegistersRequest) ([]uint16, error) {
	return nil, nil
}
func (nilResponseHandler) HandleInputRegisters(context.Context, *InputRegistersRequest) ([]uint16, error) {
	return nil, nil
}
func (nilResponseHandler) HandleReadWriteRegisters(context.Context, *ReadWriteRegistersRequest) ([]uint16, error) {
	return nil, nil
}
func (nilResponseHandler) HandleExceptionStatus(context.Context, *ExceptionStatusRequest) (uint8, error) {
	return 0, nil
}
func (nilResponseHandler) HandleCommEventCounter(context.Context, *CommEventCounterRequest) (*CommEventCounterResponse, error) {
	return nil, nil
}
func (nilResponseHandler) HandleCommEventLog(context.Context, *CommEventLogRequest) (*CommEventLogResponse, error) {
	return nil, nil
}
func (nilResponseHandler) HandleDeviceIdentification(context.Context, *DeviceIdentificationRequest) (*DeviceIdentificationResponse, error) {
	return nil, nil
}
func (nilResponseHandler) HandleDiagnostics(context.Context, *DiagnosticsRequest) ([]byte, error) {
	return nil, nil
}
func (nilResponseHandler) HandleReportServerID(context.Context, *ReportServerIDRequest) ([]byte, error) {
	return nil, nil
}
func (nilResponseHandler) HandleReadFileRecords(context.Context, *ReadFileRecordsRequest) ([][]uint16, error) {
	return nil, nil
}
func (nilResponseHandler) HandleReadFIFOQueue(context.Context, *FIFOQueueRequest) ([]uint16, error) {
	return nil, nil
}

// lockedBuffer is a bytes.Buffer safe for the server goroutines to log into.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A nil response from a handler is answered explicitly: Server Device Failure
// where the response cannot be built from nil, a normal answer where nil is an
// empty, valid result. Neither may go through the panic recovery.
func TestServer_NilHandlerResponses(t *testing.T) {
	var logs lockedBuffer
	p := e2eStart(t, "tcp", nilResponseHandler{}, e2eOpts{server: func(c *ServerConfig) {
		c.Logger = NewSlogLogger(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}})
	ctx := context.Background()
	c := p.client

	for _, tc := range []struct {
		fc      FunctionCode
		call    func() error
		failure bool // Server Device Failure; otherwise a normal answer
	}{
		{FCReadCoils, func() error { _, err := c.ReadCoils(ctx, e2eUnit, 0, 1); return err }, true},
		{FCReadDiscreteInputs, func() error { _, err := c.ReadDiscreteInputs(ctx, e2eUnit, 0, 1); return err }, true},
		{FCReadHoldingRegisters, func() error { _, err := c.ReadHoldingRegisters(ctx, e2eUnit, 0, 1); return err }, true},
		{FCReadInputRegisters, func() error { _, err := c.ReadInputRegisters(ctx, e2eUnit, 0, 1); return err }, true},
		{FCReadExceptionStatus, func() error { _, err := c.ReadExceptionStatus(ctx, e2eUnit); return err }, false},
		{FCDiagnostics, func() error { _, err := c.Diagnostics(ctx, e2eUnit, DiagReturnQueryData, nil); return err }, false},
		{FCGetCommEventCounters, func() error { _, err := c.GetCommEventCounter(ctx, e2eUnit); return err }, true},
		{FCGetCommEventLog, func() error { _, err := c.GetCommEventLog(ctx, e2eUnit); return err }, true},
		{FCReportServerID, func() error { _, err := c.ReportServerID(ctx, e2eUnit); return err }, false},
		{FCReadFileRecord, func() error {
			_, err := c.ReadFileRecords(ctx, e2eUnit, []FileRecordRequest{{FileNumber: 1, RecordLength: 1}})
			return err
		}, true},
		{FCReadWriteMultipleRegs, func() error {
			_, err := c.ReadWriteMultipleRegisters(ctx, e2eUnit, 0, 1, 0, []uint16{1})
			return err
		}, true},
		{FCReadFIFOQueue, func() error { _, err := c.ReadFIFOQueue(ctx, e2eUnit, 0); return err }, false},
		{FCEncapsulatedInterface, func() error {
			_, err := c.ReadDeviceIdentification(ctx, e2eUnit, DeviceIDBasic, 0)
			return err
		}, true},
	} {
		err := tc.call()
		switch {
		case tc.failure:
			e2eWantException(t, tc.fc.String(), err, tc.fc, exServerDeviceFailure)
		case err != nil:
			t.Errorf("%s: %v, want a normal answer", tc.fc, err)
		}
	}

	if out := logs.String(); strings.Contains(out, "panic") || strings.Contains(out, "goroutine ") {
		t.Errorf("a nil handler response went through the panic recovery:\n%s", out)
	}
	if p.conns() != 1 {
		t.Errorf("server connections = %d, want 1", p.conns())
	}
}
