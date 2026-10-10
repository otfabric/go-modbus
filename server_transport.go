// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"net"
	"runtime/debug"
	"sync"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	inttrans "github.com/otfabric/go-modbus/internal/transport"
)

// acceptTCPClients accepts new client connections for one Start..Stop cycle, as
// long as the configured connection limit allows it.
func (ms *Server) acceptTCPClients(run *serverRun) {
	defer run.wg.Done()
	for {
		sock, err := run.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			ms.logger.Warningf("failed to accept client connection: %v", err)
			continue
		}

		accepted := false
		ms.lock.Lock()
		// ms.run != run once the server has been stopped (and maybe started again).
		if ms.started && ms.run == run && uint(len(ms.tcpClients)) < ms.conf.MaxClients {
			ms.tcpClients = append(ms.tcpClients, sock)
			accepted = true
			// Registered under the lock: Shutdown clears started under the same lock before
			// it waits, so this Add can never run concurrently with its Wait.
			run.wg.Add(1)
		}
		ms.lock.Unlock()

		if accepted {
			go ms.handleTCPClient(run, sock)
		} else {
			ms.logger.Warningf("max. number of concurrent connections "+
				"reached, rejecting %v", sock.RemoteAddr())
			_ = sock.Close()
		}
	}
}

// handleTCPClient handles a single TCP client connection. A per-connection
// context is derived from the run's stop context and cancelled when the client
// disconnects, when the server stops, and when this function returns.
func (ms *Server) handleTCPClient(run *serverRun, sock net.Conn) {
	defer run.wg.Done()

	connCtx, connCancel := context.WithCancel(run.ctx)
	defer connCancel()

	var err error
	var clientRole string
	var tlsSock *tls.Conn

	effectiveConn := net.Conn(sock)

	switch ms.transportType {
	case modbusTCP:
		ms.serveConn(connCtx, connCancel,
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

			ms.serveConn(connCtx, connCancel,
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

// serverPipelineDepth is the number of requests a client may have sent ahead of
// the one being served before the server stops reading from its socket.
const serverPipelineDepth = 16

// serveConn serves one Modbus/TCP connection.
//
// Requests are read by a goroutine of their own and handed, in order, to the
// loop below, which dispatches them one at a time and writes the responses. The
// reader keeps watching the socket while a handler runs, which is how a client
// that disconnects is noticed: the read fails and connCtx, which the handlers
// receive, is cancelled. (A client that only shuts down its sending side cannot
// be told apart from one that left, and has the same effect.) Requests that were
// already read are still served, in order.
//
// ServerConfig.Timeout is an idle timeout: it limits how long the server waits
// for (the rest of) a request while it has none to answer. The read deadline is
// therefore armed only while no request is being served; a slow handler does
// not time out its own connection. Each response is written under a deadline of
// its own (see inttrans.TCP.WriteFrame).
func (ms *Server) serveConn(connCtx context.Context, connCancel context.CancelFunc, t *inttrans.TCP, clientAddr, clientRole string) {
	type inbound struct {
		req   *adu.Request
		txnID uint16
	}
	queue := make(chan inbound, serverPipelineDepth)
	quit := make(chan struct{})
	readerDone := make(chan struct{})

	// pending counts the requests read and not yet answered. It and the read
	// deadline change together, under mu.
	var mu sync.Mutex
	pending := 0
	_ = t.SetReadDeadline(time.Now().Add(ms.conf.Timeout))

	go func() {
		defer close(readerDone)
		defer close(queue)
		for {
			req, txnID, err := t.ReadFrame()
			if err != nil {
				// Disconnected, idle for too long, or not speaking Modbus/TCP.
				connCancel()
				return
			}
			mu.Lock()
			pending++
			_ = t.SetReadDeadline(time.Time{})
			mu.Unlock()
			select {
			case queue <- inbound{req: req, txnID: txnID}:
			case <-quit:
				return
			}
		}
	}()

	for in := range queue {
		res, closeLink := ms.serveRequest(connCtx, in.req, in.txnID, clientAddr, clientRole)
		if closeLink {
			break
		}
		if writeErr := t.WriteFrame(in.txnID, res); writeErr != nil {
			ms.logger.Warningf("failed to write response: %v", writeErr)
		}
		mu.Lock()
		pending--
		if pending == 0 {
			_ = t.SetReadDeadline(time.Now().Add(ms.conf.Timeout))
		}
		mu.Unlock()
	}

	// Closing the socket ends the reader if it is still reading; quit ends it if
	// it is waiting to hand over a request.
	close(quit)
	_ = t.Close()
	<-readerDone
}

// serverTransport is what handleTransport needs from a transport.
type serverTransport interface {
	ReadRequest() (*adu.Request, uint16, error)
	WriteResponse(res *adu.Response) error
	Close() error
}

// handleTransport reads requests from the transport, dispatches them to the
// appropriate handler, and writes responses, strictly one after the other. It
// is the serving loop for a transport that cannot be read while a response is
// pending. TCP and TLS connections, the only ones the server accepts today, are
// served by serveConn; the unit tests drive the dispatch logic through this loop.
func (ms *Server) handleTransport(connCtx context.Context, t serverTransport, clientAddr string, clientRole string) {
	for {
		req, txnID, err := t.ReadRequest()
		if err != nil {
			return
		}
		res, closeLink := ms.serveRequest(connCtx, req, txnID, clientAddr, clientRole)
		if closeLink {
			_ = t.Close()
			return
		}
		if writeErr := t.WriteResponse(res); writeErr != nil {
			ms.logger.Warningf("failed to write response: %v", writeErr)
		}
	}
}

// serveRequest dispatches one request and returns the response to send.
// closeLink is true when the request violates the protocol: nothing is sent and
// the connection must be closed.
func (ms *Server) serveRequest(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (res *adu.Response, closeLink bool) {
	var reqStart time.Time
	if ms.metrics != nil {
		ms.metrics.OnRequest(req.UnitID, FunctionCode(req.FunctionCode))
		reqStart = time.Now()
	}

	res, err := ms.safeDispatch(ctx, req, txnID, clientAddr, clientRole)

	if err == nil && res == nil {
		err = ErrServerDeviceFailure
		ms.logger.Errorf("internal server error (req: %v, res: %v, err: %v)", req, res, err)
	}
	// A response that does not fit in a frame cannot be sent: the handler returned
	// more data than the function code can carry.
	if err == nil && len(res.Payload)+2 > adu.MBAPLengthMax {
		ms.logger.Errorf("response to FC 0x%02x from %s is %d bytes long, the maximum is %d: answering with Server Device Failure",
			req.FunctionCode, clientAddr, len(res.Payload)+1, adu.MBAPLengthMax-1)
		err = ErrServerDeviceFailure
	}

	if err != nil {
		if err == ErrProtocolError {
			ms.logger.Warningf("protocol error, closing link (client address: '%s')", clientAddr)
			if ms.metrics != nil {
				ms.metrics.OnError(req.UnitID, FunctionCode(req.FunctionCode), time.Since(reqStart), err)
			}
			return nil, true
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
	return res, false
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
	case FCEncapsulatedInterface:
		return ms.handleReadDeviceIdentification(ctx, req, txnID, clientAddr, clientRole)
	default:
		// Reported like an optional handler that is not implemented: the client gets
		// an Illegal Function exception, ServerMetrics an OnError.
		return nil, ErrIllegalFunction
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

	// The handshake deadline must not outlive the handshake.
	err = tlsSock.SetDeadline(time.Time{})

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
		return nil, ErrIllegalFunction
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
		return nil, ErrIllegalFunction
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
		return nil, ErrIllegalFunction
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
