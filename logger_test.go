package modbus

import (
	"bytes"
	"context"
	"log"
	"testing"
)

func TestClientCustomLogger(t *testing.T) {
	var buf bytes.Buffer
	var stdl = log.New(&buf, "external-prefix: ", 0)

	_, _ = New(Config{
		Logger: NewStdLogger(stdl),
		URL:    "sometype://sometarget",
	})

	if buf.String() != "external-prefix: modbus-client(sometarget) [error]: unsupported client type 'sometype'\n" {
		t.Errorf("unexpected logger output '%s'", buf.String())
	}
}

type logTestHandler struct{}

func (logTestHandler) HandleCoils(context.Context, *CoilsRequest) ([]bool, error) { return nil, nil }
func (logTestHandler) HandleDiscreteInputs(context.Context, *DiscreteInputsRequest) ([]bool, error) {
	return nil, nil
}
func (logTestHandler) HandleHoldingRegisters(context.Context, *HoldingRegistersRequest) ([]uint16, error) {
	return nil, nil
}
func (logTestHandler) HandleInputRegisters(context.Context, *InputRegistersRequest) ([]uint16, error) {
	return nil, nil
}

func TestServerCustomLogger(t *testing.T) {
	var buf bytes.Buffer
	var stdl = log.New(&buf, "external-prefix: ", 0)

	_, _ = NewServer(&ServerConfig{
		Logger: NewStdLogger(stdl),
		URL:    "tcp://",
	}, logTestHandler{})

	if buf.String() != "external-prefix: modbus-server() [error]: missing host part in URL 'tcp://'\n" {
		t.Errorf("unexpected logger output '%s'", buf.String())
	}
}

func TestNopLogger(t *testing.T) {
	l := NopLogger()
	// All methods should be no-ops and not panic.
	l.Debugf("")
	l.Infof("")
	l.Warnf("")
	l.Errorf("")
}
