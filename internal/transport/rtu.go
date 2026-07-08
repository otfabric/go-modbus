// SPDX-License-Identifier: MIT

package transport

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/logging"
	"github.com/otfabric/go-modbus/internal/protocol"
)

// RTULink is the I/O interface for RTU (e.g. serial port or UDP adapter).
type RTULink interface {
	Close() error
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	SetDeadline(time.Time) error
}

// SerialCharTime returns the duration to send one byte at the given baud rate (11 bits per byte).
func SerialCharTime(rateBps uint) time.Duration {
	return 11 * time.Second / time.Duration(rateBps)
}

// discardLink performs a best-effort flush of stale bytes on the link by
// issuing a single bounded read with a short deadline derived from t35.
// This is not an iterative drain — it reads up to 1 KiB and discards the
// result. For typical Modbus frames (max ~256 bytes) this is sufficient.
func discardLink(link RTULink, drainTimeout time.Duration) {
	buf := make([]byte, 1024)
	_ = link.SetDeadline(time.Now().Add(drainTimeout))
	_, _ = io.ReadFull(link, buf)
}

// RTU implements Transport over an RTU (serial) link.
// It is NOT safe for concurrent use from multiple goroutines.
// Concurrency safety is provided by the session layer: Engine.execMu
// serializes all calls in single-transport mode.
type RTU struct {
	Logger       logging.Logger
	Link         RTULink
	Timeout      time.Duration
	lastActivity time.Time
	t35          time.Duration
	t1           time.Duration
}

// NewRTU returns a new RTU transport.
func NewRTU(link RTULink, addr string, speed uint, timeout time.Duration, log logging.Logger) *RTU {
	t1 := SerialCharTime(speed)
	var t35 time.Duration
	if speed >= 19200 {
		t35 = 1750 * time.Microsecond
	} else {
		t35 = (SerialCharTime(speed) * 35) / 10
	}
	rt := &RTU{
		Logger:  log,
		Link:    link,
		Timeout: timeout,
		t1:      t1,
		t35:     t35,
	}
	discardLink(rt.Link, t35*3)
	return rt
}

// Close closes the link.
func (rt *RTU) Close() error {
	return rt.Link.Close()
}

// sleepCtx sleeps for d, returning early if ctx is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		t.Stop()
		return ctx.Err()
	}
}

// ExecuteRequest sends req and returns the response.
func (rt *RTU) ExecuteRequest(ctx context.Context, req *adu.Request) (*adu.Response, error) {
	var deadline time.Time
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	} else {
		deadline = time.Now().Add(rt.Timeout)
	}
	if err := rt.Link.SetDeadline(deadline); err != nil {
		return nil, err
	}
	gap := time.Until(rt.lastActivity.Add(rt.t35))
	if gap > 0 {
		if err := sleepCtx(ctx, gap); err != nil {
			return nil, err
		}
	}
	ts := time.Now()
	frame := adu.AssembleRTUFrame(req.UnitID, req.FunctionCode, req.Payload)
	rt.Logger.Debugf("TX: % X", frame)
	if err := writeFull(rt.Link, frame); err != nil {
		return nil, err
	}
	rt.lastActivity = ts.Add(time.Duration(len(frame)) * rt.t1)
	postTxGap := time.Until(rt.lastActivity.Add(rt.t35))
	if postTxGap > 0 {
		if err := sleepCtx(ctx, postTxGap); err != nil {
			return nil, err
		}
	}
	res, err := rt.readRTUFrame()
	if err == nil {
		rt.Logger.Debugf("RX: unit=0x%02x fc=0x%02x payload=% X", res.UnitID, res.FunctionCode, res.Payload)
	}
	if err == protocol.ErrBadCRC || err == protocol.ErrProtocolError || err == protocol.ErrShortFrame {
		_ = sleepCtx(ctx, time.Duration(adu.MaxRTUFrameLength)*rt.t1)
		discardLink(rt.Link, rt.t35*3)
	}
	if err != protocol.ErrRequestTimedOut {
		rt.lastActivity = time.Now()
	}
	return res, err
}

// ReadRequest is not supported for RTU client; returns unimplemented error.
func (rt *RTU) ReadRequest() (*adu.Request, uint16, error) {
	return nil, 0, fmt.Errorf("unimplemented")
}

// WriteResponse writes a response (server use).
func (rt *RTU) WriteResponse(res *adu.Response) error {
	frame := adu.AssembleRTUFrame(res.UnitID, res.FunctionCode, res.Payload)
	rt.Logger.Debugf("TX: % X", frame)
	if err := writeFull(rt.Link, frame); err != nil {
		return err
	}
	rt.lastActivity = time.Now().Add(rt.t1 * time.Duration(len(frame)))
	return nil
}

func (rt *RTU) readRTUFrame() (*adu.Response, error) {
	rxbuf := make([]byte, adu.MaxRTUFrameLength)
	byteCount, err := io.ReadFull(rt.Link, rxbuf[0:3])
	if (byteCount > 0 || err == nil) && byteCount != 3 {
		return nil, protocol.ErrShortFrame
	}
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	bytesNeeded, err := protocol.ExpectedRTUResponseLength(protocol.FunctionCode(rxbuf[1]), rxbuf[2])
	if err != nil {
		return nil, err
	}
	if bytesNeeded == protocol.RTUResponseLengthVariable {
		switch protocol.FunctionCode(rxbuf[1]) {
		case protocol.FCDiagnostics, protocol.FCEncapsulatedInterface:
			return rt.readVariableLengthResponse(rxbuf[:3])
		default:
			return nil, protocol.ErrProtocolError
		}
	}
	if bytesNeeded == protocol.RTUResponseLengthFIFO {
		byteCount, err = io.ReadFull(rt.Link, rxbuf[3:4])
		if (byteCount > 0 || err == nil) && byteCount != 1 {
			return nil, protocol.ErrShortFrame
		}
		if err != nil && err != io.ErrUnexpectedEOF {
			return nil, err
		}
		bytesNeeded = (int(rxbuf[2]) << 8) | int(rxbuf[3])
		if bytesNeeded < 0 || 4+bytesNeeded+2 > adu.MaxRTUFrameLength {
			return nil, protocol.ErrProtocolError
		}
		byteCount, err = io.ReadFull(rt.Link, rxbuf[4:4+bytesNeeded+2])
		if err != nil && err != io.ErrUnexpectedEOF {
			return nil, err
		}
		if byteCount != bytesNeeded+2 {
			return nil, protocol.ErrShortFrame
		}
		frameLen := 4 + bytesNeeded + 2
		if !adu.ValidateRTUCRC(rxbuf[:frameLen]) {
			return nil, protocol.ErrBadCRC
		}
		unitID, fc, payload := adu.ParseRTUFrame(rxbuf[:frameLen])
		return &adu.Response{UnitID: unitID, FunctionCode: fc, Payload: payload}, nil
	}
	bytesNeeded += 2
	if byteCount+bytesNeeded > adu.MaxRTUFrameLength {
		return nil, protocol.ErrProtocolError
	}
	byteCount, err = io.ReadFull(rt.Link, rxbuf[3:3+bytesNeeded])
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	if byteCount != bytesNeeded {
		rt.Logger.Warnf("expected %v bytes, received %v", bytesNeeded, byteCount)
		return nil, protocol.ErrShortFrame
	}
	frameLen := 3 + bytesNeeded
	if !adu.ValidateRTUCRC(rxbuf[:frameLen]) {
		return nil, protocol.ErrBadCRC
	}
	unitID, fc, payload := adu.ParseRTUFrame(rxbuf[:frameLen])
	return &adu.Response{UnitID: unitID, FunctionCode: fc, Payload: payload}, nil
}

// readVariableLengthResponse reads an RTU response whose length is not known
// from the header alone (FC08 diagnostics, FC2B encapsulated interface).
// It reads byte-by-byte until an inter-frame silence (t3.5) is detected, then
// validates CRC and parses the frame.
func (rt *RTU) readVariableLengthResponse(header []byte) (*adu.Response, error) {
	const minFrameBytes = 6
	buf := make([]byte, adu.MaxRTUFrameLength)
	copy(buf, header)
	offset := len(header)
	for offset < adu.MaxRTUFrameLength {
		if err := rt.Link.SetDeadline(time.Now().Add(rt.t35)); err != nil {
			return nil, err
		}
		n, readErr := rt.Link.Read(buf[offset : offset+1])
		if n > 0 {
			offset += n
			continue
		}
		if readErr != nil && os.IsTimeout(readErr) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
		break
	}
	if offset < minFrameBytes {
		return nil, protocol.ErrShortFrame
	}
	if !adu.ValidateRTUCRC(buf[:offset]) {
		return nil, protocol.ErrBadCRC
	}
	unitID, fc, payload := adu.ParseRTUFrame(buf[:offset])
	return &adu.Response{UnitID: unitID, FunctionCode: fc, Payload: payload}, nil
}
