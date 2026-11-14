// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"math"
	"net"
	"sync"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	inttrans "github.com/otfabric/go-modbus/internal/transport"
)

// The accept loop waits after an Accept that failed before it tries again: 5 ms
// after the first failure, twice as long after each one that follows, up to 1 s.
// An Accept that succeeds starts the sequence over. (net/http's Server does the
// same.) A listener that keeps failing, for want of file descriptors say, is
// then retried about once a second and logged as often, not in a tight loop.
const (
	acceptBackoffMin = 5 * time.Millisecond
	acceptBackoffMax = time.Second
)

// nextAcceptBackoff returns the wait after one more failed Accept, given the
// previous wait (zero after an Accept that succeeded).
func nextAcceptBackoff(prev time.Duration) time.Duration {
	if prev <= 0 {
		return acceptBackoffMin
	}
	return min(2*prev, acceptBackoffMax)
}

// acceptTCPClients accepts new client connections for one Start..Stop cycle, as
// long as the configured connection limit allows it.
//
// The goroutine runs the application's code: the Logger, and the
// ConnectionMetrics of a connection it closes. It starts the goroutine of a
// connection it admits before any of that, so that a Shutdown called there,
// which waits for that goroutine, does not wait for a goroutine that only its
// caller would start.
func (ms *Server) acceptTCPClients(run *serverRun) {
	share := ms.join(run, nil)
	defer share.done()
	defer ms.leave(run)
	var backoff time.Duration
	for {
		sock, err := run.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			backoff = nextAcceptBackoff(backoff)
			ms.logger.Warningf("failed to accept client connection (next attempt in %v): %v", backoff, err)
			// The wait ends early when the server stops.
			timer := time.NewTimer(backoff)
			select {
			case <-timer.C:
			case <-run.ctx.Done():
				timer.Stop()
				return
			}
			continue
		}
		backoff = 0

		accepted, refused := false, false
		var evicted net.Conn
		ms.lock.Lock()
		// ms.run != run once the server has been stopped (and maybe started again).
		if ms.started && ms.run == run {
			if uint(len(ms.tcpClients)) >= ms.conf.MaxClients && ms.transportType == modbusTCPOverTLS {
				evicted = ms.evictPendingTLSLocked(remoteHost(sock))
			}
			if uint(len(ms.tcpClients)) < ms.conf.MaxClients {
				ms.tcpClients = append(ms.tcpClients, sock)
				if ms.transportType == modbusTCPOverTLS {
					ms.tlsPending = append(ms.tlsPending, sock)
				}
				accepted = true
				// Registered under the lock: Shutdown clears started under the same lock before
				// it waits, so this Add can never run concurrently with its Wait.
				run.wg.Add(1)
			} else {
				refused = true
			}
		}
		ms.lock.Unlock()

		if accepted {
			go ms.handleTCPClient(run, sock)
		}
		if evicted != nil {
			// Its handshake fails and its goroutine ends.
			addr := evicted.RemoteAddr().String()
			ms.logger.Warningf("max. number of concurrent connections reached, "+
				"closing %v (TLS handshake not completed) for %v",
				addr, sock.RemoteAddr())
			_ = evicted.Close()
			ms.reportRejected(addr, RejectTLSHandshakeEvicted)
		}
		if !accepted {
			addr := sock.RemoteAddr().String()
			ms.logger.Warningf("max. number of concurrent connections "+
				"reached, rejecting %v", addr)
			_ = sock.Close()
			if refused {
				ms.reportRejected(addr, RejectMaxClients)
			}
		}
		if share.released.Load() {
			// A Shutdown called above, on this goroutine.
			return
		}
	}
}

// remoteHost returns the host part of the remote address of conn, the whole
// address if it has none.
func remoteHost(conn net.Conn) string {
	addr := conn.RemoteAddr()
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

// evictPendingTLSLocked makes room for a new connection from host when every
// MaxClients slot is taken: it removes from tcpClients one connection whose TLS
// handshake has not completed and returns it, for the caller to close; nil if
// there is none to remove. Connections that completed their handshake are
// never chosen.
//
// The victim is the oldest pending connection from the host with the most
// pending connections, so that peers that do not complete their handshakes
// mostly displace one another rather than clients from other hosts. A host
// that already has as many pending connections as any other displaces its own
// oldest one; a host with fewer displaces the oldest of a host with more.
// ms.lock must be held.
func (ms *Server) evictPendingTLSLocked(host string) net.Conn {
	i := pickPendingVictim(ms.tlsPending, host)
	if i < 0 {
		return nil
	}
	victim := ms.tlsPending[i]
	ms.tlsPending = append(ms.tlsPending[:i], ms.tlsPending[i+1:]...)
	ms.removeTCPClientLocked(victim)
	return victim
}

// pickPendingVictim returns the index in pending (oldest first) of the
// connection evictPendingTLSLocked removes for a new connection from host, -1
// if pending is empty.
func pickPendingVictim(pending []net.Conn, host string) int {
	if len(pending) == 0 {
		return -1
	}
	counts := make(map[string]int, len(pending))
	for _, c := range pending {
		counts[remoteHost(c)]++
	}
	most := 0
	for _, n := range counts {
		most = max(most, n)
	}
	target := host
	if counts[host] < most {
		// The host whose oldest pending connection is the oldest among the
		// hosts with the most of them.
		for _, c := range pending {
			if h := remoteHost(c); counts[h] == most {
				target = h
				break
			}
		}
	}
	for i, c := range pending {
		if remoteHost(c) == target {
			return i
		}
	}
	return -1
}

// removeTCPClientLocked removes conn from tcpClients, if it is there. ms.lock
// must be held.
func (ms *Server) removeTCPClientLocked(conn net.Conn) {
	for i := range ms.tcpClients {
		if ms.tcpClients[i] == conn {
			ms.tcpClients[i] = ms.tcpClients[len(ms.tcpClients)-1]
			ms.tcpClients = ms.tcpClients[:len(ms.tcpClients)-1]
			return
		}
	}
}

// handshakeDoneLocked records that the TLS handshake of sock completed, and
// replaces it with tlsSock in tcpClients. It reports false if sock was evicted
// meanwhile (it is then no longer counted, and must not be served). ms.lock
// must be held.
func (ms *Server) handshakeDoneLocked(sock net.Conn, tlsSock *tls.Conn) bool {
	if !ms.dropPendingLocked(sock) {
		return false
	}
	for i := range ms.tcpClients {
		if ms.tcpClients[i] == sock {
			ms.tcpClients[i] = tlsSock
			break
		}
	}
	return true
}

// dropPendingLocked removes sock from tlsPending and reports whether it was
// there. ms.lock must be held.
func (ms *Server) dropPendingLocked(sock net.Conn) bool {
	for i, c := range ms.tlsPending {
		if c == sock {
			ms.tlsPending = append(ms.tlsPending[:i], ms.tlsPending[i+1:]...)
			return true
		}
	}
	return false
}

// handleTCPClient handles a single TCP client connection. A per-connection
// context is derived from the run's stop context and cancelled when the client
// disconnects, when the server stops, and when this function returns.
//
// The connection's goroutines (this one and the reader of serveConn) hold
// one count of run.wg between them, which a Shutdown called on either gives
// back (see runShare).
func (ms *Server) handleTCPClient(run *serverRun, sock net.Conn) {
	share := ms.join(run, nil)
	defer share.done()
	defer ms.leave(run)

	connCtx, connCancel := context.WithCancel(run.ctx)
	defer connCancel()

	var err error
	var clientRole string
	var tlsSock *tls.Conn

	effectiveConn := net.Conn(sock)

	switch ms.transportType {
	case modbusTCP:
		ms.serveConn(run, share, connCtx, connCancel,
			newTCPTransport(sock, ms.conf.Timeout, ms.conf.Logger),
			sock.RemoteAddr().String(), "")

	case modbusTCPOverTLS:
		tlsSock, clientRole, err = ms.startTLS(sock)
		admitted := false
		if err == nil {
			ms.lock.Lock()
			admitted = ms.handshakeDoneLocked(sock, tlsSock)
			ms.lock.Unlock()
		}
		switch {
		case err != nil:
			ms.logger.Warningf("TLS handshake with %s failed: %v",
				sock.RemoteAddr().String(), err)
		case !admitted:
			// Closed to make room for a newer connection while the
			// handshake was completing.
			_ = tlsSock.Close()
		default:
			effectiveConn = tlsSock
			ms.serveConn(run, share, connCtx, connCancel,
				newTCPTransport(tlsSock, ms.conf.Timeout, ms.conf.Logger),
				sock.RemoteAddr().String(), clientRole)
		}

	default:
		ms.logger.Errorf("unimplemented transport type %v", ms.transportType)
	}

	ms.lock.Lock()
	ms.dropPendingLocked(sock)
	ms.removeTCPClientLocked(effectiveConn)
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
//
// The reader joins share, the connection's count of run.wg. Once a Shutdown
// called on either goroutine has released share, no further request is
// dispatched and no response is written: the shutdown does not wait for this
// connection, so it must not run the application's code again after the
// callback that called Shutdown, apart from the outcome of the request that
// callback belongs to.
func (ms *Server) serveConn(run *serverRun, share *runShare, connCtx context.Context, connCancel context.CancelFunc, t *inttrans.TCP, clientAddr, clientRole string) {
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
		ms.join(run, share)
		defer ms.leave(run)
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
		if share.released.Load() {
			break
		}
		res, closeLink := ms.serveRequest(connCtx, in.req, in.txnID, clientAddr, clientRole)
		if closeLink || share.released.Load() {
			break
		}
		// res is nil when the handler chose not to answer (ErrNoResponse).
		// A response that could not be written, or only in part, leaves the
		// stream out of step, and a client that does not read would hold the
		// connection forever: close it.
		if res != nil {
			if writeErr := t.WriteFrame(in.txnID, res); writeErr != nil {
				ms.logger.Warningf("failed to write response, closing link (client address: '%s'): %v", clientAddr, writeErr)
				connCancel()
				break
			}
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
		if res == nil {
			continue
		}
		if writeErr := t.WriteResponse(res); writeErr != nil {
			ms.logger.Warningf("failed to write response: %v", writeErr)
		}
	}
}

// serveRequest dispatches one request and returns the response to send.
// closeLink is true when the request violates the protocol: nothing is sent and
// the connection must be closed. res is nil with closeLink false when the handler
// returned ErrNoResponse: nothing is sent and the connection is kept.
func (ms *Server) serveRequest(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (res *adu.Response, closeLink bool) {
	var reqStart time.Time
	if ms.metrics != nil {
		ms.reportMetrics(metricsOnRequest, req, clientAddr, 0, nil)
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
				ms.reportMetrics(metricsOnError, req, clientAddr, time.Since(reqStart), err)
			}
			return nil, true
		}

		if ms.metrics != nil {
			ms.reportMetrics(metricsOnError, req, clientAddr, time.Since(reqStart), err)
		}
		silent, code := ms.answerFor(req, clientAddr, err)
		if silent {
			return nil, false
		}
		res = &adu.Response{
			UnitID:        req.UnitID,
			FunctionCode:  req.FunctionCode | 0x80,
			Payload:       []byte{byte(code)},
			TransactionID: txnID,
		}
	} else if ms.metrics != nil {
		ms.reportMetrics(metricsOnResponse, req, clientAddr, time.Since(reqStart), nil)
	}
	return res, false
}

// metricsHook names one method of ServerMetrics.
type metricsHook uint8

const (
	metricsOnRequest metricsHook = iota
	metricsOnResponse
	metricsOnError
)

func (h metricsHook) String() string {
	switch h {
	case metricsOnRequest:
		return "OnRequest"
	case metricsOnResponse:
		return "OnResponse"
	default:
		return "OnError"
	}
}

// reportMetrics calls one ServerMetrics hook for req. It is the only place the
// server calls them. The hooks are application code running on the connection's
// goroutine, with the unit ID and function code as the client sent them: like
// safeDispatch for a handler, it calls the hook through call, which logs a
// hook that does not return normally with the stack, and the request is then
// served as if the hook had returned. ms.metrics must not be nil.
func (ms *Server) reportMetrics(hook metricsHook, req *adu.Request, clientAddr string, d time.Duration, err error) {
	fc := FunctionCode(req.FunctionCode)
	_ = ms.call("ServerMetrics."+hook.String(), func() string {
		// The message of v1.15.3, kept for log filters that match it.
		return fmt.Sprintf("panic in ServerMetrics.%s for FC 0x%02x from %s", hook, req.FunctionCode, clientAddr)
	}, func() error {
		switch hook {
		case metricsOnRequest:
			ms.metrics.OnRequest(req.UnitID, fc)
		case metricsOnResponse:
			ms.metrics.OnResponse(req.UnitID, fc, d)
		case metricsOnError:
			ms.metrics.OnError(req.UnitID, fc, d, err)
		}
		return nil
	})
}

// safeDispatch runs dispatchRequest, and with it the handler method, through
// call: a handler that does not return normally is logged with the stack, and
// the request is answered with ServerDeviceFailure.
func (ms *Server) safeDispatch(ctx context.Context, req *adu.Request, txnID uint16, clientAddr, clientRole string) (res *adu.Response, err error) {
	if ms.call("RequestHandler", func() string {
		// The message of v1.15.3, kept for log filters that match it.
		return fmt.Sprintf("panic in handler for FC 0x%02x from %s", req.FunctionCode, clientAddr)
	}, func() error {
		res, err = ms.dispatchRequest(ctx, req, txnID, clientAddr, clientRole)
		return nil
	}) != nil {
		return nil, ErrServerDeviceFailure
	}
	return res, err
}

// answerFor returns how the server answers a request that failed with err:
// silent when err is ErrNoResponse (nothing is sent), else with the exception
// code of err. err is usually the handler's, and errors.Is calls its methods
// (Is, Unwrap), which are the application's code: they run through call, and
// an error whose methods do not return normally is answered with
// ServerDeviceFailure.
func (ms *Server) answerFor(req *adu.Request, clientAddr string, err error) (silent bool, code ExceptionCode) {
	if ms.call("handler error", func() string {
		return fmt.Sprintf("the error of the handler for FC 0x%02x from %s did not return normally", req.FunctionCode, clientAddr)
	}, func() error {
		silent = errors.Is(err, ErrNoResponse)
		if !silent {
			code = mapErrorToExceptionCode(err)
		}
		return nil
	}) != nil {
		return false, exServerDeviceFailure
	}
	return silent, code
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
	case FCDiagnostics:
		return ms.handleDiagnostics(ctx, req, txnID, clientAddr, clientRole)
	case FCReportServerID:
		return ms.handleReportServerID(ctx, req, txnID, clientAddr, clientRole)
	case FCReadFileRecord:
		return ms.handleReadFileRecords(ctx, req, txnID, clientAddr, clientRole)
	case FCWriteFileRecord:
		return ms.handleWriteFileRecords(ctx, req, txnID, clientAddr, clientRole)
	case FCReadFIFOQueue:
		return ms.handleReadFIFOQueue(ctx, req, txnID, clientAddr, clientRole)
	default:
		// Any other function code, vendor and user-defined ones included, is not
		// supported: it is reported like an optional handler that is not
		// implemented. The client gets an Illegal Function exception,
		// ServerMetrics an OnError.
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

	// The handshake signs with the private key of TLSServerCert, which may be
	// the application's own crypto.Signer.
	err = ms.call("TLS handshake", func() string {
		return fmt.Sprintf("TLS handshake with %s did not return normally", tcpSock.RemoteAddr())
	}, tlsSock.Handshake)
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
	if cr == nil {
		ms.logger.Errorf("comm event counter handler returned nil response")
		return nil, ErrServerDeviceFailure
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
	if cl == nil {
		ms.logger.Errorf("comm event log handler returned nil response")
		return nil, ErrServerDeviceFailure
	}
	// Events the one-byte byte count cannot count cannot be sent. Fewer
	// that still overflow the frame are caught by the response size check
	// in serveRequest.
	n := len(cl.Events)
	if n > math.MaxUint8-6 {
		return nil, ErrServerDeviceFailure
	}
	payload := []byte{byte(6 + n)}
	payload = append(payload, uint16ToBytes(BigEndian, cl.Status)...)
	payload = append(payload, uint16ToBytes(BigEndian, cl.EventCount)...)
	payload = append(payload, uint16ToBytes(BigEndian, cl.MessageCount)...)
	payload = append(payload, cl.Events...)
	return newSuccessResponse(req, txnID, payload), nil
}
