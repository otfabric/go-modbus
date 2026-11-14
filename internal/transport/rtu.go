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
// Rates above 11 Gbit/s round down to zero, as the integer division does.
func SerialCharTime(rateBps uint) time.Duration {
	rate := uint64(rateBps)
	if rate > uint64(11*time.Second) {
		return 0
	}
	return 11 * time.Second / time.Duration(rate)
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

// drainLink discards what the link delivers until it has been quiet for quiet,
// reading for at least minWindow and at most maxWindow. It is used before the
// request that follows a failed or suspect exchange: an answer that is still
// being received is discarded as a whole, rather than cut after a fixed window
// and its tail taken for the start of the next answer (MODBUS over Serial Line
// V1.02, 2.5.1.1: frames are delimited by a silent interval of t3.5).
//
// It returns the number of bytes discarded. A context that ends stops the
// drain before its next read; the caller looks at ctx to tell a drain that was
// cut short from one that found the link quiet.
func drainLink(ctx context.Context, link RTULink, minWindow, quiet, maxWindow time.Duration) int {
	buf := make([]byte, 1024)
	total := 0
	start := time.Now()
	maxWindow = max(maxWindow, minWindow)
	end := minWindow // since start
	for {
		elapsed := time.Since(start)
		if elapsed >= end || elapsed >= maxWindow {
			return total
		}
		if err := link.SetDeadline(start.Add(min(end, maxWindow))); err != nil {
			return total
		}
		if ctxAfterDeadline(ctx) != nil {
			return total
		}
		n, err := link.Read(buf)
		if n > 0 {
			total += n
			end = max(end, time.Since(start)+quiet)
			continue
		}
		if err != nil {
			// A timeout (the window is over) or a broken link: stop either way.
			return total
		}
		// Serial links return (0, nil) on their short poll timeout.
	}
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
	// drain is set from the moment a request is written until its answer has
	// been read: if the exchange fails or is abandoned, the answer may still
	// arrive, and must not be read as the answer to the next request. Resync
	// sets it too.
	drain bool
	// verify is set after a drain: the requests that follow first check that
	// the line is quiet, until one finds it so. An answer that arrived after
	// the drain may have been read as the answer to the next request, and that
	// request's own answer would then be waiting on the line.
	verify bool
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

// Interrupt makes the I/O of a request in flight fail at once with a timeout,
// without closing the link. It may be called from any goroutine.
func (rt *RTU) Interrupt() {
	_ = rt.Link.SetDeadline(time.Unix(1, 0))
}

// Resync makes the next request first discard what the link delivers until it
// is quiet, as after a failed exchange. The session layer calls it when a
// response did not match its request: the link may hold another answer.
func (rt *RTU) Resync() {
	rt.drain = true
}

// drainBeforeRequest discards what is left of an earlier exchange: at least
// 3 x t3.5, and until the line has been silent for t3.5, bounded by the
// request timeout, and cut short when ctx ends. It returns the number of bytes
// discarded.
func (rt *RTU) drainBeforeRequest(ctx context.Context) int {
	return drainLink(ctx, rt.Link, rt.t35*3, rt.t35, rt.Timeout)
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
	switch {
	case rt.drain:
		rt.drainBeforeRequest(ctx)
		if err := ctx.Err(); err != nil {
			// The drain was cut short: the next request starts with it again.
			return nil, err
		}
		rt.drain = false
		rt.verify = true
	case rt.verify:
		// Bytes on a line that should be quiet: the last answer read may have
		// been another request's. Keep checking until the line is found quiet.
		n := rt.drainBeforeRequest(ctx)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rt.verify = n > 0
	}
	deadline := requestDeadline(ctx, rt.Timeout)
	if err := rt.Link.SetDeadline(deadline); err != nil {
		return nil, err
	}
	if err := ctxAfterDeadline(ctx); err != nil {
		return nil, err
	}
	// Unit ID, function code, payload and CRC must fit in one frame: a request
	// that does not is refused here and nothing is written (the ASCII and
	// Modbus/TCP framers refuse it too).
	if 2+len(req.Payload)+2 > adu.MaxRTUFrameLength {
		return nil, protocol.ErrProtocolError
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
	// From here on the request may reach the device, and its answer the link:
	// every return before a complete answer has been read leaves drain set.
	rt.drain = true
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
	res, err := rt.readRTUFrame(ctx, deadline)
	switch err {
	case nil:
		rt.drain = false
		rt.Logger.Debugf("RX: unit=0x%02x fc=0x%02x payload=% X", res.UnitID, res.FunctionCode, res.Payload)
	case protocol.ErrBadCRC, protocol.ErrProtocolError, protocol.ErrShortFrame:
		// Wait out the rest of the frame, but not past the request deadline.
		if sleepCtx(ctx, min(time.Duration(adu.MaxRTUFrameLength)*rt.t1, time.Until(deadline))) == nil {
			rt.drainBeforeRequest(ctx)
			// A drain that ctx cut short is repeated before the next request.
			if ctx.Err() == nil {
				rt.drain = false
			}
		}
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

func (rt *RTU) readRTUFrame(ctx context.Context, deadline time.Time) (*adu.Response, error) {
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
			return rt.readVariableLengthResponse(ctx, deadline, rxbuf[:3])
		default:
			return nil, protocol.ErrProtocolError
		}
	}
	if bytesNeeded == protocol.RTUResponseLengthFIFO {
		if _, err = io.ReadFull(rt.Link, rxbuf[3:4]); err != nil {
			return nil, midFrameErr(err)
		}
		bytesNeeded = (int(rxbuf[2]) << 8) | int(rxbuf[3])
		if bytesNeeded < 0 || 4+bytesNeeded+2 > adu.MaxRTUFrameLength {
			return nil, protocol.ErrProtocolError
		}
		if _, err = io.ReadFull(rt.Link, rxbuf[4:4+bytesNeeded+2]); err != nil {
			return nil, midFrameErr(err)
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
	n, err := io.ReadFull(rt.Link, rxbuf[3:3+bytesNeeded])
	if err != nil {
		if err = midFrameErr(err); err == protocol.ErrShortFrame {
			rt.Logger.Warnf("expected %v bytes, received %v", bytesNeeded, n)
		}
		return nil, err
	}
	frameLen := 3 + bytesNeeded
	if !adu.ValidateRTUCRC(rxbuf[:frameLen]) {
		return nil, protocol.ErrBadCRC
	}
	unitID, fc, payload := adu.ParseRTUFrame(rxbuf[:frameLen])
	return &adu.Response{UnitID: unitID, FunctionCode: fc, Payload: payload}, nil
}

// midFrameErr maps the end of the stream inside a frame, after its header was received, to
// ErrShortFrame: the response was cut short. Other errors (timeouts in particular) pass through.
func midFrameErr(err error) error {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return protocol.ErrShortFrame
	}
	return err
}

// readVariableLengthResponse reads an RTU response whose length is not known
// from the header alone (FC08 diagnostics, FC2B encapsulated interface).
// It reads byte-by-byte until an inter-frame silence (t3.5) is detected, then
// validates CRC and parses the frame. A device that trickles bytes cannot keep
// it reading past the request deadline or a cancelled ctx.
func (rt *RTU) readVariableLengthResponse(ctx context.Context, deadline time.Time, header []byte) (*adu.Response, error) {
	const minFrameBytes = 6
	buf := make([]byte, adu.MaxRTUFrameLength)
	copy(buf, header)
	offset := len(header)
	for offset < adu.MaxRTUFrameLength {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		byteDeadline := time.Now().Add(rt.t35)
		if byteDeadline.After(deadline) {
			byteDeadline = deadline
		}
		if err := rt.Link.SetDeadline(byteDeadline); err != nil {
			return nil, err
		}
		n, readErr := rt.Link.Read(buf[offset : offset+1])
		if n > 0 {
			offset += n
			continue
		}
		if readErr != nil && os.IsTimeout(readErr) {
			if !time.Now().Before(deadline) {
				return nil, protocol.ErrRequestTimedOut
			}
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
