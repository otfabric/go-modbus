// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/otfabric/go-modbus/internal/adu"
)

// ErrNotAuthorized is matched by the error ServerMetrics.OnError receives for
// a request that ServerConfig.Authorizer refused. The error also wraps the
// one the authorizer returned.
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

// newAuthorization returns the AuthorizationRequest of req with the fields
// every request has.
func newAuthorization(req *adu.Request, clientAddr, clientRole string, access RequestAccess) *AuthorizationRequest {
	return &AuthorizationRequest{
		ClientAddr:   clientAddr,
		ClientRole:   clientRole,
		UnitID:       req.UnitID,
		FunctionCode: FunctionCode(req.FunctionCode),
		Access:       access,
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

// roleErrKey is the key of the connection context's value holding the
// error of an unusable Modbus Role extension (see withRoleErr).
type roleErrKey struct{}

// withRoleErr returns ctx carrying roleErr, the error of the connection's
// Modbus Role extension, for AuthorizationRequest.ClientRoleErr. A nil
// roleErr returns ctx itself. The key is unexported: handlers cannot see it.
func withRoleErr(ctx context.Context, roleErr error) context.Context {
	if roleErr == nil {
		return ctx
	}
	return context.WithValue(ctx, roleErrKey{}, roleErr)
}

// authorize asks ServerConfig.Authorizer about ar, the request req a handler
// is about to serve: nil when it is served (or there is no authorizer), else
// a *notAuthorizedError that the caller returns as the request's error.
// The authorizer, and the methods of its error, run through call.
func (ms *Server) authorize(ctx context.Context, req *adu.Request, ar *AuthorizationRequest) error {
	ar.ClientRoleErr, _ = ctx.Value(roleErrKey{}).(error)
	var authErr error
	if ms.call("RequestAuthorizer.AuthorizeRequest", func() string {
		return fmt.Sprintf("RequestAuthorizer.AuthorizeRequest for FC 0x%02x from %s did not return normally", req.FunctionCode, ar.ClientAddr)
	}, func() error {
		authErr = ms.conf.Authorizer.AuthorizeRequest(ctx, ar)
		return nil
	}) != nil {
		return &notAuthorizedError{cause: errAuthorizerFailed, code: exServerDeviceFailure}
	}
	if authErr == nil {
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
	return refused
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
