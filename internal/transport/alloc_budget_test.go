// SPDX-License-Identifier: MIT

package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"runtime"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/logging"
	"github.com/otfabric/go-modbus/internal/protocol"
)

// Allocation budgets of the three response readers. A length a device declares
// is checked before it sizes anything, so what one request allocates depends
// on the frame limits of the framing, not on the number the device sent. Each
// case feeds the largest length its field can hold and bounds both the number
// of allocations and the bytes allocated; the budgets leave room for the
// request frame, the fixed read buffers and the response, and are far below
// the declared sizes.

// allocatedPerRun returns the allocations and the bytes allocated by one call
// of f, averaged over runs calls, as testing.AllocsPerRun does for the count.
func allocatedPerRun(runs int, f func()) (allocs, bytes float64) {
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(1))
	f() // warm up
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range runs {
		f()
	}
	runtime.ReadMemStats(&after)
	return float64(after.Mallocs-before.Mallocs) / float64(runs),
		float64(after.TotalAlloc-before.TotalAlloc) / float64(runs)
}

func TestResponseReaders_AllocationBudget(t *testing.T) {
	const (
		maxAllocs = 64
		// Well below the 64 KiB a 16-bit length can declare.
		maxBytes = 8 << 10
	)
	mbap := func(txn, proto, length uint16, rest ...byte) []byte {
		h := []byte{byte(txn >> 8), byte(txn), byte(proto >> 8), byte(proto), byte(length >> 8), byte(length), 0x11}
		return append(h, rest...)
	}
	// Ten frames with an unknown protocol identifier, each declaring the
	// largest length: the reader gives up after ten without reading a body.
	var unknownProtocol []byte
	for range maxAnomalies {
		unknownProtocol = append(unknownProtocol, mbap(1, 7, 0xFFFF)...)
	}
	tcp := func(in []byte) func() error {
		return func() error {
			tt := NewTCP(newRecordingConn(in), time.Second, logging.NopLogger())
			_, err := tt.ExecuteRequest(context.Background(), &adu.Request{UnitID: 0x11, FunctionCode: 0x03, Payload: []byte{0, 0, 0, 1}})
			return err
		}
	}
	rtu := func(in []byte) func() error {
		return func() error {
			_, err := rawRTU(newRecordingConn(in)).ExecuteRequest(context.Background(), rtuReq())
			return err
		}
	}
	ascii := func(in []byte) func() error {
		return func() error {
			at := NewASCII(newRecordingConn(in), time.Second, logging.NopLogger())
			_, err := at.ExecuteRequest(context.Background(), asciiReq())
			return err
		}
	}
	cases := []struct {
		name string
		run  func() error
		want error // nil: the response is accepted
	}{
		{"mbap length 0xFFFF", tcp(mbap(1, 0, 0xFFFF)), protocol.ErrInvalidMBAPLength},
		{"mbap length 255, one above the maximum", tcp(mbap(1, 0, 255)), protocol.ErrInvalidMBAPLength},
		{"mbap length 0", tcp(mbap(1, 0, 0)), protocol.ErrInvalidMBAPLength},
		{"mbap unknown protocol, length 0xFFFF, ten times", tcp(unknownProtocol), protocol.ErrUnknownProtocolID},
		{"mbap length 254, the largest frame", tcp(adu.AssembleMBAP(1, 0x11, 0x03, make([]byte, 252))), nil},
		{"rtu byte count 255", rtu([]byte{0x11, 0x03, 0xFF}), protocol.ErrProtocolError},
		{"rtu byte count 252, one above what fits", rtu([]byte{0x11, 0x03, 252}), protocol.ErrProtocolError},
		{"rtu fifo byte count 0xFFFF", rtu([]byte{0x11, 0x18, 0xFF, 0xFF}), protocol.ErrProtocolError},
		{"rtu byte count 251, the largest frame", rtu(adu.AssembleRTUFrame(0x11, 0x03, append([]byte{251}, make([]byte, 251)...))), nil},
		{"ascii line of 600 characters", ascii([]byte(":" + string(bytes.Repeat([]byte{'0'}, 600)) + "\r\n")), protocol.ErrProtocolError},
		{"ascii 64 KiB after one start of frame", ascii(append([]byte{':'}, bytes.Repeat([]byte{'0'}, 64<<10)...)), protocol.ErrProtocolError},
		{"ascii 64 KiB of frame starts", ascii(bytes.Repeat([]byte{':', '0'}, 32<<10)), io.EOF},
		{"ascii the largest frame", ascii(adu.AssembleASCIIFrame(0x11, 0x03, make([]byte, 252))), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			allocs, size := allocatedPerRun(50, func() { _ = tc.run() })
			if allocs > maxAllocs || size > maxBytes {
				t.Fatalf("%.0f allocations and %.0f bytes per request, budget %d and %d", allocs, size, maxAllocs, maxBytes)
			}
			t.Logf("%.0f allocations, %.0f bytes per request", allocs, size)
		})
	}
}
