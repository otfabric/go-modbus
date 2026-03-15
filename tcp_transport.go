package modbus

import (
	"context"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	maxTCPFrameLength int = 260
	mbapHeaderLength  int = 7
	// mbapLengthMin and mbapLengthMax: MBAP length field = unit_id + function_code + payload (per Modbus spec).
	mbapLengthMin = 2
	mbapLengthMax = 254
)

type tcpTransport struct {
	logger    *logger
	socket    net.Conn
	timeout   time.Duration
	lastTxnId uint16
}

// Returns a new TCP transport.
func newTCPTransport(socket net.Conn, timeout time.Duration, l Logger) (tt *tcpTransport) {
	tt = &tcpTransport{
		socket:  socket,
		timeout: timeout,
		logger:  newLogger(fmt.Sprintf("tcp-transport(%s)", socket.RemoteAddr()), l),
	}

	return
}

// Closes the underlying tcp socket.
func (tt *tcpTransport) Close() (err error) {
	err = tt.socket.Close()

	return
}

// Runs a request across the socket and returns a response.
func (tt *tcpTransport) ExecuteRequest(ctx context.Context, req *pdu) (res *pdu, err error) {
	var frame []byte
	var deadline time.Time

	// use the context deadline if set, otherwise fall back to the configured timeout
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	} else {
		deadline = time.Now().Add(tt.timeout)
	}

	// set an i/o deadline on the socket (read and write)
	err = tt.socket.SetDeadline(deadline)
	if err != nil {
		return
	}

	// validate MBAP length before send (length = 2 + len(payload))
	if len(req.payload)+2 > mbapLengthMax {
		return nil, fmt.Errorf("%w: would be %d", ErrInvalidMBAPLength, len(req.payload)+2)
	}

	// increase the transaction ID counter
	tt.lastTxnId++

	frame = tt.assembleMBAPFrame(tt.lastTxnId, req)
	tt.logger.Debugf("TX: % X", frame)

	_, err = tt.socket.Write(frame)
	if err != nil {
		return
	}

	res, err = tt.readResponse()
	if err == nil {
		tt.logger.Debugf("RX: unit=0x%02x fc=0x%02x payload=% X",
			res.unitID, res.functionCode, res.payload)
	}

	return
}

// Reads a request from the socket.
func (tt *tcpTransport) ReadRequest() (req *pdu, err error) {
	var txnId uint16

	// set an i/o deadline on the socket (read and write)
	err = tt.socket.SetDeadline(time.Now().Add(tt.timeout))
	if err != nil {
		return
	}

	req, txnId, err = tt.readMBAPFrame()
	if err != nil {
		return
	}

	tt.logger.Debugf("RX: unit=0x%02x fc=0x%02x payload=% X",
		req.unitID, req.functionCode, req.payload)

	// store the incoming transaction id
	tt.lastTxnId = txnId

	return
}

// Writes a response to the socket.
func (tt *tcpTransport) WriteResponse(res *pdu) (err error) {
	frame := tt.assembleMBAPFrame(tt.lastTxnId, res)
	tt.logger.Debugf("TX: % X", frame)

	_, err = tt.socket.Write(frame)
	if err != nil {
		return
	}

	return
}

// Reads as many MBAP+modbus frames as necessary until either the response
// matching tt.lastTxnId is received or an error occurs.
func (tt *tcpTransport) readResponse() (res *pdu, err error) {
	var txnId uint16

	for {
		// grab a frame
		res, txnId, err = tt.readMBAPFrame()

		// ignore unknown protocol identifiers
		if err == ErrUnknownProtocolId {
			continue
		}

		// abort on any other erorr
		if err != nil {
			return
		}

		// ignore unknown transaction identifiers
		if tt.lastTxnId != txnId {
			tt.logger.Warningf("received unexpected transaction id "+
				"(expected 0x%04x, received 0x%04x)",
				tt.lastTxnId, txnId)
			continue
		}

		res.responseTransactionID = txnId
		break
	}

	return
}

// Reads an entire frame (MBAP header + modbus PDU) from the socket.
func (tt *tcpTransport) readMBAPFrame() (p *pdu, txnId uint16, err error) {
	var rxbuf []byte
	var bytesNeeded int
	var protocolId uint16
	var unitID uint8

	// read the MBAP header
	rxbuf = make([]byte, mbapHeaderLength)
	_, err = io.ReadFull(tt.socket, rxbuf)
	if err != nil {
		return
	}

	// decode the transaction identifier
	txnId = bytesToUint16(BigEndian, rxbuf[0:2])
	// decode the protocol identifier
	protocolId = bytesToUint16(BigEndian, rxbuf[2:4])
	// store the source unit id
	unitID = rxbuf[6]

	// MBAP length field = unit_id + function_code + payload (2..254 per spec)
	mbapLen := int(bytesToUint16(BigEndian, rxbuf[4:6]))
	if mbapLen < mbapLengthMin || mbapLen > mbapLengthMax {
		tt.logger.Warningf("invalid MBAP length %d (expected %d-%d)", mbapLen, mbapLengthMin, mbapLengthMax)
		err = fmt.Errorf("%w: received %d", ErrInvalidMBAPLength, mbapLen)
		return
	}

	// the byte count includes the unit ID field, which we already have
	bytesNeeded = mbapLen - 1

	// read the PDU
	rxbuf = make([]byte, bytesNeeded)
	_, err = io.ReadFull(tt.socket, rxbuf)
	if err != nil {
		return
	}

	// validate the protocol identifier
	if protocolId != 0x0000 {
		err = ErrUnknownProtocolId
		tt.logger.Warningf("received unexpected protocol id 0x%04x", protocolId)
		return
	}

	// store unit id, function code and payload in the PDU object
	p = &pdu{
		unitID:       unitID,
		functionCode: FunctionCode(rxbuf[0]),
		payload:      rxbuf[1:],
	}

	return
}

// Turns a PDU into an MBAP frame (MBAP header + PDU) and returns it as bytes.
func (tt *tcpTransport) assembleMBAPFrame(txnId uint16, p *pdu) (payload []byte) {
	// transaction identifier
	payload = uint16ToBytes(BigEndian, txnId)
	// protocol identifier (always 0x0000)
	payload = append(payload, 0x00, 0x00)
	// length (covers unit identifier + function code + payload fields)
	payload = append(payload, uint16ToBytes(BigEndian, uint16(2+len(p.payload)))...)
	// unit identifier
	payload = append(payload, p.unitID)
	// function code
	payload = append(payload, byte(p.functionCode))
	// payload
	payload = append(payload, p.payload...)

	return
}
