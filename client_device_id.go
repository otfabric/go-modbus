package modbus

import (
	"context"
	"fmt"
	"time"

	"github.com/otfabric/modbus/internal/adu"
)

// DeviceIDCategory selects the device identification category for FC43 requests.
type DeviceIDCategory uint8

const (
	DeviceIDBasic      DeviceIDCategory = 0x01
	DeviceIDRegular    DeviceIDCategory = 0x02
	DeviceIDExtended   DeviceIDCategory = 0x03
	DeviceIDIndividual DeviceIDCategory = 0x04
)

// DeviceIDObjectID identifies a device identification object (FC43).
type DeviceIDObjectID uint8

// DeviceIdentificationObject represents one object from an FC43/MEI response.
type DeviceIdentificationObject struct {
	ID    DeviceIDObjectID
	Name  string
	Value string
}

// DeviceIdentification groups all decoded data from an FC43/MEI response.
type DeviceIdentification struct {
	Category        DeviceIDCategory
	ConformityLevel uint8
	MoreFollows     bool
	NextObjectID    DeviceIDObjectID
	Objects         []DeviceIdentificationObject
}

// SupportsStreamAccess reports whether the device's conformity level includes
// stream access (categories 01–03). All valid conformity levels support stream access.
func (di *DeviceIdentification) SupportsStreamAccess() bool {
	switch di.ConformityLevel {
	case 0x01, 0x02, 0x03, 0x81, 0x82, 0x83:
		return true
	default:
		return false
	}
}

// SupportsIndividualAccess reports whether the device's conformity level includes
// individual object access (category 04). Conformity levels 0x81–0x83 indicate both
// stream and individual access are supported.
func (di *DeviceIdentification) SupportsIndividualAccess() bool {
	switch di.ConformityLevel {
	case 0x81, 0x82, 0x83:
		return true
	default:
		return false
	}
}

// objectDescription returns a descriptive label for known device ID object IDs.
func objectDescription(id DeviceIDObjectID) string {
	switch {
	case id == 0x00:
		return "VendorName"
	case id == 0x01:
		return "ProductCode"
	case id == 0x02:
		return "MajorMinorRevision"
	case id == 0x03:
		return "VendorUrl"
	case id == 0x04:
		return "ProductName"
	case id == 0x05:
		return "ModelName"
	case id == 0x06:
		return "UserApplicationName"
	case id >= 0x07 && id <= 0x7F:
		return "Reserved"
	default:
		return "Extended"
	}
}

// ReadDeviceIdentification reads device identification objects using FC43 / MEI type 0x0E.
// It automatically pages through MoreFollows and returns all objects for the requested category.
//
// category selects what to read (use DeviceIDBasic, DeviceIDRegular, DeviceIDExtended,
// or DeviceIDIndividual). startObject is the first object ID to fetch (use 0 for stream
// access; for Individual, pass the desired object ID). The device responds at its
// conformity level if a higher category is requested.
//
// Pagination is bounded to 32 round-trips as a safety cap to prevent runaway
// loops when a device incorrectly keeps setting MoreFollows.
func (mc *Client) ReadDeviceIdentification(ctx context.Context, unitID uint8, category DeviceIDCategory, startObject DeviceIDObjectID) (di *DeviceIdentification, err error) {
	const maxPages = 32

	var req *adu.Request
	var res *adu.Response
	var offset int
	var objID DeviceIDObjectID
	var objLen uint8
	var objCount int
	var allObjs []DeviceIdentificationObject
	var nextObjID DeviceIDObjectID
	var conformityLevel uint8
	var categoryResp DeviceIDCategory
	var seenObjValues map[DeviceIDObjectID]string

	if category < 0x01 || category > 0x04 {
		err = newParameterError("ReadDeviceIdentification", "category",
			fmt.Sprintf("must be 0x01..0x04, got 0x%02X", uint8(category)))
		return
	}

	nextObjID = startObject
	seenObjValues = make(map[DeviceIDObjectID]string)

	m := mc.getMetrics()
	start := time.Now()
	if m != nil {
		m.OnRequest(unitID, FCEncapsulatedInterface)
	}
	defer func() { reportOutcome(m, unitID, FCEncapsulatedInterface, start, err) }()

	for page := 0; page < maxPages; page++ {
		req = &adu.Request{
			UnitID:       unitID,
			FunctionCode: byte(FCEncapsulatedInterface),
			Payload:      []byte{byte(MEIReadDeviceIdentification), byte(category), byte(nextObjID)},
		}

		res, err = mc.executeRequest(ctx, req)
		if err != nil {
			return
		}

		if err = checkResponseFC(res, req.FunctionCode); err != nil {
			return
		}

		if len(res.Payload) < 6 {
			err = newProtocolError("ReadDeviceIdentification",
				fmt.Sprintf("payload too short: %d bytes, need at least 6", len(res.Payload)))
			return
		}

		if res.Payload[0] != byte(MEIReadDeviceIdentification) {
			err = newProtocolError("ReadDeviceIdentification",
				fmt.Sprintf("unexpected MEI type 0x%02X, expected 0x%02X", res.Payload[0], byte(MEIReadDeviceIdentification)))
			return
		}

		pageCategory := DeviceIDCategory(res.Payload[1])
		pageConformityLevel := res.Payload[2]

		switch pageConformityLevel {
		case 0x01, 0x02, 0x03, 0x81, 0x82, 0x83:
		default:
			err = newProtocolError("ReadDeviceIdentification",
				fmt.Sprintf("invalid conformity level 0x%02X (allowed: 0x01-0x03, 0x81-0x83)", pageConformityLevel))
			return
		}

		if page == 0 {
			categoryResp = pageCategory
			conformityLevel = pageConformityLevel
		} else {
			if pageCategory != categoryResp {
				err = newProtocolError("ReadDeviceIdentification",
					fmt.Sprintf("category changed across pages: 0x%02X → 0x%02X", uint8(categoryResp), uint8(pageCategory)))
				return
			}
			if pageConformityLevel != conformityLevel {
				err = newProtocolError("ReadDeviceIdentification",
					fmt.Sprintf("conformity level changed across pages: 0x%02X → 0x%02X", conformityLevel, pageConformityLevel))
				return
			}
		}
		objCount = int(res.Payload[5])
		offset = 6

		for i := 0; i < objCount; i++ {
			if offset+2 > len(res.Payload) {
				err = newProtocolError("ReadDeviceIdentification",
					fmt.Sprintf("truncated object header at offset %d (object %d/%d)", offset, i, objCount))
				return
			}

			objID = DeviceIDObjectID(res.Payload[offset])
			objLen = res.Payload[offset+1]
			offset += 2

			if offset+int(objLen) > len(res.Payload) {
				err = newProtocolError("ReadDeviceIdentification",
					fmt.Sprintf("truncated object body at offset %d: need %d bytes, have %d", offset, objLen, len(res.Payload)-offset))
				return
			}

			objValue := string(res.Payload[offset : offset+int(objLen)])
			if prevValue, dup := seenObjValues[objID]; dup {
				if prevValue != objValue {
					err = newProtocolError("ReadDeviceIdentification",
						fmt.Sprintf("duplicate object ID 0x%02X with conflicting value across pages", uint8(objID)))
					return
				}
			} else {
				seenObjValues[objID] = objValue
				allObjs = append(allObjs, DeviceIdentificationObject{
					ID:    objID,
					Name:  objectDescription(objID),
					Value: objValue,
				})
			}

			offset += int(objLen)
		}

		if offset != len(res.Payload) {
			err = newProtocolError("ReadDeviceIdentification",
				fmt.Sprintf("trailing data: consumed %d bytes, payload has %d", offset, len(res.Payload)))
			return
		}

		moreFollows := res.Payload[3]
		if moreFollows != 0x00 && moreFollows != 0xff {
			err = newProtocolError("ReadDeviceIdentification",
				fmt.Sprintf("invalid MoreFollows value 0x%02X (expected 0x00 or 0xFF)", moreFollows))
			return
		}

		if moreFollows == 0xff {
			if category == DeviceIDIndividual {
				err = newProtocolError("ReadDeviceIdentification",
					"individual access must not set MoreFollows=0xFF")
				return
			}
			newNextObjID := DeviceIDObjectID(res.Payload[4])
			if newNextObjID == nextObjID {
				err = newProtocolError("ReadDeviceIdentification",
					fmt.Sprintf("pagination stuck: NextObjectID not advancing (0x%02X)", uint8(newNextObjID)))
				return
			}
			nextObjID = newNextObjID
			continue
		}

		// MoreFollows == 0x00: final page.
		finalNextObjID := DeviceIDObjectID(res.Payload[4])
		if finalNextObjID != 0x00 {
			err = newProtocolError("ReadDeviceIdentification",
				fmt.Sprintf("MoreFollows=0x00 but NextObjectID=0x%02X (must be 0x00)", uint8(finalNextObjID)))
			return
		}

		if category == DeviceIDIndividual {
			if len(allObjs) != 1 {
				err = newProtocolError("ReadDeviceIdentification",
					fmt.Sprintf("individual access must return exactly 1 object, got %d", len(allObjs)))
				return
			}
		}

		di = &DeviceIdentification{
			Category:        categoryResp,
			ConformityLevel: conformityLevel,
			MoreFollows:     false,
			NextObjectID:    finalNextObjID,
			Objects:         allObjs,
		}
		return
	}

	err = newProtocolError("ReadDeviceIdentification",
		fmt.Sprintf("pagination exceeded max page count (%d)", maxPages))
	return
}

// ReadAllDeviceIdentification reads all device identification the unit supports:
// basic, regular, and extended (FC43 / MEI 0x0E). It requests the Extended category
// (ReadDeviceIDExtended); the device responds with all objects it implements, up to
// its conformity level. Use this when you want a single, complete snapshot of
// device identification without calling ReadDeviceIdentification multiple times.
func (mc *Client) ReadAllDeviceIdentification(ctx context.Context, unitID uint8) (*DeviceIdentification, error) {
	return mc.ReadDeviceIdentification(ctx, unitID, DeviceIDExtended, 0x00)
}
