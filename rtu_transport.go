package modbus

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

const (
	maxRTUFrameLength int = 256
	// rtuResponseLengthVariable: read until inter-frame silence (FC08, FC2B).
	rtuResponseLengthVariable int = -1
	// rtuResponseLengthFIFO: FC18 uses 2-byte byte count; read one more byte then use it.
	rtuResponseLengthFIFO int = -2
)

type rtuTransport struct {
	logger       *logger
	link         rtuLink
	timeout      time.Duration
	lastActivity time.Time
	t35          time.Duration
	t1           time.Duration
}

type rtuLink interface {
	Close() error
	Read([]byte) (int, error)
	Write([]byte) (int, error)
	SetDeadline(time.Time) error
}

// Returns a new RTU transport.
func newRTUTransport(link rtuLink, addr string, speed uint, timeout time.Duration, l Logger) (rt *rtuTransport) {
	rt = &rtuTransport{
		logger:  newLogger(fmt.Sprintf("rtu-transport(%s)", addr), l),
		link:    link,
		timeout: timeout,
		t1:      serialCharTime(speed),
	}

	if speed >= 19200 {
		// for baud rates equal to or greater than 19200 bauds, a fixed value of
		// 1750 uS is specified for t3.5.
		rt.t35 = 1750 * time.Microsecond
	} else {
		// for lower baud rates, the inter-frame delay should be 3.5 character times
		rt.t35 = (serialCharTime(speed) * 35) / 10
	}

	discard(rt.link)

	return
}

// Closes the rtu link.
func (rt *rtuTransport) Close() (err error) {
	err = rt.link.Close()

	return
}

// Runs a request across the rtu link and returns a response.
func (rt *rtuTransport) ExecuteRequest(ctx context.Context, req *pdu) (res *pdu, err error) {
	var ts time.Time
	var t time.Duration
	var n int
	var deadline time.Time

	// use the context deadline if set, otherwise fall back to the configured timeout
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	} else {
		deadline = time.Now().Add(rt.timeout)
	}

	// set an i/o deadline on the link
	err = rt.link.SetDeadline(deadline)
	if err != nil {
		return
	}

	// if the line was active less than 3.5 char times ago,
	// let t3.5 expire before transmitting
	t = time.Since(rt.lastActivity.Add(rt.t35))
	if t < 0 {
		time.Sleep(t * (-1))
	}

	ts = time.Now()

	// build an RTU ADU out of the request object and
	// send the final ADU+CRC on the wire
	frame := rt.assembleRTUFrame(req)
	rt.logger.Debugf("TX: % X", frame)
	n, err = rt.link.Write(frame)
	if err != nil {
		return
	}

	// estimate how long the serial line was busy for.
	// note that on most platforms, Write() will be buffered and return
	// immediately rather than block until the buffer is drained
	rt.lastActivity = ts.Add(time.Duration(n) * rt.t1)

	// observe inter-frame delays
	time.Sleep(time.Until(rt.lastActivity.Add(rt.t35)))

	// read the response back from the wire
	res, err = rt.readRTUFrame()
	if err == nil {
		rt.logger.Debugf("RX: unit=0x%02x fc=0x%02x payload=% X",
			res.unitID, res.functionCode, res.payload)
	}

	if err == ErrBadCRC || err == ErrProtocolError || err == ErrShortFrame {
		// wait for and flush any data coming off the link to allow
		// devices to re-sync
		time.Sleep(time.Duration(maxRTUFrameLength) * rt.t1)
		discard(rt.link)
	}

	// mark the time if we heard anything back
	if err != ErrRequestTimedOut {
		rt.lastActivity = time.Now()
	}

	return
}

// Reads a request from the rtu link.
func (rt *rtuTransport) ReadRequest() (req *pdu, err error) {
	// reading requests from RTU links is currently unsupported
	err = fmt.Errorf("unimplemented")

	return
}

// Writes a response to the rtu link.
func (rt *rtuTransport) WriteResponse(res *pdu) (err error) {
	var n int

	// build an RTU ADU out of the request object and
	// send the final ADU+CRC on the wire
	frame := rt.assembleRTUFrame(res)
	rt.logger.Debugf("TX: % X", frame)
	n, err = rt.link.Write(frame)
	if err != nil {
		return
	}

	rt.lastActivity = time.Now().Add(rt.t1 * time.Duration(n))

	return
}

// Waits for, reads and decodes a frame from the rtu link.
func (rt *rtuTransport) readRTUFrame() (res *pdu, err error) {
	var rxbuf []byte
	var byteCount int
	var bytesNeeded int
	var crc crc

	rxbuf = make([]byte, maxRTUFrameLength)

	// read the serial ADU header: unit id (1 byte), function code (1 byte) and
	// PDU length/exception code (1 byte)
	byteCount, err = io.ReadFull(rt.link, rxbuf[0:3])
	if (byteCount > 0 || err == nil) && byteCount != 3 {
		err = ErrShortFrame
		return
	}
	if err != nil && err != io.ErrUnexpectedEOF {
		return
	}

	// figure out how many further bytes to read
	bytesNeeded, err = expectedResponseLength(FunctionCode(rxbuf[1]), uint8(rxbuf[2]))
	if err != nil {
		return
	}

	// Variable-length responses: read until inter-frame silence (t3.5)
	if bytesNeeded == rtuResponseLengthVariable {
		switch FunctionCode(rxbuf[1]) {
		case FCDiagnostics:
			return rt.readVariableLengthDiagnostics(rxbuf[:3])
		case FCEncapsulatedInterface:
			return rt.readVariableLengthEncapsulated(rxbuf[:3])
		default:
			err = ErrProtocolError
			return
		}
	}

	// FC18: byte count is 2 bytes; we have only the first. Read one more then the rest.
	if bytesNeeded == rtuResponseLengthFIFO {
		byteCount, err = io.ReadFull(rt.link, rxbuf[3:4])
		if (byteCount > 0 || err == nil) && byteCount != 1 {
			err = ErrShortFrame
			return
		}
		if err != nil && err != io.ErrUnexpectedEOF {
			return
		}
		bytesNeeded = (int(rxbuf[2]) << 8) | int(rxbuf[3])
		if bytesNeeded < 0 || 4+bytesNeeded+2 > maxRTUFrameLength {
			err = ErrProtocolError
			return
		}
		byteCount, err = io.ReadFull(rt.link, rxbuf[4:4+bytesNeeded+2])
		if err != nil && err != io.ErrUnexpectedEOF {
			return
		}
		if byteCount != bytesNeeded+2 {
			err = ErrShortFrame
			return
		}
		crc.init()
		crc.add(rxbuf[0 : 4+bytesNeeded])
		if !crc.isEqual(rxbuf[4+bytesNeeded], rxbuf[4+bytesNeeded+1]) {
			err = ErrBadCRC
			return
		}
		res = &pdu{
			unitID:       rxbuf[0],
			functionCode: FunctionCode(rxbuf[1]),
			payload:      rxbuf[2 : 4+bytesNeeded],
		}
		return
	}

	// we need to read 2 additional bytes of CRC after the payload
	bytesNeeded += 2

	// never read more than the max allowed frame length
	if byteCount+bytesNeeded > maxRTUFrameLength {
		err = ErrProtocolError
		return
	}

	byteCount, err = io.ReadFull(rt.link, rxbuf[3:3+bytesNeeded])
	if err != nil && err != io.ErrUnexpectedEOF {
		return
	}
	if byteCount != bytesNeeded {
		rt.logger.Warningf("expected %v bytes, received %v", bytesNeeded, byteCount)
		err = ErrShortFrame
		return
	}

	// compute the CRC on the entire frame, excluding the CRC
	crc.init()
	crc.add(rxbuf[0 : 3+bytesNeeded-2])

	// compare CRC values
	if !crc.isEqual(rxbuf[3+bytesNeeded-2], rxbuf[3+bytesNeeded-1]) {
		err = ErrBadCRC
		return
	}

	res = &pdu{
		unitID:       rxbuf[0],
		functionCode: FunctionCode(rxbuf[1]),
		// pass the byte count + trailing data as payload, withtout the CRC
		payload: rxbuf[2 : 3+bytesNeeded-2],
	}

	return
}

// readVariableLengthDiagnostics reads an FC08 Diagnostics response, which has no
// byte-count field. It reads until inter-frame silence (t3.5) or the buffer is full.
// header is the first 3 bytes already read: unitID, functionCode (0x08), subFuncHi.
func (rt *rtuTransport) readVariableLengthDiagnostics(header []byte) (res *pdu, err error) {
	const minFrameBytes = 6 // unitID(1) + fc(1) + subFunc(2) + crc(2)
	var crc crc

	buf := make([]byte, maxRTUFrameLength)
	copy(buf, header)
	offset := len(header)

	for offset < maxRTUFrameLength {
		err = rt.link.SetDeadline(time.Now().Add(rt.t35))
		if err != nil {
			return
		}
		n, readErr := rt.link.Read(buf[offset : offset+1])
		if n > 0 {
			offset += n
			continue
		}
		if readErr != nil && os.IsTimeout(readErr) {
			break
		}
		if readErr != nil {
			err = readErr
			return
		}
		break
	}

	if offset < minFrameBytes {
		err = ErrShortFrame
		return
	}

	crc.init()
	crc.add(buf[:offset-2])
	if !crc.isEqual(buf[offset-2], buf[offset-1]) {
		err = ErrBadCRC
		return
	}

	res = &pdu{
		unitID:       buf[0],
		functionCode: FunctionCode(buf[1]),
		payload:      append([]byte(nil), buf[2:offset-2]...),
	}
	return
}

// readVariableLengthEncapsulated reads an FC2B Encapsulated Interface (e.g. Read Device ID)
// response. It has no byte-count field; the first payload byte is MEI type. Reads until
// inter-frame silence (t3.5) or the buffer is full.
func (rt *rtuTransport) readVariableLengthEncapsulated(header []byte) (res *pdu, err error) {
	const minFrameBytes = 6 // unitID(1) + fc(1) + MEI(1) + at least 1 data byte + crc(2)
	var crc crc

	buf := make([]byte, maxRTUFrameLength)
	copy(buf, header)
	offset := len(header)

	for offset < maxRTUFrameLength {
		err = rt.link.SetDeadline(time.Now().Add(rt.t35))
		if err != nil {
			return
		}
		n, readErr := rt.link.Read(buf[offset : offset+1])
		if n > 0 {
			offset += n
			continue
		}
		if readErr != nil && os.IsTimeout(readErr) {
			break
		}
		if readErr != nil {
			err = readErr
			return
		}
		break
	}

	if offset < minFrameBytes {
		err = ErrShortFrame
		return
	}

	crc.init()
	crc.add(buf[:offset-2])
	if !crc.isEqual(buf[offset-2], buf[offset-1]) {
		err = ErrBadCRC
		return
	}

	res = &pdu{
		unitID:       buf[0],
		functionCode: FunctionCode(buf[1]),
		payload:      append([]byte(nil), buf[2:offset-2]...),
	}
	return
}

// Turns a PDU object into bytes.
func (rt *rtuTransport) assembleRTUFrame(p *pdu) (adu []byte) {
	var crc crc

	adu = append(adu, p.unitID)
	adu = append(adu, byte(p.functionCode))
	adu = append(adu, p.payload...)

	// run the ADU through the CRC generator
	crc.init()
	crc.add(adu)

	// append the CRC to the ADU
	adu = append(adu, crc.value()...)

	return
}

// Computes the expected length of a Modbus RTU response.
// For normal responses, the returned int is the number of payload bytes still to read
// after the first 3 bytes (unitID, functionCode, first payload byte).
// Return -1 for variable-length responses (FC08 Diagnostics); the caller must use
// readVariableLengthDiagnostics.
//
// PDU length rules (aligned with Modbus application protocol / dissector heuristics):
// - FC03/04 response: byte_count(1) + 2*quantity; byte_count = 2*quantity.
// - FC01/02 response: byte_count(1) + ceil(quantity/8); FC05/06/0F/10/16: 5 bytes (echo).
// - Exception response: 1 byte (exception code). FC08: variable (no byte_count field).
func expectedResponseLength(responseCode FunctionCode, responseLength uint8) (int, error) {
	switch responseCode {
	case FCReadHoldingRegisters,
		FCReadInputRegisters,
		FCReadCoils,
		FCReadDiscreteInputs:
		return int(responseLength), nil
	case FCWriteSingleRegister,
		FCWriteMultipleRegisters,
		FCWriteSingleCoil,
		FCWriteMultipleCoils:
		return 3, nil
	case FCMaskWriteRegister:
		return 5, nil
	case FCReadFileRecord,
		FCWriteFileRecord,
		FCReadWriteMultipleRegs:
		// First payload byte is byte count.
		return int(responseLength), nil
	case FCReadFIFOQueue:
		// Byte count is 2 bytes (big-endian); we only have the first. Caller reads one more then uses it.
		return rtuResponseLengthFIFO, nil
	case FCDiagnostics:
		// Variable-length PDU — no byte-count field.
		return rtuResponseLengthVariable, nil
	case FCEncapsulatedInterface:
		// MEI type (first payload byte) is not length; variable-length like FC08.
		return rtuResponseLengthVariable, nil
	case FCReportServerID:
		return int(responseLength), nil
	case FCReadExceptionStatus:
		// Response: unit_id + fc + 1 byte (exception status). First payload byte is the status.
		return 0, nil
	case FunctionCode(0x80 | uint8(FCReadHoldingRegisters)),
		FunctionCode(0x80 | uint8(FCReadInputRegisters)),
		FunctionCode(0x80 | uint8(FCReadCoils)),
		FunctionCode(0x80 | uint8(FCReadDiscreteInputs)),
		FunctionCode(0x80 | uint8(FCWriteSingleRegister)),
		FunctionCode(0x80 | uint8(FCWriteMultipleRegisters)),
		FunctionCode(0x80 | uint8(FCWriteSingleCoil)),
		FunctionCode(0x80 | uint8(FCWriteMultipleCoils)),
		FunctionCode(0x80 | uint8(FCMaskWriteRegister)),
		FunctionCode(0x80 | uint8(FCDiagnostics)),
		FunctionCode(0x80 | uint8(FCReportServerID)),
		FunctionCode(0x80 | uint8(FCReadExceptionStatus)),
		FunctionCode(0x80 | uint8(FCReadFileRecord)),
		FunctionCode(0x80 | uint8(FCWriteFileRecord)),
		FunctionCode(0x80 | uint8(FCReadWriteMultipleRegs)),
		FunctionCode(0x80 | uint8(FCReadFIFOQueue)),
		FunctionCode(0x80 | uint8(FCEncapsulatedInterface)):
		return 0, nil
	default:
		return 0, ErrProtocolError
	}
}

// Discards the contents of the link's rx buffer, eating up to 1kB of data.
// Note that on a serial line, this call may block for up to serialConf.Timeout
// i.e. 10ms.
func discard(link rtuLink) {
	var rxbuf = make([]byte, 1024)

	_ = link.SetDeadline(time.Now().Add(500 * time.Microsecond))
	_, _ = io.ReadFull(link, rxbuf)
}

// Returns how long it takes to send 1 byte on a serial line at the
// specified baud rate.
func serialCharTime(rate_bps uint) (ct time.Duration) {
	// note: an RTU byte on the wire is:
	// - 1 start bit,
	// - 8 data bits,
	// - 1 parity or stop bit
	// - 1 stop bit
	ct = (11) * time.Second / time.Duration(rate_bps)

	return
}
