package modbus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"net"
	"runtime/debug"
	"time"

	"github.com/otfabric/modbus/internal/adu"
	inttrans "github.com/otfabric/modbus/internal/transport"
)

// Accepts new client connections if the configured connection limit allows it.
func (ms *Server) acceptTCPClients() {
	for {
		sock, err := ms.tcpListener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			ms.logger.Warningf("failed to accept client connection: %v", err)
			continue
		}

		accepted := false
		ms.lock.Lock()
		if ms.started && uint(len(ms.tcpClients)) < ms.conf.MaxClients {
			ms.tcpClients = append(ms.tcpClients, sock)
			accepted = true
		}
		ms.lock.Unlock()

		if accepted {
			ms.wg.Add(1)
			go ms.handleTCPClient(sock)
		} else {
			ms.logger.Warningf("max. number of concurrent connections "+
				"reached, rejecting %v", sock.RemoteAddr())
			_ = sock.Close()
		}
	}
}

// handleTCPClient handles a single TCP client connection. A per-connection
// context is derived from the server's stopCtx and cancelled when this
// function returns (client disconnect or server stop).
func (ms *Server) handleTCPClient(sock net.Conn) {
	defer ms.wg.Done()

	connCtx, connCancel := context.WithCancel(ms.stopCtx)
	defer connCancel()

	var err error
	var clientRole string
	var tlsSock *tls.Conn

	effectiveConn := net.Conn(sock)

	switch ms.transportType {
	case modbusTCP:
		ms.handleTransport(connCtx,
			newTCPTransport(sock, ms.conf.Timeout, ms.conf.Logger),
			sock.RemoteAddr().String(), "")

	case modbusTCPOverTLS:
		tlsSock, clientRole, err = ms.startTLS(sock)
		if err != nil {
			ms.logger.Warningf("TLS handshake with %s failed: %v",
				sock.RemoteAddr().String(), err)
		} else {
			effectiveConn = tlsSock
			ms.lock.Lock()
			for i := range ms.tcpClients {
				if ms.tcpClients[i] == sock {
					ms.tcpClients[i] = tlsSock
					break
				}
			}
			ms.lock.Unlock()

			ms.handleTransport(connCtx,
				newTCPTransport(tlsSock, ms.conf.Timeout, ms.conf.Logger),
				sock.RemoteAddr().String(), clientRole)
		}

	default:
		ms.logger.Errorf("unimplemented transport type %v", ms.transportType)
	}

	ms.lock.Lock()
	for i := range ms.tcpClients {
		if ms.tcpClients[i] == effectiveConn {
			ms.tcpClients[i] = ms.tcpClients[len(ms.tcpClients)-1]
			ms.tcpClients = ms.tcpClients[:len(ms.tcpClients)-1]
			break
		}
	}
	ms.lock.Unlock()

	_ = effectiveConn.Close()
}

// handleTransport reads requests from the transport, dispatches them to the
// appropriate handler, and writes responses. The connCtx is cancelled when the
// client disconnects or the server stops; handlers observe it for cooperative
// cancellation.
func (ms *Server) handleTransport(connCtx context.Context, t inttrans.Transport, clientAddr string, clientRole string) {
	for {
		req, txnID, err := t.ReadRequest()
		if err != nil {
			return
		}

		var reqStart time.Time
		if ms.metrics != nil {
			ms.metrics.OnRequest(req.UnitID, FunctionCode(req.FunctionCode))
			reqStart = time.Now()
		}

		res, err := ms.safeDispatch(connCtx, req, txnID, clientAddr, clientRole)

		if err == nil && res == nil {
			err = ErrServerDeviceFailure
			ms.logger.Errorf("internal server error (req: %v, res: %v, err: %v)", req, res, err)
		}

		if err != nil {
			if err == ErrProtocolError {
				ms.logger.Warningf("protocol error, closing link (client address: '%s')", clientAddr)
				if ms.metrics != nil {
					ms.metrics.OnError(req.UnitID, FunctionCode(req.FunctionCode), time.Since(reqStart), err)
				}
				_ = t.Close()
				return
			}

			if ms.metrics != nil {
				ms.metrics.OnError(req.UnitID, FunctionCode(req.FunctionCode), time.Since(reqStart), err)
			}
			res = &adu.Response{
				UnitID:        req.UnitID,
				FunctionCode:  req.FunctionCode | 0x80,
				Payload:       []byte{byte(mapErrorToExceptionCode(err))},
				TransactionID: txnID,
			}
		} else if ms.metrics != nil {
			ms.metrics.OnResponse(req.UnitID, FunctionCode(req.FunctionCode), time.Since(reqStart))
		}

		if writeErr := t.WriteResponse(res); writeErr != nil {
			ms.logger.Warningf("failed to write response: %v", writeErr)
		}
	}
}

// safeDispatch wraps dispatchRequest with panic recovery so that a handler
// panic does not crash the client goroutine.
func (ms *Server) safeDispatch(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (res *adu.Response, err error) {
	defer func() {
		if r := recover(); r != nil {
			ms.logger.Errorf("panic in handler for FC 0x%02x from %s: %v\n%s",
				req.FunctionCode, clientAddr, r, debug.Stack())
			res = nil
			err = ErrServerDeviceFailure
		}
	}()
	return ms.dispatchRequest(ctx, req, txnID, clientAddr, clientRole)
}

// dispatchRequest routes the request to the appropriate FC handler.
func (ms *Server) dispatchRequest(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	switch FunctionCode(req.FunctionCode) {
	case FCReadCoils, FCReadDiscreteInputs:
		return ms.handleReadBools(ctx, req, txnID, clientAddr, clientRole)
	case FCWriteSingleCoil:
		return ms.handleWriteSingleCoil(ctx, req, txnID, clientAddr, clientRole)
	case FCWriteMultipleCoils:
		return ms.handleWriteMultipleCoils(ctx, req, txnID, clientAddr, clientRole)
	case FCReadHoldingRegisters, FCReadInputRegisters:
		return ms.handleReadRegisters(ctx, req, txnID, clientAddr, clientRole)
	case FCWriteSingleRegister:
		return ms.handleWriteSingleRegister(ctx, req, txnID, clientAddr, clientRole)
	case FCWriteMultipleRegisters:
		return ms.handleWriteMultipleRegisters(ctx, req, txnID, clientAddr, clientRole)
	case FCMaskWriteRegister:
		return ms.handleMaskWriteRegister(ctx, req, txnID, clientAddr, clientRole)
	case FCReadWriteMultipleRegs:
		return ms.handleReadWriteMultipleRegisters(ctx, req, txnID, clientAddr, clientRole)
	case FCReadExceptionStatus:
		return ms.handleExceptionStatus(ctx, req, txnID, clientAddr, clientRole)
	case FCGetCommEventCounters:
		return ms.handleCommEventCounter(ctx, req, txnID, clientAddr, clientRole)
	case FCGetCommEventLog:
		return ms.handleCommEventLog(ctx, req, txnID, clientAddr, clientRole)
	default:
		return &adu.Response{
			UnitID:        req.UnitID,
			FunctionCode:  req.FunctionCode | 0x80,
			Payload:       []byte{byte(exIllegalFunction)},
			TransactionID: txnID,
		}, nil
	}
}

// startTLS performs a TLS handshake with client authentication.
func (ms *Server) startTLS(tcpSock net.Conn) (
	tlsSock *tls.Conn, clientRole string, err error) {

	err = tcpSock.SetDeadline(time.Now().Add(ms.conf.TLSHandshakeTimeout))
	if err != nil {
		return
	}

	tlsSock = tls.Server(tcpSock, &tls.Config{
		Certificates: []tls.Certificate{*ms.conf.TLSServerCert},
		ClientCAs:    ms.conf.TLSClientCAs,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	})

	err = tlsSock.Handshake()
	if err != nil {
		return
	}

	connState := tlsSock.ConnectionState()
	if len(connState.PeerCertificates) == 0 {
		err = errors.New("no client certificate received")
		return
	}
	clientRole = ms.extractRole(connState.PeerCertificates[0])

	return
}

// extractRole looks for Modbus Role extensions in a certificate.
func (ms *Server) extractRole(cert *x509.Certificate) (role string) {
	var err error
	var found bool
	var badCert bool

	for _, ext := range cert.Extensions {
		if ext.Id.Equal(modbusRoleOID) {
			if found {
				ms.logger.Warning("client certificate contains more than one role OIDs")
				badCert = true
				break
			}
			found = true

			if len(ext.Value) < 2 || ext.Value[0] != 0x0c {
				badCert = true
				break
			}

			_, err = asn1.Unmarshal(ext.Value, &role)
			if err != nil {
				ms.logger.Warningf("failed to decode Modbus Role extension: %v", err)
				badCert = true
				break
			}
		}
	}

	if badCert {
		role = ""
	}

	return
}

// Server-side response helpers.

// decodeAddrQuantity extracts address and quantity from a 4-byte payload.
func decodeAddrQuantity(payload []byte) (addr, quantity uint16, err error) {
	if len(payload) != 4 {
		return 0, 0, ErrProtocolError
	}
	return bytesToUint16(BigEndian, payload[0:2]), bytesToUint16(BigEndian, payload[2:4]), nil
}

// newSuccessResponse creates a response echoing the request's unit ID and FC.
func newSuccessResponse(req *adu.Request, txnID uint16, payload []byte) *adu.Response {
	return &adu.Response{
		UnitID:        req.UnitID,
		FunctionCode:  req.FunctionCode,
		Payload:       payload,
		TransactionID: txnID,
	}
}

// newEchoAddrQuantityResponse creates a response echoing addr and quantity.
func newEchoAddrQuantityResponse(req *adu.Request, txnID uint16, addr, quantity uint16) *adu.Response {
	payload := uint16ToBytes(BigEndian, addr)
	payload = append(payload, uint16ToBytes(BigEndian, quantity)...)
	return newSuccessResponse(req, txnID, payload)
}

// handleExceptionStatus dispatches FC07 to ExceptionStatusHandler if implemented.
func (ms *Server) handleExceptionStatus(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	h, ok := ms.handler.(ExceptionStatusHandler)
	if !ok {
		return &adu.Response{
			UnitID:        req.UnitID,
			FunctionCode:  req.FunctionCode | 0x80,
			Payload:       []byte{byte(exIllegalFunction)},
			TransactionID: txnID,
		}, nil
	}
	status, err := h.HandleExceptionStatus(ctx, &ExceptionStatusRequest{
		ClientAddr:   clientAddr,
		ClientRole:   clientRole,
		UnitID:       req.UnitID,
		FunctionCode: FunctionCode(req.FunctionCode),
	})
	if err != nil {
		return nil, err
	}
	return newSuccessResponse(req, txnID, []byte{status}), nil
}

// handleCommEventCounter dispatches FC0B to CommEventCounterHandler if implemented.
func (ms *Server) handleCommEventCounter(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	h, ok := ms.handler.(CommEventCounterHandler)
	if !ok {
		return &adu.Response{
			UnitID:        req.UnitID,
			FunctionCode:  req.FunctionCode | 0x80,
			Payload:       []byte{byte(exIllegalFunction)},
			TransactionID: txnID,
		}, nil
	}
	cr, err := h.HandleCommEventCounter(ctx, &CommEventCounterRequest{
		ClientAddr:   clientAddr,
		ClientRole:   clientRole,
		UnitID:       req.UnitID,
		FunctionCode: FunctionCode(req.FunctionCode),
	})
	if err != nil {
		return nil, err
	}
	payload := uint16ToBytes(BigEndian, cr.Status)
	payload = append(payload, uint16ToBytes(BigEndian, cr.EventCount)...)
	return newSuccessResponse(req, txnID, payload), nil
}

// handleCommEventLog dispatches FC0C to CommEventLogHandler if implemented.
func (ms *Server) handleCommEventLog(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (*adu.Response, error) {
	h, ok := ms.handler.(CommEventLogHandler)
	if !ok {
		return &adu.Response{
			UnitID:        req.UnitID,
			FunctionCode:  req.FunctionCode | 0x80,
			Payload:       []byte{byte(exIllegalFunction)},
			TransactionID: txnID,
		}, nil
	}
	cl, err := h.HandleCommEventLog(ctx, &CommEventLogRequest{
		ClientAddr:   clientAddr,
		ClientRole:   clientRole,
		UnitID:       req.UnitID,
		FunctionCode: FunctionCode(req.FunctionCode),
	})
	if err != nil {
		return nil, err
	}
	byteCount := 6 + len(cl.Events)
	payload := []byte{byte(byteCount)}
	payload = append(payload, uint16ToBytes(BigEndian, cl.Status)...)
	payload = append(payload, uint16ToBytes(BigEndian, cl.EventCount)...)
	payload = append(payload, uint16ToBytes(BigEndian, cl.MessageCount)...)
	payload = append(payload, cl.Events...)
	return newSuccessResponse(req, txnID, payload), nil
}
