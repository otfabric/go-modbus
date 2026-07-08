// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"sort"

	"github.com/otfabric/go-modbus/internal/adu"
)

// DeviceIdentificationRequest is passed to
// DeviceIdentificationHandler.HandleDeviceIdentification for FC43 / MEI type
// 0x0E (Read Device Identification) requests.
type DeviceIdentificationRequest struct {
	ClientAddr   string
	ClientRole   string
	UnitID       uint8
	FunctionCode FunctionCode
	// MEIType is the Modbus Encapsulated Interface type. Only
	// MEIReadDeviceIdentification (0x0E) reaches the handler; other MEI types
	// are rejected by the server before dispatch.
	MEIType MEIType
	// Category is the Read Device ID code (0x01 basic, 0x02 regular,
	// 0x03 extended, 0x04 individual).
	Category DeviceIDCategory
	// ObjectID is the first object to return for stream access (0x01-0x03) or
	// the requested object for individual access (0x04).
	ObjectID DeviceIDObjectID
}

// DeviceIdentificationResponse is returned by
// DeviceIdentificationHandler.HandleDeviceIdentification. The handler returns
// the full set of objects the device implements together with its conformity
// level; the server performs category filtering, ordering, framing, and
// MoreFollows/NextObjectID pagination automatically.
type DeviceIdentificationResponse struct {
	// ConformityLevel is the device conformity level (0x01-0x03 for stream-only
	// access, or 0x81-0x83 to also advertise individual access). When 0, the
	// server derives a sensible level from the objects provided.
	ConformityLevel uint8
	// Objects is the complete set of identification objects the device
	// implements, in any order.
	Objects []DeviceIdentificationObject
}

// DeviceIdentificationHandler is an optional interface for serving FC43 / MEI
// type 0x0E (Read Device Identification). If the RequestHandler also implements
// DeviceIdentificationHandler, the server dispatches FC43 requests to
// HandleDeviceIdentification instead of returning Illegal Function.
//
// The handler returns the full object set and conformity level; the server owns
// all MEI framing, category filtering, and pagination.
type DeviceIdentificationHandler interface {
	HandleDeviceIdentification(ctx context.Context, req *DeviceIdentificationRequest) (*DeviceIdentificationResponse, error)
}

// maxDeviceIDObjectsBytes is the number of payload bytes available for the
// object list in a single FC43 response. The response PDU is:
//
//	FC(1) + MEIType(1) + ReadDevIDCode(1) + ConformityLevel(1) +
//	MoreFollows(1) + NextObjectID(1) + NumberOfObjects(1) + objects...
//
// leaving maxPDUSize - 7 bytes for the object list.
const maxDeviceIDObjectsBytes = 253 - 7

// handleReadDeviceIdentification handles FC43 / MEI type 0x0E (Read Device
// Identification). Requires the RequestHandler to also implement
// DeviceIdentificationHandler.
func (ms *Server) handleReadDeviceIdentification(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	if len(req.Payload) < 3 {
		return nil, ErrProtocolError
	}

	meiType := MEIType(req.Payload[0])
	category := DeviceIDCategory(req.Payload[1])
	objectID := DeviceIDObjectID(req.Payload[2])

	// Only Read Device Identification (0x0E) is supported. Other MEI types
	// (e.g. CANopen General Reference 0x0D) are not implemented.
	if meiType != MEIReadDeviceIdentification {
		return nil, ErrIllegalFunction
	}

	if category < DeviceIDBasic || category > DeviceIDIndividual {
		return nil, ErrIllegalDataValue
	}

	h, ok := ms.handler.(DeviceIdentificationHandler)
	if !ok {
		return &adu.Response{
			UnitID:        req.UnitID,
			FunctionCode:  req.FunctionCode | 0x80,
			Payload:       []byte{byte(exIllegalFunction)},
			TransactionID: txnID,
		}, nil
	}

	resp, err := h.HandleDeviceIdentification(ctx, &DeviceIdentificationRequest{
		ClientAddr:   clientAddr,
		ClientRole:   clientRole,
		UnitID:       req.UnitID,
		FunctionCode: FunctionCode(req.FunctionCode),
		MEIType:      meiType,
		Category:     category,
		ObjectID:     objectID,
	})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		ms.logger.Errorf("device identification handler returned nil response")
		return nil, ErrServerDeviceFailure
	}

	conformity := resp.ConformityLevel
	if conformity == 0 {
		conformity = deriveConformityLevel(resp.Objects)
	}

	if category == DeviceIDIndividual {
		return ms.buildIndividualDeviceIDResponse(req, txnID, category, conformity, objectID, resp.Objects)
	}

	return ms.buildStreamDeviceIDResponse(req, txnID, category, conformity, objectID, resp.Objects)
}

// buildIndividualDeviceIDResponse serves ReadDevId code 0x04 (individual access):
// exactly one object identified by objectID.
func (ms *Server) buildIndividualDeviceIDResponse(req *adu.Request, txnID uint16, category DeviceIDCategory, conformity uint8, objectID DeviceIDObjectID, objects []DeviceIdentificationObject) (*adu.Response, error) {
	for i := range objects {
		if objects[i].ID == objectID {
			payload := deviceIDResponseHeader(category, conformity, 0x00, 0x00, 1)
			payload = appendDeviceIDObject(payload, objects[i])
			return newSuccessResponse(req, txnID, payload), nil
		}
	}
	// Individual access to an unknown object is an illegal data address.
	return nil, ErrIllegalDataAddress
}

// buildStreamDeviceIDResponse serves ReadDevId codes 0x01-0x03 (stream access),
// filtering by category, ordering by object ID, and paginating with
// MoreFollows/NextObjectID.
func (ms *Server) buildStreamDeviceIDResponse(req *adu.Request, txnID uint16, category DeviceIDCategory, conformity uint8, startObject DeviceIDObjectID, objects []DeviceIdentificationObject) (*adu.Response, error) {
	filtered := filterDeviceIDObjectsByCategory(objects, category)

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].ID < filtered[j].ID
	})

	// Determine the start index. Per spec, if the requested Object Id does not
	// match any known object, restart at the beginning (object 0).
	startIdx := 0
	if startObject != 0 {
		matched := false
		for i := range filtered {
			if filtered[i].ID == startObject {
				startIdx = i
				matched = true
				break
			}
		}
		if !matched {
			startIdx = 0
		}
	}

	var packed []DeviceIdentificationObject
	used := 0
	nextIdx := len(filtered)
	for i := startIdx; i < len(filtered); i++ {
		objSize := 2 + len(filtered[i].Value)
		// Always include at least one object, even if oversized, so a single
		// large object does not stall pagination. Objects are indivisible.
		if len(packed) > 0 && used+objSize > maxDeviceIDObjectsBytes {
			nextIdx = i
			break
		}
		packed = append(packed, filtered[i])
		used += objSize
	}

	moreFollows := byte(0x00)
	nextObjID := byte(0x00)
	if nextIdx < len(filtered) {
		moreFollows = 0xff
		nextObjID = byte(filtered[nextIdx].ID)
	}

	payload := deviceIDResponseHeader(category, conformity, moreFollows, nextObjID, uint8(len(packed)))
	for i := range packed {
		payload = appendDeviceIDObject(payload, packed[i])
	}

	return newSuccessResponse(req, txnID, payload), nil
}

// deviceIDResponseHeader builds the fixed 6-byte MEI response header that
// follows the function code.
func deviceIDResponseHeader(category DeviceIDCategory, conformity, moreFollows, nextObjID, numObjects uint8) []byte {
	return []byte{
		byte(MEIReadDeviceIdentification),
		byte(category),
		conformity,
		moreFollows,
		nextObjID,
		numObjects,
	}
}

// appendDeviceIDObject appends one object (ID, length, value) to payload.
func appendDeviceIDObject(payload []byte, obj DeviceIdentificationObject) []byte {
	payload = append(payload, byte(obj.ID), byte(len(obj.Value)))
	payload = append(payload, []byte(obj.Value)...)
	return payload
}

// filterDeviceIDObjectsByCategory returns the objects visible at the requested
// stream category: basic = 0x00-0x02, regular = 0x00-0x7F, extended = 0x00-0xFF.
func filterDeviceIDObjectsByCategory(objects []DeviceIdentificationObject, category DeviceIDCategory) []DeviceIdentificationObject {
	var maxID DeviceIDObjectID
	switch category {
	case DeviceIDBasic:
		maxID = 0x02
	case DeviceIDRegular:
		maxID = 0x7f
	default: // DeviceIDExtended
		maxID = 0xff
	}

	out := make([]DeviceIdentificationObject, 0, len(objects))
	for i := range objects {
		if objects[i].ID <= maxID {
			out = append(out, objects[i])
		}
	}
	return out
}

// deriveConformityLevel infers a conformity level from the object set when the
// handler leaves ConformityLevel unset. The 0x80 bit is set to advertise
// individual access, which the server always supports.
func deriveConformityLevel(objects []DeviceIdentificationObject) uint8 {
	level := uint8(0x01) // basic
	for i := range objects {
		switch {
		case objects[i].ID >= 0x80:
			return 0x83 // extended (individual access advertised)
		case objects[i].ID >= 0x03 && level < 0x02:
			level = 0x02 // regular
		}
	}
	return level | 0x80
}
