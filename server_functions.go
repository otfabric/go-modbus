// SPDX-License-Identifier: MIT

package modbus

import (
	"context"

	"github.com/otfabric/go-modbus/internal/adu"
)

// This file holds the optional handlers for the function codes beyond the
// basic data model: Diagnostics (FC08), Report Server ID (FC17), Read and
// Write File Record (FC20, FC21) and Read FIFO Queue (FC24), plus a raw
// handler for function codes the server does not decode itself.
//
// Like the other optional handlers, each is used when the RequestHandler
// passed to NewServer also implements it; otherwise the request goes to the
// RawRequestHandler, and without one it is answered with Illegal Function. The server parses and validates the request, calls
// the handler, and encodes the response; an error returned by a handler is
// sent as the matching exception, and ErrNoResponse sends nothing.

// DiagnosticsRequest is passed to DiagnosticsHandler.HandleDiagnostics for
// FC08 (Diagnostics) requests.
type DiagnosticsRequest struct {
	ClientAddr   string
	ClientRole   string
	UnitID       uint8
	FunctionCode FunctionCode
	// SubFunction selects the diagnostic (see the DiagnosticSubFunction
	// constants); vendor sub-functions are passed through as they are.
	SubFunction DiagnosticSubFunction
	// Data is the request data after the sub-function, possibly empty.
	Data []byte
}

// DiagnosticsHandler is an optional interface for serving FC08 (Diagnostics).
// The handler returns the response data; the server echoes the sub-function
// in front of it. For Return Query Data (0x0000) the data should echo
// req.Data. For Force Listen Only Mode (0x0004), a serial-line device sends no
// response: return ErrNoResponse to behave like one. Other state changes
// (restart communications, clear counters) are the handler's to apply.
// At most 250 data bytes fit in a response.
type DiagnosticsHandler interface {
	HandleDiagnostics(ctx context.Context, req *DiagnosticsRequest) (data []byte, err error)
}

// ReportServerIDRequest is passed to ReportServerIDHandler.HandleReportServerID
// for FC17 (Report Server ID) requests.
type ReportServerIDRequest struct {
	ClientAddr   string
	ClientRole   string
	UnitID       uint8
	FunctionCode FunctionCode
}

// ReportServerIDHandler is an optional interface for serving FC17 (Report
// Server ID). The handler returns the device-specific payload: by convention
// the server ID bytes, a run indicator byte (0xFF on, 0x00 off) and optional
// additional data (Client.ReportServerID reads the last byte as the run
// indicator). The server adds the byte count. At most 251 bytes fit.
type ReportServerIDHandler interface {
	HandleReportServerID(ctx context.Context, req *ReportServerIDRequest) (data []byte, err error)
}

// ReadFileRecordsRequest is passed to ReadFileRecordHandler.HandleReadFileRecords
// for FC20 (Read File Record) requests.
type ReadFileRecordsRequest struct {
	ClientAddr   string
	ClientRole   string
	UnitID       uint8
	FunctionCode FunctionCode
	// Requests are the sub-requests, in request order.
	Requests []FileRecordRequest
}

// ReadFileRecordHandler is an optional interface for serving FC20 (Read File
// Record). The handler returns one slice of registers per sub-request, in the
// same order, each exactly RecordLength registers long; anything else is a
// handler error answered with Server Device Failure. Return
// ErrIllegalDataAddress for a file or record that does not exist.
type ReadFileRecordHandler interface {
	HandleReadFileRecords(ctx context.Context, req *ReadFileRecordsRequest) (records [][]uint16, err error)
}

// WriteFileRecordsRequest is passed to
// WriteFileRecordHandler.HandleWriteFileRecords for FC21 (Write File Record)
// requests.
type WriteFileRecordsRequest struct {
	ClientAddr   string
	ClientRole   string
	UnitID       uint8
	FunctionCode FunctionCode
	// Records are the sub-requests, in request order.
	Records []FileRecord
}

// WriteFileRecordHandler is an optional interface for serving FC21 (Write
// File Record). On success the server answers with the echo of the request.
type WriteFileRecordHandler interface {
	HandleWriteFileRecords(ctx context.Context, req *WriteFileRecordsRequest) error
}

// FIFOQueueRequest is passed to FIFOQueueHandler.HandleReadFIFOQueue for FC24
// (Read FIFO Queue) requests.
type FIFOQueueRequest struct {
	ClientAddr   string
	ClientRole   string
	UnitID       uint8
	FunctionCode FunctionCode
	// Addr is the FIFO pointer address.
	Addr uint16
}

// FIFOQueueHandler is an optional interface for serving FC24 (Read FIFO
// Queue). The handler returns the queued register values, oldest first. A
// queue of more than 31 values is answered with Illegal Data Value, as the
// specification requires.
type FIFOQueueHandler interface {
	HandleReadFIFOQueue(ctx context.Context, req *FIFOQueueRequest) (values []uint16, err error)
}

// RawRequest is passed to RawRequestHandler.HandleRawRequest.
type RawRequest struct {
	ClientAddr   string
	ClientRole   string
	UnitID       uint8
	FunctionCode FunctionCode
	// Payload is the request PDU after the function code.
	Payload []byte
}

// RawRequestHandler is an optional interface for serving function codes the
// server does not handle itself: vendor and user-defined function codes, and
// standard ones whose optional handler the RequestHandler does not implement
// (for example a device that answers FC07 from a recording rather than through
// ExceptionStatusHandler). The handler returns the response PDU after the
// function code; the server prepends the function code. Function codes 0x01
// to 0x06, 0x0F and 0x10, which RequestHandler serves, never reach it.
type RawRequestHandler interface {
	HandleRawRequest(ctx context.Context, req *RawRequest) (payload []byte, err error)
}

// Response size limits, in bytes of response PDU after the function code.
const (
	maxDiagnosticsData   = maxPDUPayload - 2
	maxReportServerIDLen = maxPDUPayload - 1
	maxPDUPayload        = 252
)

// handleRaw answers req with the RawRequestHandler, if there is one, and with
// Illegal Function otherwise.
func (ms *Server) handleRaw(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	h, ok := ms.handler.(RawRequestHandler)
	if !ok {
		return nil, ErrIllegalFunction
	}
	payload, err := h.HandleRawRequest(ctx, &RawRequest{
		ClientAddr:   clientAddr,
		ClientRole:   clientRole,
		UnitID:       req.UnitID,
		FunctionCode: FunctionCode(req.FunctionCode),
		Payload:      append([]byte(nil), req.Payload...),
	})
	if err != nil {
		return nil, err
	}
	if len(payload) > maxPDUPayload {
		return nil, ErrServerDeviceFailure
	}
	return newSuccessResponse(req, txnID, payload), nil
}

// handleDiagnostics serves FC08.
func (ms *Server) handleDiagnostics(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	h, ok := ms.handler.(DiagnosticsHandler)
	if !ok {
		return ms.handleRaw(ctx, req, txnID, clientAddr, clientRole)
	}
	if len(req.Payload) < 2 {
		return nil, ErrProtocolError
	}
	sub := req.Payload[0:2]
	data, err := h.HandleDiagnostics(ctx, &DiagnosticsRequest{
		ClientAddr:   clientAddr,
		ClientRole:   clientRole,
		UnitID:       req.UnitID,
		FunctionCode: FunctionCode(req.FunctionCode),
		SubFunction:  DiagnosticSubFunction(bytesToUint16(BigEndian, sub)),
		Data:         append([]byte(nil), req.Payload[2:]...),
	})
	if err != nil {
		return nil, err
	}
	if len(data) > maxDiagnosticsData {
		return nil, ErrServerDeviceFailure
	}
	return newSuccessResponse(req, txnID, append(append([]byte(nil), sub...), data...)), nil
}

// handleReportServerID serves FC17.
func (ms *Server) handleReportServerID(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	h, ok := ms.handler.(ReportServerIDHandler)
	if !ok {
		return ms.handleRaw(ctx, req, txnID, clientAddr, clientRole)
	}
	if len(req.Payload) != 0 {
		return nil, ErrProtocolError
	}
	data, err := h.HandleReportServerID(ctx, &ReportServerIDRequest{
		ClientAddr:   clientAddr,
		ClientRole:   clientRole,
		UnitID:       req.UnitID,
		FunctionCode: FunctionCode(req.FunctionCode),
	})
	if err != nil {
		return nil, err
	}
	if len(data) > maxReportServerIDLen {
		return nil, ErrServerDeviceFailure
	}
	return newSuccessResponse(req, txnID, append([]byte{byte(len(data))}, data...)), nil
}

// handleReadFileRecords serves FC20.
func (ms *Server) handleReadFileRecords(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	h, ok := ms.handler.(ReadFileRecordHandler)
	if !ok {
		return ms.handleRaw(ctx, req, txnID, clientAddr, clientRole)
	}
	p := req.Payload
	if len(p) < 1 || int(p[0]) != len(p)-1 {
		return nil, ErrProtocolError
	}
	n := int(p[0])
	if n < 7 || n > maxFileByteCount || n%7 != 0 {
		return nil, ErrIllegalDataValue
	}
	var subs []FileRecordRequest
	respLen := 0
	for off := 1; off < len(p); off += 7 {
		if p[off] != 0x06 {
			return nil, ErrIllegalDataAddress
		}
		r := FileRecordRequest{
			FileNumber:   bytesToUint16(BigEndian, p[off+1:off+3]),
			RecordNumber: bytesToUint16(BigEndian, p[off+3:off+5]),
			RecordLength: bytesToUint16(BigEndian, p[off+5:off+7]),
		}
		if r.FileNumber == 0 || r.RecordNumber > 0x270F || r.RecordLength == 0 {
			return nil, ErrIllegalDataAddress
		}
		respLen += 2 + 2*int(r.RecordLength)
		subs = append(subs, r)
	}
	// The response must fit in one PDU: a request that asks for more is invalid.
	if respLen > maxFileByteCount {
		return nil, ErrIllegalDataValue
	}
	records, err := h.HandleReadFileRecords(ctx, &ReadFileRecordsRequest{
		ClientAddr:   clientAddr,
		ClientRole:   clientRole,
		UnitID:       req.UnitID,
		FunctionCode: FunctionCode(req.FunctionCode),
		Requests:     subs,
	})
	if err != nil {
		return nil, err
	}
	if len(records) != len(subs) {
		return nil, ErrServerDeviceFailure
	}
	payload := []byte{byte(respLen)}
	for i, rec := range records {
		if len(rec) != int(subs[i].RecordLength) {
			return nil, ErrServerDeviceFailure
		}
		payload = append(payload, byte(1+2*len(rec)), 0x06)
		payload = append(payload, uint16sToBytes(BigEndian, rec)...)
	}
	return newSuccessResponse(req, txnID, payload), nil
}

// handleWriteFileRecords serves FC21.
func (ms *Server) handleWriteFileRecords(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	h, ok := ms.handler.(WriteFileRecordHandler)
	if !ok {
		return ms.handleRaw(ctx, req, txnID, clientAddr, clientRole)
	}
	p := req.Payload
	if len(p) < 1 || int(p[0]) != len(p)-1 {
		return nil, ErrProtocolError
	}
	if n := int(p[0]); n < 9 || n > maxFileReqDataLen {
		return nil, ErrIllegalDataValue
	}
	var recs []FileRecord
	for off := 1; off < len(p); {
		if off+7 > len(p) {
			return nil, ErrProtocolError
		}
		if p[off] != 0x06 {
			return nil, ErrIllegalDataAddress
		}
		r := FileRecord{
			FileNumber:   bytesToUint16(BigEndian, p[off+1:off+3]),
			RecordNumber: bytesToUint16(BigEndian, p[off+3:off+5]),
		}
		length := int(bytesToUint16(BigEndian, p[off+5:off+7]))
		off += 7
		if off+2*length > len(p) {
			return nil, ErrProtocolError
		}
		if r.FileNumber == 0 || r.RecordNumber > 0x270F || length == 0 {
			return nil, ErrIllegalDataAddress
		}
		r.Data = bytesToUint16s(BigEndian, p[off:off+2*length])
		off += 2 * length
		recs = append(recs, r)
	}
	if err := h.HandleWriteFileRecords(ctx, &WriteFileRecordsRequest{
		ClientAddr:   clientAddr,
		ClientRole:   clientRole,
		UnitID:       req.UnitID,
		FunctionCode: FunctionCode(req.FunctionCode),
		Records:      recs,
	}); err != nil {
		return nil, err
	}
	return newSuccessResponse(req, txnID, append([]byte(nil), p...)), nil
}

// handleReadFIFOQueue serves FC24.
func (ms *Server) handleReadFIFOQueue(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	h, ok := ms.handler.(FIFOQueueHandler)
	if !ok {
		return ms.handleRaw(ctx, req, txnID, clientAddr, clientRole)
	}
	if len(req.Payload) != 2 {
		return nil, ErrProtocolError
	}
	values, err := h.HandleReadFIFOQueue(ctx, &FIFOQueueRequest{
		ClientAddr:   clientAddr,
		ClientRole:   clientRole,
		UnitID:       req.UnitID,
		FunctionCode: FunctionCode(req.FunctionCode),
		Addr:         bytesToUint16(BigEndian, req.Payload),
	})
	if err != nil {
		return nil, err
	}
	if len(values) > maxFIFOCount {
		return nil, ErrIllegalDataValue
	}
	payload := uint16ToBytes(BigEndian, uint16(2+2*len(values)))
	payload = append(payload, uint16ToBytes(BigEndian, uint16(len(values)))...)
	payload = append(payload, uint16sToBytes(BigEndian, values)...)
	return newSuccessResponse(req, txnID, payload), nil
}
