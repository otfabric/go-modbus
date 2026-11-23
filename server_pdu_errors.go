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
//   - PDU length errors: a request the MBAP header delimited whose PDU is
//     shorter or longer than the fields of its function code, or than its
//     own byte count announces. On a TCP stream that is also what a client
//     whose MBAP length undercounts or overcounts its request looks like:
//     the rest of that request is then still in the stream, or the server
//     has taken bytes of the next one. The server cannot tell the two apart,
//     so it treats the stream as out of step. It answers with Illegal Data
//     Value (exception 03), which MODBUS Application Protocol V1.1b3,
//     section 7, defines for "a fault in the structure of the remainder of a
//     complex request, such as that the implied length is incorrect", as a
//     best effort, and then closes the connection (CloseProtocolError)
//     without reading or serving anything after it. For a function code
//     whose requests have a fixed size (requestPDULength) the MBAP length is
//     checked as soon as the function code is read, so the server neither
//     waits for nor takes bytes the MBAP length overcounts.
//
//   - Inconsistent but complete PDUs: the length agrees with the fields and
//     the byte count, but the fields contradict one another (an FC21
//     sub-request that overruns the byte count) or the data is not what the
//     function takes (FC08 data of odd length). The framing is not in doubt:
//     the server answers with exception 03 and keeps the connection.
//
// A handler that returns ErrProtocolError itself still closes the connection
// (CloseProtocolError) without an answer: that is the application's choice,
// and it is kept.

// malformedRequestError is the error of a request whose PDU does not have the
// structure its function code requires. Every kind matches ErrProtocolError,
// which ServerMetrics.OnError received for such a request up to v1.19.0, and
// ErrIllegalDataValue, the exception the server answers it with. A length
// error (closeLink) then closes the connection.
type malformedRequestError struct {
	reason    string
	truncated bool
	closeLink bool
}

func (e *malformedRequestError) Error() string {
	if e.truncated {
		return "truncated request PDU: " + e.reason
	}
	return "malformed request PDU: " + e.reason
}

// Is reports whether target is ErrProtocolError or ErrIllegalDataValue.
func (e *malformedRequestError) Is(target error) bool {
	return target == ErrProtocolError || target == ErrIllegalDataValue
}

// The errors of the server's own request checks. They are values, so a bad
// request costs no allocation.
var (
	// errPDUTruncated: fewer bytes than the function code's fields need.
	errPDUTruncated = &malformedRequestError{reason: "shorter than the fields of its function code", truncated: true, closeLink: true}
	// errPDUShortOfByteCount: fewer bytes than the request's byte count announces.
	errPDUShortOfByteCount = &malformedRequestError{reason: "shorter than its byte count", truncated: true, closeLink: true}
	// errPDUTrailing: bytes after the last field of the function code.
	errPDUTrailing = &malformedRequestError{reason: "bytes after the fields of its function code", closeLink: true}
	// errPDUBeyondByteCount: bytes after the data its byte count announces.
	errPDUBeyondByteCount = &malformedRequestError{reason: "bytes after the data its byte count announces", closeLink: true}
	// errPDUSubRequest: a sub-request that does not fit in the byte count.
	errPDUSubRequest = &malformedRequestError{reason: "sub-request overruns the byte count"}
	// errPDUOddData: FC08 data that is not a whole number of two-byte items.
	errPDUOddData = &malformedRequestError{reason: "diagnostics data is not a whole number of 16-bit items"}
)

// requestPDULength returns the length of the request PDU, function code
// included, of a function code whose requests have a fixed size (V1.1b3,
// sections 6.1-6.6, 6.7, 6.9, 6.10, 6.13, 6.16, 6.18), and 0 for any other.
// FC43 is not among them: its length depends on the MEI type. The handlers of
// these function codes check the same lengths (checkPDULength).
func requestPDULength(fc byte) int {
	switch FunctionCode(fc) {
	case FCReadCoils, FCReadDiscreteInputs, FCReadHoldingRegisters, FCReadInputRegisters,
		FCWriteSingleCoil, FCWriteSingleRegister:
		return 5
	case FCReadExceptionStatus, FCGetCommEventCounters, FCGetCommEventLog, FCReportServerID:
		return 1
	case FCMaskWriteRegister:
		return 7
	case FCReadFIFOQueue:
		return 3
	default:
		return 0
	}
}

// servesFixedLength reports whether the server dispatches fc, a function code
// requestPDULength gives a length for, to a handler: the core functions
// always, the optional ones when the handler implements their interface. It
// mirrors the first check of their handle methods, so that a request of such
// a function code whose length the transport rejected is answered with the
// exception the full request would have had first: Illegal Function before
// Illegal Data Value.
func (ms *Server) servesFixedLength(fc FunctionCode) bool {
	var ok bool
	switch fc {
	case FCReadCoils, FCReadDiscreteInputs, FCReadHoldingRegisters, FCReadInputRegisters,
		FCWriteSingleCoil, FCWriteSingleRegister:
		ok = true
	case FCReadExceptionStatus:
		_, ok = ms.handler.(ExceptionStatusHandler)
	case FCGetCommEventCounters:
		_, ok = ms.handler.(CommEventCounterHandler)
	case FCGetCommEventLog:
		_, ok = ms.handler.(CommEventLogHandler)
	case FCReportServerID:
		_, ok = ms.handler.(ReportServerIDHandler)
	case FCMaskWriteRegister:
		_, ok = ms.handler.(MaskWriteHandler)
	case FCReadFIFOQueue:
		_, ok = ms.handler.(FIFOQueueHandler)
	default:
	}
	return ok
}

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
