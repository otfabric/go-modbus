// SPDX-License-Identifier: MIT

package secevent

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/otfabric/go-modbus/internal/protocol"
)

// Kind names one sort of event, as a stable dotted code such as
// "conn.rejected_limit". The values are part of the API and are never renamed.
type Kind string

// Category groups kinds.
type Category uint8

// The categories. The zero value is not a category.
const (
	CategoryConnection Category = iota + 1
	CategoryPeer
	CategoryTLS
	CategoryProtocol
	CategoryAccess
	CategoryControl
	CategoryRecon
	CategoryUnsolicited
	CategoryFlood
	CategoryResource
	CategoryRemote
	CategoryMeta
)

var categoryNames = [...]string{
	CategoryConnection:  "connection",
	CategoryPeer:        "peer",
	CategoryTLS:         "tls",
	CategoryProtocol:    "protocol",
	CategoryAccess:      "access",
	CategoryControl:     "control",
	CategoryRecon:       "recon",
	CategoryUnsolicited: "unsolicited",
	CategoryFlood:       "flood",
	CategoryResource:    "resource",
	CategoryRemote:      "remote",
	CategoryMeta:        "meta",
}

// String returns the name of the category ("connection", "tls", ...), or
// "unspecified" for the zero value.
func (c Category) String() string {
	if int(c) < len(categoryNames) && categoryNames[c] != "" {
		return categoryNames[c]
	}
	if c == 0 {
		return "unspecified"
	}
	return fmt.Sprintf("Category(%d)", uint8(c))
}

// Severity says how much an event deserves attention. The levels follow
// RFC 5424 (Info, Notice, Warning, Alert, Critical), in increasing order.
type Severity uint8

// The severities. The zero value is not a severity.
const (
	SeverityInfo Severity = iota + 1
	SeverityNotice
	SeverityWarning
	SeverityAlert
	SeverityCritical
)

// String returns "info", "notice", "warning", "alert" or "critical", or
// "unspecified" for the zero value.
func (s Severity) String() string {
	switch s {
	case SeverityInfo:
		return "info"
	case SeverityNotice:
		return "notice"
	case SeverityWarning:
		return "warning"
	case SeverityAlert:
		return "alert"
	case SeverityCritical:
		return "critical"
	case 0:
		return "unspecified"
	default:
		return fmt.Sprintf("Severity(%d)", uint8(s))
	}
}

// Action says what the endpoint did about the observation.
type Action uint8

// The actions. The zero value is read as ActionObserved.
const (
	// ActionObserved: the endpoint only noted it.
	ActionObserved Action = iota + 1
	// ActionRejected: the connection or request was refused.
	ActionRejected
	// ActionThrottled: excess requests were dropped.
	ActionThrottled
	// ActionDisconnected: the endpoint closed the connection.
	ActionDisconnected
	// ActionBlocked: the request was not passed to the handler.
	ActionBlocked
)

// String returns "observed", "rejected", "throttled", "disconnected" or
// "blocked".
func (a Action) String() string {
	switch a {
	case ActionObserved, 0:
		return "observed"
	case ActionRejected:
		return "rejected"
	case ActionThrottled:
		return "throttled"
	case ActionDisconnected:
		return "disconnected"
	case ActionBlocked:
		return "blocked"
	default:
		return fmt.Sprintf("Action(%d)", uint8(a))
	}
}

// FunctionCode and ExceptionCode are the Modbus types of the same names.
type (
	FunctionCode  = protocol.FunctionCode
	ExceptionCode = protocol.ExceptionCode
)

// FieldMask says which of the protocol fields of an Event are set, since zero
// is a valid unit, address and quantity.
type FieldMask uint16

// The protocol fields of an Event.
const (
	FieldUnitID FieldMask = 1 << iota
	FieldFunctionCode
	FieldSubFunction
	FieldException
	FieldAddress
	FieldQuantity
	FieldTransactionID
	FieldExpectedTxnID
)

// TLSPeer describes the certificate and parameters of a tcp+tls peer. It
// holds no key material.
type TLSPeer struct {
	Version           uint16    `json:"version"`
	CipherSuite       uint16    `json:"cipher_suite"`
	ServerName        string    `json:"server_name,omitempty"`
	Subject           string    `json:"subject,omitempty"`
	Issuer            string    `json:"issuer,omitempty"`
	SerialHex         string    `json:"serial,omitempty"`
	NotAfter          time.Time `json:"not_after,omitzero"`
	FingerprintSHA256 string    `json:"fingerprint_sha256,omitempty"`
	Verified          bool      `json:"verified"`
}

// PeerInfo identifies the other end of a connection.
type PeerInfo struct {
	// Network is "tcp".
	Network    string         `json:"network,omitempty"`
	RemoteAddr netip.AddrPort `json:"remote_addr,omitzero"`
	LocalAddr  netip.AddrPort `json:"local_addr,omitzero"`
	// TLS is nil on a plain connection.
	TLS *TLSPeer `json:"tls,omitempty"`
	// Role is the Modbus role of the client certificate (tcp+tls), "" when
	// there is none.
	Role        string    `json:"role,omitempty"`
	ConnectedAt time.Time `json:"connected_at,omitzero"`
}

// Event is one observation.
type Event struct {
	Time     time.Time
	Kind     Kind
	Category Category
	Severity Severity
	Action   Action

	// Protocol is "modbus", Transport "tcp" or "tcp+tls", Role "server".
	Protocol  string
	Transport string
	Role      string
	// ConnID is the identifier of the connection the event belongs to, from
	// NextConnID; it is 0 for events that belong to none.
	ConnID uint64
	Peer   PeerInfo
	Local  string

	// Fields says which of the protocol fields below are set.
	Fields        FieldMask
	UnitID        uint8
	FunctionCode  FunctionCode
	SubFunction   uint16
	Exception     ExceptionCode
	Address       uint16
	Quantity      uint16
	TransactionID uint16
	ExpectedTxnID uint16

	// Count is the number of occurrences the event stands for: 0 or 1 for a
	// single one, more for a merged one (see Dispatcher). FirstSeen is the
	// time of the first occurrence it stands for.
	Count     int
	FirstSeen time.Time

	// Detail is one line of English, with no register values, payloads or
	// key material. Err is the underlying error, when there is one. Raw is a
	// copy of the offending bytes, cut to the dispatcher's MaxRaw.
	Detail string
	Err    error
	Raw    []byte

	// Technique is a MITRE ATT&CK for ICS hint for SIEM routing, not an
	// attribution.
	Technique string
}

// Has reports whether every field of f is set.
func (e *Event) Has(f FieldMask) bool { return e.Fields&f == f }

// String returns the event as one line.
func (e Event) String() string {
	var b strings.Builder
	b.WriteString(e.Severity.String())
	b.WriteByte(' ')
	b.WriteString(string(e.Kind))
	if a := e.Action; a != 0 && a != ActionObserved {
		b.WriteString(" action=")
		b.WriteString(a.String())
	}
	if e.ConnID != 0 {
		fmt.Fprintf(&b, " conn=%d", e.ConnID)
	}
	if e.Peer.RemoteAddr.IsValid() {
		b.WriteString(" peer=")
		b.WriteString(e.Peer.RemoteAddr.String())
	}
	if e.Peer.Role != "" {
		b.WriteString(" role=")
		b.WriteString(e.Peer.Role)
	}
	if e.Has(FieldUnitID) {
		fmt.Fprintf(&b, " unit=%d", e.UnitID)
	}
	if e.Has(FieldFunctionCode) {
		fmt.Fprintf(&b, " fc=0x%02x", uint8(e.FunctionCode))
	}
	if e.Count > 1 {
		fmt.Fprintf(&b, " count=%d", e.Count)
	}
	if e.Detail != "" {
		b.WriteString(": ")
		b.WriteString(e.Detail)
	}
	if e.Err != nil {
		b.WriteString(" (")
		b.WriteString(e.Err.Error())
		b.WriteByte(')')
	}
	return b.String()
}

// eventJSON is the stable JSON form of an Event: the field names are fixed.
type eventJSON struct {
	Time          time.Time `json:"time"`
	Kind          Kind      `json:"kind"`
	Category      string    `json:"category"`
	Severity      string    `json:"severity"`
	Action        string    `json:"action"`
	Protocol      string    `json:"protocol,omitempty"`
	Transport     string    `json:"transport,omitempty"`
	Role          string    `json:"role,omitempty"`
	ConnID        uint64    `json:"conn_id,omitempty"`
	Peer          *PeerInfo `json:"peer,omitempty"`
	Local         string    `json:"local,omitempty"`
	UnitID        *uint8    `json:"unit_id,omitempty"`
	FunctionCode  *uint8    `json:"function_code,omitempty"`
	SubFunction   *uint16   `json:"sub_function,omitempty"`
	Exception     *uint8    `json:"exception,omitempty"`
	Address       *uint16   `json:"address,omitempty"`
	Quantity      *uint16   `json:"quantity,omitempty"`
	TransactionID *uint16   `json:"transaction_id,omitempty"`
	ExpectedTxnID *uint16   `json:"expected_transaction_id,omitempty"`
	Count         int       `json:"count,omitempty"`
	FirstSeen     time.Time `json:"first_seen,omitzero"`
	Detail        string    `json:"detail,omitempty"`
	Error         string    `json:"error,omitempty"`
	Raw           string    `json:"raw,omitempty"`
	Technique     string    `json:"technique,omitempty"`
}

// MarshalJSON returns the stable JSON form of the event: fixed field names,
// the fields that are not set left out, Raw as hex.
func (e Event) MarshalJSON() ([]byte, error) {
	j := eventJSON{
		Time:      e.Time,
		Kind:      e.Kind,
		Category:  e.Category.String(),
		Severity:  e.Severity.String(),
		Action:    e.Action.String(),
		Protocol:  e.Protocol,
		Transport: e.Transport,
		Role:      e.Role,
		ConnID:    e.ConnID,
		Local:     e.Local,
		Count:     e.Count,
		FirstSeen: e.FirstSeen,
		Detail:    e.Detail,
		Technique: e.Technique,
	}
	if e.Peer != (PeerInfo{}) {
		p := e.Peer
		j.Peer = &p
	}
	if e.Has(FieldUnitID) {
		j.UnitID = &e.UnitID
	}
	if e.Has(FieldFunctionCode) {
		fc := uint8(e.FunctionCode)
		j.FunctionCode = &fc
	}
	if e.Has(FieldSubFunction) {
		j.SubFunction = &e.SubFunction
	}
	if e.Has(FieldException) {
		ex := uint8(e.Exception)
		j.Exception = &ex
	}
	if e.Has(FieldAddress) {
		j.Address = &e.Address
	}
	if e.Has(FieldQuantity) {
		j.Quantity = &e.Quantity
	}
	if e.Has(FieldTransactionID) {
		j.TransactionID = &e.TransactionID
	}
	if e.Has(FieldExpectedTxnID) {
		j.ExpectedTxnID = &e.ExpectedTxnID
	}
	if e.Err != nil {
		j.Error = e.Err.Error()
	}
	if len(e.Raw) > 0 {
		j.Raw = hex.EncodeToString(e.Raw)
	}
	return json.Marshal(j)
}
