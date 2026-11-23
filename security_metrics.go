// SPDX-License-Identifier: MIT

package modbus

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"strconv"
	"time"
)

// The interfaces of this file are optional extensions of ServerMetrics and
// ClientMetrics, found by type assertion on ServerConfig.Metrics and
// Config.Metrics, like ConnectionMetrics and AttemptMetrics. They report what
// happened, as typed facts; they do not change what the library does, and
// they do not say whether what happened is an attack. A value that does not
// implement one costs nothing.

// ErrInvalidRole is reported, wrapped, in TLSHandshakeInfo.RoleErr when a
// client certificate carries a Modbus Role extension the server cannot use:
// more than one, or one that is not exactly one DER UTF8String (bytes after
// it included). The client is then
// served with no role (ClientRole is empty).
var ErrInvalidRole = errors.New("modbus: invalid Modbus Role extension")

// TLSHandshakeMetrics is an optional extension of ServerMetrics and of
// ClientMetrics: a ServerConfig.Metrics or Config.Metrics that implements it
// is told the outcome of every TLS handshake of a tcp+tls connection.
//
// On a server, OnTLSHandshake is called once for every tcp+tls connection the
// server admitted (not refused through AllowClient, MaxClients or
// MaxClientsPerHost), when its handshake has ended: completed, failed, timed
// out after TLSHandshakeTimeout, or cut short because the connection was
// closed to make room for a newer one (ConnectionMetrics also reports that,
// as RejectTLSHandshakeEvicted) or because the server stopped. It runs on the
// connection's goroutine, before ConnectionLifecycleMetrics.OnConnectionOpened
// for a connection that is then served. A handshake that completed while the
// connection was being evicted is reported as completed, and the connection
// is not served: OnConnectionOpened says which connections were.
//
// On a client, it is called for every handshake of a dial (Open, a re-dial,
// a connection of the pool), on the goroutine that dials, after the
// handshake and before the connection is used. As for AttemptMetrics, it must
// not make requests on the same client.
//
// It must not block. A method that does not return normally is logged at
// error level with its stack, and the library goes on as if it had returned:
// the handshake's outcome is not changed. On a server it may call Stop or
// Shutdown (see RequestHandler).
type TLSHandshakeMetrics interface {
	OnTLSHandshake(info TLSHandshakeInfo)
}

// TLSHandshakeInfo is the outcome of one TLS handshake, as reported to
// TLSHandshakeMetrics. It is the receiver's: the certificates are parsed
// afresh for each call and are not used by the library afterwards.
type TLSHandshakeInfo struct {
	// ConnectionID is the connection the handshake ran on (see ConnectionID):
	// the same in the other reports of that connection. Since v1.22.0.
	ConnectionID ConnectionID
	// LocalAddr and RemoteAddr are the addresses of the connection
	// (host:port); empty when the connection does not report one.
	LocalAddr  string
	RemoteAddr string
	// Err is nil when the handshake completed and the peer's certificate was
	// verified, else the error it failed with, as crypto/tls returned it: a
	// certificate that does not verify, a timeout (os.ErrDeadlineExceeded),
	// a connection that was closed, or "no client certificate received". A
	// client's handshake that failed is also returned, wrapped, by the dial.
	Err error
	// Duration is the time the handshake took.
	Duration time.Duration
	// Version and CipherSuite are those negotiated (tls.VersionTLS13,
	// tls.TLS_AES_128_GCM_SHA256, ...); zero when the handshake did not get
	// that far. ServerName is the server name the client asked for (SNI).
	Version     uint16
	CipherSuite uint16
	ServerName  string
	// PeerCertificates is the certificate chain the peer sent, leaf first;
	// empty when none was received, which may be the case for a handshake
	// that failed.
	PeerCertificates []*x509.Certificate
	// Verified reports whether the peer's chain was verified against
	// ServerConfig.TLSClientCAs (server) or Config.TLSRootCAs (client). It is
	// true for every completed handshake.
	Verified bool
	// VerifiedChains are the chains crypto/tls built from PeerCertificates to
	// a trusted root when it verified them, each leaf first and root last;
	// empty when the handshake failed. Like PeerCertificates, they are parsed
	// afresh for each call. Since v1.23.0.
	VerifiedChains [][]*x509.Certificate
	// Role is the role of the client from the Modbus Role extension of its
	// certificate, as the handlers receive it in ClientRole; empty when it
	// has none or the handshake failed. Server only.
	Role string
	// RoleErr is non-nil when the client's certificate carries a Modbus Role
	// extension that the server could not use (it wraps ErrInvalidRole), so
	// that a missing role (Role empty, RoleErr nil) can be told from an
	// unusable one. Server only.
	RoleErr error
}

// newTLSHandshakeInfo returns the TLSHandshakeInfo of a handshake on conn that
// took d and ended with err. The peer's certificates are parsed again from
// copies of their DER encoding, so that the receiver cannot change the ones
// the connection holds.
func newTLSHandshakeInfo(conn *tls.Conn, d time.Duration, err error) TLSHandshakeInfo {
	info := TLSHandshakeInfo{
		LocalAddr:  addrString(conn.LocalAddr()),
		RemoteAddr: addrString(conn.RemoteAddr()),
		Err:        err,
		Duration:   d,
	}
	st := conn.ConnectionState()
	info.Version, info.CipherSuite, info.ServerName = st.Version, st.CipherSuite, st.ServerName
	info.Verified = err == nil && len(st.VerifiedChains) > 0
	info.PeerCertificates = copyCertificates(st.PeerCertificates)
	if err == nil {
		for _, chain := range st.VerifiedChains {
			info.VerifiedChains = append(info.VerifiedChains, copyCertificates(chain))
		}
	}
	return info
}

// copyCertificates returns certificates parsed afresh from copies of the DER
// encoding of certs, in order. A certificate whose encoding does not parse
// (crypto/tls parsed each once already, so none should) is left out.
func copyCertificates(certs []*x509.Certificate) []*x509.Certificate {
	var out []*x509.Certificate
	for _, c := range certs {
		cp, err := x509.ParseCertificate(append([]byte(nil), c.Raw...))
		if err != nil {
			continue
		}
		out = append(out, cp)
	}
	return out
}

// addrString returns a.String(), or "" for a nil address.
func addrString(a net.Addr) string {
	if a == nil {
		return ""
	}
	return a.String()
}

// AcceptErrorMetrics is an optional extension of ServerMetrics. A
// ServerConfig.Metrics that implements it is told of every Accept of the
// listener that failed while the server was not stopping (no file
// descriptors left, for example), with the error and the wait before the
// next attempt (see OBSERVABILITY.md, "Accept errors"). It runs on the accept
// loop, which it delays: it must not block. One that does not return
// normally is logged, and the server goes on as if it had returned; it may
// call Stop or Shutdown.
type AcceptErrorMetrics interface {
	OnAcceptError(err error, retryIn time.Duration)
}

// ProtocolViolationMetrics is an optional extension of ServerMetrics. A
// ServerConfig.Metrics that implements it is told of every request on a tcp
// or tcp+tls connection that breaks the framing or the structure the Modbus
// specification gives its function code, with what the server read of it and
// what it did about it:
//
//   - an MBAP header the server cannot frame: a length outside 2-254, or a
//     protocol ID other than 0 (Err is ErrInvalidMBAPLength or
//     ErrUnknownProtocolID; the connection is closed, nothing is answered);
//   - a request PDU shorter or longer than the fields of its function code or
//     than its byte count announces, which on a TCP stream is what a wrong
//     MBAP length looks like (Err matches ErrProtocolError and
//     ErrIllegalDataValue; answered with exception 3, or 1 for a function
//     code the server does not serve, and the connection is closed);
//   - a complete PDU whose fields contradict one another or whose data is
//     not what the function code takes (Err matches ErrProtocolError and
//     ErrIllegalDataValue; answered with exception 3, the connection kept);
//   - a request whose function code has the exception bit set, 0x80-0xFF
//     (Err matches ErrIllegalFunction; not answered, the connection kept).
//
// Not reported here: a function code the server does not serve, a value out
// of range (a quantity of 0, an address beyond 0xFFFF) and the errors of the
// handlers, ErrProtocolError included. Those are ordinary outcomes,
// reported to ServerMetrics.OnError and ServerConfig.PDUTap. A request that
// is reported here is also reported there, when it was framed, and
// ConnectionCloseMetrics reports the close of a connection it ended.
//
// OnProtocolViolation runs on the connection's goroutine, after
// ServerMetrics.OnError for a framed request and before
// ConnectionLifecycleMetrics.OnConnectionClosed. It must not block; one that
// does not return normally is logged, and the server goes on as if it had
// returned. It may call Stop or Shutdown (see RequestHandler).
type ProtocolViolationMetrics interface {
	OnProtocolViolation(v ProtocolViolation)
}

// ProtocolViolation is a request that breaks the framing or the structure of
// its function code, as reported to ProtocolViolationMetrics.
type ProtocolViolation struct {
	// ConnectionID is the connection the request arrived on (see
	// ConnectionID). Since v1.22.0.
	ConnectionID ConnectionID
	// RequestSeq is the position of the request among those read from the
	// connection, as in its ServerPDUExchange and AuthorizationRequest (see
	// AuthorizationRequest.RequestSeq); zero for an MBAP header the server
	// could not frame, which is not a request. Since v1.23.0.
	RequestSeq uint64
	// ClientAddr and ClientRole identify the connection, as the handlers
	// receive them (ClientRole is empty on tcp).
	ClientAddr string
	ClientRole string
	// TransactionID, ProtocolID, Length and UnitID are the fields of the MBAP
	// header as received.
	TransactionID uint16
	ProtocolID    uint16
	Length        uint16
	UnitID        uint8
	// FunctionCode is the request's function code. HasFunctionCode is false
	// for an MBAP header the server could not frame: nothing after it was
	// read, and FunctionCode is zero.
	FunctionCode    FunctionCode
	HasFunctionCode bool
	// Err says what is wrong (see ProtocolViolationMetrics).
	Err error
	// Exception is the exception code the server decided to answer with;
	// zero when it decided to send no answer. Closed reports whether the
	// server decided to close the connection because of it. Both are
	// decisions, not observed outcomes: the callback runs before the answer
	// is written and before the connection is closed. A write that then
	// fails, or a client that left first, is reported by
	// ConnectionCloseMetrics (CloseWriteFailed, CloseClientClosed), which
	// also reports the close itself.
	Exception ExceptionCode
	Closed    bool
}

// ResponseAnomalyMetrics is an optional extension of ClientMetrics. A
// Config.Metrics that implements it is told of what the client tolerated
// while it waited for a response, and which therefore does not show in the
// outcome of the request: frames it skipped and bytes it discarded (see
// ResponseAnomalyKind). Responses that fail a request (a bad CRC or LRC, a
// truncated frame, another unit ID, a wrong echo, an exception) are reported
// through the request's error, to ClientMetrics.OnError and
// AttemptMetrics.OnAttempt, and not here.
//
// OnResponseAnomaly runs on the goroutine executing the request, while the
// request holds its connection: like AttemptMetrics, it must not block and
// must not make requests on the same client. One that does not return
// normally is logged, and the request goes on as if it had returned.
type ResponseAnomalyMetrics interface {
	OnResponseAnomaly(a ResponseAnomaly)
}

// ResponseAnomalyKind says what a client tolerated, as reported to
// ResponseAnomalyMetrics.
type ResponseAnomalyKind uint8

const (
	// AnomalyTransactionID is an MBAP frame (tcp, tcp+tls, udp) whose
	// transaction ID is not the request's: a late answer to an earlier
	// request, for example. The client skipped it and went on waiting.
	AnomalyTransactionID ResponseAnomalyKind = iota + 1
	// AnomalyProtocolID is an MBAP frame (tcp, tcp+tls, udp) whose protocol
	// ID is not 0. The client skipped it and went on waiting. After 10
	// skipped frames of either kind the request fails, with
	// ErrBadTransactionID or ErrUnknownProtocolID (the last one skipped).
	AnomalyProtocolID
	// AnomalyDiscarded is bytes a serial framing (rtu, ascii, rtuovertcp,
	// rtuoverudp, asciiovertcp) read and discarded before it wrote the
	// request: the rest of an earlier exchange that failed or was abandoned
	// (a late answer), or noise on the line.
	AnomalyDiscarded
)

// String returns the name of the kind ("transaction id", "protocol id",
// "discarded"), or "ResponseAnomalyKind(n)" for a value without one.
func (k ResponseAnomalyKind) String() string {
	switch k {
	case AnomalyTransactionID:
		return "transaction id"
	case AnomalyProtocolID:
		return "protocol id"
	case AnomalyDiscarded:
		return "discarded"
	default:
		return "ResponseAnomalyKind(" + strconv.Itoa(int(k)) + ")"
	}
}

// ResponseAnomaly is one thing a client tolerated during the exchange of a
// request, as reported to ResponseAnomalyMetrics.
type ResponseAnomaly struct {
	Kind ResponseAnomalyKind
	// ConnectionID is the connection the exchange ran on (see ConnectionID).
	// Since v1.22.0.
	ConnectionID ConnectionID
	// UnitID and FunctionCode are those of the request in flight.
	UnitID       uint8
	FunctionCode FunctionCode
	// ExpectedTransactionID is the request's transaction ID, TransactionID
	// and ProtocolID those of the skipped frame (AnomalyTransactionID and
	// AnomalyProtocolID; zero for AnomalyDiscarded). A transaction ID
	// correlates a response with its request: it is not protection against
	// replay.
	ExpectedTransactionID uint16
	TransactionID         uint16
	ProtocolID            uint16
	// Discarded is the number of bytes discarded (AnomalyDiscarded; zero
	// for the others).
	Discarded int
}
