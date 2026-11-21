// SPDX-License-Identifier: MIT

package secevent

import "sort"

// The event kinds an endpoint reports. A kind is never renamed or reused.
const (
	// KindConnAccepted: a connection was accepted, before any check on it.
	KindConnAccepted Kind = "conn.accepted"
	// KindConnUnexpected: ServerConfig.AllowClient refused the peer.
	KindConnUnexpected Kind = "conn.unexpected"
	// KindConnRejectedLimit: every ServerConfig.MaxClients slot was taken.
	KindConnRejectedLimit Kind = "conn.rejected_limit"
	// KindConnNoRequest: a connection ended without one request read from it
	// (a connect scan, a probe, a client that never sent).
	KindConnNoRequest Kind = "conn.no_request"
	// KindConnIdleTimeout: a connection that had sent requests was closed
	// for idling for ServerConfig.Timeout.
	KindConnIdleTimeout Kind = "conn.idle_timeout"
	// KindConnClosed: the end of every served connection, with the reason.
	KindConnClosed Kind = "conn.closed"
	// KindConnAcceptError: the listener's Accept failed (no file
	// descriptors left, for example) and the server is retrying.
	KindConnAcceptError Kind = "conn.accept_error"

	// KindTLSHandshakeEvicted: a tcp+tls connection that had not completed
	// its handshake was closed to make room for a newer one.
	KindTLSHandshakeEvicted Kind = "tls.handshake_evicted"

	// KindProtoBadMBAP: the peer sent an MBAP header the server cannot frame
	// (length outside 2..254, protocol identifier other than 0).
	KindProtoBadMBAP Kind = "proto.bad_mbap"
	// KindProtoBadPDU: a request violated the protocol (a PDU whose length
	// does not match its function code).
	KindProtoBadPDU Kind = "proto.bad_pdu"

	// KindDoSWriteStall: a response could not be written within
	// ServerConfig.Timeout (a peer that does not read its answers).
	KindDoSWriteStall Kind = "dos.write_stall"

	// KindMetaEventsDropped: Count events were lost because the queue to the
	// sink was full.
	KindMetaEventsDropped Kind = "meta.events_dropped"
	// KindMetaSinkPanic: the sink did not return normally. Reported once.
	KindMetaSinkPanic Kind = "meta.sink_panic"
)

// KindInfo is what the catalogue knows of a kind.
type KindInfo struct {
	Category Category
	Severity Severity
	// Technique is the MITRE ATT&CK for ICS hint ("" for none).
	Technique string
	// AlwaysDeliver kinds are never merged with their repeats.
	AlwaysDeliver bool
	// MergeByPeer kinds are merged per peer address (the host, not the
	// port) rather than per connection: the connection events, which a
	// connection flood would otherwise turn into one event per connection.
	// The first event of a host in a window keeps its connection id; the
	// merged one carries the last.
	MergeByPeer bool
}

var catalogue = map[Kind]KindInfo{
	KindConnAccepted:        {Category: CategoryConnection, Severity: SeverityInfo, MergeByPeer: true},
	KindConnUnexpected:      {Category: CategoryConnection, Severity: SeverityWarning, Technique: "T0846", MergeByPeer: true},
	KindConnRejectedLimit:   {Category: CategoryConnection, Severity: SeverityWarning, Technique: "T0814", MergeByPeer: true},
	KindConnNoRequest:       {Category: CategoryConnection, Severity: SeverityNotice, Technique: "T0846", MergeByPeer: true},
	KindConnIdleTimeout:     {Category: CategoryConnection, Severity: SeverityInfo},
	KindConnClosed:          {Category: CategoryConnection, Severity: SeverityInfo, MergeByPeer: true},
	KindConnAcceptError:     {Category: CategoryResource, Severity: SeverityCritical, Technique: "T0814"},
	KindTLSHandshakeEvicted: {Category: CategoryTLS, Severity: SeverityNotice, Technique: "T0814", MergeByPeer: true},
	KindProtoBadMBAP:        {Category: CategoryProtocol, Severity: SeverityWarning, Technique: "T0866"},
	KindProtoBadPDU:         {Category: CategoryProtocol, Severity: SeverityWarning, Technique: "T0866"},
	KindDoSWriteStall:       {Category: CategoryResource, Severity: SeverityWarning, Technique: "T0814"},
	KindMetaEventsDropped:   {Category: CategoryMeta, Severity: SeverityWarning, AlwaysDeliver: true},
	KindMetaSinkPanic:       {Category: CategoryMeta, Severity: SeverityWarning, AlwaysDeliver: true},
}

// Describe returns the catalogue entry of kind, false for a kind that is not
// in it (an application's own kind, reported with Report).
func Describe(kind Kind) (KindInfo, bool) {
	info, ok := catalogue[kind]
	return info, ok
}

// Kinds returns every kind of the catalogue, sorted.
func Kinds() []Kind {
	out := make([]Kind, 0, len(catalogue))
	for k := range catalogue {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// applyDefaults fills the category, severity, technique and action an event
// leaves unset from the catalogue.
func applyDefaults(e *Event) {
	if info, ok := catalogue[e.Kind]; ok {
		if e.Category == 0 {
			e.Category = info.Category
		}
		if e.Severity == 0 {
			e.Severity = info.Severity
		}
		if e.Technique == "" {
			e.Technique = info.Technique
		}
	}
	if e.Severity == 0 {
		e.Severity = SeverityInfo
	}
	if e.Action == 0 {
		e.Action = ActionObserved
	}
	if e.Protocol == "" {
		e.Protocol = "modbus"
	}
}
