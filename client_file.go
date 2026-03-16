package modbus

import (
	"context"
	"fmt"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
)

// FileRecordRequest describes one sub-request for ReadFileRecords (FC20).
// Each sub-request reads a contiguous slice of registers from a single file.
type FileRecordRequest struct {
	FileNumber   uint16 // file number (1–0xFFFF)
	RecordNumber uint16 // starting record number within the file (0–0x270F)
	RecordLength uint16 // number of 16-bit registers to read (≥ 1)
}

// FileRecord describes one sub-request for WriteFileRecords (FC21).
// Each record writes a contiguous slice of registers to a single file.
// The record length is implied by len(Data).
type FileRecord struct {
	FileNumber   uint16   // file number (1–0xFFFF)
	RecordNumber uint16   // starting record number within the file (0–0x270F)
	Data         []uint16 // register values to write (len gives record length)
}

// Reads one or more groups of file records (function code 20).
// Each FileRecordRequest selects a contiguous run of registers from one file.
// The returned slice has one []uint16 entry per request, in the same order.
// Register data is returned as big-endian uint16 values as received from the device.
//
// Spec limits:
//
//	FileNumber   must be 1–0xFFFF
//	RecordNumber must be 0–0x270F (decimal 0–9999)
//	Total request byte count must not exceed 0xF5 (at most 35 sub-requests)
func (mc *Client) ReadFileRecords(ctx context.Context, unitID uint8, requests []FileRecordRequest) (records [][]uint16, err error) {
	if len(requests) == 0 {
		err = newParameterError("ReadFileRecords", "requests", "must not be empty")
		return
	}

	byteCount := len(requests) * 7
	if byteCount > maxFileByteCount {
		err = newParameterError("ReadFileRecords", "requests",
			fmt.Sprintf("total byte count %d exceeds maximum %d", byteCount, maxFileByteCount))
		return
	}

	for i, r := range requests {
		if r.FileNumber == 0 {
			err = newParameterError("ReadFileRecords", "FileNumber",
				fmt.Sprintf("sub-request %d: must be 1–0xFFFF, got 0", i))
			return
		}
		if r.RecordNumber > 0x270F {
			err = newParameterError("ReadFileRecords", "RecordNumber",
				fmt.Sprintf("sub-request %d: %d exceeds maximum 0x270F (9999)", i, r.RecordNumber))
			return
		}
		if r.RecordLength == 0 {
			err = newParameterError("ReadFileRecords", "RecordLength",
				fmt.Sprintf("sub-request %d: must be >= 1", i))
			return
		}
	}

	req := &adu.Request{
		UnitID:       unitID,
		FunctionCode: byte(FCReadFileRecord),
		Payload:      []byte{byte(byteCount)},
	}
	for _, r := range requests {
		req.Payload = append(req.Payload, 0x06)
		req.Payload = append(req.Payload, uint16ToBytes(BigEndian, r.FileNumber)...)
		req.Payload = append(req.Payload, uint16ToBytes(BigEndian, r.RecordNumber)...)
		req.Payload = append(req.Payload, uint16ToBytes(BigEndian, r.RecordLength)...)
	}

	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, FCReadFileRecord)
	}
	defer func() { reportOutcome(m, unitID, FCReadFileRecord, start, err) }()

	res, err := mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	if err = checkResponseFC(res, req.FunctionCode); err != nil {
		return
	}

	if len(res.Payload) < 1 {
		err = newProtocolError("ReadFileRecords", "empty payload, expected data length prefix")
		return
	}
	respDataLen := int(res.Payload[0])
	if len(res.Payload) != 1+respDataLen {
		err = newProtocolError("ReadFileRecords",
			fmt.Sprintf("data length %d does not match payload length %d", respDataLen, len(res.Payload)-1))
		return
	}

	offset := 1
	for i, r := range requests {
		if offset >= len(res.Payload) {
			err = newProtocolError("ReadFileRecords",
				fmt.Sprintf("sub-response %d: truncated payload (no length byte)", i))
			return
		}
		fileRespLen := int(res.Payload[offset])
		if fileRespLen < 1 {
			err = newProtocolError("ReadFileRecords",
				fmt.Sprintf("sub-response %d: file response length is 0 (must be >= 1)", i))
			return
		}
		offset++

		if offset >= len(res.Payload) {
			err = newProtocolError("ReadFileRecords",
				fmt.Sprintf("sub-response %d: truncated payload (no reference type)", i))
			return
		}
		if res.Payload[offset] != 0x06 {
			err = newProtocolError("ReadFileRecords",
				fmt.Sprintf("sub-response %d: unexpected reference type 0x%02X (expected 0x06)", i, res.Payload[offset]))
			return
		}
		offset++

		dataLen := fileRespLen - 1
		if dataLen%2 != 0 {
			err = newProtocolError("ReadFileRecords",
				fmt.Sprintf("sub-response %d: odd data length %d (expected even for register data)", i, dataLen))
			return
		}
		expectedDataLen := int(r.RecordLength) * 2
		if dataLen != expectedDataLen {
			err = newProtocolError("ReadFileRecords",
				fmt.Sprintf("sub-response %d: expected %d data bytes, got %d", i, expectedDataLen, dataLen))
			return
		}
		if offset+dataLen > len(res.Payload) {
			err = newProtocolError("ReadFileRecords",
				fmt.Sprintf("sub-response %d: data truncated at offset %d", i, offset))
			return
		}
		records = append(records, bytesToUint16s(BigEndian, res.Payload[offset:offset+dataLen]))
		offset += dataLen
	}

	if offset != len(res.Payload) {
		err = newProtocolError("ReadFileRecords",
			fmt.Sprintf("trailing data: consumed %d bytes, payload has %d", offset, len(res.Payload)))
		return
	}

	return
}

// Writes one or more groups of file records (function code 21).
// Each FileRecord specifies the target file, starting record number, and
// the register values to write. The normal response is an echo of the request.
// Register data is encoded as big-endian uint16 values on the wire.
//
// Spec limits:
//
//	FileNumber   must be 1–0xFFFF
//	RecordNumber must be 0–0x270F (decimal 0–9999)
//	Total request data length must be in the range 0x09–0xFB
func (mc *Client) WriteFileRecords(ctx context.Context, unitID uint8, records []FileRecord) (err error) {
	if len(records) == 0 {
		err = newParameterError("WriteFileRecords", "records", "must not be empty")
		return
	}

	reqDataLen := 0
	for _, r := range records {
		reqDataLen += 7 + 2*len(r.Data)
	}
	if reqDataLen > maxFileReqDataLen {
		err = newParameterError("WriteFileRecords", "records",
			fmt.Sprintf("total data length %d exceeds maximum %d", reqDataLen, maxFileReqDataLen))
		return
	}

	for i, r := range records {
		if r.FileNumber == 0 {
			err = newParameterError("WriteFileRecords", "FileNumber",
				fmt.Sprintf("record %d: must be 1–0xFFFF, got 0", i))
			return
		}
		if r.RecordNumber > 0x270F {
			err = newParameterError("WriteFileRecords", "RecordNumber",
				fmt.Sprintf("record %d: %d exceeds maximum 0x270F (9999)", i, r.RecordNumber))
			return
		}
		if len(r.Data) == 0 {
			err = newParameterError("WriteFileRecords", "Data",
				fmt.Sprintf("record %d: must not be empty", i))
			return
		}
	}

	req := &adu.Request{
		UnitID:       unitID,
		FunctionCode: byte(FCWriteFileRecord),
		Payload:      []byte{byte(reqDataLen)},
	}
	for _, r := range records {
		req.Payload = append(req.Payload, 0x06)
		req.Payload = append(req.Payload, uint16ToBytes(BigEndian, r.FileNumber)...)
		req.Payload = append(req.Payload, uint16ToBytes(BigEndian, r.RecordNumber)...)
		req.Payload = append(req.Payload, uint16ToBytes(BigEndian, uint16(len(r.Data)))...)
		for _, v := range r.Data {
			req.Payload = append(req.Payload, uint16ToBytes(BigEndian, v)...)
		}
	}

	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, FCWriteFileRecord)
	}
	defer func() { reportOutcome(m, unitID, FCWriteFileRecord, start, err) }()

	res, err := mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	if err = checkResponseFC(res, req.FunctionCode); err != nil {
		return
	}

	if len(res.Payload) != len(req.Payload) {
		err = newProtocolError("WriteFileRecords",
			fmt.Sprintf("response payload length %d does not match request %d", len(res.Payload), len(req.Payload)))
		return
	}
	for i := range req.Payload {
		if res.Payload[i] != req.Payload[i] {
			err = newProtocolError("WriteFileRecords",
				fmt.Sprintf("response echo mismatch at byte %d: expected 0x%02X, got 0x%02X", i, req.Payload[i], res.Payload[i]))
			return
		}
	}

	return
}
