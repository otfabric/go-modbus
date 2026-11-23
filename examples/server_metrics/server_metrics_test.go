// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/otfabric/go-modbus"
)

// The example's clients reach every hook it shows: three requests, one
// exception, one connection served and closed by its client, one refused
// for MaxClients, and the PDUs of the three requests.
func TestRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out bytes.Buffer
	s, err := run(ctx, &out)
	if err != nil {
		t.Fatal(err)
	}
	fc03, fc06 := modbus.FCReadHoldingRegisters, modbus.FCWriteSingleRegister
	if s.requests[fc03].Load() != 2 || s.responses[fc03].Load() != 1 || s.errs[fc03].Load() != 1 {
		t.Errorf("FC03: requests %d, responses %d, errors %d; want 2, 1, 1",
			s.requests[fc03].Load(), s.responses[fc03].Load(), s.errs[fc03].Load())
	}
	if s.requests[fc06].Load() != 1 || s.responses[fc06].Load() != 1 {
		t.Errorf("FC06: requests %d, responses %d; want 1, 1", s.requests[fc06].Load(), s.responses[fc06].Load())
	}
	if r, o, c := s.events[modbus.ServerConnectionRejected].Load(), s.events[modbus.ServerConnectionOpened].Load(),
		s.events[modbus.ServerConnectionClosed].Load(); r != 1 || o != 1 || c != 1 {
		t.Errorf("connection events: rejected %d, opened %d, closed %d; want 1 each", r, o, c)
	}
	if s.opened.Load() != 1 || s.open.Load() != 0 || s.closed[modbus.CloseClientClosed].Load() != 1 {
		t.Errorf("opened %d, open %d, closed by the client %d; want 1, 0, 1",
			s.opened.Load(), s.open.Load(), s.closed[modbus.CloseClientClosed].Load())
	}
	if s.rejected[modbus.RejectMaxClients].Load() != 1 || s.pdus.Load() != 3 {
		t.Errorf("rejected %d, pdus %d; want 1, 3", s.rejected[modbus.RejectMaxClients].Load(), s.pdus.Load())
	}
	if out.Len() == 0 {
		t.Error("nothing printed")
	}
}
