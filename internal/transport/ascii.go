// SPDX-License-Identifier: MIT

package transport

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/logging"
	"github.com/otfabric/go-modbus/internal/protocol"
)

// ASCII implements Transport over a Modbus ASCII link (serial line, or a
// serial-to-Ethernet bridge reached over TCP).
//
// Frames are self-delimiting (':' ... CR LF), so unlike RTU no inter-frame
// silence is required and inter-character gaps of up to the request timeout
// are tolerated.
//
// It is NOT safe for concurrent use from multiple goroutines.
// Concurrency safety is provided by the session layer.
type ASCII struct {
	Logger  logging.Logger
	Link    RTULink
	Timeout time.Duration
	// rxbuf holds bytes received after the end of the last frame returned by
	// readFrame (server use: a client may pipeline requests).
	rxbuf []byte
}

// NewASCII returns a new ASCII transport.
func NewASCII(link RTULink, timeout time.Duration, log logging.Logger) *ASCII {
	return &ASCII{
		Logger:  log,
		Link:    link,
		Timeout: timeout,
	}
}

// Close closes the link.
func (at *ASCII) Close() error {
	return at.Link.Close()
}

// ExecuteRequest sends req and returns the response.
func (at *ASCII) ExecuteRequest(ctx context.Context, req *adu.Request) (*adu.Response, error) {
	var deadline time.Time
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	} else {
		deadline = time.Now().Add(at.Timeout)
	}
	if err := at.Link.SetDeadline(deadline); err != nil {
		return nil, err
	}
	if 2+len(req.Payload)+1 > (adu.MaxASCIIFrameLength-3)/2 {
		return nil, protocol.ErrProtocolError
	}
	// Anything still buffered belongs to an earlier exchange (e.g. a response
	// that arrived after its request timed out): drop it.
	at.rxbuf = at.rxbuf[:0]

	frame := adu.AssembleASCIIFrame(req.UnitID, req.FunctionCode, req.Payload)
	at.Logger.Debugf("TX: %s", bytes.TrimRight(frame, "\r\n"))
	if err := writeFull(at.Link, frame); err != nil {
		return nil, err
	}

	unitID, fc, payload, err := at.readFrame(ctx, deadline)
	if err != nil {
		return nil, err
	}
	at.Logger.Debugf("RX: unit=0x%02x fc=0x%02x payload=% X", unitID, fc, payload)
	return &adu.Response{UnitID: unitID, FunctionCode: fc, Payload: payload}, nil
}

// ReadRequest reads one request from the link (server use). The transaction
// ID is always 0: Modbus ASCII has none.
func (at *ASCII) ReadRequest() (*adu.Request, uint16, error) {
	deadline := time.Now().Add(at.Timeout)
	if err := at.Link.SetDeadline(deadline); err != nil {
		return nil, 0, err
	}
	unitID, fc, payload, err := at.readFrame(context.Background(), deadline)
	if err != nil {
		return nil, 0, err
	}
	at.Logger.Debugf("RX: unit=0x%02x fc=0x%02x payload=% X", unitID, fc, payload)
	return &adu.Request{UnitID: unitID, FunctionCode: fc, Payload: payload}, 0, nil
}

// WriteResponse writes a response (server use).
func (at *ASCII) WriteResponse(res *adu.Response) error {
	frame := adu.AssembleASCIIFrame(res.UnitID, res.FunctionCode, res.Payload)
	at.Logger.Debugf("TX: %s", bytes.TrimRight(frame, "\r\n"))
	return writeFull(at.Link, frame)
}

// readFrame reads from the link until one complete frame (':' through LF) is
// available, then decodes it. Bytes preceding the start delimiter are
// discarded, and a ':' received mid-frame restarts frame assembly, as required
// by the "MODBUS over Serial Line" specification.
func (at *ASCII) readFrame(ctx context.Context, deadline time.Time) (unitID uint8, fc byte, payload []byte, err error) {
	chunk := make([]byte, adu.MaxASCIIFrameLength)
	for {
		if end := bytes.IndexByte(at.rxbuf, adu.ASCIIFrameLF); end >= 0 {
			line := at.rxbuf[:end+1]
			start := bytes.LastIndexByte(line, adu.ASCIIFrameStart)
			if start < 0 {
				// Line noise without a start delimiter: skip it.
				at.rxbuf = append(at.rxbuf[:0], at.rxbuf[end+1:]...)
				continue
			}
			unitID, fc, payload, err = adu.ParseASCIIFrame(line[start:])
			at.rxbuf = append(at.rxbuf[:0], at.rxbuf[end+1:]...)
			switch {
			case errors.Is(err, adu.ErrASCIIFrameLRC):
				return 0, 0, nil, protocol.ErrBadLRC
			case err != nil:
				return 0, 0, nil, protocol.ErrProtocolError
			}
			return unitID, fc, payload, nil
		}
		// No complete frame yet: keep only the frame being assembled.
		if start := bytes.LastIndexByte(at.rxbuf, adu.ASCIIFrameStart); start < 0 {
			at.rxbuf = at.rxbuf[:0]
		} else if start > 0 {
			at.rxbuf = append(at.rxbuf[:0], at.rxbuf[start:]...)
		}
		if len(at.rxbuf) > adu.MaxASCIIFrameLength {
			at.rxbuf = at.rxbuf[:0]
			return 0, 0, nil, protocol.ErrProtocolError
		}

		if err := ctx.Err(); err != nil {
			return 0, 0, nil, err
		}
		// Serial links return (0, nil) on their short poll timeout, so the
		// deadline is enforced here as well as by the link.
		if !time.Now().Before(deadline) {
			return 0, 0, nil, protocol.ErrRequestTimedOut
		}
		n, rerr := at.Link.Read(chunk)
		at.rxbuf = append(at.rxbuf, chunk[:n]...)
		if rerr != nil && n == 0 {
			return 0, 0, nil, rerr
		}
	}
}
