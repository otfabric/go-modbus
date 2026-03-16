package modbus

import (
	"context"
	"fmt"
	"time"

	"github.com/otfabric/modbus/internal/adu"
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
// Data contains the raw device-specific payload (server ID bytes, run indicator,
// and any optional additional data).
//
// RunIndicatorStatus is a heuristic parse: when the payload contains at least
// 2 bytes, the last byte is interpreted as the run indicator (0xFF = ON,
// 0x00 = OFF). This follows the common convention but may misinterpret
// vendor-specific data on devices that pack additional information into the
// payload. When the heuristic does not apply (fewer than 2 bytes), the field
// is nil. Callers that need strict parsing should inspect Data directly.
type ReportServerIDResponse struct {
	Data               []byte
	RunIndicatorStatus *bool
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
	cl = &CommEventLogResponse{
		Status:       bytesToUint16(BigEndian, res.Payload[1:3]),
		EventCount:   bytesToUint16(BigEndian, res.Payload[3:5]),
		MessageCount: bytesToUint16(BigEndian, res.Payload[5:7]),
		Events:       append([]byte(nil), res.Payload[7:1+byteCount]...),
	}
	return
}

// Diagnostics sends a Diagnostics request (FC 0x08). subFunction selects the
// diagnostic (use DiagnosticSubFunction constants). data is optional request
// data (sub-function-specific; use nil or empty for none). The response echoes
// the sub-function and returns sub-function-specific data.
func (mc *Client) Diagnostics(ctx context.Context, unitID uint8, subFunction DiagnosticSubFunction, data []byte) (dr *DiagnosticResponse, err error) {
	req := &adu.Request{
		UnitID:       unitID,
		FunctionCode: byte(FCDiagnostics),
		Payload:      uint16ToBytes(BigEndian, uint16(subFunction)),
	}
	if len(data) > 0 {
		req.Payload = append(req.Payload, data...)
	}

	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, FCDiagnostics)
	}
	defer func() { reportOutcome(m, unitID, FCDiagnostics, start, err) }()

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

// DiagnosticLoopback sends a Return Query Data (sub-function 0x0000) request and
// returns the echoed value. Useful for verifying communication with a remote device.
func (mc *Client) DiagnosticLoopback(ctx context.Context, unitID uint8, value uint16) (uint16, error) {
	dr, err := mc.Diagnostics(ctx, unitID, DiagReturnQueryData, uint16ToBytes(BigEndian, value))
	if err != nil {
		return 0, err
	}
	if len(dr.Data) != 2 {
		return 0, newProtocolError("DiagnosticLoopback",
			fmt.Sprintf("expected exactly 2 data bytes, got %d", len(dr.Data)))
	}
	return bytesToUint16(BigEndian, dr.Data[0:2]), nil
}

// DiagnosticRegister reads the diagnostic register (sub-function 0x0002).
func (mc *Client) DiagnosticRegister(ctx context.Context, unitID uint8) (uint16, error) {
	dr, err := mc.Diagnostics(ctx, unitID, DiagReturnDiagnosticRegister, nil)
	if err != nil {
		return 0, err
	}
	if len(dr.Data) != 2 {
		return 0, newProtocolError("DiagnosticRegister",
			fmt.Sprintf("expected exactly 2 data bytes, got %d", len(dr.Data)))
	}
	return bytesToUint16(BigEndian, dr.Data[0:2]), nil
}

// BusMessageCount reads the bus message count (sub-function 0x000B).
func (mc *Client) BusMessageCount(ctx context.Context, unitID uint8) (uint16, error) {
	dr, err := mc.Diagnostics(ctx, unitID, DiagReturnBusMessageCount, nil)
	if err != nil {
		return 0, err
	}
	if len(dr.Data) != 2 {
		return 0, newProtocolError("BusMessageCount",
			fmt.Sprintf("expected exactly 2 data bytes, got %d", len(dr.Data)))
	}
	return bytesToUint16(BigEndian, dr.Data[0:2]), nil
}

// DiagnosticForceListenOnlyMode sends Force Listen Only Mode (sub-function 0x0004).
// Per the spec, no response is returned by serial-line devices (the device silently
// enters listen-only mode). Over TCP/TLS through a gateway, behavior depends on the
// gateway implementation — it may return a normal echo or time out.
func (mc *Client) DiagnosticForceListenOnlyMode(ctx context.Context, unitID uint8) error {
	_, err := mc.Diagnostics(ctx, unitID, DiagForceListenOnlyMode, uint16ToBytes(BigEndian, 0x0000))
	return err
}

// DiagnosticClearCounters clears all counters and the diagnostic register (sub-function 0x000A).
func (mc *Client) DiagnosticClearCounters(ctx context.Context, unitID uint8) error {
	dr, err := mc.Diagnostics(ctx, unitID, DiagClearCountersAndDiagnosticReg, uint16ToBytes(BigEndian, 0x0000))
	if err != nil {
		return err
	}
	if len(dr.Data) != 2 {
		return newProtocolError("DiagnosticClearCounters",
			fmt.Sprintf("expected exactly 2 data bytes, got %d", len(dr.Data)))
	}
	return nil
}

// DiagnosticBusCommunicationErrorCount reads the bus communication error count (sub-function 0x000C).
func (mc *Client) DiagnosticBusCommunicationErrorCount(ctx context.Context, unitID uint8) (uint16, error) {
	dr, err := mc.Diagnostics(ctx, unitID, DiagReturnBusCommunicationErrorCount, uint16ToBytes(BigEndian, 0x0000))
	if err != nil {
		return 0, err
	}
	if len(dr.Data) != 2 {
		return 0, newProtocolError("DiagnosticBusCommunicationErrorCount",
			fmt.Sprintf("expected exactly 2 data bytes, got %d", len(dr.Data)))
	}
	return bytesToUint16(BigEndian, dr.Data[0:2]), nil
}

// DiagnosticBusExceptionErrorCount reads the bus exception error count (sub-function 0x000D).
func (mc *Client) DiagnosticBusExceptionErrorCount(ctx context.Context, unitID uint8) (uint16, error) {
	dr, err := mc.Diagnostics(ctx, unitID, DiagReturnBusExceptionErrorCount, uint16ToBytes(BigEndian, 0x0000))
	if err != nil {
		return 0, err
	}
	if len(dr.Data) != 2 {
		return 0, newProtocolError("DiagnosticBusExceptionErrorCount",
			fmt.Sprintf("expected exactly 2 data bytes, got %d", len(dr.Data)))
	}
	return bytesToUint16(BigEndian, dr.Data[0:2]), nil
}

// DiagnosticServerMessageCount reads the server message count (sub-function 0x000E).
func (mc *Client) DiagnosticServerMessageCount(ctx context.Context, unitID uint8) (uint16, error) {
	dr, err := mc.Diagnostics(ctx, unitID, DiagReturnServerMessageCount, uint16ToBytes(BigEndian, 0x0000))
	if err != nil {
		return 0, err
	}
	if len(dr.Data) != 2 {
		return 0, newProtocolError("DiagnosticServerMessageCount",
			fmt.Sprintf("expected exactly 2 data bytes, got %d", len(dr.Data)))
	}
	return bytesToUint16(BigEndian, dr.Data[0:2]), nil
}

// DiagnosticServerNoResponseCount reads the server no-response count (sub-function 0x000F).
func (mc *Client) DiagnosticServerNoResponseCount(ctx context.Context, unitID uint8) (uint16, error) {
	dr, err := mc.Diagnostics(ctx, unitID, DiagReturnServerNoResponseCount, uint16ToBytes(BigEndian, 0x0000))
	if err != nil {
		return 0, err
	}
	if len(dr.Data) != 2 {
		return 0, newProtocolError("DiagnosticServerNoResponseCount",
			fmt.Sprintf("expected exactly 2 data bytes, got %d", len(dr.Data)))
	}
	return bytesToUint16(BigEndian, dr.Data[0:2]), nil
}

// DiagnosticServerNAKCount reads the server NAK count (sub-function 0x0010).
func (mc *Client) DiagnosticServerNAKCount(ctx context.Context, unitID uint8) (uint16, error) {
	dr, err := mc.Diagnostics(ctx, unitID, DiagReturnServerNAKCount, uint16ToBytes(BigEndian, 0x0000))
	if err != nil {
		return 0, err
	}
	if len(dr.Data) != 2 {
		return 0, newProtocolError("DiagnosticServerNAKCount",
			fmt.Sprintf("expected exactly 2 data bytes, got %d", len(dr.Data)))
	}
	return bytesToUint16(BigEndian, dr.Data[0:2]), nil
}

// DiagnosticServerBusyCount reads the server busy count (sub-function 0x0011).
func (mc *Client) DiagnosticServerBusyCount(ctx context.Context, unitID uint8) (uint16, error) {
	dr, err := mc.Diagnostics(ctx, unitID, DiagReturnServerBusyCount, uint16ToBytes(BigEndian, 0x0000))
	if err != nil {
		return 0, err
	}
	if len(dr.Data) != 2 {
		return 0, newProtocolError("DiagnosticServerBusyCount",
			fmt.Sprintf("expected exactly 2 data bytes, got %d", len(dr.Data)))
	}
	return bytesToUint16(BigEndian, dr.Data[0:2]), nil
}

// DiagnosticBusCharacterOverrunCount reads the bus character overrun count (sub-function 0x0012).
func (mc *Client) DiagnosticBusCharacterOverrunCount(ctx context.Context, unitID uint8) (uint16, error) {
	dr, err := mc.Diagnostics(ctx, unitID, DiagReturnBusCharacterOverrunCount, uint16ToBytes(BigEndian, 0x0000))
	if err != nil {
		return 0, err
	}
	if len(dr.Data) != 2 {
		return 0, newProtocolError("DiagnosticBusCharacterOverrunCount",
			fmt.Sprintf("expected exactly 2 data bytes, got %d", len(dr.Data)))
	}
	return bytesToUint16(BigEndian, dr.Data[0:2]), nil
}

// DiagnosticClearOverrunCounterAndFlag clears the overrun counter and error flag (sub-function 0x0014).
func (mc *Client) DiagnosticClearOverrunCounterAndFlag(ctx context.Context, unitID uint8) error {
	dr, err := mc.Diagnostics(ctx, unitID, DiagClearOverrunCounterAndFlag, uint16ToBytes(BigEndian, 0x0000))
	if err != nil {
		return err
	}
	if len(dr.Data) != 2 {
		return newProtocolError("DiagnosticClearOverrunCounterAndFlag",
			fmt.Sprintf("expected exactly 2 data bytes, got %d", len(dr.Data)))
	}
	return nil
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
	data := append([]byte(nil), res.Payload[1:1+byteCount]...)
	rs = &ReportServerIDResponse{
		Data: data,
	}
	if len(data) >= 2 {
		status := data[len(data)-1] == 0xFF
		rs.RunIndicatorStatus = &status
	}
	return
}
