// SPDX-License-Identifier: MIT

package transport

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/logging"
)

// Fuzz targets for the client-side response readers of the three framings:
// whatever bytes a hostile device sends, the reader must not panic, must
// consume no more than it was given, and a response it accepts must be one
// the framing can carry.

// hostileConn is a net.Conn whose reads come from a fixed byte string and
// whose writes are discarded.
type hostileConn struct {
	r *bytes.Reader
}

func (c *hostileConn) Read(p []byte) (int, error)       { return c.r.Read(p) }
func (c *hostileConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *hostileConn) Close() error                     { return nil }
func (c *hostileConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *hostileConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *hostileConn) SetDeadline(time.Time) error      { return nil }
func (c *hostileConn) SetReadDeadline(time.Time) error  { return nil }
func (c *hostileConn) SetWriteDeadline(time.Time) error { return nil }

func FuzzHostileTCPResponse(f *testing.F) {
	good := adu.AssembleMBAP(1, 0x11, 0x03, []byte{2, 0xAB, 0xCD})
	f.Add(good)
	f.Add(append(adu.AssembleMBAP(9, 0x11, 0x03, []byte{2, 0, 0}), good...))
	f.Add([]byte{0, 1, 0, 1, 0, 6, 0x11, 3, 2, 0, 0, 0, 1, 0, 0, 0, 6, 0x11, 3, 2, 0, 0})
	f.Add([]byte{0, 1, 0, 0, 0xFF, 0xFF, 0x11})
	f.Add([]byte{0, 1, 0, 0, 0, 0, 0x11})
	f.Add([]byte{0, 1, 0, 0, 0, 1, 0x11, 0x83})
	f.Fuzz(func(t *testing.T, data []byte) {
		tt := NewTCP(&hostileConn{r: bytes.NewReader(data)}, time.Second, logging.NopLogger())
		req := &adu.Request{UnitID: 0x11, FunctionCode: 0x03, Payload: []byte{0, 0, 0, 1}}
		res, err := tt.ExecuteRequest(context.Background(), req)
		if err != nil {
			return
		}
		if res.TransactionID != 1 {
			t.Fatalf("accepted transaction ID %d for request 1", res.TransactionID)
		}
		if 2+len(res.Payload) > adu.MBAPLengthMax {
			t.Fatalf("accepted a %d-byte payload", len(res.Payload))
		}
	})
}

func FuzzHostileRTUResponse(f *testing.F) {
	f.Add(adu.AssembleRTUFrame(0x11, 0x03, []byte{2, 0xAB, 0xCD}))
	f.Add(adu.AssembleRTUFrame(0x11, 0x08, []byte{0, 0, 0x12, 0x34}))
	f.Add(adu.AssembleRTUFrame(0x11, 0x2B, []byte{0x0E, 1, 0x81, 0, 0, 1, 0, 1, 'a'}))
	f.Add(adu.AssembleRTUFrame(0x11, 0x18, []byte{0, 4, 0, 1, 0, 7}))
	f.Add(adu.AssembleRTUFrame(0x11, 0x83, []byte{2}))
	f.Add(adu.AssembleRTUFrame(0x11, 0x07, []byte{0xA5}))
	f.Add([]byte{0x11, 0x18, 0xFF, 0xFF})
	f.Add([]byte{0x11, 0x03, 0xFF})
	f.Fuzz(func(t *testing.T, data []byte) {
		link := &scriptedLink{chunks: [][]byte{append([]byte(nil), data...)}, readErr: io.EOF}
		rt := rawRTU(link)
		res, err := rt.ExecuteRequest(context.Background(), rtuReq())
		if err != nil {
			return
		}
		frame := adu.AssembleRTUFrame(res.UnitID, res.FunctionCode, res.Payload)
		if len(frame) > adu.MaxRTUFrameLength {
			t.Fatalf("accepted a %d-byte RTU frame", len(frame))
		}
		if !bytes.HasPrefix(data, frame) {
			t.Fatalf("accepted % X, which is not the start of the input % X", frame, data)
		}
	})
}

func FuzzHostileASCIIResponse(f *testing.F) {
	f.Add(adu.AssembleASCIIFrame(0x11, 0x03, []byte{2, 0xAB, 0xCD}))
	f.Add(append([]byte("noise:0:"), adu.AssembleASCIIFrame(0x11, 0x03, []byte{2, 0, 1})[1:]...))
	f.Add([]byte(":1103\r\n"))
	f.Add([]byte(":zz\r\n"))
	f.Add(bytes.Repeat([]byte{':', '0'}, 400))
	f.Fuzz(func(t *testing.T, data []byte) {
		link := &scriptedLink{chunks: [][]byte{append([]byte(nil), data...)}, readErr: io.EOF}
		at := NewASCII(link, time.Second, logging.NopLogger())
		res, err := at.ExecuteRequest(context.Background(), asciiReq())
		if err != nil {
			return
		}
		frame := adu.AssembleASCIIFrame(res.UnitID, res.FunctionCode, res.Payload)
		if len(frame) > adu.MaxASCIIFrameLength {
			t.Fatalf("accepted a %d-byte ASCII frame", len(frame))
		}
		if !bytes.Contains(bytes.ToUpper(data), frame) {
			t.Fatalf("accepted %q, which the input %q does not contain", frame, data)
		}
	})
}
