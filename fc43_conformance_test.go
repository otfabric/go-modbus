// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/otfabric/go-modbus/internal/adu"
)

// Conformance tests for FC43 / MEI 0x0E, Read Device Identification (MODBUS
// Application Protocol V1.1b3, section 6.21): the conformity levels the server
// sends, the objects it serves at each level, handler data it refuses,
// pagination that terminates, and the client's checks of a response against
// its request.

// fc43Serve runs one FC43 request with the given request payload (after the
// function code) against a handler that returns objects at level.
func fc43Serve(t *testing.T, level uint8, objects []DeviceIdentificationObject, payload ...byte) (*adu.Response, error) {
	t.Helper()
	ms, err := NewServer(&ServerConfig{URL: "tcp://localhost:5599"}, &deviceIDTestHandler{objects: objects, conformity: level})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	req := &adu.Request{UnitID: 1, FunctionCode: byte(FCEncapsulatedInterface), Payload: payload}
	return ms.handleReadDeviceIdentification(context.Background(), req, 1, "", "")
}

// fc43Page is a decoded stream or individual response page.
type fc43Page struct {
	code, level, more, next byte
	ids                     []DeviceIDObjectID
	values                  []string
}

// fc43Decode decodes a response payload, failing the test on one that is not
// well formed (lengths, Number Of Objects counting the objects of the PDU).
func fc43Decode(t *testing.T, p []byte) fc43Page {
	t.Helper()
	if len(p) < 6 || p[0] != 0x0E {
		t.Fatalf("response payload % X", p)
	}
	pg := fc43Page{code: p[1], level: p[2], more: p[3], next: p[4]}
	off := 6
	for range int(p[5]) {
		if off+2 > len(p) || off+2+int(p[off+1]) > len(p) {
			t.Fatalf("response payload % X: object at %d truncated", p, off)
		}
		pg.ids = append(pg.ids, DeviceIDObjectID(p[off]))
		pg.values = append(pg.values, string(p[off+2:off+2+int(p[off+1])]))
		off += 2 + int(p[off+1])
	}
	if off != len(p) {
		t.Fatalf("response payload % X: %d bytes after the objects", p, len(p)-off)
	}
	if 1+len(p) > 253 {
		t.Fatalf("response PDU of %d bytes", 1+len(p))
	}
	return pg
}

// fc43MaxID is the highest object ID of a stream category (V1.1b3 6.21).
func fc43MaxID(category byte) DeviceIDObjectID {
	switch category {
	case 1:
		return 0x02
	case 2:
		return 0x7F
	default:
		return 0xFF
	}
}

// TestFC43Server_ConformityLevelAllValues: of the 256 values a handler can
// return as ConformityLevel, 0 derives a level from the objects and the six
// levels of 6.21 are sent as they are; every other value is a Server Device
// Failure. No response carries a level outside the six.
func TestFC43Server_ConformityLevelAllValues(t *testing.T) {
	for v := range 256 {
		level := uint8(v)
		for _, code := range []byte{1, 2, 3, 4} {
			res, err := fc43Serve(t, level, regularDeviceIDObjects(), 0x0E, code, 0x00)
			valid := validConformityLevel(level)
			switch {
			case level == 0:
				if err != nil {
					t.Fatalf("level 0, code %d: %v", code, err)
				}
				if got := fc43Decode(t, res.Payload).level; got != 0x82 {
					t.Fatalf("level 0 (derived), code %d: sent 0x%02x, want 0x82", code, got)
				}
			case !valid:
				if !errors.Is(err, ErrServerDeviceFailure) {
					t.Fatalf("level 0x%02x, code %d: err %v, want ErrServerDeviceFailure", level, code, err)
				}
			case code == 4 && level&0x80 == 0:
				if !errors.Is(err, ErrIllegalDataValue) {
					t.Fatalf("level 0x%02x, individual: err %v, want ErrIllegalDataValue", level, err)
				}
			default:
				if err != nil {
					t.Fatalf("level 0x%02x, code %d: %v", level, code, err)
				}
				if got := fc43Decode(t, res.Payload).level; got != level {
					t.Fatalf("level 0x%02x, code %d: sent 0x%02x", level, code, got)
				}
			}
		}
	}
}

// TestFC43Server_CategoryLevelObjects crosses the four ReadDevId codes, the
// six conformity levels and the derived one, and three object sets. A stream
// request is served at the lower of its category and the level's, under the
// request's ReadDevId code; individual access needs a level with 0x80 and an
// object visible at the level's category.
func TestFC43Server_CategoryLevelObjects(t *testing.T) {
	extended := append(regularDeviceIDObjects(),
		DeviceIdentificationObject{ID: 0x80, Value: "private-80"},
		DeviceIdentificationObject{ID: 0x90, Value: "private-90"},
	)
	sets := map[string][]DeviceIdentificationObject{
		"basic":    basicDeviceIDObjects(),
		"regular":  regularDeviceIDObjects(),
		"extended": extended,
	}
	for name, objects := range sets {
		have := map[DeviceIDObjectID]bool{}
		for _, o := range objects {
			have[o.ID] = true
		}
		for _, handlerLevel := range []uint8{0, 0x01, 0x02, 0x03, 0x81, 0x82, 0x83} {
			level := handlerLevel
			if level == 0 {
				level = deriveConformityLevel(objects)
			}
			for code := byte(1); code <= 3; code++ {
				t.Run(fmt.Sprintf("%s/level %02x/code %d", name, handlerLevel, code), func(t *testing.T) {
					res, err := fc43Serve(t, handlerLevel, objects, 0x0E, code, 0x00)
					if err != nil {
						t.Fatal(err)
					}
					pg := fc43Decode(t, res.Payload)
					if pg.code != code || pg.level != level || pg.more != 0 || pg.next != 0 {
						t.Fatalf("header code %d level 0x%02x more 0x%02x next 0x%02x", pg.code, pg.level, pg.more, pg.next)
					}
					limit := fc43MaxID(min(code, level&0x7F))
					var want []DeviceIDObjectID
					for _, o := range sortedObjects(objects) {
						if o.ID <= limit {
							want = append(want, o.ID)
						}
					}
					if !reflect.DeepEqual(pg.ids, want) {
						t.Fatalf("objects %v, want %v", pg.ids, want)
					}
				})
			}
			t.Run(fmt.Sprintf("%s/level %02x/individual", name, handlerLevel), func(t *testing.T) {
				for _, id := range []DeviceIDObjectID{0x00, 0x02, 0x03, 0x05, 0x06, 0x7F, 0x80, 0x90, 0xFF} {
					res, err := fc43Serve(t, handlerLevel, objects, 0x0E, 0x04, byte(id))
					switch {
					case level&0x80 == 0:
						if !errors.Is(err, ErrIllegalDataValue) {
							t.Fatalf("object 0x%02x: err %v, want ErrIllegalDataValue (stream-only level)", uint8(id), err)
						}
					case !have[id] || id > fc43MaxID(level&0x7F):
						if !errors.Is(err, ErrIllegalDataAddress) {
							t.Fatalf("object 0x%02x: err %v, want ErrIllegalDataAddress", uint8(id), err)
						}
					default:
						if err != nil {
							t.Fatalf("object 0x%02x: %v", uint8(id), err)
						}
						pg := fc43Decode(t, res.Payload)
						if pg.code != 4 || pg.level != level || pg.more != 0 || pg.next != 0 || len(pg.ids) != 1 || pg.ids[0] != id {
							t.Fatalf("object 0x%02x: %+v", uint8(id), pg)
						}
					}
				}
			})
		}
	}
}

func sortedObjects(objects []DeviceIdentificationObject) []DeviceIdentificationObject {
	out := append([]DeviceIdentificationObject(nil), objects...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j].ID < out[i].ID {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// TestFC43Server_StreamStartNotVisible: a stream request from an object the
// device has, but beyond the category it is served at, restarts at object 0
// like one the device does not have (6.21: "responds as if object 0 were
// pointed out").
func TestFC43Server_StreamStartNotVisible(t *testing.T) {
	for _, start := range []byte{0x05, 0x7F, 0xFF} {
		res, err := fc43Serve(t, 0x83, regularDeviceIDObjects(), 0x0E, 0x01, start)
		if err != nil {
			t.Fatal(err)
		}
		pg := fc43Decode(t, res.Payload)
		if want := []DeviceIDObjectID{0, 1, 2}; !reflect.DeepEqual(pg.ids, want) {
			t.Errorf("basic from 0x%02x: objects %v, want %v", start, pg.ids, want)
		}
	}
	// A regular device asked for extended from 0x80 serves regular from 0.
	res, err := fc43Serve(t, 0x82, append(regularDeviceIDObjects(), DeviceIdentificationObject{ID: 0x80, Value: "x"}), 0x0E, 0x03, 0x80)
	if err != nil {
		t.Fatal(err)
	}
	if pg := fc43Decode(t, res.Payload); pg.code != 3 || len(pg.ids) != 6 || pg.ids[0] != 0 {
		t.Errorf("regular device, extended from 0x80: %+v", pg)
	}
}

// TestFC43Server_DuplicateObjectIDs: a handler that returns an object ID
// twice gets Server Device Failure for every request, whether the duplicates
// would share a page, sit on either side of a page boundary, or lie outside
// the category asked for.
func TestFC43Server_DuplicateObjectIDs(t *testing.T) {
	big := strings.Repeat("B", 200)
	cases := map[string][]DeviceIdentificationObject{
		"within one page": append(basicDeviceIDObjects(), DeviceIdentificationObject{ID: 0x01, Value: "again"}),
		"same value":      append(basicDeviceIDObjects(), basicDeviceIDObjects()[2]),
		"across a page boundary": append(basicDeviceIDObjects(),
			DeviceIdentificationObject{ID: 0x80, Value: big},
			DeviceIdentificationObject{ID: 0x81, Value: big},
			DeviceIdentificationObject{ID: 0x80, Value: "short"}),
		"outside the category": append(basicDeviceIDObjects(),
			DeviceIdentificationObject{ID: 0x90, Value: "a"},
			DeviceIdentificationObject{ID: 0x90, Value: "b"}),
	}
	for name, objects := range cases {
		for _, req := range [][]byte{{0x0E, 1, 0}, {0x0E, 3, 0}, {0x0E, 3, 0x81}, {0x0E, 4, 0x00}} {
			if _, err := fc43Serve(t, 0, objects, req...); !errors.Is(err, ErrServerDeviceFailure) {
				t.Errorf("%s, request % X: err %v, want ErrServerDeviceFailure", name, req, err)
			}
		}
	}
}

// TestFC43Server_ObjectSizes: an object of 244 bytes fills a page of its own
// (2 + 244 = 246 bytes after the 7-byte header); one byte more does not fit
// any response and is a Server Device Failure, not an empty page.
func TestFC43Server_ObjectSizes(t *testing.T) {
	fits := append(basicDeviceIDObjects(), DeviceIdentificationObject{ID: 0x80, Value: strings.Repeat("v", 244)})
	res, err := fc43Serve(t, 0x83, fits, 0x0E, 0x03, 0x80)
	if err != nil {
		t.Fatal(err)
	}
	if pg := fc43Decode(t, res.Payload); len(pg.ids) != 1 || pg.ids[0] != 0x80 || len(res.Payload)+1 != 253 {
		t.Fatalf("244-byte object: %d-byte PDU, %+v", len(res.Payload)+1, pg.ids)
	}
	tooBig := append(basicDeviceIDObjects(), DeviceIdentificationObject{ID: 0x80, Value: strings.Repeat("v", 245)})
	for _, req := range [][]byte{{0x0E, 1, 0}, {0x0E, 3, 0}, {0x0E, 4, 0x80}} {
		if _, err := fc43Serve(t, 0x83, tooBig, req...); !errors.Is(err, ErrServerDeviceFailure) {
			t.Errorf("245-byte object, request % X: err %v, want ErrServerDeviceFailure", req, err)
		}
	}
	// The builder refuses a page it cannot fill even when called directly.
	ms, _ := NewServer(&ServerConfig{URL: "tcp://localhost:5599"}, &deviceIDTestHandler{})
	req := &adu.Request{UnitID: 1, FunctionCode: byte(FCEncapsulatedInterface)}
	if _, err := ms.buildStreamDeviceIDResponse(req, 1, DeviceIDExtended, 0x83, 0x80,
		[]DeviceIdentificationObject{{ID: 0x80, Value: strings.Repeat("v", 245)}}); !errors.Is(err, ErrServerDeviceFailure) {
		t.Errorf("builder, oversized object: err %v", err)
	}
}

// TestFC43Server_PaginationTerminates follows MoreFollows / Next Object ID
// from object 0, as a client does, over object sets of many sizes: every page
// holds at least one object, its Next Object ID is above every ID on it, the
// walk ends within 256 pages, and together the pages hold every visible
// object once, in order.
func TestFC43Server_PaginationTerminates(t *testing.T) {
	var all256, big, mixed []DeviceIdentificationObject
	for id := range 256 {
		all256 = append(all256, DeviceIdentificationObject{ID: DeviceIDObjectID(id), Value: strings.Repeat("x", id%17)})
	}
	for id := 0x80; id < 0x90; id++ {
		big = append(big, DeviceIdentificationObject{ID: DeviceIDObjectID(id), Value: strings.Repeat("y", 244)})
	}
	big = append(big, basicDeviceIDObjects()...)
	for id := 0; id < 256; id += 7 {
		mixed = append(mixed, DeviceIdentificationObject{ID: DeviceIDObjectID(255 - id), Value: strings.Repeat("z", (id*37)%245)})
	}
	sets := map[string][]DeviceIdentificationObject{
		"empty": nil, "basic": basicDeviceIDObjects(), "all 256": all256, "large values": big, "mixed, unsorted": mixed,
	}
	for name, objects := range sets {
		for code := byte(1); code <= 3; code++ {
			var got []DeviceIDObjectID
			next := byte(0)
			pages := 0
			for {
				pages++
				if pages > 256 {
					t.Fatalf("%s, code %d: more than 256 pages", name, code)
				}
				res, err := fc43Serve(t, 0x83, objects, 0x0E, code, next)
				if err != nil {
					t.Fatalf("%s, code %d, from 0x%02x: %v", name, code, next, err)
				}
				pg := fc43Decode(t, res.Payload)
				got = append(got, pg.ids...)
				if pg.more == 0x00 {
					if pg.next != 0 {
						t.Fatalf("%s: last page with Next Object ID 0x%02x", name, pg.next)
					}
					break
				}
				if pg.more != 0xFF || len(pg.ids) == 0 {
					t.Fatalf("%s: page %+v", name, pg)
				}
				for _, id := range pg.ids {
					if DeviceIDObjectID(pg.next) <= id {
						t.Fatalf("%s: Next Object ID 0x%02x does not advance past 0x%02x", name, pg.next, uint8(id))
					}
				}
				next = pg.next
			}
			var want []DeviceIDObjectID
			for _, o := range sortedObjects(objects) {
				if o.ID <= fc43MaxID(code) {
					want = append(want, o.ID)
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s, code %d: objects %v, want %v", name, code, got, want)
			}
		}
	}
	// An empty object list: one page, no object.
	res, err := fc43Serve(t, 0x81, nil, 0x0E, 0x01, 0x00)
	if err != nil || string(res.Payload) != string([]byte{0x0E, 0x01, 0x81, 0x00, 0x00, 0x00}) {
		t.Fatalf("empty list: % X, %v", res.Payload, err)
	}
}

// --- Client ---------------------------------------------------------------

// TestFC43Client_RequestBytes pins the request PDUs: full retrieval starts at
// object 0, an explicit start object is sent as given, individual access
// names the object.
func TestFC43Client_RequestBytes(t *testing.T) {
	ctx := context.Background()
	vendor := DeviceIdentificationObject{ID: 0x00, Value: "V"}
	cases := []struct {
		name string
		call func(*Client) error
		page []byte
		want []byte
	}{
		{"ReadAllDeviceIdentification", func(c *Client) error { _, err := c.ReadAllDeviceIdentification(ctx, 7); return err },
			devIDPage(DeviceIDExtended, 0x83, 0, 0, vendor), []byte{0x0E, 0x03, 0x00}},
		{"basic from 0", func(c *Client) error { _, err := c.ReadDeviceIdentification(ctx, 7, DeviceIDBasic, 0); return err },
			devIDPage(DeviceIDBasic, 0x81, 0, 0, vendor), []byte{0x0E, 0x01, 0x00}},
		{"regular from 0x03", func(c *Client) error { _, err := c.ReadDeviceIdentification(ctx, 7, DeviceIDRegular, 0x03); return err },
			devIDPage(DeviceIDRegular, 0x82, 0, 0, DeviceIdentificationObject{ID: 0x03, Value: "u"}), []byte{0x0E, 0x02, 0x03}},
		{"individual 0x05", func(c *Client) error {
			_, err := c.ReadDeviceIdentification(ctx, 7, DeviceIDIndividual, 0x05)
			return err
		},
			devIDPage(DeviceIDIndividual, 0x82, 0, 0, DeviceIdentificationObject{ID: 0x05, Value: "m"}), []byte{0x0E, 0x04, 0x05}},
	}
	for _, tc := range cases {
		c, tr, _ := newScriptedClient(t, pagedDevice(tc.page))
		if err := tc.call(c); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		reqs := tr.requests()
		if len(reqs) != 1 || reqs[0].UnitID != 7 || reqs[0].FunctionCode != 0x2B || string(reqs[0].Payload) != string(tc.want) {
			t.Fatalf("%s: requests %+v, want 2B % X", tc.name, reqs, tc.want)
		}
	}
}

// TestFC43Client_ResponseIdentity: a response whose ReadDevId code is not the
// request's (other than a lower stream code for stream access, tolerated), or
// an individual response for another object or with another number of
// objects, is a protocol error.
func TestFC43Client_ResponseIdentity(t *testing.T) {
	ctx := context.Background()
	vendor := DeviceIdentificationObject{ID: 0x00, Value: "V"}
	model := DeviceIdentificationObject{ID: 0x05, Value: "M"}

	// A device that answers a stream request above its level with the code of
	// its level: section 6.21 asks for the request's code, but up to v1.19.0
	// it was accepted, and it still is. Category reports the response's code.
	for _, tc := range []struct {
		name      string
		requested DeviceIDCategory
		answered  DeviceIDCategory
	}{
		{"extended answered as basic", DeviceIDExtended, DeviceIDBasic},
		{"extended answered as regular", DeviceIDExtended, DeviceIDRegular},
		{"regular answered as basic", DeviceIDRegular, DeviceIDBasic},
	} {
		c, _, _ := newScriptedClient(t, pagedDevice(devIDPage(tc.answered, 0x80|uint8(tc.answered), 0, 0, vendor)))
		di, err := c.ReadDeviceIdentification(ctx, 7, tc.requested, 0)
		if err != nil || di.Category != tc.answered || di.ConformityLevel != 0x80|uint8(tc.answered) || len(di.Objects) != 1 {
			t.Errorf("%s: %+v, %v; want Category 0x%02X, ConformityLevel 0x%02X and the object",
				tc.name, di, err, uint8(tc.answered), 0x80|uint8(tc.answered))
		}
	}

	// Over two pages the tolerated code must stay the same: it is kept in
	// Category, with the conformity level, and a page that changes it (even
	// to the request's own code) is a protocol error.
	product := DeviceIdentificationObject{ID: 0x01, Value: "P"}
	c, _, _ := newScriptedClient(t, pagedDevice(
		devIDPage(DeviceIDBasic, 0x81, 0xFF, 0x01, vendor),
		devIDPage(DeviceIDBasic, 0x81, 0, 0, product)))
	di, err := c.ReadDeviceIdentification(ctx, 7, DeviceIDExtended, 0)
	if err != nil || di.Category != DeviceIDBasic || di.ConformityLevel != 0x81 || len(di.Objects) != 2 {
		t.Errorf("two pages answered as basic: %+v, %v; want Category basic, level 0x81, two objects", di, err)
	}
	for _, second := range []struct {
		code  DeviceIDCategory
		level byte
		want  string
	}{
		{DeviceIDExtended, 0x81, "category changed across pages: 0x01 → 0x03"},
		{DeviceIDRegular, 0x81, "category changed across pages: 0x01 → 0x02"},
		{DeviceIDBasic, 0x82, "conformity level changed across pages: 0x81 → 0x82"},
	} {
		c, _, _ := newScriptedClient(t, pagedDevice(
			devIDPage(DeviceIDBasic, 0x81, 0xFF, 0x01, vendor),
			devIDPage(second.code, second.level, 0, 0, product)))
		di, err := c.ReadDeviceIdentification(ctx, 7, DeviceIDExtended, 0)
		if di != nil {
			t.Errorf("second page 0x%02X/0x%02X: identification %+v returned with the error", uint8(second.code), second.level, di)
		}
		wantProtocolErrorText(t, err, second.want)
	}

	cases := []struct {
		name     string
		category DeviceIDCategory
		start    DeviceIDObjectID
		page     []byte
		want     string
	}{
		{"basic answered as extended", DeviceIDBasic, 0, devIDPage(DeviceIDExtended, 0x83, 0, 0, vendor),
			"ReadDevId code 0x03 in the response, the request had 0x01"},
		{"regular answered with code 0", DeviceIDRegular, 0, devIDPage(0, 0x82, 0, 0, vendor),
			"ReadDevId code 0x00 in the response, the request had 0x02"},
		{"basic answered as individual", DeviceIDBasic, 0, devIDPage(DeviceIDIndividual, 0x81, 0, 0, vendor),
			"ReadDevId code 0x04 in the response, the request had 0x01"},
		{"individual answered as stream", DeviceIDIndividual, 0x05, devIDPage(DeviceIDRegular, 0x82, 0, 0, model),
			"ReadDevId code 0x02 in the response, the request had 0x04"},
		{"individual, another object", DeviceIDIndividual, 0x05, devIDPage(DeviceIDIndividual, 0x82, 0, 0, vendor),
			"individual access to object 0x05 answered with object 0x00"},
		{"individual, no object", DeviceIDIndividual, 0x05, devIDPage(DeviceIDIndividual, 0x82, 0, 0),
			"individual access must return exactly 1 object, the response announces 0"},
		{"individual, two objects", DeviceIDIndividual, 0x05, devIDPage(DeviceIDIndividual, 0x82, 0, 0, model, vendor),
			"individual access must return exactly 1 object, the response announces 2"},
	}
	for _, tc := range cases {
		c, _, _ := newScriptedClient(t, pagedDevice(tc.page))
		di, err := c.ReadDeviceIdentification(ctx, 7, tc.category, tc.start)
		if di != nil {
			t.Errorf("%s: identification %+v returned with the error", tc.name, di)
		}
		wantProtocolErrorText(t, err, tc.want)
	}
}

// TestFC43Client_SpecificationExamples replays the worked examples of 6.21.
//
// The single-response example ("Basic device identification") is consistent
// and is decoded as given. Two of its object lengths disagree with the text
// printed for the value (0x0D for "Product code XX", 15 characters); the
// fixture keeps the printed text and gives it its own length.
//
// The two-transaction example gives Number Of Objects 03 on both pages, which
// is the number of objects of the whole identification, while the field is
// defined as the "Number of identification Object returned in the response",
// and 6.21's own single-response example and individual access ("Number Of
// Objects = 1") count the objects of the PDU. The client counts per PDU and
// does not loosen that to accept the example: the pages as printed are
// rejected (the first one announces an object it does not carry), and the
// same exchange with per-PDU counts is accepted.
func TestFC43Client_SpecificationExamples(t *testing.T) {
	ctx := context.Background()
	vendor := DeviceIdentificationObject{ID: 0x00, Value: "Company identification"}
	product := DeviceIdentificationObject{ID: 0x01, Value: "Product code XX"}
	revision := DeviceIdentificationObject{ID: 0x02, Value: "V2.11"}
	longProduct := DeviceIdentificationObject{ID: 0x01, Value: "Product code XXXXXXXXXXXXXXXX"}

	single := []byte{0x0E, 0x01, 0x01, 0x00, 0x00, 0x03,
		0x00, 0x16, 'C', 'o', 'm', 'p', 'a', 'n', 'y', ' ', 'i', 'd', 'e', 'n', 't', 'i', 'f', 'i', 'c', 'a', 't', 'i', 'o', 'n',
		0x01, 0x0F, 'P', 'r', 'o', 'd', 'u', 'c', 't', ' ', 'c', 'o', 'd', 'e', ' ', 'X', 'X',
		0x02, 0x05, 'V', '2', '.', '1', '1'}
	c, tr, _ := newScriptedClient(t, pagedDevice(single))
	di, err := c.ReadDeviceIdentification(ctx, 1, DeviceIDBasic, 0)
	if err != nil {
		t.Fatalf("single-response example: %v", err)
	}
	if di.Category != DeviceIDBasic || di.ConformityLevel != 0x01 || len(di.Objects) != 3 ||
		di.Objects[0].Value != vendor.Value || di.Objects[1].Value != product.Value || di.Objects[2].Value != revision.Value {
		t.Fatalf("single-response example: %+v", di)
	}
	if reqs := tr.requests(); len(reqs) != 1 || string(reqs[0].Payload) != "\x0e\x01\x00" {
		t.Fatalf("single-response example: requests %+v, want 2B 0E 01 00", reqs)
	}

	// As printed: Number Of Objects 03 on both pages.
	printed1 := devIDPage(DeviceIDBasic, 0x01, 0xFF, 0x02, vendor, longProduct)
	printed1[5] = 0x03
	printed2 := devIDPage(DeviceIDBasic, 0x01, 0x00, 0x00, revision)
	printed2[5] = 0x03
	c, _, _ = newScriptedClient(t, pagedDevice(printed1, printed2))
	_, err = c.ReadDeviceIdentification(ctx, 1, DeviceIDBasic, 0)
	wantProtocolErrorText(t, err, "truncated object header")
	// The second page alone (as if the first had been accepted).
	c, _, _ = newScriptedClient(t, pagedDevice(printed2))
	_, err = c.ReadDeviceIdentification(ctx, 1, DeviceIDBasic, 0x02)
	wantProtocolErrorText(t, err, "truncated object header")

	// The same exchange counted per PDU.
	c, tr, _ = newScriptedClient(t, pagedDevice(
		devIDPage(DeviceIDBasic, 0x01, 0xFF, 0x02, vendor, longProduct),
		devIDPage(DeviceIDBasic, 0x01, 0x00, 0x00, revision)))
	di, err = c.ReadDeviceIdentification(ctx, 1, DeviceIDBasic, 0)
	if err != nil || len(di.Objects) != 3 || di.Objects[1].Value != longProduct.Value {
		t.Fatalf("two-page example, per-PDU counts: %+v, %v", di, err)
	}
	if reqs := tr.requests(); len(reqs) != 2 || string(reqs[0].Payload) != "\x0e\x01\x00" || string(reqs[1].Payload) != "\x0e\x01\x02" {
		t.Fatalf("two-page example: requests %+v", reqs)
	}
}

// TestFC43_ServerClientRoundTrip: the client reads back, at every category,
// what the server serves for a device with a private object and a value that
// forces pagination, and individual access gives each visible object.
func TestFC43_ServerClientRoundTrip(t *testing.T) {
	objects := append(regularDeviceIDObjects(),
		DeviceIdentificationObject{ID: 0x80, Value: strings.Repeat("A", 240)},
		DeviceIdentificationObject{ID: 0x81, Value: strings.Repeat("B", 240)},
	)
	ms, err := NewServer(&ServerConfig{URL: "tcp://localhost:5599"}, &deviceIDTestHandler{objects: objects, conformity: 0x83})
	if err != nil {
		t.Fatal(err)
	}
	c, _, _ := newScriptedClient(t, func(req *adu.Request) (*adu.Response, error) {
		res, err := ms.handleReadDeviceIdentification(context.Background(), req, 1, "", "")
		if err != nil {
			return &adu.Response{UnitID: req.UnitID, FunctionCode: req.FunctionCode | 0x80,
				Payload: []byte{byte(mapErrorToExceptionCode(err))}}, nil
		}
		return res, nil
	})
	ctx := context.Background()
	for cat, n := range map[DeviceIDCategory]int{DeviceIDBasic: 3, DeviceIDRegular: 6, DeviceIDExtended: 8} {
		di, err := c.ReadDeviceIdentification(ctx, 1, cat, 0)
		if err != nil || len(di.Objects) != n || di.Category != cat || di.ConformityLevel != 0x83 {
			t.Fatalf("category %d: %+v, %v", cat, di, err)
		}
	}
	for _, o := range objects {
		di, err := c.ReadDeviceIdentification(ctx, 1, DeviceIDIndividual, o.ID)
		if err != nil || len(di.Objects) != 1 || di.Objects[0].Value != o.Value {
			t.Fatalf("individual 0x%02x: %+v, %v", uint8(o.ID), di, err)
		}
	}
	if _, err := c.ReadDeviceIdentification(ctx, 1, DeviceIDIndividual, 0x7E); !errors.Is(err, ErrIllegalDataAddress) {
		t.Fatalf("individual unknown: %v", err)
	}
}
