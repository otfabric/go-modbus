// SPDX-License-Identifier: MIT

package transport

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/logging"
	"github.com/otfabric/go-modbus/internal/protocol"
)

func TestRequestDeadline(t *testing.T) {
	const timeout = time.Second
	near := func(got, want time.Time) bool { return got.Sub(want).Abs() < 100*time.Millisecond }

	if got := requestDeadline(context.Background(), timeout); !near(got, time.Now().Add(timeout)) {
		t.Errorf("no context deadline: %v from now, want the timeout", time.Until(got))
	}

	long, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	if got := requestDeadline(long, timeout); !near(got, time.Now().Add(timeout)) {
		t.Errorf("long context deadline: %v from now, want the timeout", time.Until(got))
	}

	short, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	d, _ := short.Deadline()
	if got := requestDeadline(short, timeout); !got.Equal(d) {
		t.Errorf("short context deadline: %v, want the context's %v", got, d)
	}

	if got := requestDeadline(long, 0); got.Before(time.Now().Add(59 * time.Minute)) {
		t.Errorf("zero timeout with a context deadline: %v from now, want the context's", time.Until(got))
	}
	if got := requestDeadline(context.Background(), 0); !near(got, time.Now()) {
		t.Errorf("zero timeout without a context deadline: %v from now, want now", time.Until(got))
	}
}

// TestExecuteRequest_TimeoutUnderLongDeadline checks, for every transport,
// that the configured timeout ends a request whose context has a much later
// deadline.
func TestExecuteRequest_TimeoutUnderLongDeadline(t *testing.T) {
	const timeout = 50 * time.Millisecond
	transports := []struct {
		name string
		new  func(link net.Conn) Transport
		req  *adu.Request
	}{
		{"tcp", func(l net.Conn) Transport { return NewTCP(l, timeout, logging.NopLogger()) },
			&adu.Request{UnitID: 1, FunctionCode: byte(protocol.FCReadHoldingRegisters), Payload: []byte{0, 0, 0, 1}}},
		{"rtu", func(l net.Conn) Transport { return NewRTU(l, "", 19200, timeout, logging.NopLogger()) },
			&adu.Request{UnitID: 1, FunctionCode: byte(protocol.FCReadHoldingRegisters), Payload: []byte{0, 0, 0, 1}}},
		{"ascii", func(l net.Conn) Transport { return NewASCII(l, timeout, logging.NopLogger()) },
			&adu.Request{UnitID: 1, FunctionCode: byte(protocol.FCReadHoldingRegisters), Payload: []byte{0, 0, 0, 1}}},
	}
	for _, tc := range transports {
		t.Run(tc.name, func(t *testing.T) {
			p1, p2 := net.Pipe()
			defer func() { _ = p1.Close(); _ = p2.Close() }()
			go func() {
				// Take the request and never answer.
				buf := make([]byte, 512)
				for {
					if _, err := p1.Read(buf); err != nil {
						return
					}
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			start := time.Now()
			_, err := tc.new(p2).ExecuteRequest(ctx, tc.req)
			if took := time.Since(start); took > 2*time.Second {
				t.Errorf("request took %v under a 10 s context deadline, timeout %v", took, timeout)
			}
			// The client maps the I/O timeout to ErrRequestTimedOut.
			if !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Errorf("err = %v, want an I/O timeout", err)
			}
			if ctx.Err() != nil {
				t.Error("the context expired: the timeout did not apply")
			}
		})
	}
}
