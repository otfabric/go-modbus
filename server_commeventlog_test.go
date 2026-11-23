// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"testing"
)

// TestServerFC0C_EventFieldLimit: the event field holds 0-64 bytes (V1.1b3
// section 6.10). A handler that returns more is answered with Server Device
// Failure, never with a truncated log.
func TestServerFC0C_EventFieldLimit(t *testing.T) {
	for _, n := range []int{0, 1, 63, 64, 65, 249, 1000} {
		h := &serialFCHandler{status: 0x1234, eventCount: 0x0102, messageCount: 0x0304, events: make([]byte, n)}
		for i := range h.events {
			h.events[i] = byte(i)
		}
		tr, _ := serveOne(t, h, FCGetCommEventLog)
		if len(tr.responses) != 1 || tr.closed {
			t.Fatalf("%d events: responses %+v, closed %v", n, tr.responses, tr.closed)
		}
		res := tr.responses[0]
		if n > 64 {
			if res.FunctionCode != 0x8C || !bytes.Equal(res.Payload, []byte{byte(exServerDeviceFailure)}) {
				t.Errorf("%d events: got FC 0x%02X % X, want Server Device Failure", n, res.FunctionCode, res.Payload)
			}
			continue
		}
		want := append([]byte{byte(6 + n), 0x12, 0x34, 0x01, 0x02, 0x03, 0x04}, h.events...)
		if res.FunctionCode != 0x0C || !bytes.Equal(res.Payload, want) {
			t.Errorf("%d events: got FC 0x%02X % X, want % X", n, res.FunctionCode, res.Payload, want)
		}
	}
}

// TestServerFC0C_SpecExample replays the worked example of V1.1b3 section
// 6.10: status 0, event count 264, message count 289, events 20 00.
func TestServerFC0C_SpecExample(t *testing.T) {
	h := &serialFCHandler{status: 0x0000, eventCount: 0x0108, messageCount: 0x0121, events: []byte{0x20, 0x00}}
	tr, _ := serveOne(t, h, FCGetCommEventLog)
	want := []byte{0x08, 0x00, 0x00, 0x01, 0x08, 0x01, 0x21, 0x20, 0x00}
	if len(tr.responses) != 1 || tr.responses[0].FunctionCode != 0x0C || !bytes.Equal(tr.responses[0].Payload, want) {
		t.Fatalf("responses %+v, want 0C % X", tr.responses, want)
	}
}
