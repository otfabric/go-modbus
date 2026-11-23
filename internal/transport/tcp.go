// SPDX-License-Identifier: MIT

package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/logging"
	"github.com/otfabric/go-modbus/internal/protocol"
)

func discardBytes(r io.Reader, n int) error {
	buf := make([]byte, n)
	_, err := io.ReadFull(r, buf)
	return err
}

// TCP implements Transport over a Modbus/TCP (MBAP) connection.
// It is NOT safe for concurrent use from multiple goroutines.
// Concurrency safety is provided by the session layer: in single-transport
// mode, Engine.execMu serializes all calls; in pool mode, each goroutine
// gets its own TCP instance from the pool.
type TCP struct {
	Logger  logging.Logger
	Socket  net.Conn
	Timeout time.Duration
	// RequestPDULength, when set (server use), returns the length of the
	// request PDU, function code included, of a function code whose requests
	// have a fixed size, and 0 for any other. ReadFrame then checks the MBAP
	// length of such a request as soon as it has read the function code, and
	// reads no further when the two disagree (see RequestLengthError).
	RequestPDULength func(fc byte) int
	// OnFrameStart, when set (server use), is called by ReadFrame as soon as
	// the first byte of a request has been read, before it reads the rest.
	OnFrameStart func()
	// OnAnomaly, when set (client use), receives the frames readResponse
	// skips: another transaction ID, a protocol ID other than 0.
	OnAnomaly AnomalyFunc
	lastTxnID uint16
	// badHeader is the last MBAP header that could not be framed (an invalid
	// length or a protocol ID other than 0), hasBadHeader whether there is one.
	badHeader    [adu.MBAPHeaderLength]byte
	hasBadHeader bool
}

// LastBadHeader returns the last MBAP header the transport read and could not
// frame (ErrInvalidMBAPLength, ErrUnknownProtocolID), and whether there was
// one. The caller must not use it concurrently with ReadFrame.
func (tt *TCP) LastBadHeader() ([adu.MBAPHeaderLength]byte, bool) {
	return tt.badHeader, tt.hasBadHeader
}

// RequestLengthError is returned by ReadFrame, together with the request's
// unit ID and function code and an empty payload, when the MBAP length of a
// request disagrees with the fixed length RequestPDULength gives for its
// function code. The rest of the frame is not read: the stream is out of step
// (the MBAP length is wrong, or the PDU is), and reading what the MBAP length
// announces would wait for, and take, bytes of the next request.
type RequestLengthError struct {
	// Want is the PDU length of the function code, Have the one the MBAP
	// length announces (both with the function code).
	Want, Have int
}

func (e *RequestLengthError) Error() string {
	return fmt.Sprintf("MBAP length announces a request PDU of %d bytes, the function code's is %d", e.Have, e.Want)
}

// NewTCP returns a new TCP transport.
func NewTCP(socket net.Conn, timeout time.Duration, log logging.Logger) *TCP {
	return &TCP{
		Logger:  log,
		Socket:  socket,
		Timeout: timeout,
	}
}

// Close closes the socket.
func (tt *TCP) Close() error {
	return tt.Socket.Close()
}

// ExecuteRequest sends req and returns the response.
func (tt *TCP) ExecuteRequest(ctx context.Context, req *adu.Request) (*adu.Response, error) {
	deadline := requestDeadline(ctx, tt.Timeout)
	if err := tt.Socket.SetDeadline(deadline); err != nil {
		return nil, err
	}
	if err := ctxAfterDeadline(ctx); err != nil {
		return nil, err
	}
	if len(req.Payload)+2 > adu.MBAPLengthMax {
		return nil, fmt.Errorf("%w: would be %d", protocol.ErrInvalidMBAPLength, len(req.Payload)+2)
	}
	tt.lastTxnID++
	frame := adu.AssembleMBAP(tt.lastTxnID, req.UnitID, req.FunctionCode, req.Payload)
	tt.Logger.Debugf("TX: % X", frame)
	if err := writeFull(tt.Socket, frame); err != nil {
		return nil, err
	}
	res, err := tt.readResponse(req)
	if err == nil {
		tt.Logger.Debugf("RX: unit=0x%02x fc=0x%02x payload=% X", res.UnitID, res.FunctionCode, res.Payload)
	}
	return res, err
}

// Interrupt makes the I/O of a request in flight fail at once with a timeout,
// without closing the socket. It may be called from any goroutine. A stream
// interrupted in the middle of an exchange is out of sync and must not be reused.
func (tt *TCP) Interrupt() {
	_ = tt.Socket.SetDeadline(time.Unix(1, 0))
}

// ReadRequest reads one request from the socket (server use). Timeout bounds the
// wait for the request only; it does not carry over to WriteResponse.
func (tt *TCP) ReadRequest() (*adu.Request, uint16, error) {
	if err := tt.Socket.SetReadDeadline(time.Now().Add(tt.Timeout)); err != nil {
		return nil, 0, err
	}
	req, txnID, err := tt.ReadFrame()
	if err != nil {
		return nil, 0, err
	}
	tt.lastTxnID = txnID
	return req, txnID, nil
}

// WriteResponse writes a response to the request last returned by ReadRequest
// (server use). The write gets its own deadline of Timeout.
func (tt *TCP) WriteResponse(res *adu.Response) error {
	return tt.WriteFrame(tt.lastTxnID, res)
}

// ReadFrame reads one request from the socket without touching deadlines or the
// transport's transaction state: the caller manages the read deadline (see
// SetReadDeadline). Together with WriteFrame it lets a server read requests in
// one goroutine while it answers earlier ones in another.
func (tt *TCP) ReadFrame() (*adu.Request, uint16, error) {
	req, txnID, err := tt.readMBAPFrameRaw()
	if err != nil {
		var lenErr *RequestLengthError
		if errors.As(err, &lenErr) {
			tt.Logger.Debugf("RX: unit=0x%02x fc=0x%02x: %v", req.UnitID, req.FunctionCode, err)
			return req, txnID, err
		}
		return nil, 0, err
	}
	tt.Logger.Debugf("RX: unit=0x%02x fc=0x%02x payload=% X", req.UnitID, req.FunctionCode, req.Payload)
	return req, txnID, nil
}

// WriteFrame writes res under transaction ID txnID, with a write deadline of
// Timeout. It does not use the transport's transaction state and may run
// concurrently with ReadFrame.
func (tt *TCP) WriteFrame(txnID uint16, res *adu.Response) error {
	frame := adu.AssembleMBAP(txnID, res.UnitID, res.FunctionCode, res.Payload)
	tt.Logger.Debugf("TX: % X", frame)
	if err := tt.Socket.SetWriteDeadline(time.Now().Add(tt.Timeout)); err != nil {
		return err
	}
	return writeFull(tt.Socket, frame)
}

// SetReadDeadline sets the deadline of pending and future reads (zero: none).
func (tt *TCP) SetReadDeadline(t time.Time) error {
	return tt.Socket.SetReadDeadline(t)
}

// readHeader reads an MBAP header into header. With OnFrameStart set, that
// is called once the first byte has arrived, and the rest is read after it.
func (tt *TCP) readHeader(header []byte) error {
	if tt.OnFrameStart == nil {
		_, err := io.ReadFull(tt.Socket, header)
		return err
	}
	n := 0
	for n == 0 {
		m, err := tt.Socket.Read(header)
		n = m
		if err != nil && n == 0 {
			return err
		}
		if err != nil {
			break
		}
	}
	tt.OnFrameStart()
	if _, err := io.ReadFull(tt.Socket, header[n:]); err != nil {
		if err == io.EOF {
			// The client left in the middle of the header.
			return io.ErrUnexpectedEOF
		}
		return err
	}
	return nil
}

const maxAnomalies = 10

func (tt *TCP) readResponse(req *adu.Request) (*adu.Response, error) {
	txnMismatches := 0
	protoMismatches := 0
	for {
		res, txnID, err := tt.readMBAPFrame()
		if err != nil {
			if errors.Is(err, protocol.ErrUnknownProtocolID) {
				protoMismatches++
				tt.Logger.Warnf("received unexpected protocol ID (count %d/%d)", protoMismatches, maxAnomalies)
				if tt.OnAnomaly != nil {
					tt.OnAnomaly(req, Anomaly{
						Kind:          AnomalyProtocolID,
						Expected:      tt.lastTxnID,
						TransactionID: adu.BytesToUint16(adu.BigEndian, tt.badHeader[0:2]),
						ProtocolID:    adu.BytesToUint16(adu.BigEndian, tt.badHeader[2:4]),
					})
				}
				if txnMismatches+protoMismatches >= maxAnomalies {
					return nil, protocol.ErrUnknownProtocolID
				}
				continue
			}
			return nil, err
		}
		if tt.lastTxnID != txnID {
			txnMismatches++
			tt.Logger.Warnf("received unexpected transaction id (expected 0x%04x, got 0x%04x, count %d/%d)",
				tt.lastTxnID, txnID, txnMismatches, maxAnomalies)
			if tt.OnAnomaly != nil {
				tt.OnAnomaly(req, Anomaly{Kind: AnomalyTransactionID, Expected: tt.lastTxnID, TransactionID: txnID})
			}
			if txnMismatches+protoMismatches >= maxAnomalies {
				return nil, protocol.ErrBadTransactionID
			}
			continue
		}
		res.TransactionID = txnID
		return res, nil
	}
}

func (tt *TCP) readMBAPFrame() (*adu.Response, uint16, error) {
	req, txnID, err := tt.readMBAPFrameRaw()
	if err != nil {
		return nil, 0, err
	}
	return &adu.Response{
		UnitID:        req.UnitID,
		FunctionCode:  req.FunctionCode,
		Payload:       req.Payload,
		TransactionID: txnID,
	}, txnID, nil
}

func (tt *TCP) readMBAPFrameRaw() (*adu.Request, uint16, error) {
	header := make([]byte, adu.MBAPHeaderLength)
	if err := tt.readHeader(header); err != nil {
		return nil, 0, err
	}
	txnID, unitID, mbapLen, err := adu.ParseMBAPHeader(header)
	if err != nil {
		tt.badHeader = [adu.MBAPHeaderLength]byte(header)
		tt.hasBadHeader = true
		if errors.Is(err, adu.ErrInvalidMBAPLength) {
			tt.Logger.Warnf("invalid MBAP length (expected %d-%d)", adu.MBAPLengthMin, adu.MBAPLengthMax)
			return nil, 0, protocol.ErrInvalidMBAPLength
		}
		if errors.Is(err, adu.ErrUnknownProtocolID) {
			tt.Logger.Warnf("received unexpected protocol id")
			if len(header) >= 6 {
				bodyLen := int(adu.BytesToUint16(adu.BigEndian, header[4:6]))
				if bodyLen >= adu.MBAPLengthMin && bodyLen <= adu.MBAPLengthMax {
					_ = discardBytes(tt.Socket, bodyLen-1)
				}
			}
			return nil, 0, protocol.ErrUnknownProtocolID
		}
		return nil, 0, err
	}
	body := make([]byte, mbapLen-1)
	if tt.RequestPDULength != nil {
		// The function code first: a request of a fixed size whose MBAP
		// length disagrees is reported without reading what that length
		// announces.
		if _, err := io.ReadFull(tt.Socket, body[:1]); err != nil {
			return nil, 0, err
		}
		if want := tt.RequestPDULength(body[0]); want > 0 && want != len(body) {
			return &adu.Request{UnitID: unitID, FunctionCode: body[0]}, txnID,
				&RequestLengthError{Want: want, Have: len(body)}
		}
		if _, err := io.ReadFull(tt.Socket, body[1:]); err != nil {
			return nil, 0, err
		}
		return &adu.Request{
			UnitID:       unitID,
			FunctionCode: body[0],
			Payload:      body[1:],
		}, txnID, nil
	}
	if _, err := io.ReadFull(tt.Socket, body); err != nil {
		return nil, 0, err
	}
	return &adu.Request{
		UnitID:       unitID,
		FunctionCode: body[0],
		Payload:      body[1:],
	}, txnID, nil
}
