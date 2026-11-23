// SPDX-License-Identifier: MIT

package transport

import (
	"context"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/logging"
)

// The bytes a serial framing discards before a request are reported, with the
// request they were discarded for.
func TestAnomalyDiscardedBeforeRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		new  func(link RTULink) Transport
		late []byte
		// resync makes the next request drain first.
		resync func(Transport)
	}{
		{"rtu", func(l RTULink) Transport { return NewRTU(l, "", 19200, time.Second, logging.NopLogger()) },
			adu.AssembleRTUFrame(1, 3, []byte{2, 0, 9}), func(tr Transport) { tr.(*RTU).Resync() }},
		{"ascii", func(l RTULink) Transport { return NewASCII(l, time.Second, logging.NopLogger()) },
			adu.AssembleASCIIFrame(1, 3, []byte{2, 0, 9}), func(tr Transport) { tr.(*ASCII).Resync() }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, peer := tcpPair(t)
			tr := tc.new(client)
			var got []Anomaly
			var reqs []*adu.Request
			f := func(req *adu.Request, a Anomaly) { reqs = append(reqs, req); got = append(got, a) }
			switch v := tr.(type) {
			case *RTU:
				v.OnAnomaly = f
			case *ASCII:
				v.OnAnomaly = f
			}
			// A late answer on the line, then a request that must drain it.
			if _, err := peer.Write(tc.late); err != nil {
				t.Fatal(err)
			}
			time.Sleep(20 * time.Millisecond)
			tc.resync(tr)
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			req := readRegsReq(1)
			_, _ = tr.ExecuteRequest(ctx, req)
			if len(got) == 0 {
				t.Fatal("no anomaly reported for the discarded bytes")
			}
			total := 0
			for i, a := range got {
				if a.Kind != AnomalyDiscarded || reqs[i] != req {
					t.Errorf("anomaly %+v for %v, want discarded bytes for the request", a, reqs[i])
				}
				total += a.Discarded
			}
			if total != len(tc.late) {
				t.Errorf("%d bytes reported discarded, want %d", total, len(tc.late))
			}
		})
	}
}

// Nothing is reported when there is nothing to discard, or no function.
func TestReportDiscardedNothing(t *testing.T) {
	called := false
	f := func(*adu.Request, Anomaly) { called = true }
	reportDiscarded(f, &adu.Request{}, 0)
	reportDiscarded(nil, &adu.Request{}, 5)
	if called {
		t.Error("reported with nothing discarded")
	}
}
