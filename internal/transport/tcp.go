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
	Logger    logging.Logger
	Socket    net.Conn
	Timeout   time.Duration
	lastTxnID uint16
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
	var deadline time.Time
	if d, ok := ctx.Deadline(); ok {
		deadline = d
	} else {
		deadline = time.Now().Add(tt.Timeout)
	}
	if err := tt.Socket.SetDeadline(deadline); err != nil {
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
	res, err := tt.readResponse()
	if err == nil {
		tt.Logger.Debugf("RX: unit=0x%02x fc=0x%02x payload=% X", res.UnitID, res.FunctionCode, res.Payload)
	}
	return res, err
}

// ReadRequest reads one request from the socket (server use).
func (tt *TCP) ReadRequest() (*adu.Request, uint16, error) {
	if err := tt.Socket.SetDeadline(time.Now().Add(tt.Timeout)); err != nil {
		return nil, 0, err
	}
	req, txnID, err := tt.readMBAPFrameRaw()
	if err != nil {
		return nil, 0, err
	}
	tt.Logger.Debugf("RX: unit=0x%02x fc=0x%02x payload=% X", req.UnitID, req.FunctionCode, req.Payload)
	tt.lastTxnID = txnID
	return req, txnID, nil
}

// WriteResponse writes a response (server use).
func (tt *TCP) WriteResponse(res *adu.Response) error {
	frame := adu.AssembleMBAP(tt.lastTxnID, res.UnitID, res.FunctionCode, res.Payload)
	tt.Logger.Debugf("TX: % X", frame)
	return writeFull(tt.Socket, frame)
}

const maxAnomalies = 10

func (tt *TCP) readResponse() (*adu.Response, error) {
	txnMismatches := 0
	protoMismatches := 0
	for {
		res, txnID, err := tt.readMBAPFrame()
		if err != nil {
			if errors.Is(err, protocol.ErrUnknownProtocolID) {
				protoMismatches++
				tt.Logger.Warnf("received unexpected protocol ID (count %d/%d)", protoMismatches, maxAnomalies)
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
	if _, err := io.ReadFull(tt.Socket, header); err != nil {
		return nil, 0, err
	}
	txnID, unitID, mbapLen, err := adu.ParseMBAPHeader(header)
	if err != nil {
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
	if _, err := io.ReadFull(tt.Socket, body); err != nil {
		return nil, 0, err
	}
	return &adu.Request{
		UnitID:       unitID,
		FunctionCode: body[0],
		Payload:      body[1:],
	}, txnID, nil
}
