// SPDX-License-Identifier: MIT

package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/protocol"
)

// Responses to function codes the length table does not know, as sent by
// SendRawRequest: read until silence when they answer the request, one
// exception byte for an exception to it, and a protocol error otherwise.
func TestRTUReadFrame_UnknownFunctionCode(t *testing.T) {
	const vendor = 0x64
	cases := []struct {
		name    string
		reqFC   byte
		frame   []byte
		want    *adu.Response
		wantErr error
	}{
		{
			name:  "answer to the request",
			reqFC: vendor,
			frame: adu.AssembleRTUFrame(0x11, vendor, []byte{0xA5, 0x01, 0x02, 0x03}),
			want:  &adu.Response{UnitID: 0x11, FunctionCode: vendor, Payload: []byte{0xA5, 0x01, 0x02, 0x03}},
		},
		{
			name:  "one payload byte",
			reqFC: vendor,
			frame: adu.AssembleRTUFrame(0x11, vendor, []byte{0x7E}),
			want:  &adu.Response{UnitID: 0x11, FunctionCode: vendor, Payload: []byte{0x7E}},
		},
		{
			name:  "empty payload",
			reqFC: vendor,
			frame: adu.AssembleRTUFrame(0x11, vendor, nil),
			want:  &adu.Response{UnitID: 0x11, FunctionCode: vendor, Payload: []byte{}},
		},
		{
			name:  "largest payload",
			reqFC: vendor,
			frame: adu.AssembleRTUFrame(0x11, vendor, bytes.Repeat([]byte{0x3C}, 252)),
			want:  &adu.Response{UnitID: 0x11, FunctionCode: vendor, Payload: bytes.Repeat([]byte{0x3C}, 252)},
		},
		{
			name:  "exception to the request",
			reqFC: vendor,
			frame: adu.AssembleRTUFrame(0x11, vendor|0x80, []byte{0x01}),
			want:  &adu.Response{UnitID: 0x11, FunctionCode: vendor | 0x80, Payload: []byte{0x01}},
		},
		{
			name:  "unimplemented standard function code",
			reqFC: 0x09,
			frame: adu.AssembleRTUFrame(0x11, 0x09, []byte{0x01, 0x02}),
			want:  &adu.Response{UnitID: 0x11, FunctionCode: 0x09, Payload: []byte{0x01, 0x02}},
		},
		{
			name:  "known function code read by its length",
			reqFC: vendor,
			frame: adu.AssembleRTUFrame(0x11, 0x03, []byte{0x02, 0x12, 0x34}),
			want:  &adu.Response{UnitID: 0x11, FunctionCode: 0x03, Payload: []byte{0x02, 0x12, 0x34}},
		},
		{
			name:    "another unknown function code",
			reqFC:   vendor,
			frame:   adu.AssembleRTUFrame(0x11, vendor+1, []byte{0x01}),
			wantErr: protocol.ErrProtocolError,
		},
		{
			name:    "exception to another unknown function code",
			reqFC:   vendor,
			frame:   adu.AssembleRTUFrame(0x11, (vendor+1)|0x80, []byte{0x01}),
			wantErr: protocol.ErrProtocolError,
		},
		{
			name:    "no request to go by",
			reqFC:   0,
			frame:   adu.AssembleRTUFrame(0x11, vendor, []byte{0x01, 0x02}),
			wantErr: protocol.ErrProtocolError,
		},
		{
			name:  "corrupted",
			reqFC: vendor,
			frame: func() []byte {
				f := adu.AssembleRTUFrame(0x11, vendor, []byte{0x01, 0x02})
				f[3] ^= 0xFF
				return f
			}(),
			wantErr: protocol.ErrBadCRC,
		},
		{
			name:    "frame too short",
			reqFC:   vendor,
			frame:   []byte{0x11, vendor, 0x00},
			wantErr: protocol.ErrShortFrame,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The frame arrives in one piece and the line then falls silent.
			link := &hookLink{scriptedLink: scriptedLink{chunks: splitEvery(tc.frame, 1), readErr: os.ErrDeadlineExceeded}}
			res, err := rawRTU(link).readRTUFrame(tc.reqFC)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) || res != nil {
					t.Fatalf("got %+v, %v; want %v", res, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("readRTUFrame: %v", err)
			}
			if res.UnitID != tc.want.UnitID || res.FunctionCode != tc.want.FunctionCode || !bytes.Equal(res.Payload, tc.want.Payload) {
				t.Fatalf("got %+v, want %+v", res, tc.want)
			}
		})
	}
}

// The whole exchange for an unknown function code: the request frame on the
// wire and the response read back through ExecuteRequest.
func TestRTUExecuteRequest_UnknownFunctionCode(t *testing.T) {
	req := &adu.Request{UnitID: 0x22, FunctionCode: 0x41, Payload: []byte{0x10, 0x20}}
	answer := adu.AssembleRTUFrame(0x22, 0x41, []byte{0x01, 0x02, 0x03, 0x04, 0x05})
	link := &hookLink{scriptedLink: scriptedLink{chunks: splitEvery(answer, 1), readErr: os.ErrDeadlineExceeded}}
	res, err := rawRTU(link).ExecuteRequest(context.Background(), req)
	if err != nil {
		t.Fatalf("ExecuteRequest: %v", err)
	}
	if res.FunctionCode != 0x41 || !bytes.Equal(res.Payload, []byte{0x01, 0x02, 0x03, 0x04, 0x05}) {
		t.Fatalf("response %+v", res)
	}
	if want := adu.AssembleRTUFrame(0x22, 0x41, []byte{0x10, 0x20}); !bytes.Equal(link.written.Bytes(), want) {
		t.Fatalf("wrote % X, want % X", link.written.Bytes(), want)
	}

	// The link closes inside the frame: reported as such, as for FC08.
	link = &hookLink{scriptedLink: scriptedLink{chunks: [][]byte{answer[:3]}, readErr: io.EOF}}
	if _, err := rawRTU(link).ExecuteRequest(context.Background(), req); !errors.Is(err, io.EOF) {
		t.Fatalf("link closed: want io.EOF, got %v", err)
	}
	// The line falls silent inside the frame: the CRC cannot match.
	link = &hookLink{scriptedLink: scriptedLink{chunks: [][]byte{answer[:6]}, readErr: os.ErrDeadlineExceeded}}
	if _, err := rawRTU(link).ExecuteRequest(context.Background(), req); !errors.Is(err, protocol.ErrBadCRC) {
		t.Fatalf("pause inside the frame: want ErrBadCRC, got %v", err)
	}
}

// FuzzRTUReadFrame feeds arbitrary response bytes, followed by line silence, to
// the RTU reader, for an arbitrary request function code. It must never panic,
// and a frame it accepts must be a whole frame with a valid CRC.
func FuzzRTUReadFrame(f *testing.F) {
	f.Add(byte(0x41), adu.AssembleRTUFrame(0x11, 0x41, []byte{0x01, 0x02}))
	f.Add(byte(0x41), adu.AssembleRTUFrame(0x11, 0xC1, []byte{0x01}))
	f.Add(byte(0x41), adu.AssembleRTUFrame(0x11, 0x41, nil))
	f.Add(byte(0x03), adu.AssembleRTUFrame(0x11, 0x03, []byte{0x02, 0x12, 0x34}))
	f.Add(byte(0x08), adu.AssembleRTUFrame(0x11, 0x08, []byte{0x00, 0x00, 0x12, 0x34}))
	f.Add(byte(0x18), adu.AssembleRTUFrame(0x11, 0x18, []byte{0x00, 0x04, 0x00, 0x01, 0xBE, 0xEF}))
	f.Add(byte(0), []byte{0x11, 0x41, 0x00})
	f.Fuzz(func(t *testing.T, reqFC byte, data []byte) {
		link := &hookLink{scriptedLink: scriptedLink{chunks: splitEvery(data, 1), readErr: os.ErrDeadlineExceeded}}
		res, err := rawRTU(link).readRTUFrame(reqFC)
		if err != nil {
			return
		}
		frame := adu.AssembleRTUFrame(res.UnitID, res.FunctionCode, res.Payload)
		if len(frame) > adu.MaxRTUFrameLength || !bytes.HasPrefix(data, frame) {
			t.Fatalf("accepted % X from % X", frame, data)
		}
	})
}
