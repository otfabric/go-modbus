// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// ServerPDUExchange is one request a server read from a client and the
// response it answered with, as raw PDUs: the function code byte followed by
// the data. The MBAP header is not included.
//
// Request and Response are copies owned by the receiver of the exchange; they
// may be kept and modified.
type ServerPDUExchange struct {
	// ClientAddr is the client's remote host:port, ClientRole its role (tcp+tls;
	// empty on tcp), as the handlers receive them.
	ClientAddr string
	ClientRole string
	// UnitID is the unit id the request was addressed to.
	UnitID uint8
	// Request is the request PDU as received.
	Request []byte
	// Response is the response PDU the server answers with: an exception
	// response has the function code with bit 0x80 set, the server's own
	// exceptions (Illegal Function for a function code no handler serves, for
	// example) included. It is nil when the server sends no response: a handler
	// that returned ErrNoResponse, a request whose function code has the
	// exception bit set (0x80-0xFF; the connection is kept), or a request PDU
	// shorter than its fields or byte count and a handler that returned
	// ErrProtocolError, on which the server closes the connection.
	Response []byte
	// Received is the time the server read the request from the connection,
	// Answered the time its answer was decided, before the response is
	// written. Both are read with time.Now and carry a monotonic clock
	// reading: Answered.Sub(Received) includes the time the request waited
	// behind earlier ones of the same connection.
	Received time.Time
	Answered time.Time
}

// ServerPDUTap is an optional callback that sees the raw PDUs of every
// request a server dispatches (ServerConfig.PDUTap). It is meant for audit
// trails and recordings of what a client asked and was answered, not for
// metrics.
//
// OnServerPDU is called on the connection's goroutine once for every request
// read from a tcp or tcp+tls connection, after its answer was decided and
// before the response is written, in the order of the connection's requests.
// ctx is the connection's context, as the handlers receive it. A response that
// cannot then be written closes the connection (ConnectionCloseMetrics reports
// CloseWriteFailed). It is not called for bytes the server cannot frame as a
// request (CloseMalformedFrame), nor after a Shutdown called from one of the
// connection's own callbacks.
//
// Implementations must be non-blocking and safe for concurrent use: the
// connections of a server call it at the same time, and a slow tap delays the
// connection it reports. One that does not return normally is stopped there
// and logged at error level with its stack, through ServerConfig.Logger; the
// response is sent as if it had returned.
type ServerPDUTap interface {
	OnServerPDU(ctx context.Context, ex ServerPDUExchange)
}

// ServerPDUTapFunc adapts an ordinary function to the ServerPDUTap interface.
// A ServerConfig holding one is not comparable with ==: comparing it panics,
// as comparing two functions would.
type ServerPDUTapFunc func(ctx context.Context, ex ServerPDUExchange)

// OnServerPDU calls f(ctx, ex).
func (f ServerPDUTapFunc) OnServerPDU(ctx context.Context, ex ServerPDUExchange) { f(ctx, ex) }

// tapServerExchange reports req and its response res (nil: none sent) to
// ServerConfig.PDUTap, when there is one, through call.
func (ms *Server) tapServerExchange(ctx context.Context, req *adu.Request, res *adu.Response, clientAddr, clientRole string, received time.Time) {
	tap := ms.conf.PDUTap
	if tap == nil {
		return
	}
	ex := ServerPDUExchange{
		ClientAddr: clientAddr,
		ClientRole: clientRole,
		UnitID:     req.UnitID,
		Request:    pduBytes(req.FunctionCode, req.Payload),
		Received:   received,
		Answered:   time.Now(),
	}
	if res != nil {
		ex.Response = pduBytes(res.FunctionCode, res.Payload)
	}
	_ = ms.call("ServerPDUTap.OnServerPDU", func() string {
		return "ServerPDUTap.OnServerPDU for " + clientAddr + " did not return normally"
	}, func() error {
		tap.OnServerPDU(ctx, ex)
		return nil
	})
}
