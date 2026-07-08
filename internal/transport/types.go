// SPDX-License-Identifier: MIT

package transport

import (
	"context"
	"io"

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
