// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"math"
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
	// Without a DeviceIdentificationHandler, and for MEI types other than Read
	// Device Identification (0x0E), such as CANopen General Reference (0x0D), the
	// answer is Illegal Function.
	h, ok := ms.handler.(DeviceIdentificationHandler)
	if !ok || len(req.Payload) >= 1 && MEIType(req.Payload[0]) != MEIReadDeviceIdentification {
		return nil, ErrIllegalFunction
	}
	if err := checkPDULength(req.Payload, 3); err != nil {
		return nil, err
	}

	category := DeviceIDCategory(req.Payload[1])
	objectID := DeviceIDObjectID(req.Payload[2])

	if category < DeviceIDBasic || category > DeviceIDIndividual {
		return nil, ErrIllegalDataValue
	}

	resp, err := h.HandleDeviceIdentification(ctx, &DeviceIdentificationRequest{
		ClientAddr:   clientAddr,
		ClientRole:   clientRole,
		UnitID:       req.UnitID,
		FunctionCode: FunctionCode(req.FunctionCode),
		MEIType:      MEIReadDeviceIdentification,
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

	conformity, err := ms.effectiveConformityLevel(resp)
	if err != nil {
		return nil, err
	}
	if err := ms.checkDeviceIDObjects(resp.Objects); err != nil {
		return nil, err
	}
	// The objects the device has at its conformity level. "If the server device
	// is asked for a description level (readDevice Code) higher that its
	// conformity level, It must respond in accordance with its actual
	// conformity level" (V1.1b3 6.21): a request above the level is served at
	// the level, but the response keeps the request's ReadDevId code ("Same as
	// request ReadDevId code").
	levelCategory := DeviceIDCategory(conformity & 0x7F)

	if category == DeviceIDIndividual {
		if conformity&0x80 == 0 {
			// The level advertises stream access only (0x01-0x03): individual
			// access is a ReadDevId code this device does not have, exception 03
			// ("An exception code 03 is sent back in the response if the Read
			// device ID code is illegal").
			return nil, ErrIllegalDataValue
		}
		return ms.buildIndividualDeviceIDResponse(req, txnID, category, conformity, objectID,
			filterDeviceIDObjectsByCategory(resp.Objects, levelCategory))
	}

	return ms.buildStreamDeviceIDResponse(req, txnID, category, conformity, objectID,
		filterDeviceIDObjectsByCategory(resp.Objects, min(category, levelCategory)))
}

// effectiveConformityLevel returns the conformity level to send for resp: the
// handler's, or one derived from its objects when the handler leaves it 0.
// Only the six levels of V1.1b3 6.21 (0x01-0x03, 0x81-0x83) are ever sent;
// any other value from the handler is a handler error answered with Server
// Device Failure.
func (ms *Server) effectiveConformityLevel(resp *DeviceIdentificationResponse) (uint8, error) {
	level := resp.ConformityLevel
	if level == 0 {
		return deriveConformityLevel(resp.Objects), nil
	}
	if !validConformityLevel(level) {
		ms.logger.Errorf("device identification handler returned conformity level 0x%02x: "+
			"only 0x01-0x03 and 0x81-0x83 (or 0 to derive one) are valid", level)
		return 0, ErrServerDeviceFailure
	}
	return level, nil
}

// validConformityLevel reports whether level is one of the conformity levels
// V1.1b3 6.21 defines.
func validConformityLevel(level uint8) bool {
	switch level {
	case 0x01, 0x02, 0x03, 0x81, 0x82, 0x83:
		return true
	default:
		return false
	}
}

// checkDeviceIDObjects checks the handler's object set before any response
// is built from it: every object ID at most once (a duplicate would make two
// objects answer to one ID, and a page could name as Next Object ID the ID it
// started from, which a client following it would ask for forever), and every
// object small enough for a response of its own ("An object is indivisible,
// therefore any object must have a size consistent with the size of
// transaction response", V1.1b3 6.21). Either failure is a handler error,
// answered with Server Device Failure.
func (ms *Server) checkDeviceIDObjects(objects []DeviceIdentificationObject) error {
	var seen [256]bool
	for i := range objects {
		id := objects[i].ID
		if seen[id] {
			ms.logger.Errorf("device identification handler returned object 0x%02x more than once", uint8(id))
			return ErrServerDeviceFailure
		}
		seen[id] = true
		if n := 2 + len(objects[i].Value); n > maxDeviceIDObjectsBytes {
			ms.logger.Errorf("device identification object 0x%02x is %d bytes long, at most %d fit a response",
				uint8(id), n, maxDeviceIDObjectsBytes)
			return ErrServerDeviceFailure
		}
	}
	return nil
}

// buildIndividualDeviceIDResponse serves ReadDevId code 0x04 (individual access):
// exactly one object identified by objectID, among the objects visible at the
// device's conformity level.
func (ms *Server) buildIndividualDeviceIDResponse(req *adu.Request, txnID uint16, category DeviceIDCategory, conformity uint8, objectID DeviceIDObjectID, objects []DeviceIdentificationObject) (*adu.Response, error) {
	for i := range objects {
		if objects[i].ID == objectID {
			payload, err := appendDeviceIDObject(deviceIDResponseHeader(category, conformity, 0x00, 0x00, 1), objects[i])
			if err != nil {
				return nil, err
			}
			return newSuccessResponse(req, txnID, payload), nil
		}
	}
	// "if the Object Id doesn't match to any known object, the server returns
	// an exception response with exception code = 02" (V1.1b3 6.21).
	return nil, ErrIllegalDataAddress
}

// buildStreamDeviceIDResponse serves ReadDevId codes 0x01-0x03 (stream access)
// from objects, the objects visible at the effective category: ordered by
// object ID and paginated with MoreFollows/NextObjectID. objects has no
// duplicate IDs and no object larger than a page (checkDeviceIDObjects), so
// every page holds at least one object and the Next Object ID of a page is
// above every ID on it: following it always terminates.
func (ms *Server) buildStreamDeviceIDResponse(req *adu.Request, txnID uint16, category DeviceIDCategory, conformity uint8, startObject DeviceIDObjectID, objects []DeviceIdentificationObject) (*adu.Response, error) {
	sort.Slice(objects, func(i, j int) bool {
		return objects[i].ID < objects[j].ID
	})

	// "If the Object Id does not match any known object, the server responds
	// as if object 0 were pointed out (restart at the beginning)" (V1.1b3
	// 6.21). An object beyond the effective category is not known here.
	startIdx := 0
	for i := range objects {
		if objects[i].ID == startObject {
			startIdx = i
			break
		}
	}

	used := 0
	endIdx := len(objects)
	for i := startIdx; i < len(objects); i++ {
		objSize := 2 + len(objects[i].Value)
		if used+objSize > maxDeviceIDObjectsBytes {
			endIdx = i
			break
		}
		used += objSize
	}
	if endIdx == startIdx && startIdx < len(objects) {
		// An object larger than a page: the page would be empty and name its
		// own start as the next object. checkDeviceIDObjects refuses such an
		// object before this is reached.
		return nil, ErrServerDeviceFailure
	}

	moreFollows := byte(0x00)
	nextObjID := byte(0x00)
	if endIdx < len(objects) {
		moreFollows = 0xff
		nextObjID = byte(objects[endIdx].ID)
	}

	payload := deviceIDResponseHeader(category, conformity, moreFollows, nextObjID, adu.Uint8(endIdx-startIdx))
	for i := startIdx; i < endIdx; i++ {
		// The page budget above bounds every object of the page to
		// maxDeviceIDObjectsBytes, so its length fits its byte.
		payload = putDeviceIDObject(payload, objects[i])
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

// appendDeviceIDObject appends one object (ID, length, value) to payload. A
// value longer than its one-byte length field can count cannot be sent:
// Server Device Failure. (checkDeviceIDObjects already refuses any object
// that does not fit a response; this keeps the builders safe on their own.)
func appendDeviceIDObject(payload []byte, obj DeviceIdentificationObject) ([]byte, error) {
	if len(obj.Value) > math.MaxUint8 {
		return nil, ErrServerDeviceFailure
	}
	return putDeviceIDObject(payload, obj), nil
}

// putDeviceIDObject appends one object (ID, length, value) to payload. The
// caller has bounded the value's length to 255.
func putDeviceIDObject(payload []byte, obj DeviceIdentificationObject) []byte {
	payload = append(payload, byte(obj.ID), adu.Uint8(len(obj.Value)))
	return append(payload, obj.Value...)
}

// filterDeviceIDObjectsByCategory returns, in a new slice, the objects visible
// at a stream category: basic = 0x00-0x02, regular = 0x00-0x7F, extended =
// 0x00-0xFF (V1.1b3 6.21).
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
