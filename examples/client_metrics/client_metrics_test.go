// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/otfabric/go-modbus"
)

// The example's calls reach every hook it shows on tcp: one outcome per
// call, one attempt per call (nothing was retried), one PDU per answer.
func TestRun(t *testing.T) {
	var out bytes.Buffer
	c, err := run(context.Background(), &out)
	if err != nil {
		t.Fatal(err)
	}
	fc03, fc06 := modbus.FCReadHoldingRegisters, modbus.FCWriteSingleRegister
	if c.requests[fc03].Load() != 2 || c.responses[fc03].Load() != 1 || c.errs[fc03].Load() != 1 {
		t.Errorf("FC03: requests %d, responses %d, errors %d; want 2, 1, 1",
			c.requests[fc03].Load(), c.responses[fc03].Load(), c.errs[fc03].Load())
	}
	if c.requests[fc06].Load() != 1 || c.responses[fc06].Load() != 1 {
		t.Errorf("FC06: requests %d, responses %d; want 1, 1", c.requests[fc06].Load(), c.responses[fc06].Load())
	}
	if c.exceptions.Load() != 1 || c.attempts.Load() != 3 || c.pdus.Load() != 3 {
		t.Errorf("exceptions %d, attempts %d, pdus %d; want 1, 3, 3", c.exceptions.Load(), c.attempts.Load(), c.pdus.Load())
	}
	// Open dialled once; the deferred Close in run closed that connection.
	if c.dials.Load() != 1 || c.failedDials.Load() != 0 || c.closes.Load() != 1 || c.lastConnection.Load() == 0 {
		t.Errorf("dials %d (failed %d), closes %d, last connection %d; want 1, 0, 1, non-zero",
			c.dials.Load(), c.failedDials.Load(), c.closes.Load(), c.lastConnection.Load())
	}
	if c.handshakes.Load() != 0 {
		t.Errorf("%d TLS handshakes on tcp", c.handshakes.Load())
	}
	if out.Len() == 0 {
		t.Error("nothing printed")
	}
}
