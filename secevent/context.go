// SPDX-License-Identifier: MIT

package secevent

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

var connSeq atomic.Uint64

// NextConnID returns a new connection identifier: 1, 2, 3, ... for the
// process, shared by every endpoint, so that the events of one connection can
// be put together.
func NextConnID() uint64 { return connSeq.Add(1) }

// Reporter is what a handler reaches through its context to report an event
// of its own and to learn who it is talking to.
type Reporter interface {
	// ReportEvent fills what e leaves unset from the connection (time,
	// protocol, transport, role, connection and peer) and hands it to the
	// endpoint's dispatcher.
	ReportEvent(e Event)
	// Peer returns the peer of the connection.
	Peer() PeerInfo
}

type reporterKey struct{}

// NewContext returns a context that carries r, for Report and PeerFromContext.
func NewContext(ctx context.Context, r Reporter) context.Context {
	return context.WithValue(ctx, reporterKey{}, r)
}

// Report reports e as an event of the connection ctx belongs to, which is the
// context a RequestHandler method receives. It returns false, and does
// nothing, when ctx carries no reporter: the server has no security policy
// with a sink, or ctx is not a handler's.
//
// A handler knows what the library cannot: which addresses exist, which
// writes are allowed. Detail must hold no register values or payloads.
func Report(ctx context.Context, e Event) bool {
	r, ok := ctx.Value(reporterKey{}).(Reporter)
	if !ok {
		return false
	}
	r.ReportEvent(e)
	return true
}

// PeerFromContext returns the peer of the connection ctx belongs to. It
// reports false when ctx carries no reporter.
func PeerFromContext(ctx context.Context) (PeerInfo, bool) {
	r, ok := ctx.Value(reporterKey{}).(Reporter)
	if !ok {
		return PeerInfo{}, false
	}
	return r.Peer(), true
}

// ConnReporter is the Reporter of one connection: it holds what every event
// of the connection shares.
type ConnReporter struct {
	d    *Dispatcher
	mu   sync.Mutex
	base Event
}

// NewConnReporter returns the Reporter of a connection whose events go to d.
// base supplies what they share: Protocol, Transport, Role, ConnID, Local and
// Peer.
func NewConnReporter(d *Dispatcher, base Event) *ConnReporter {
	return &ConnReporter{d: d, base: base}
}

// Base returns a copy of the event that carries what every event of the
// connection shares.
func (c *ConnReporter) Base() Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.base
}

// Peer returns the peer of the connection.
func (c *ConnReporter) Peer() PeerInfo { return c.Base().Peer }

// ReportEvent hands e to the dispatcher, filled in from the connection.
func (c *ConnReporter) ReportEvent(e Event) {
	b := c.Base()
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	if e.Protocol == "" {
		e.Protocol = b.Protocol
	}
	if e.Transport == "" {
		e.Transport = b.Transport
	}
	if e.Role == "" {
		e.Role = b.Role
	}
	if e.ConnID == 0 {
		e.ConnID = b.ConnID
	}
	if e.Local == "" {
		e.Local = b.Local
	}
	if e.Peer == (PeerInfo{}) {
		e.Peer = b.Peer
	}
	c.d.Emit(e)
}
