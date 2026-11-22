// SPDX-License-Identifier: MIT

package modbus

// This file separates the kinds of bad request the server meets, which up to
// v1.19.0 shared ErrProtocolError and all closed the connection.
//
//   - Framing errors: bytes the transport cannot frame as a request (an MBAP
//     header with an invalid length or protocol identifier, a frame cut
//     short by the peer). They never reach serveRequest: the connection is
//     closed (CloseMalformedFrame, CloseClientClosed, CloseReadFailed).
//
//   - Truncated PDUs: a request the MBAP header delimited that has fewer
//     bytes than the fields its function code requires, or than its own
//     byte count announces. On a TCP stream that is what a client whose MBAP
//     length undercounts its request looks like, and the rest of that
//     request is then still in the stream, where it would be read as the
//     next MBAP header. It is treated as a synchronization error: nothing is
//     sent and the connection is closed (CloseProtocolError).
//
//   - Malformed but complete PDUs: every field the function code requires is
//     there, but the PDU carries bytes after them (a request of a function
//     code that takes no data, data beyond the byte count) or its fields
//     contradict one another (a sub-request that overruns the byte count).
//     The stream is in step. The server answers with Illegal Data Value
//     (exception 03), which MODBUS Application Protocol V1.1b3, section 7,
//     defines for "a fault in the structure of the remainder of a complex
//     request, such as that the implied length is incorrect", and keeps the
//     connection.
//
// A handler that returns ErrProtocolError itself still closes the connection
// (CloseProtocolError): that is the application's choice, and it is kept.

// malformedRequestError is the error of a request whose PDU does not have the
// structure its function code requires. Both kinds match ErrProtocolError,
// which ServerMetrics.OnError received for such a request up to v1.19.0. A
// truncated one closes the connection; any other also matches
// ErrIllegalDataValue, the exception the server answers it with.
type malformedRequestError struct {
	reason    string
	truncated bool
}

func (e *malformedRequestError) Error() string {
	if e.truncated {
		return "truncated request PDU: " + e.reason
	}
	return "malformed request PDU: " + e.reason
}

// Is reports whether target is ErrProtocolError, or ErrIllegalDataValue for a
// PDU that is not truncated.
func (e *malformedRequestError) Is(target error) bool {
	return target == ErrProtocolError || !e.truncated && target == ErrIllegalDataValue
}

// The errors of the server's own request checks. They are values, so a bad
// request costs no allocation.
var (
	// errPDUTruncated: fewer bytes than the function code's fields need.
	errPDUTruncated = &malformedRequestError{reason: "shorter than the fields of its function code", truncated: true}
	// errPDUShortOfByteCount: fewer bytes than the request's byte count announces.
	errPDUShortOfByteCount = &malformedRequestError{reason: "shorter than its byte count", truncated: true}
	// errPDUTrailing: bytes after the last field of the function code.
	errPDUTrailing = &malformedRequestError{reason: "bytes after the fields of its function code"}
	// errPDUBeyondByteCount: bytes after the data its byte count announces.
	errPDUBeyondByteCount = &malformedRequestError{reason: "bytes after the data its byte count announces"}
	// errPDUSubRequest: a sub-request that does not fit in the byte count.
	errPDUSubRequest = &malformedRequestError{reason: "sub-request overruns the byte count"}
	// errPDUOddData: FC08 data that is not a whole number of two-byte items.
	errPDUOddData = &malformedRequestError{reason: "diagnostics data is not a whole number of 16-bit items"}
)

// checkPDULength checks that a request of a function code whose PDU has a
// fixed size has exactly want bytes after the function code.
func checkPDULength(payload []byte, want int) error {
	switch {
	case len(payload) < want:
		return errPDUTruncated
	case len(payload) > want:
		return errPDUTrailing
	default:
		return nil
	}
}

// checkDataLength checks that have bytes of data follow a byte count that
// announces want.
func checkDataLength(have, want int) error {
	switch {
	case have < want:
		return errPDUShortOfByteCount
	case have > want:
		return errPDUBeyondByteCount
	default:
		return nil
	}
}

// exceptionFCRequestError is the error of a request whose function code has
// the high bit set (0x80-0xFF). Those codes are the exception responses
// (MODBUS Application Protocol V1.1b3, section 7): no request carries one,
// and the usual answer, the function code with 0x80 OR-ed in, is the
// request's own code again, which reads as an exception response to the
// function code 0x80 below it. The server sends nothing and keeps the
// connection, so that no response appears under the request's transaction ID
// that could be taken for the answer to another function; the client times
// out. The error matches ErrIllegalFunction, which ServerMetrics.OnError
// received for such a request up to v1.19.0.
type exceptionFCRequestError struct{}

func (exceptionFCRequestError) Error() string {
	return "request function code has the exception bit (0x80) set: not answered"
}

// Is reports whether target is ErrIllegalFunction.
func (exceptionFCRequestError) Is(target error) bool {
	return target == ErrIllegalFunction
}

var errExceptionFCRequest error = exceptionFCRequestError{}
