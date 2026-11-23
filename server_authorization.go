// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// ErrNotAuthorized is matched by the error ServerMetrics.OnError receives for
// a request that ServerConfig.Authorizer refused, and by
// ServerConnectionEvent.Err for a connection ServerConfig.ConnectionAuthorizer
// refused (RejectNotAuthorized). The error also wraps the one the authorizer
// returned.
var ErrNotAuthorized = errors.New("modbus: request not authorized")

// RequestAuthorizer decides whether the server serves a request
// (ServerConfig.Authorizer). It is the place to enforce an application's own
// access rules, for example by the role of a tcp+tls client, at the server's
// boundary rather than in every handler method.
//
// AuthorizeRequest is called once for every request the server would hand to
// a handler method: after the server has checked the request's framing, its
// structure and its values, and that a handler serves its function code, and
// right before the handler method is called. A request the server answers
// itself (an exception for a malformed request, Illegal Function for a
// function code no handler serves) does not reach it. ctx is the connection's
// context, as the handler would receive it.
//
// nil serves the request. Any other error refuses it: the handler method is
// not called, so the request has no effect, and the server answers with an
// exception, as for an error of a handler:
//
//   - an error that matches one of the exception errors (ErrIllegalFunction,
//     ErrIllegalDataAddress, ErrIllegalDataValue, ErrServerDeviceFailure,
//     ErrAcknowledge, ErrServerDeviceBusy, ErrMemoryParityError,
//     ErrGWPathUnavailable, ErrGWTargetFailedToRespond) is answered with that
//     exception;
//   - ErrNoResponse (or an error that matches it) leaves the request
//     unanswered, the connection kept, as a handler returning it does: the
//     client runs into its timeout. Use it only where a silent device is what
//     the client expects;
//   - any other error is answered with Illegal Function (exception 01), which
//     MODBUS Application Protocol V1.1b3, section 7, defines as a function
//     code that "is not an allowable action for the server".
//
// ServerMetrics.OnError is called for a refused request with an error that
// matches ErrNotAuthorized and wraps the authorizer's error, and
// ServerConfig.PDUTap sees the request and the exception answered.
//
// The server holds no lock while it calls AuthorizeRequest, on the
// connection's goroutine: it must be safe for concurrent use, and should
// return quickly, as the connection waits for it. One that does not return
// normally is logged at error level with its stack, and the request is
// refused with Server Device Failure (exception 04): a failed authorizer
// never lets a request through. It may call Stop or Shutdown (see
// RequestHandler); the request is then not answered. The methods of the
// error it returns are called through the same guard.
type RequestAuthorizer interface {
	AuthorizeRequest(ctx context.Context, req *AuthorizationRequest) error
}

// RequestAuthorizerFunc is a function that serves as a RequestAuthorizer. A
// ServerConfig holding one is not comparable with ==: comparing it panics, as
// comparing two functions would.
type RequestAuthorizerFunc func(ctx context.Context, req *AuthorizationRequest) error

// AuthorizeRequest returns f(ctx, req).
func (f RequestAuthorizerFunc) AuthorizeRequest(ctx context.Context, req *AuthorizationRequest) error {
	return f(ctx, req)
}

// RequestAccess says what a request does to the data of a device, as told to
// a RequestAuthorizer.
type RequestAccess uint8

const (
	// AccessRead is a request that reads data: FC01, FC02, FC03, FC04, FC20
	// and FC24.
	AccessRead RequestAccess = iota + 1
	// AccessWrite is a request that writes data: FC05, FC06, FC15, FC16,
	// FC21 and FC22.
	AccessWrite
	// AccessReadWrite is FC23, which writes and then reads registers.
	AccessReadWrite
	// AccessDiagnostic is a request that reads the state or the
	// identification of the device rather than its data, or acts on its
	// communication: FC07, FC08, FC11, FC12, FC17 and FC43. What an FC08
	// request does depends on its sub-function (AuthorizationRequest.
	// SubFunction): some reset counters, restart the communications port or
	// make the device listen only; the server does not classify them.
	AccessDiagnostic
)

// String returns the name of the access ("read", "write", "read/write",
// "diagnostic"), or "RequestAccess(n)" for a value without one.
func (a RequestAccess) String() string {
	switch a {
	case AccessRead:
		return "read"
	case AccessWrite:
		return "write"
	case AccessReadWrite:
		return "read/write"
	case AccessDiagnostic:
		return "diagnostic"
	default:
		return "RequestAccess(" + strconv.Itoa(int(a)) + ")"
	}
}

// AuthorizationRequest is what a RequestAuthorizer is asked about: one
// request, as the server read and checked it. The server makes it for the
// call and does not use it afterwards.
//
// The address fields hold what the function code addresses, and are zero
// where it addresses nothing of that kind:
//
//	FC01-FC04         ReadAddr, ReadQuantity
//	FC05, FC06, FC22  WriteAddr, WriteQuantity (1)
//	FC15, FC16        WriteAddr, WriteQuantity
//	FC23              ReadAddr, ReadQuantity, WriteAddr, WriteQuantity
//	FC24              ReadAddr (the FIFO pointer address; ReadQuantity is 0,
//	                  the queue's length is the device's)
//	FC20, FC21        FileRecords
//	FC08              SubFunction (HasSubFunction)
//	FC43              SubFunction (the MEI type, HasSubFunction)
//	FC07, FC11, FC12, FC17  none
//
// The server gives unit id 0 (broadcast) no meaning of its own: the
// authorizer sees it as the client sent it, and decides.
type AuthorizationRequest struct {
	// ConnectionID is the connection the request arrived on (see
	// ConnectionID). Since v1.22.0.
	ConnectionID ConnectionID
	// RequestSeq is the position of the request among those the server read
	// from the connection: 1 for the first, counting every request read,
	// whether it was served, refused or malformed. It is per connection:
	// ConnectionID and RequestSeq together identify the request within the
	// process, in its AuthorizationDecisionEvent, ServerPDUExchange and
	// ProtocolViolation. Like ConnectionID it is for correlating reports,
	// not for metric labels. Since v1.23.0.
	RequestSeq uint64
	// TransactionID is the MBAP transaction ID the request arrived with. The
	// client chooses it and may repeat it: it is not a key of its own.
	// Since v1.23.0.
	TransactionID uint16
	// ClientAddr and ClientRole identify the client, as the handlers receive
	// them: its remote host:port, and on tcp+tls the role of its verified
	// certificate (empty on tcp, and for a certificate without a usable
	// role).
	ClientAddr string
	ClientRole string
	// ClientRoleErr tells a certificate without a role from one whose Modbus
	// Role extension the server could not use. It is nil on tcp, and on
	// tcp+tls for a certificate with a valid role or none; for an unusable
	// one (ClientRole is then empty) it is the error TLSHandshakeInfo.RoleErr
	// reported for the connection, which matches ErrInvalidRole. It is the
	// same for every request of a connection. The server refuses nothing
	// because of it: what to do is the authorizer's decision.
	ClientRoleErr error
	UnitID        uint8
	// FunctionCode is the request's function code, Access what it does.
	FunctionCode FunctionCode
	Access       RequestAccess
	// SubFunction is the sub-function of FC08 or the MEI type of FC43;
	// HasSubFunction says whether the function code has one.
	SubFunction    uint16
	HasSubFunction bool
	// ReadAddr, ReadQuantity, WriteAddr and WriteQuantity: the data addresses
	// read and written, see above.
	ReadAddr      uint16
	ReadQuantity  uint16
	WriteAddr     uint16
	WriteQuantity uint16
	// FileRecords are the sub-requests of FC20 and FC21, in order; for FC21
	// RecordLength is the number of registers the record writes.
	FileRecords []FileRecordRequest
}

// AuthorizationDecisionMetrics is an optional extension of ServerMetrics. A
// ServerConfig.Metrics that implements it is told every decision of
// ServerConfig.Authorizer, allowed or refused, with what the authorizer was
// asked: the decision with its inputs, which ServerMetrics.OnError does not
// carry for a refused request (it has the unit ID and the function code
// only). Request.ConnectionID and Request.RequestSeq join the decision to
// the ServerPDUExchange of the same request.
//
// OnAuthorizationDecision runs on the connection's goroutine, right after the
// authorizer returned (or failed): before the handler method for a request
// that is served, before ServerMetrics.OnError for one that is refused. It
// must not block; one that does not return normally is logged at error level
// with its stack, and the request is served or refused as decided. It may
// call Stop or Shutdown (see RequestHandler): as for one called from
// ServerMetrics.OnRequest, the request is then not answered, and an allowed
// request's handler is still called, with its context cancelled.
type AuthorizationDecisionMetrics interface {
	OnAuthorizationDecision(ev AuthorizationDecisionEvent)
}

// AuthorizationDecisionEvent is one decision of ServerConfig.Authorizer, as
// reported to AuthorizationDecisionMetrics. It reports what was decided; it
// says nothing of how serious a refusal is.
type AuthorizationDecisionEvent struct {
	// Request is what the authorizer was asked, as the server made it: a
	// copy taken before the call, which changes the authorizer made to its
	// AuthorizationRequest do not reach. FileRecords is the receiver's.
	Request AuthorizationRequest
	// Allowed reports whether the request is served.
	Allowed bool
	// Err is nil for an allowed request. For a refused one it is the error
	// the authorizer returned, or, when Failed, an error that says the
	// authorizer did not return normally and with what value. Its methods
	// are the application's: the server called them through its guard.
	Err error
	// Failed reports that the authorizer did not return normally (it
	// panicked, or called runtime.Goexit): the request is refused with
	// Server Device Failure.
	Failed bool
	// Exception is the exception the server decided to answer a refused
	// request with; zero for an allowed request and for one refused with
	// ErrNoResponse, which is not answered. It is a decision: the callback
	// runs before the answer is written.
	Exception ExceptionCode
}

// ConnectionAuthorizer decides whether the server serves a tcp+tls
// connection once its TLS handshake has completed and the client's
// certificate chain has been verified against ServerConfig.TLSClientCAs
// (ServerConfig.ConnectionAuthorizer). It is the place for an application's
// own rules about which verified clients may connect at all: pinning a
// certificate or an issuing CA, checking a subject, a SAN or the Modbus Role,
// consulting a revocation list the application keeps. The server imposes no
// model of users, tenants or roles of its own.
//
// AuthorizeConnection is called once for every connection whose handshake
// completed and that the server would serve, on the connection's goroutine,
// after TLSHandshakeMetrics.OnTLSHandshake and before the connection is
// served: before ConnectionLifecycleMetrics.OnConnectionOpened and before any
// request is read. info is what TLSHandshakeMetrics reports for the
// handshake (Err nil, Verified true, the role and RoleErr included), parsed
// afresh for this call: it is the receiver's. ctx is the connection's
// context, cancelled when the server stops.
//
// nil serves the connection. Any other error refuses it: the connection is
// closed, no request of it is read, and it is reported to
// ConnectionMetrics.OnConnectionRejected and ServerConnectionMetrics as
// RejectNotAuthorized, with ServerConnectionEvent.Err matching
// ErrNotAuthorized and wrapping the error. It is never reported as opened
// or closed.
//
// The connection holds its MaxClients slot while AuthorizeConnection runs,
// and is not evicted for a newer one: it must not block, and should return
// quickly. It must be safe for concurrent use. One that does not return
// normally is logged at error level with its stack, and the connection is
// refused: a failed authorizer never lets a connection through. It may call
// Stop or Shutdown (see RequestHandler); the connection is then closed
// without being served, and nothing more is reported for it.
type ConnectionAuthorizer interface {
	AuthorizeConnection(ctx context.Context, info TLSHandshakeInfo) error
}

// ConnectionAuthorizerFunc is a function that serves as a
// ConnectionAuthorizer. A ServerConfig holding one is not comparable with ==:
// comparing it panics, as comparing two functions would.
type ConnectionAuthorizerFunc func(ctx context.Context, info TLSHandshakeInfo) error

// AuthorizeConnection returns f(ctx, info).
func (f ConnectionAuthorizerFunc) AuthorizeConnection(ctx context.Context, info TLSHandshakeInfo) error {
	return f(ctx, info)
}

// errConnectionAuthorizerFailed is the cause of a connection refused because
// ServerConfig.ConnectionAuthorizer did not return normally; it is wrapped
// with the error call returned.
var errConnectionAuthorizerFailed = errors.New("the connection authorizer did not return normally")

// authorizeConnection asks ServerConfig.ConnectionAuthorizer, through call,
// whether to serve the tcp+tls connection id on tlsSock, whose handshake took
// d: nil to serve it, else the error it is refused with, which matches
// ErrNotAuthorized.
func (ms *Server) authorizeConnection(ctx context.Context, id ConnectionID, tlsSock *tls.Conn, d time.Duration, role string, roleErr error) error {
	info := newTLSHandshakeInfo(tlsSock, d, nil)
	info.ConnectionID, info.Role, info.RoleErr = id, role, roleErr
	var authErr error
	if callErr := ms.call("ConnectionAuthorizer.AuthorizeConnection", func() string {
		return fmt.Sprintf("ConnectionAuthorizer.AuthorizeConnection for %s did not return normally", info.RemoteAddr)
	}, func() error {
		authErr = ms.conf.ConnectionAuthorizer.AuthorizeConnection(ctx, info)
		return nil
	}); callErr != nil {
		return &connNotAuthorizedError{cause: fmt.Errorf("%w: %w", errConnectionAuthorizerFailed, callErr)}
	}
	if authErr == nil {
		return nil
	}
	return &connNotAuthorizedError{cause: authErr}
}

// connNotAuthorizedError is the error of a connection
// ServerConfig.ConnectionAuthorizer refused: it matches ErrNotAuthorized and
// wraps the authorizer's error (cause). Its methods call cause's only in
// Error, which the server does not call outside its guard.
type connNotAuthorizedError struct{ cause error }

func (e *connNotAuthorizedError) Error() string {
	return "modbus: connection not authorized: " + e.cause.Error()
}

func (e *connNotAuthorizedError) Unwrap() error { return e.cause }

func (e *connNotAuthorizedError) Is(target error) bool { return target == ErrNotAuthorized }

// newAuthorization returns the AuthorizationRequest of req with the fields
// every request has.
func newAuthorization(req *adu.Request, clientAddr, clientRole string, access RequestAccess) *AuthorizationRequest {
	return &AuthorizationRequest{
		RequestSeq:    req.Seq,
		TransactionID: req.TransactionID,
		ClientAddr:    clientAddr,
		ClientRole:    clientRole,
		UnitID:        req.UnitID,
		FunctionCode:  FunctionCode(req.FunctionCode),
		Access:        access,
	}
}

// exceptionErrors are the errors a RequestAuthorizer can refuse a request
// with to choose the exception answered, in the order
// mapErrorToExceptionCode tries them.
var exceptionErrors = []struct {
	err  error
	code ExceptionCode
}{
	{ErrIllegalFunction, exIllegalFunction},
	{ErrIllegalDataAddress, exIllegalDataAddress},
	{ErrIllegalDataValue, exIllegalDataValue},
	{ErrServerDeviceFailure, exServerDeviceFailure},
	{ErrAcknowledge, exAcknowledge},
	{ErrMemoryParityError, exMemoryParityError},
	{ErrServerDeviceBusy, exServerDeviceBusy},
	{ErrGWPathUnavailable, exGWPathUnavailable},
	{ErrGWTargetFailedToRespond, exGWTargetFailedToRespond},
}

// notAuthorizedError is the error of a request ServerConfig.Authorizer
// refused. It matches ErrNotAuthorized, the authorizer's error (cause), and
// the error the server answers it with: ErrNoResponse when silent, else the
// exception error of code. Its methods call none of cause's: answerFor and
// mapErrorToExceptionCode, which look at it, find the answer through Is.
type notAuthorizedError struct {
	cause  error
	silent bool
	code   ExceptionCode
}

func (e *notAuthorizedError) Error() string {
	return ErrNotAuthorized.Error() + ": " + e.cause.Error()
}

func (e *notAuthorizedError) Unwrap() error { return e.cause }

func (e *notAuthorizedError) Is(target error) bool {
	if target == ErrNotAuthorized {
		return true
	}
	if e.silent {
		return target == ErrNoResponse
	}
	for _, x := range exceptionErrors {
		if x.code == e.code {
			return target == x.err
		}
	}
	return false
}

// errAuthorizerFailed is the cause of a request refused because the
// authorizer did not return normally.
var errAuthorizerFailed = errors.New("the authorizer did not return normally")

// connKey is the key of the connection context's value holding the
// connection's connState (see withConn).
type connKey struct{}

// connState is what the server keeps about a connection in its context: its
// identifier, the error of its unusable Modbus Role extension (for
// AuthorizationRequest.ClientRoleErr) and its local address.
type connState struct {
	id        ConnectionID
	roleErr   error
	localAddr string
}

// withConn returns ctx carrying st. The key is unexported: handlers cannot
// see it.
func withConn(ctx context.Context, st connState) context.Context {
	return context.WithValue(ctx, connKey{}, st)
}

// connFrom returns the connState ctx carries, the zero one if none.
func connFrom(ctx context.Context) connState {
	st, _ := ctx.Value(connKey{}).(connState)
	return st
}

// authorize asks ServerConfig.Authorizer about ar, the request req a handler
// is about to serve: nil when it is served (or there is no authorizer), else
// a *notAuthorizedError that the caller returns as the request's error.
// The authorizer, and the methods of its error, run through call. The
// decision is then reported to AuthorizationDecisionMetrics, when
// ServerConfig.Metrics implements it.
func (ms *Server) authorize(ctx context.Context, req *adu.Request, ar *AuthorizationRequest) error {
	st := connFrom(ctx)
	ar.ConnectionID, ar.ClientRoleErr = st.id, st.roleErr
	var asked AuthorizationRequest
	if ms.decisionMetrics != nil {
		// What the authorizer was asked, before it could change ar.
		asked = *ar
		asked.FileRecords = slices.Clone(ar.FileRecords)
	}
	var authErr error
	if callErr := ms.call("RequestAuthorizer.AuthorizeRequest", func() string {
		return fmt.Sprintf("RequestAuthorizer.AuthorizeRequest for FC 0x%02x from %s did not return normally", req.FunctionCode, ar.ClientAddr)
	}, func() error {
		authErr = ms.conf.Authorizer.AuthorizeRequest(ctx, ar)
		return nil
	}); callErr != nil {
		ms.reportDecision(&asked, AuthorizationDecisionEvent{Err: callErr, Failed: true, Exception: exServerDeviceFailure})
		return &notAuthorizedError{cause: errAuthorizerFailed, code: exServerDeviceFailure}
	}
	if authErr == nil {
		ms.reportDecision(&asked, AuthorizationDecisionEvent{Allowed: true})
		return nil
	}
	refused := &notAuthorizedError{cause: authErr, code: exIllegalFunction}
	if ms.call("authorizer error", func() string {
		return fmt.Sprintf("the error of the authorizer for FC 0x%02x from %s did not return normally", req.FunctionCode, ar.ClientAddr)
	}, func() error {
		if errors.Is(authErr, ErrNoResponse) {
			refused.silent = true
			return nil
		}
		for _, x := range exceptionErrors {
			if errors.Is(authErr, x.err) {
				refused.code = x.code
				break
			}
		}
		return nil
	}) != nil {
		refused.silent, refused.code = false, exServerDeviceFailure
	}
	ms.logger.Debugf("FC 0x%02x from %s not authorized: %v", req.FunctionCode, ar.ClientAddr, authErr)
	ev := AuthorizationDecisionEvent{Err: authErr}
	if !refused.silent {
		ev.Exception = refused.code
	}
	ms.reportDecision(&asked, ev)
	return refused
}

// reportDecision reports ev, the decision on asked, to
// AuthorizationDecisionMetrics through call, when ServerConfig.Metrics
// implements it.
func (ms *Server) reportDecision(asked *AuthorizationRequest, ev AuthorizationDecisionEvent) {
	dm := ms.decisionMetrics
	if dm == nil {
		return
	}
	ev.Request = *asked
	_ = ms.call("AuthorizationDecisionMetrics.OnAuthorizationDecision", func() string {
		return fmt.Sprintf("AuthorizationDecisionMetrics.OnAuthorizationDecision for FC 0x%02x from %s did not return normally", byte(asked.FunctionCode), asked.ClientAddr)
	}, func() error {
		dm.OnAuthorizationDecision(ev)
		return nil
	})
}

// fileRecordRefs returns the FileRecordRequest of every record of an FC21
// request, its RecordLength the number of registers it writes.
func fileRecordRefs(recs []FileRecord) []FileRecordRequest {
	refs := make([]FileRecordRequest, len(recs))
	for i, r := range recs {
		refs[i] = FileRecordRequest{FileNumber: r.FileNumber, RecordNumber: r.RecordNumber, RecordLength: adu.Uint16(len(r.Data))}
	}
	return refs
}
