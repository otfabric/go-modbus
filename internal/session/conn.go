// SPDX-License-Identifier: MIT

package session

import (
	"context"
	"time"

	"github.com/otfabric/go-modbus/internal/connid"
	"github.com/otfabric/go-modbus/internal/protocol"
)

// ConnEventKind is what happened to a connection, as reported in a ConnEvent.
type ConnEventKind uint8

const (
	// ConnDialed is a dial that ended, with or without a connection.
	ConnDialed ConnEventKind = iota + 1
	// ConnDiscarded is a connection closed because of a failure.
	ConnDiscarded
	// ConnClosed is a connection closed by the shutdown of its engine or pool.
	ConnClosed
)

// ConnOrigin says why a connection was dialled.
type ConnOrigin uint8

const (
	// OriginOpen is a dial made by Engine.Open or NewPool: the single
	// connection, or one of the pool's MinConns.
	OriginOpen ConnOrigin = iota + 1
	// OriginOnDemand is a pool dial made by a request that found no idle
	// connection.
	OriginOnDemand
	// OriginRedial is a dial of the single connection made by a request that
	// found the engine without one.
	OriginRedial
)

// DiscardReason says why a connection was discarded.
type DiscardReason uint8

const (
	// DiscardTransportError is a connection whose exchange failed.
	DiscardTransportError DiscardReason = iota + 1
	// DiscardSuspectResponse is a connection that returned a response
	// Config.Suspect flagged.
	DiscardSuspectResponse
	// DiscardRetry is the single connection, dropped before a retry.
	DiscardRetry
	// DiscardOpenFailed is a pool connection closed because a later
	// pre-warm dial of the same NewPool failed.
	DiscardOpenFailed
)

// ConnEvent reports what happened to one connection. It is passed to
// Config.Connections, outside every lock of the engine and the pool.
type ConnEvent struct {
	Kind ConnEventKind
	// ID is the connection's identifier (internal/connid), allocated before
	// it was dialled: a failed dial has one too.
	ID uint64
	// Pooled is true for a connection of the pool.
	Pooled bool
	// Origin is set for ConnDialed.
	Origin ConnOrigin
	// Reason is set for ConnDiscarded.
	Reason DiscardReason
	// Duration is how long the dial took (ConnDialed).
	Duration time.Duration
	// Age is the time since the connection was dialled (ConnDiscarded,
	// ConnClosed).
	Age time.Duration
	// Err is the error of the dial (ConnDialed), or the one the connection
	// was discarded for (ConnDiscarded; may be nil).
	Err error
}

// connMeta is what the engine and the pool keep about a connection.
type connMeta struct {
	id     uint64
	dialed time.Time
}

// observer is Config.Connections; a nil observer reports nothing.
type observer func(ConnEvent)

func (o observer) discarded(m connMeta, pooled bool, reason DiscardReason, err error) {
	if o != nil {
		o(ConnEvent{Kind: ConnDiscarded, ID: m.id, Pooled: pooled, Reason: reason, Age: time.Since(m.dialed), Err: err})
	}
}

func (o observer) closed(m connMeta, pooled bool) {
	if o != nil {
		o(ConnEvent{Kind: ConnClosed, ID: m.id, Pooled: pooled, Age: time.Since(m.dialed)})
	}
}

// dialConn allocates an identifier, dials with ctx carrying it (connid.With)
// and reports the dial to o. A dial that reports success without a transport
// is reported with ErrClientNotOpen; the caller decides what it means.
func dialConn[Req, Res any](ctx context.Context, dial func(context.Context) (Transport[Req, Res], error), o observer, origin ConnOrigin, pooled bool) (Transport[Req, Res], connMeta, error) {
	m := connMeta{id: connid.Next()}
	start := time.Now()
	t, err := dial(connid.With(ctx, m.id))
	m.dialed = time.Now()
	if o != nil {
		reported := err
		if err == nil && t == nil {
			reported = protocol.ErrClientNotOpen
		}
		o(ConnEvent{Kind: ConnDialed, ID: m.id, Pooled: pooled, Origin: origin, Duration: m.dialed.Sub(start), Err: reported})
	}
	return t, m, err
}
