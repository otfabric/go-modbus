// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"reflect"
	"testing"

	"github.com/otfabric/go-modbus/internal/adu"
)

// fnFunctions is a device whose FC08/17/20/21/24 handlers are functions; the
// mandatory handlers refuse everything.
type fnFunctions struct {
	noSerialFCHandler
	diag      func(*DiagnosticsRequest) ([]byte, error)
	serverID  func(*ReportServerIDRequest) ([]byte, error)
	readFile  func(*ReadFileRecordsRequest) ([][]uint16, error)
	writeFile func(*WriteFileRecordsRequest) error
	fifo      func(*FIFOQueueRequest) ([]uint16, error)
}

func (h *fnFunctions) HandleDiagnostics(_ context.Context, req *DiagnosticsRequest) ([]byte, error) {
	return h.diag(req)
}

func (h *fnFunctions) HandleReportServerID(_ context.Context, req *ReportServerIDRequest) ([]byte, error) {
	return h.serverID(req)
}

func (h *fnFunctions) HandleReadFileRecords(_ context.Context, req *ReadFileRecordsRequest) ([][]uint16, error) {
	return h.readFile(req)
}

func (h *fnFunctions) HandleWriteFileRecords(_ context.Context, req *WriteFileRecordsRequest) error {
	return h.writeFile(req)
}

func (h *fnFunctions) HandleReadFIFOQueue(_ context.Context, req *FIFOQueueRequest) ([]uint16, error) {
	return h.fifo(req)
}

// fileReadReq builds an FC20 request payload from (file, record, length) triples.
func fileReadReq(subs ...[3]uint16) []byte {
	p := []byte{byte(7 * len(subs))}
	for _, s := range subs {
		p = append(p, 0x06)
		p = append(p, u16(s[0], s[1], s[2])...)
	}
	return p
}

// fileWriteReq builds an FC21 request payload with one sub-request.
func fileWriteReq(file, rec uint16, data ...uint16) []byte {
	body := append([]byte{0x06}, u16(file, rec, uint16(len(data)))...)
	body = append(body, u16(data...)...)
	return append([]byte{byte(len(body))}, body...)
}

func TestServerFunctions_Success(t *testing.T) {
	dev := e2eNewDevice()
	dev.setUnit(0x2A)
	cases := []struct {
		name    string
		fc      FunctionCode
		payload []byte
		want    []byte
	}{
		{"FC08 return query data", FCDiagnostics, []byte{0x00, 0x00, 0xA5, 0x37}, []byte{0x00, 0x00, 0xA5, 0x37}},
		{"FC08 sub-function only", FCDiagnostics, []byte{0x00, 0x00}, []byte{0x00, 0x00}},
		{"FC08 diagnostic register", FCDiagnostics, []byte{0x00, 0x02, 0x00, 0x00}, []byte{0x00, 0x02, 0xA5, 0x5A}},
		{"FC17", FCReportServerID, nil, []byte{5, 'G', 'O', 'M', 'B', 0xFF}},
		// The example of the specification, section 6.14.
		{
			"FC20 two sub-requests", FCReadFileRecord, fileReadReq([3]uint16{4, 1, 2}, [3]uint16{9, 0, 2}),
			[]byte{0x0C, 0x05, 0x06, 0x0D, 0xFE, 0x00, 0x20, 0x05, 0x06, 0x00, 0x01, 0x00, 0x02},
		},
		{"FC21 echo", FCWriteFileRecord, fileWriteReq(4, 7, 0x06AF, 0x04BE), fileWriteReq(4, 7, 0x06AF, 0x04BE)},
		// The example of the specification, section 6.18.
		{"FC24", FCReadFIFOQueue, u16(0x04DE), []byte{0x00, 0x06, 0x00, 0x02, 0x01, 0xB8, 0x12, 0x84}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, m := serveOne(t, dev, tc.fc, tc.payload...)
			want := &adu.Response{UnitID: 0x2A, FunctionCode: byte(tc.fc), Payload: tc.want, TransactionID: 0x0101}
			if len(tr.responses) != 1 || !reflect.DeepEqual(tr.responses[0], want) {
				t.Fatalf("responses = %+v, want one %+v", tr.responses, want)
			}
			checkEvents(t, m, []metricEvent{
				{kind: "request", unit: 0x2A, fc: tc.fc},
				{kind: "response", unit: 0x2A, fc: tc.fc},
			}, nil)
			for _, call := range dev.takeCalls() {
				if call.ClientAddr != "192.0.2.7:4242" || call.ClientRole != "operator" || call.FC != tc.fc {
					t.Errorf("handler saw %+v", call)
				}
			}
		})
	}
}

// Malformed requests drop the link; requests that are well formed but out of
// range get an exception; handlers that break their contract cost a Server
// Device Failure.
func TestServerFunctions_RequestValidation(t *testing.T) {
	const wantOK ExceptionCode = 0xFF
	dev := e2eNewDevice()
	dev.setUnit(0x2A)
	dev.setFIFO(0x0032, make([]uint16, 32)...)
	readOK := func(req *ReadFileRecordsRequest) ([][]uint16, error) {
		out := make([][]uint16, len(req.Requests))
		for i, r := range req.Requests {
			out[i] = make([]uint16, r.RecordLength)
		}
		return out, nil
	}
	cases := []struct {
		name    string
		handler RequestHandler
		fc      FunctionCode
		payload []byte
		// wantExc 0 means the link is dropped without a response, wantOK a
		// normal response.
		wantExc ExceptionCode
		wantErr error
	}{
		{"FC08 empty", dev, FCDiagnostics, nil, 0, ErrProtocolError},
		{"FC08 half a sub-function", dev, FCDiagnostics, []byte{0x00}, 0, ErrProtocolError},
		{"FC08 unknown sub-function", dev, FCDiagnostics, []byte{0x12, 0x34}, exIllegalFunction, ErrIllegalFunction},
		{
			"FC08 handler data too long",
			&fnFunctions{diag: func(*DiagnosticsRequest) ([]byte, error) { return make([]byte, 251), nil }},
			FCDiagnostics, []byte{0, 0}, exServerDeviceFailure, ErrServerDeviceFailure,
		},
		{"FC17 with a payload", dev, FCReportServerID, []byte{0x00}, 0, ErrProtocolError},
		{
			"FC17 handler data too long",
			&fnFunctions{serverID: func(*ReportServerIDRequest) ([]byte, error) { return make([]byte, 252), nil }},
			FCReportServerID, nil, exServerDeviceFailure, ErrServerDeviceFailure,
		},

		{"FC20 empty", dev, FCReadFileRecord, nil, 0, ErrProtocolError},
		{"FC20 byte count too large", dev, FCReadFileRecord, append(fileReadReq([3]uint16{4, 1, 1}), 0), 0, ErrProtocolError},
		{"FC20 byte count too small", dev, FCReadFileRecord, fileReadReq([3]uint16{4, 1, 1})[:7], 0, ErrProtocolError},
		{"FC20 byte count zero", dev, FCReadFileRecord, []byte{0}, exIllegalDataValue, ErrIllegalDataValue},
		{"FC20 byte count not a multiple of 7", dev, FCReadFileRecord, []byte{8, 6, 0, 4, 0, 1, 0, 1, 0}, exIllegalDataValue, ErrIllegalDataValue},
		{"FC20 byte count above 0xF5", dev, FCReadFileRecord, fileReadReq(make([][3]uint16, 36)...), exIllegalDataValue, ErrIllegalDataValue},
		{"FC20 reference type", dev, FCReadFileRecord, []byte{7, 7, 0, 4, 0, 1, 0, 1}, exIllegalDataAddress, ErrIllegalDataAddress},
		{"FC20 file 0", dev, FCReadFileRecord, fileReadReq([3]uint16{0, 1, 1}), exIllegalDataAddress, ErrIllegalDataAddress},
		{"FC20 record 10000", dev, FCReadFileRecord, fileReadReq([3]uint16{4, 0x2710, 1}), exIllegalDataAddress, ErrIllegalDataAddress},
		{"FC20 record length 0", dev, FCReadFileRecord, fileReadReq([3]uint16{4, 1, 0}), exIllegalDataAddress, ErrIllegalDataAddress},
		{"FC20 response too large", dev, FCReadFileRecord, fileReadReq([3]uint16{4, 0, 122}), exIllegalDataValue, ErrIllegalDataValue},
		{"FC20 missing file", dev, FCReadFileRecord, fileReadReq([3]uint16{5, 0, 1}), exIllegalDataAddress, ErrIllegalDataAddress},
		{
			"FC20 handler returns too few records", &fnFunctions{readFile: func(*ReadFileRecordsRequest) ([][]uint16, error) { return [][]uint16{{1}}, nil }},
			FCReadFileRecord, fileReadReq([3]uint16{4, 1, 1}, [3]uint16{4, 2, 1}), exServerDeviceFailure, ErrServerDeviceFailure,
		},
		{
			"FC20 handler returns a short record", &fnFunctions{readFile: func(*ReadFileRecordsRequest) ([][]uint16, error) { return [][]uint16{{1}}, nil }},
			FCReadFileRecord, fileReadReq([3]uint16{4, 1, 2}), exServerDeviceFailure, ErrServerDeviceFailure,
		},
		{"FC20 largest response", &fnFunctions{readFile: readOK}, FCReadFileRecord, fileReadReq([3]uint16{4, 0, 121}), wantOK, nil},

		{"FC21 empty", dev, FCWriteFileRecord, nil, 0, ErrProtocolError},
		{"FC21 byte count disagrees", dev, FCWriteFileRecord, append(fileWriteReq(4, 7, 1), 0), 0, ErrProtocolError},
		{"FC21 byte count below 9", dev, FCWriteFileRecord, []byte{7, 6, 0, 4, 0, 7, 0, 0}, exIllegalDataValue, ErrIllegalDataValue},
		{"FC21 byte count above 0xFB", dev, FCWriteFileRecord, fileWriteReq(4, 0, make([]uint16, 123)...), exIllegalDataValue, ErrIllegalDataValue},
		{"FC21 data shorter than its length", dev, FCWriteFileRecord, []byte{9, 6, 0, 4, 0, 7, 0, 2, 0, 1}, 0, ErrProtocolError},
		{"FC21 truncated sub-request", dev, FCWriteFileRecord, []byte{12, 6, 0, 4, 0, 7, 0, 1, 0, 1, 6, 0, 4}, 0, ErrProtocolError},
		{"FC21 reference type", dev, FCWriteFileRecord, []byte{9, 5, 0, 4, 0, 7, 0, 1, 0, 1}, exIllegalDataAddress, ErrIllegalDataAddress},
		{"FC21 file 0", dev, FCWriteFileRecord, fileWriteReq(0, 7, 1), exIllegalDataAddress, ErrIllegalDataAddress},
		{"FC21 record length 0", dev, FCWriteFileRecord, []byte{9, 6, 0, 4, 0, 7, 0, 0, 0, 1}, exIllegalDataAddress, ErrIllegalDataAddress},
		{"FC21 missing file", dev, FCWriteFileRecord, fileWriteReq(5, 0, 1), exIllegalDataAddress, ErrIllegalDataAddress},

		{"FC24 empty", dev, FCReadFIFOQueue, nil, 0, ErrProtocolError},
		{"FC24 one byte", dev, FCReadFIFOQueue, []byte{0x04}, 0, ErrProtocolError},
		{"FC24 three bytes", dev, FCReadFIFOQueue, []byte{0x04, 0xDE, 0x00}, 0, ErrProtocolError},
		{"FC24 more than 31 values", dev, FCReadFIFOQueue, u16(0x0032), exIllegalDataValue, ErrIllegalDataValue},
		{"FC24 unknown queue", dev, FCReadFIFOQueue, u16(0x0999), exIllegalDataAddress, ErrIllegalDataAddress},

		{"vendor function code", dev, 0x41, []byte{1, 2, 3}, exIllegalFunction, ErrIllegalFunction},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr, m := serveOne(t, tc.handler, tc.fc, tc.payload...)
			dev.takeCalls()
			outcome := "error"
			switch tc.wantExc {
			case wantOK:
				outcome = "response"
				if tr.closed || len(tr.responses) != 1 || tr.responses[0].FunctionCode != byte(tc.fc) {
					t.Fatalf("responses = %+v, closed %v; want a normal response", tr.responses, tr.closed)
				}
				if n := len(tr.responses[0].Payload); n > 252 {
					t.Fatalf("response payload of %d bytes", n)
				}
			case 0:
				if len(tr.responses) != 0 {
					t.Fatalf("server answered a malformed request: %+v", tr.responses[0])
				}
				if !tr.closed {
					t.Fatal("link was not closed after a protocol error")
				}
			default:
				if tr.closed {
					t.Fatal("link was closed for a request that only warrants an exception")
				}
				want := &adu.Response{UnitID: 0x2A, FunctionCode: byte(tc.fc) | 0x80, Payload: []byte{byte(tc.wantExc)}, TransactionID: 0x0101}
				if len(tr.responses) != 1 || !reflect.DeepEqual(tr.responses[0], want) {
					t.Fatalf("responses = %+v, want one %+v", tr.responses, want)
				}
			}
			checkEvents(t, m, []metricEvent{
				{kind: "request", unit: 0x2A, fc: tc.fc},
				{kind: outcome, unit: 0x2A, fc: tc.fc},
			}, tc.wantErr)
		})
	}
}

// Without its optional handler, each function code is answered with Illegal
// Function and reported to the metrics as an error; so is every vendor,
// user-defined or unknown function code, and FC43 with an MEI type other than
// 14. The link stays up.
func TestServerFunctions_UnsupportedFunctionCodes(t *testing.T) {
	requests := []struct {
		fc      FunctionCode
		payload []byte
	}{
		{FCReadExceptionStatus, nil},
		{FCDiagnostics, []byte{0x00, 0x00, 0x12, 0x34}},
		{FCGetCommEventCounters, nil},
		{FCGetCommEventLog, nil},
		{FCReportServerID, nil},
		{FCReadFileRecord, fileReadReq([3]uint16{4, 1, 1})},
		{FCWriteFileRecord, fileWriteReq(4, 7, 1)},
		{FCMaskWriteRegister, u16(1, 2, 3)},
		{FCReadWriteMultipleRegs, append(append(u16(0, 1, 0, 1), 0x02), u16(7)...)},
		{FCReadFIFOQueue, u16(0x04DE)},
		{FCEncapsulatedInterface, []byte{byte(MEIReadDeviceIdentification), byte(DeviceIDBasic), 0x00}},
		{FCEncapsulatedInterface, []byte{0x0D, 0x01}},
		{0x41, []byte{1, 2, 3}},
		{0x64, nil},
		{0x7F, []byte{0}},
	}
	for _, r := range requests {
		tr, m := serveOne(t, &noSerialFCHandler{}, r.fc, r.payload...)
		want := &adu.Response{UnitID: 0x2A, FunctionCode: byte(r.fc) | 0x80, Payload: []byte{byte(exIllegalFunction)}, TransactionID: 0x0101}
		if len(tr.responses) != 1 || !reflect.DeepEqual(tr.responses[0], want) {
			t.Errorf("fc 0x%02X: responses = %+v, want %+v", uint8(r.fc), tr.responses, want)
		}
		if tr.closed {
			t.Errorf("fc 0x%02X: link closed", uint8(r.fc))
		}
		checkEvents(t, m, []metricEvent{
			{kind: "request", unit: 0x2A, fc: r.fc},
			{kind: "error", unit: 0x2A, fc: r.fc},
		}, ErrIllegalFunction)
	}
	// A device with every handler still has no vendor functions.
	dev := e2eNewDevice()
	dev.setUnit(0x2A)
	for _, fc := range []FunctionCode{0x41, 0x64, 0x7F} {
		tr, _ := serveOne(t, dev, fc, 0x00)
		if len(tr.responses) != 1 || tr.responses[0].FunctionCode != byte(fc)|0x80 {
			t.Errorf("fc 0x%02X with every handler: %+v, want Illegal Function", uint8(fc), tr.responses)
		}
	}
	if n := dev.callCount(); n != 0 {
		t.Errorf("vendor function codes reached the device %d times", n)
	}
}

// FuzzServerFunctions feeds arbitrary requests for the function codes in this
// file to a server whose device implements every handler. The server must not
// panic, and must either drop the link without answering or send exactly one
// well-formed response that fits in a PDU.
//
// Run extended exploration with
// `go test -run=^$ -fuzz=FuzzServerFunctions -fuzztime=30s`.
func FuzzServerFunctions(f *testing.F) {
	for _, s := range [][]byte{
		append([]byte{0x08}, 0x00, 0x00, 0x12, 0x34),
		append([]byte{0x08}, 0x00, 0x0B),
		{0x11},
		append([]byte{0x14}, fileReadReq([3]uint16{4, 1, 2}, [3]uint16{9, 0, 4})...),
		append([]byte{0x15}, fileWriteReq(4, 7, 1, 2)...),
		append([]byte{0x18}, 0x04, 0xDE),
		{0x41, 0x01},
	} {
		f.Add(s)
	}
	dev := e2eNewDevice()
	dev.setUnit(0x2A)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 || len(data) > adu.MBAPLengthMax-1 {
			return
		}
		fc := FunctionCode(data[0])
		tr, _ := serveOne(t, dev, fc, data[1:]...)
		dev.takeCalls()
		switch {
		case len(tr.responses) == 0:
			if !tr.closed {
				t.Fatalf("fc 0x%02X: no response and the link is still open", uint8(fc))
			}
		case len(tr.responses) > 1:
			t.Fatalf("fc 0x%02X: %d responses", uint8(fc), len(tr.responses))
		default:
			res := tr.responses[0]
			if len(res.Payload) > 252 {
				t.Fatalf("fc 0x%02X: %d-byte response payload", uint8(fc), len(res.Payload))
			}
			if res.FunctionCode&0x80 != 0 {
				if res.FunctionCode != byte(fc)|0x80 || len(res.Payload) != 1 || !validExceptionCode(res.Payload[0]) {
					t.Fatalf("fc 0x%02X: bad exception response %+v", uint8(fc), res)
				}
			} else if res.FunctionCode != byte(fc) {
				t.Fatalf("fc 0x%02X: response fc 0x%02X", uint8(fc), res.FunctionCode)
			}
		}
	})
}
