// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"strconv"
	"time"

	"github.com/otfabric/go-modbus/internal/connid"
	"github.com/otfabric/go-modbus/internal/session"
)

// ConnectionID identifies one transport connection of a client or a server:
// a TCP, TLS, UDP or serial link. It is allocated once per connection, before
// the connection is dialled or as soon as it is accepted, and stays the same
// for its whole life. A failed dial, a rejected connection and a failed TLS
// handshake have one too, so that their reports can be told apart.
//
// Identifiers come from one counter shared by every client and server of the
// process, starting at 1: an identifier is unique within the process for as
// long as it runs and is never reused, but it means nothing across processes
// or restarts, and its value carries no other information (ordering between
// identifiers reflects allocation order, nothing more). 0 means no connection:
// the field of a report that is not tied to one.
//
// It is meant for correlating the reports of one connection (in logs, traces,
// audit records). Do not use it as a metric label (a Prometheus label, for
// example): every connection has a new one, so the number of series grows
// without bound.
type ConnectionID uint64

// String returns the identifier in decimal.
func (id ConnectionID) String() string { return strconv.FormatUint(uint64(id), 10) }

// ServerConnectionMetrics is an optional extension of ServerMetrics: the
// detailed lifecycle of the tcp and tcp+tls connections of a server, with
// their ConnectionID. A ServerConfig.Metrics that implements it is called at
// the same points as ConnectionMetrics.OnConnectionRejected,
// ConnectionLifecycleMetrics and ConnectionCloseMetrics, whether or not it
// implements those, which keep their signatures and are still called:
//
//   - ServerConnectionRejected for every connection reported to
//     OnConnectionRejected, right after it;
//   - ServerConnectionOpened for every connection reported to
//     OnConnectionOpened (on tcp+tls, once its TLS handshake completed), right
//     after it;
//   - ServerConnectionClosed once for every opened connection, after
//     OnConnectionCloseReason, with the same exception: a connection that a
//     Shutdown called from one of its own callbacks stopped is not reported.
//
// A tcp+tls connection whose TLS handshake fails is neither opened nor
// closed here; TLSHandshakeMetrics reports it, with the same ConnectionID. A
// Shutdown or Stop called from a callback of a connection (this one
// included) ends that connection's callbacks: nothing more is reported for it.
//
// OnServerConnection runs on the accept loop (Rejected) or on the
// connection's goroutine, with no lock held, and must not block. One that
// does not return normally is logged at error level with the stack, and the
// server goes on as if it had returned. It may call Stop or Shutdown (see
// RequestHandler).
type ServerConnectionMetrics interface {
	OnServerConnection(ev ServerConnectionEvent)
}

// ServerConnectionEventKind is what happened to a server connection, as
// reported in a ServerConnectionEvent.
type ServerConnectionEventKind uint8

const (
	// ServerConnectionRejected is a connection the server closed as soon as
	// it was accepted, or (RejectTLSHandshakeEvicted) during its TLS
	// handshake. RejectReason says why.
	ServerConnectionRejected ServerConnectionEventKind = iota + 1
	// ServerConnectionOpened is a connection the server starts serving.
	ServerConnectionOpened
	// ServerConnectionClosed is a connection the server stopped serving.
	// CloseReason and Err say why.
	ServerConnectionClosed
)

// String returns the name of the kind ("rejected", "opened", "closed"), or
// "ServerConnectionEventKind(n)" for a value without one.
func (k ServerConnectionEventKind) String() string {
	switch k {
	case ServerConnectionRejected:
		return "rejected"
	case ServerConnectionOpened:
		return "opened"
	case ServerConnectionClosed:
		return "closed"
	default:
		return "ServerConnectionEventKind(" + strconv.Itoa(int(k)) + ")"
	}
}

// ServerConnectionEvent is one step of the lifecycle of a server connection
// (see ServerConnectionMetrics).
type ServerConnectionEvent struct {
	Kind ServerConnectionEventKind
	// ConnectionID is the connection's identifier, the same as in the
	// TLSHandshakeInfo, ProtocolViolation, AuthorizationRequest and
	// ServerPDUExchange of the connection.
	ConnectionID ConnectionID
	// RemoteAddr and LocalAddr are the addresses of the connection (host:port),
	// empty when the socket does not have one.
	RemoteAddr string
	LocalAddr  string
	// Role and RoleErr are the client's role and the error of an unusable
	// Modbus Role extension, as in TLSHandshakeInfo (tcp+tls; Opened and
	// Closed only).
	Role    string
	RoleErr error
	// RejectReason is set for ServerConnectionRejected.
	RejectReason RejectReason
	// CloseReason and Err are set for ServerConnectionClosed: why the server
	// stopped serving the connection, and the error that ended it (never nil),
	// as reported to ConnectionCloseMetrics.
	CloseReason CloseReason
	Err         error
	// Age is how long the server served the connection (Closed only): the time
	// since it was opened.
	Age time.Duration
}

// ClientConnectionMetrics is an optional extension of ClientMetrics: the
// lifecycle of the transport connections of a client, single or pooled, with
// their ConnectionID. A Config.Metrics that implements it is told:
//
//   - ClientConnectionDialed for every dial, whether or not it succeeded,
//     with Origin (Open, including the pre-warmed MinConns of a pool; a pool
//     dial on demand; a re-dial of the single connection), the dial's
//     Duration and its Err;
//   - ClientConnectionDiscarded for a connection closed because of a failure,
//     with DiscardReason and the error;
//   - ClientConnectionClosed for a connection closed by the shutdown of the
//     client (Close, a failed Open, a request or dial that ended after Close):
//     a normal end, never a failure.
//
// Every connection that was dialled successfully is reported once more, as
// Discarded or as Closed, unless the client is never closed. A failed dial is
// reported only as Dialed.
//
// There are no replacement pairs: when a pool dials after a discard, the new
// connection is not reported as the replacement of the old one, because a
// pool does not dial for a given connection (any waiting request may take the
// freed slot, or none). Correlate by time and Target if you need to.
//
// OnClientConnection runs synchronously, on the goroutine of the Open, request
// or Close that caused the event, with none of the client's locks held. It
// must be non-blocking and must not make requests on the same client; it may
// call Close. The connections a Close closes are reported once that Close has
// closed them all and no longer counts as closing: a Close called from
// OnClientConnection then returns at once, and one called concurrently may
// return before they are reported. One that does not return normally is
// logged at error level with the stack, and the client goes on as if it had
// returned.
type ClientConnectionMetrics interface {
	OnClientConnection(ev ClientConnectionEvent)
}

// ClientConnectionEventKind is what happened to a client connection, as
// reported in a ClientConnectionEvent.
type ClientConnectionEventKind uint8

const (
	// ClientConnectionDialed is a dial that ended: Err is nil when it opened a
	// connection.
	ClientConnectionDialed ClientConnectionEventKind = iota + 1
	// ClientConnectionDiscarded is a connection the client closed because of a
	// failure (see DiscardReason).
	ClientConnectionDiscarded
	// ClientConnectionClosed is a connection the client closed because it was
	// shut down: a normal end.
	ClientConnectionClosed
)

// String returns the name of the kind ("dialed", "discarded", "closed"), or
// "ClientConnectionEventKind(n)" for a value without one.
func (k ClientConnectionEventKind) String() string {
	switch k {
	case ClientConnectionDialed:
		return "dialed"
	case ClientConnectionDiscarded:
		return "discarded"
	case ClientConnectionClosed:
		return "closed"
	default:
		return "ClientConnectionEventKind(" + strconv.Itoa(int(k)) + ")"
	}
}

// ConnectionOrigin says why a client dialled a connection.
type ConnectionOrigin uint8

const (
	// ConnectionOriginOpen is a dial made by Open: the single connection, or
	// one of the MinConns connections of a pool, pre-warmed.
	ConnectionOriginOpen ConnectionOrigin = iota + 1
	// ConnectionOriginOnDemand is a pool dial made by a request that found no
	// idle connection.
	ConnectionOriginOnDemand
	// ConnectionOriginRedial is a dial of the single connection made by a
	// request that found the client without one: after the previous one was
	// discarded, before a retry for example. AttemptMetrics.OnRetryDial reports
	// the same dials.
	ConnectionOriginRedial
)

// String returns the name of the origin ("open", "on demand", "redial"), or
// "ConnectionOrigin(n)" for a value without one.
func (o ConnectionOrigin) String() string {
	switch o {
	case ConnectionOriginOpen:
		return "open"
	case ConnectionOriginOnDemand:
		return "on demand"
	case ConnectionOriginRedial:
		return "redial"
	default:
		return "ConnectionOrigin(" + strconv.Itoa(int(o)) + ")"
	}
}

// DiscardReason says why a client discarded a connection.
type DiscardReason uint8

const (
	// DiscardTransportError is a connection whose exchange failed (a timeout,
	// a read or write error, a frame that could not be read, a cancelled
	// request): Err is that error. A single serial connection (rtu, ascii)
	// is kept after an error that leaves it usable, and is not reported.
	DiscardTransportError DiscardReason = iota + 1
	// DiscardSuspectResponse is a connection that returned a response showing
	// that requests and responses are no longer aligned on it (see
	// ResponseAnomalyMetrics): the response is still returned to the caller.
	// Err is nil.
	DiscardSuspectResponse
	// DiscardRetry is the single connection, closed before a retry so that
	// the retry runs on a new one: Err is the error of the attempt.
	DiscardRetry
	// DiscardOpenFailed is a pre-warmed pool connection closed because a
	// later pre-warm dial of the same Open failed: Err is that dial's error.
	DiscardOpenFailed
)

// String returns the name of the reason ("transport error", "suspect
// response", "retry", "open failed"), or "DiscardReason(n)" for a value
// without one.
func (r DiscardReason) String() string {
	switch r {
	case DiscardTransportError:
		return "transport error"
	case DiscardSuspectResponse:
		return "suspect response"
	case DiscardRetry:
		return "retry"
	case DiscardOpenFailed:
		return "open failed"
	default:
		return "DiscardReason(" + strconv.Itoa(int(r)) + ")"
	}
}

// ClientConnectionEvent is one step of the lifecycle of a client connection
// (see ClientConnectionMetrics).
type ClientConnectionEvent struct {
	Kind ClientConnectionEventKind
	// ConnectionID is the connection's identifier, the same as in the
	// TLSHandshakeInfo, ResponseAnomaly and PDUExchange of the connection.
	ConnectionID ConnectionID
	// Target is the endpoint of the client's URL: host:port, or the serial
	// device.
	Target string
	// Pooled is true for a connection of the client's pool: Config.MaxConns > 1
	// on a transport that pools (tcp, rtuovertcp, rtuoverudp, udp,
	// asciiovertcp). A tcp+tls or serial client has a single connection.
	Pooled bool
	// Origin is set for ClientConnectionDialed.
	Origin ConnectionOrigin
	// DiscardReason is set for ClientConnectionDiscarded.
	DiscardReason DiscardReason
	// Duration is how long the dial took, TLS handshake included (Dialed).
	Duration time.Duration
	// Age is the time since the connection was dialled (Discarded, Closed).
	Age time.Duration
	// Err is the error of the dial (Dialed: nil when it succeeded), or the one
	// the connection was discarded for (Discarded; see DiscardReason).
	Err error
}

// connectionObserver returns the function the session engine reports
// connection events to: ClientConnectionMetrics.OnClientConnection called
// through call, nil when Config.Metrics does not implement it.
func (mc *Client) connectionObserver() func(session.ConnEvent) {
	cm, ok := mc.conf.Metrics.(ClientConnectionMetrics)
	if !ok {
		return nil
	}
	return func(e session.ConnEvent) {
		ev := ClientConnectionEvent{
			ConnectionID: ConnectionID(e.ID),
			Target:       mc.endpoint,
			Pooled:       e.Pooled,
			Duration:     e.Duration,
			Age:          e.Age,
			Err:          e.Err,
		}
		switch e.Kind {
		case session.ConnDialed:
			ev.Kind = ClientConnectionDialed
			ev.Origin = ConnectionOrigin(e.Origin)
		case session.ConnDiscarded:
			ev.Kind = ClientConnectionDiscarded
			ev.DiscardReason = DiscardReason(e.Reason)
		case session.ConnClosed:
			ev.Kind = ClientConnectionClosed
		}
		_ = mc.call("ClientConnectionMetrics.OnClientConnection", func() error { cm.OnClientConnection(ev); return nil })
	}
}

// connIDFrom returns the ConnectionID the session engine put in ctx for the
// dial that ctx belongs to, 0 if none.
func connIDFrom(ctx context.Context) ConnectionID { return ConnectionID(connid.From(ctx)) }
