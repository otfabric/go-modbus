// SPDX-License-Identifier: MIT

package modbus

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// DiagnosticSubFunction is the two-byte sub-function code for Diagnostics (FC 0x08).
// Use the constants below for well-known sub-functions; raw uint16 values are valid.
type DiagnosticSubFunction uint16

const (
	DiagReturnQueryData                  DiagnosticSubFunction = 0x0000 // Loopback request data
	DiagRestartCommunications            DiagnosticSubFunction = 0x0001
	DiagReturnDiagnosticRegister         DiagnosticSubFunction = 0x0002
	DiagChangeASCIIInputDelimiter        DiagnosticSubFunction = 0x0003
	DiagForceListenOnlyMode              DiagnosticSubFunction = 0x0004
	DiagClearCountersAndDiagnosticReg    DiagnosticSubFunction = 0x000A
	DiagReturnBusMessageCount            DiagnosticSubFunction = 0x000B
	DiagReturnBusCommunicationErrorCount DiagnosticSubFunction = 0x000C
	DiagReturnBusExceptionErrorCount     DiagnosticSubFunction = 0x000D
	DiagReturnServerMessageCount         DiagnosticSubFunction = 0x000E
	DiagReturnServerNoResponseCount      DiagnosticSubFunction = 0x000F
	DiagReturnServerNAKCount             DiagnosticSubFunction = 0x0010
	DiagReturnServerBusyCount            DiagnosticSubFunction = 0x0011
	DiagReturnBusCharacterOverrunCount   DiagnosticSubFunction = 0x0012
	DiagClearOverrunCounterAndFlag       DiagnosticSubFunction = 0x0014
)

// String returns a short name for the sub-function for logging and debugging.
func (s DiagnosticSubFunction) String() string {
	switch s {
	case DiagReturnQueryData:
		return "ReturnQueryData"
	case DiagRestartCommunications:
		return "RestartCommunications"
	case DiagReturnDiagnosticRegister:
		return "ReturnDiagnosticRegister"
	case DiagChangeASCIIInputDelimiter:
		return "ChangeASCIIInputDelimiter"
	case DiagForceListenOnlyMode:
		return "ForceListenOnlyMode"
	case DiagClearCountersAndDiagnosticReg:
		return "ClearCountersAndDiagnosticReg"
	case DiagReturnBusMessageCount:
		return "ReturnBusMessageCount"
	case DiagReturnBusCommunicationErrorCount:
		return "ReturnBusCommunicationErrorCount"
	case DiagReturnBusExceptionErrorCount:
		return "ReturnBusExceptionErrorCount"
	case DiagReturnServerMessageCount:
		return "ReturnServerMessageCount"
	case DiagReturnServerNoResponseCount:
		return "ReturnServerNoResponseCount"
	case DiagReturnServerNAKCount:
		return "ReturnServerNAKCount"
	case DiagReturnServerBusyCount:
		return "ReturnServerBusyCount"
	case DiagReturnBusCharacterOverrunCount:
		return "ReturnBusCharacterOverrunCount"
	case DiagClearOverrunCounterAndFlag:
		return "ClearOverrunCounterAndFlag"
	default:
		return fmt.Sprintf("DiagnosticSubFunction(0x%04X)", uint16(s))
	}
}

// DiagnosticResponse is the response from Diagnostics (FC 0x08). SubFunction is
// echoed from the request; Data is the sub-function-specific data (e.g. loopback
// data, diagnostic register value).
type DiagnosticResponse struct {
	SubFunction DiagnosticSubFunction
	Data        []byte
}

// ReportServerIDResponse is the response from Report Server ID (FC 0x11).
// Data contains the raw device-specific payload after the byte count: the
// Server ID, the Run Indicator Status byte (0x00 OFF, 0xFF ON) and any
// Additional Data (MODBUS Application Protocol V1.1b3, section 6.13). The
// length of the Server ID is device specific, so where the run indicator is
// cannot be told from the response alone.
//
// RunIndicatorStatus is a guess from the last byte of Data, not a parse: it
// is set whenever Data has at least 2 bytes, true when that byte is 0xFF and
// false otherwise. That byte is the run indicator only on a device that sends
// no Additional Data. When the length of the device's Server ID is known, use
// RunIndicator instead, which also tells a byte that is neither 0x00 nor 0xFF.
type ReportServerIDResponse struct {
	Data               []byte
	RunIndicatorStatus *bool
}

// RunIndicator returns the Run Indicator Status of a device whose Server ID
// is serverIDLen bytes long: the byte of Data at that offset. ok is false
// when Data is too short for it or the byte is neither 0x00 (OFF) nor 0xFF
// (ON).
func (rs *ReportServerIDResponse) RunIndicator(serverIDLen int) (on, ok bool) {
	if serverIDLen < 0 || serverIDLen >= len(rs.Data) {
		return false, false
	}
	switch rs.Data[serverIDLen] {
	case 0xFF:
		return true, true
	case 0x00:
		return false, true
	default:
		return false, false
	}
}

// CommEventCounterResponse holds the parsed response from GetCommEventCounter (FC 0x0B).
type CommEventCounterResponse struct {
	Status     uint16 // 0xFFFF if device is busy processing; 0x0000 otherwise
	EventCount uint16
}

// CommEventLogResponse holds the parsed response from GetCommEventLog (FC 0x0C).
type CommEventLogResponse struct {
	Status       uint16
	EventCount   uint16
	MessageCount uint16
	Events       []byte // 0–64 event bytes, most recent first
}

// ReadExceptionStatus reads the eight Exception Status outputs (FC 0x07).
// Returns the status byte whose bits represent the eight outputs.
//
// The spec labels this function "Serial Line only," but it is supported on all
// transports because it can traverse gateways transparently.
func (mc *Client) ReadExceptionStatus(ctx context.Context, unitID uint8) (status uint8, err error) {
	req := &adu.Request{
		UnitID:       unitID,
		FunctionCode: byte(FCReadExceptionStatus),
	}

	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, FCReadExceptionStatus)
	}
	defer func() { reportOutcome(m, unitID, FCReadExceptionStatus, start, err) }()

	res, err := mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	if err = checkResponseFC(res, req.FunctionCode); err != nil {
		return
	}

	if len(res.Payload) != 1 {
		err = newProtocolError("ReadExceptionStatus",
			fmt.Sprintf("expected 1 byte payload, got %d", len(res.Payload)))
		return
	}
	status = res.Payload[0]
	return
}

// GetCommEventCounter reads the communication event counter (FC 0x0B).
// Returns the status word and event count from the remote device.
//
// The spec labels this function "Serial Line only," but it is supported on all
// transports because it can traverse gateways transparently.
func (mc *Client) GetCommEventCounter(ctx context.Context, unitID uint8) (cr *CommEventCounterResponse, err error) {
	req := &adu.Request{
		UnitID:       unitID,
		FunctionCode: byte(FCGetCommEventCounters),
	}

	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, FCGetCommEventCounters)
	}
	defer func() { reportOutcome(m, unitID, FCGetCommEventCounters, start, err) }()

	res, err := mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	if err = checkResponseFC(res, req.FunctionCode); err != nil {
		return
	}

	if len(res.Payload) != 4 {
		err = newProtocolError("GetCommEventCounter",
			fmt.Sprintf("expected 4 byte payload, got %d", len(res.Payload)))
		return
	}
	cr = &CommEventCounterResponse{
		Status:     bytesToUint16(BigEndian, res.Payload[0:2]),
		EventCount: bytesToUint16(BigEndian, res.Payload[2:4]),
	}
	return
}

// GetCommEventLog reads the communication event log (FC 0x0C).
// Returns status, event count, message count, and 0–64 event bytes.
//
// The spec labels this function "Serial Line only," but it is supported on all
// transports because it can traverse gateways transparently.
func (mc *Client) GetCommEventLog(ctx context.Context, unitID uint8) (cl *CommEventLogResponse, err error) {
	req := &adu.Request{
		UnitID:       unitID,
		FunctionCode: byte(FCGetCommEventLog),
	}

	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, FCGetCommEventLog)
	}
	defer func() { reportOutcome(m, unitID, FCGetCommEventLog, start, err) }()

	res, err := mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	if err = checkResponseFC(res, req.FunctionCode); err != nil {
		return
	}

	if len(res.Payload) < 1 {
		err = newProtocolError("GetCommEventLog", "empty payload, expected byte count prefix")
		return
	}
	byteCount := int(res.Payload[0])
	if len(res.Payload) != 1+byteCount {
		err = newProtocolError("GetCommEventLog",
			fmt.Sprintf("byte count %d does not match payload length %d", byteCount, len(res.Payload)-1))
		return
	}
	if byteCount < 6 {
		err = newProtocolError("GetCommEventLog",
			fmt.Sprintf("byte count %d too small, need at least 6 (Status + EventCount + MessageCount)", byteCount))
		return
	}
	// The event field holds 0-64 bytes (section 6.10).
	if byteCount > 6+maxCommEvents {
		err = newProtocolError("GetCommEventLog",
			fmt.Sprintf("byte count %d too large: at most %d (64 events)", byteCount, 6+maxCommEvents))
		return
	}
	cl = &CommEventLogResponse{
		Status:       bytesToUint16(BigEndian, res.Payload[1:3]),
		EventCount:   bytesToUint16(BigEndian, res.Payload[3:5]),
		MessageCount: bytesToUint16(BigEndian, res.Payload[5:7]),
		Events:       append([]byte(nil), res.Payload[7:1+byteCount]...),
	}
	return
}

// Diagnostics sends a Diagnostics request (FC 0x08). subFunction selects the
// diagnostic (use DiagnosticSubFunction constants; vendor sub-functions are
// sent as they are). data is the request data (sub-function-specific; nil or
// empty for none), a whole number of 16-bit items (section 6.8): an odd length
// is a *ParameterError (Param "data") and nothing is sent. The response must
// echo the sub-function; its data is returned as received, whatever its
// length, so that vendor sub-functions can be read. The typed wrappers below
// check the data of the sub-functions they send, and send the data field
// 00 00 where the specification gives it (section 6.8.1).
//
// At most 250 bytes of data fit a request. With more, Diagnostics returns a
// *ParameterError (Param "data") and sends nothing, on every transport. That
// error also matches, with errors.Is, what the transport's framer returned for
// such a request before Diagnostics checked it: ErrInvalidMBAPLength on tcp,
// tcp+tls and udp, ErrProtocolError on rtu, rtuovertcp, rtuoverudp, ascii and
// asciiovertcp. A function code that AllowedFunctionCodes refuses and a client
// that is not open are reported first, as for a request that fits, and
// ClientMetrics sees OnRequest and OnError for it.
func (mc *Client) Diagnostics(ctx context.Context, unitID uint8, subFunction DiagnosticSubFunction, data []byte) (dr *DiagnosticResponse, err error) {
	req := &adu.Request{
		UnitID:       unitID,
		FunctionCode: byte(FCDiagnostics),
		Payload:      uint16ToBytes(BigEndian, uint16(subFunction)),
	}

	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, FCDiagnostics)
	}
	defer func() { reportOutcome(m, unitID, FCDiagnostics, start, err) }()

	// maxDiagnosticsData (250) is also the limit of a request: the 253 bytes of
	// a PDU less the function code and the two bytes of the sub-function (MODBUS
	// Application Protocol V1.1b3, section 4.1).
	if len(data) > maxDiagnosticsData {
		if _, err = mc.requestEngine(req); err == nil {
			err = mc.requestSizeError("Diagnostics", "data",
				fmt.Sprintf("%d bytes, at most %d fit a request", len(data), maxDiagnosticsData))
		}
		return
	}
	// The data field is N x 2 bytes (section 6.8), for vendor sub-functions
	// too: an odd length cannot be a valid request.
	if len(data)%2 != 0 {
		if _, err = mc.requestEngine(req); err == nil {
			err = newParameterError("Diagnostics", "data",
				fmt.Sprintf("%d bytes: the data field is a whole number of 16-bit items", len(data)))
		}
		return
	}
	req.Payload = append(req.Payload, data...)

	res, err := mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	if err = checkResponseFC(res, req.FunctionCode); err != nil {
		return
	}

	if len(res.Payload) < 2 {
		err = newProtocolError("Diagnostics",
			fmt.Sprintf("payload too short: %d bytes, need at least 2", len(res.Payload)))
		return
	}
	respSubFunc := DiagnosticSubFunction(bytesToUint16(BigEndian, res.Payload[0:2]))
	if respSubFunc != subFunction {
		err = newProtocolError("Diagnostics",
			fmt.Sprintf("sub-function mismatch: expected 0x%04X (%s), got 0x%04X",
				uint16(subFunction), subFunction, uint16(respSubFunc)))
		return
	}
	dr = &DiagnosticResponse{
		SubFunction: respSubFunc,
		Data:        res.Payload[2:],
	}
	return
}

// diagNoData is the data field of the sub-functions whose request carries
// no information: "00 00" (MODBUS Application Protocol V1.1b3, section 6.8.1).
var diagNoData = []byte{0x00, 0x00}

// diagCounter sends sub-function sub with the data field 00 00 and returns the
// 16-bit value of the response's data field (a counter, or the diagnostic
// register), which must be exactly two bytes.
func (mc *Client) diagCounter(ctx context.Context, unitID uint8, sub DiagnosticSubFunction, op string) (uint16, error) {
	dr, err := mc.Diagnostics(ctx, unitID, sub, diagNoData)
	if err != nil {
		return 0, err
	}
	if len(dr.Data) != 2 {
		return 0, newProtocolError(op,
			fmt.Sprintf("expected exactly 2 data bytes, got %d", len(dr.Data)))
	}
	return bytesToUint16(BigEndian, dr.Data[0:2]), nil
}

// diagEcho sends sub-function sub with data and checks that the response
// echoes it, as section 6.8.1 defines for the sub-functions whose response is
// "Echo Request Data".
func (mc *Client) diagEcho(ctx context.Context, unitID uint8, sub DiagnosticSubFunction, data []byte, op string) error {
	dr, err := mc.Diagnostics(ctx, unitID, sub, data)
	if err != nil {
		return err
	}
	if !bytes.Equal(dr.Data, data) {
		return newProtocolError(op,
			fmt.Sprintf("response data % X is not the echo of the request data % X", dr.Data, data))
	}
	return nil
}

// DiagnosticLoopback sends a Return Query Data (sub-function 0x0000) request
// with value as its data and checks that the device echoes it. Useful for
// verifying communication with a remote device. It returns the echoed value;
// a response whose data is not the two bytes sent is a *ProtocolError (up to
// v1.19.0 a different value was returned without an error).
func (mc *Client) DiagnosticLoopback(ctx context.Context, unitID uint8, value uint16) (uint16, error) {
	if err := mc.diagEcho(ctx, unitID, DiagReturnQueryData, uint16ToBytes(BigEndian, value), "DiagnosticLoopback"); err != nil {
		return 0, err
	}
	return value, nil
}

// DiagnosticRegister reads the diagnostic register (sub-function 0x0002). The
// request carries the data field 00 00 (up to v1.19.0 it carried none).
func (mc *Client) DiagnosticRegister(ctx context.Context, unitID uint8) (uint16, error) {
	return mc.diagCounter(ctx, unitID, DiagReturnDiagnosticRegister, "DiagnosticRegister")
}

// BusMessageCount reads the bus message count (sub-function 0x000B). The
// request carries the data field 00 00 (up to v1.19.0 it carried none).
func (mc *Client) BusMessageCount(ctx context.Context, unitID uint8) (uint16, error) {
	return mc.diagCounter(ctx, unitID, DiagReturnBusMessageCount, "BusMessageCount")
}

// DiagnosticForceListenOnlyMode sends Force Listen Only Mode (sub-function
// 0x0004, data 00 00). The specification defines no response: a serial-line
// device silently enters listen-only mode, and the call then fails with
// ErrRequestTimedOut. Over TCP/TLS through a gateway, behavior depends on the
// gateway implementation: it may return a normal response, which is accepted
// whatever its data, or time out. This library's server has no listen-only
// state of its own; a DiagnosticsHandler may implement one.
func (mc *Client) DiagnosticForceListenOnlyMode(ctx context.Context, unitID uint8) error {
	_, err := mc.Diagnostics(ctx, unitID, DiagForceListenOnlyMode, diagNoData)
	return err
}

// DiagnosticClearCounters clears all counters and the diagnostic register
// (sub-function 0x000A). The response must echo the request data 00 00.
func (mc *Client) DiagnosticClearCounters(ctx context.Context, unitID uint8) error {
	return mc.diagEcho(ctx, unitID, DiagClearCountersAndDiagnosticReg, diagNoData, "DiagnosticClearCounters")
}

// DiagnosticBusCommunicationErrorCount reads the bus communication error count (sub-function 0x000C).
func (mc *Client) DiagnosticBusCommunicationErrorCount(ctx context.Context, unitID uint8) (uint16, error) {
	return mc.diagCounter(ctx, unitID, DiagReturnBusCommunicationErrorCount, "DiagnosticBusCommunicationErrorCount")
}

// DiagnosticBusExceptionErrorCount reads the bus exception error count (sub-function 0x000D).
func (mc *Client) DiagnosticBusExceptionErrorCount(ctx context.Context, unitID uint8) (uint16, error) {
	return mc.diagCounter(ctx, unitID, DiagReturnBusExceptionErrorCount, "DiagnosticBusExceptionErrorCount")
}

// DiagnosticServerMessageCount reads the server message count (sub-function 0x000E).
func (mc *Client) DiagnosticServerMessageCount(ctx context.Context, unitID uint8) (uint16, error) {
	return mc.diagCounter(ctx, unitID, DiagReturnServerMessageCount, "DiagnosticServerMessageCount")
}

// DiagnosticServerNoResponseCount reads the server no-response count (sub-function 0x000F).
func (mc *Client) DiagnosticServerNoResponseCount(ctx context.Context, unitID uint8) (uint16, error) {
	return mc.diagCounter(ctx, unitID, DiagReturnServerNoResponseCount, "DiagnosticServerNoResponseCount")
}

// DiagnosticServerNAKCount reads the server NAK count (sub-function 0x0010).
func (mc *Client) DiagnosticServerNAKCount(ctx context.Context, unitID uint8) (uint16, error) {
	return mc.diagCounter(ctx, unitID, DiagReturnServerNAKCount, "DiagnosticServerNAKCount")
}

// DiagnosticServerBusyCount reads the server busy count (sub-function 0x0011).
func (mc *Client) DiagnosticServerBusyCount(ctx context.Context, unitID uint8) (uint16, error) {
	return mc.diagCounter(ctx, unitID, DiagReturnServerBusyCount, "DiagnosticServerBusyCount")
}

// DiagnosticBusCharacterOverrunCount reads the bus character overrun count (sub-function 0x0012).
func (mc *Client) DiagnosticBusCharacterOverrunCount(ctx context.Context, unitID uint8) (uint16, error) {
	return mc.diagCounter(ctx, unitID, DiagReturnBusCharacterOverrunCount, "DiagnosticBusCharacterOverrunCount")
}

// DiagnosticClearOverrunCounterAndFlag clears the overrun counter and error
// flag (sub-function 0x0014). The response must echo the request data 00 00.
func (mc *Client) DiagnosticClearOverrunCounterAndFlag(ctx context.Context, unitID uint8) error {
	return mc.diagEcho(ctx, unitID, DiagClearOverrunCounterAndFlag, diagNoData, "DiagnosticClearOverrunCounterAndFlag")
}

// ReportServerID requests the Report Server ID (FC 0x11). The response contains
// device-specific server ID, run indicator status, and optional additional data.
func (mc *Client) ReportServerID(ctx context.Context, unitID uint8) (rs *ReportServerIDResponse, err error) {
	req := &adu.Request{
		UnitID:       unitID,
		FunctionCode: byte(FCReportServerID),
	}

	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, FCReportServerID)
	}
	defer func() { reportOutcome(m, unitID, FCReportServerID, start, err) }()

	res, err := mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	if err = checkResponseFC(res, req.FunctionCode); err != nil {
		return
	}

	if len(res.Payload) < 1 {
		err = newProtocolError("ReportServerID", "empty payload, expected byte count prefix")
		return
	}
	byteCount := res.Payload[0]
	if len(res.Payload) != 1+int(byteCount) {
		err = newProtocolError("ReportServerID",
			fmt.Sprintf("byte count %d does not match payload length %d", byteCount, len(res.Payload)-1))
		return
	}
	// The length was checked above: the data is the rest of the payload.
	data := append([]byte(nil), res.Payload[1:]...)
	rs = &ReportServerIDResponse{
		Data: data,
	}
	// A response with Byte Count 0 has no run indicator, which the
	// specification makes mandatory; it is passed on as received (Data empty).
	if len(data) >= 2 {
		status := data[len(data)-1] == 0xFF
		rs.RunIndicatorStatus = &status
	}
	return
}
