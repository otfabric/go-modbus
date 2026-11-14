// SPDX-License-Identifier: MIT

package transport

import (
	"context"
	"io"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// Transport executes Modbus requests over a concrete link (TCP, RTU, etc.).
// It operates on adu.Request and adu.Response.
type Transport interface {
	Close() error
	ExecuteRequest(ctx context.Context, req *adu.Request) (*adu.Response, error)
	ReadRequest() (*adu.Request, uint16, error)
	WriteResponse(res *adu.Response) error
}

// writeFull writes all bytes in buf to w, retrying short writes.
// net.Conn.Write typically writes all bytes or returns an error, but this
// guard ensures correctness for any io.Writer implementation.
func writeFull(w io.Writer, buf []byte) error {
	for len(buf) > 0 {
		n, err := w.Write(buf)
		buf = buf[n:]
		if err != nil {
			return err
		}
	}
	return nil
}

// ctxAfterDeadline returns ctx.Err(). A transport calls it right after it has
// set a deadline on its link and before it uses the link.
//
// The session layer honours a cancelled context by setting the link's deadline
// in the past (Interrupt), from another goroutine. A deadline the transport
// sets after that replaces it, and the I/O that follows would run its full
// time. Looking at the context after the deadline is set closes that window:
// a cancellation that came first is seen here, and one that comes later sets
// its deadline after the transport's.
func ctxAfterDeadline(ctx context.Context) error {
	return ctx.Err()
}

// requestDeadline returns the I/O deadline of a request: timeout from now,
// or the context's deadline when that comes first. A timeout of zero or less
// leaves the context's deadline alone.
func requestDeadline(ctx context.Context, timeout time.Duration) time.Time {
	d, ok := ctx.Deadline()
	if timeout <= 0 {
		if ok {
			return d
		}
		return time.Now()
	}
	if t := time.Now().Add(timeout); !ok || t.Before(d) {
		return t
	}
	return d
}
