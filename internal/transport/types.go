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
