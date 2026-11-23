// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-modbus/internal/goid"
)

// Modbus Role PEM OID (see R-21 of the MBAPS spec).
var modbusRoleOID asn1.ObjectIdentifier = asn1.ObjectIdentifier{
	1, 3, 6, 1, 4, 1, 50316, 802, 1,
}

// defaultTLSHandshakeTimeout is the default ServerConfig.TLSHandshakeTimeout.
const defaultTLSHandshakeTimeout = 10 * time.Second

// ClientFilter decides whether the server serves a client, by its remote
// address, as soon as the connection is accepted (ServerConfig.AllowClient).
type ClientFilter interface {
	AllowClient(remote net.Addr) bool
}

// ClientFilterFunc is a function that serves as a ClientFilter. A ServerConfig
// holding one is not comparable with ==: comparing it panics, as comparing
// two functions would.
type ClientFilterFunc func(remote net.Addr) bool

// AllowClient returns f(remote).
func (f ClientFilterFunc) AllowClient(remote net.Addr) bool { return f(remote) }

// Server configuration object.
type ServerConfig struct {
	URL string
	// Timeout is the idle timeout of a client connection: a connection on which
	// no complete request arrives for this long, while no request is being
	// served, is closed. It does not limit how long a handler may run. Each
	// response is written under a deadline of its own, of the same duration.
	// Defaults to 120 s.
	Timeout time.Duration
	// MaxClients limits the number of concurrent client connections. Defaults
	// to 10. On tcp+tls a connection whose TLS handshake has not completed
	// counts too, but when every slot is taken a new connection replaces the
	// oldest one still handshaking (see TLSHandshakeTimeout): a connection
	// that completed its handshake is never closed to make room.
	MaxClients uint
	// AllowClient, when set, is asked about every tcp and tcp+tls connection
	// as soon as it is accepted, with the client's address: false closes it
	// before anything is read from it, and ConnectionMetrics reports it as
	// RejectNotAllowed. A refused connection takes no MaxClients slot. It runs
	// on the goroutine that accepts connections, so it should return quickly;
	// one that panics refuses the connection. A function serves as one through
	// ClientFilterFunc.
	AllowClient ClientFilter
	// MaxClientsPerHost, when set, limits the tcp and tcp+tls connections
	// the server serves at once from one remote host (the IP address of the
	// client, without its port), connections still in their TLS handshake
	// included. A connection beyond it is closed as soon as it is accepted,
	// before anything is read from it: it takes no MaxClients slot, does not
	// replace a connection still handshaking, and ConnectionMetrics reports
	// it as RejectPerHostLimit. Zero (the default) sets no limit. Clients
	// behind one NAT or proxy share a host.
	MaxClientsPerHost uint
	// FirstRequestTimeout, when set, replaces Timeout as the idle timeout of
	// a connection until its first complete request has been read: a client
	// that connects and sends no request is closed after it, and
	// ConnectionCloseMetrics reports CloseFirstRequestTimeout. It runs from
	// the moment the server starts serving the connection (on tcp+tls, once
	// its handshake completed; TLSHandshakeTimeout bounds the handshake).
	// Zero (the default) uses Timeout; negative is a configuration error.
	FirstRequestTimeout time.Duration
	// FrameTimeout, when set, bounds the time a request takes to arrive in
	// full once its first byte has been read: a request still incomplete
	// after it closes the connection, and ConnectionCloseMetrics reports
	// CloseFrameTimeout. It applies to every request, also while earlier
	// ones are being served, and together with the idle timeout: the earlier
	// of the two ends the wait. Zero (the default) leaves a request the idle
	// timeout alone, as before; negative is a configuration error. A value
	// well above the round-trip time of the slowest legitimate client keeps
	// it from cutting off a request split over several segments.
	FrameTimeout time.Duration
	// Authorizer, when set, is asked about every request before a handler
	// method serves it, and can refuse it: the handler is then not called,
	// and the client is answered with an exception (see RequestAuthorizer).
	// Nil (the default) serves every request, as before. A function serves
	// as one through RequestAuthorizerFunc.
	Authorizer    RequestAuthorizer
	TLSServerCert *tls.Certificate
	TLSClientCAs  *x509.CertPool
	// TLSHandshakeTimeout bounds the TLS handshake of a tcp+tls connection,
	// from accept. Defaults to 10 s.
	TLSHandshakeTimeout time.Duration
	Logger              Logger
	Metrics             ServerMetrics
	// PDUTap, when set, receives the raw request and response PDUs of every
	// request the server dispatches, with the client's address and role (see
	// ServerPDUTap). Nil (the default) disables it; nothing is copied then.
	PDUTap ServerPDUTap
}

// CoilsRequest is passed to HandleCoils for both read (FC01) and write (FC05/FC15) requests.
//
// When IsWrite is true, Args contains the coil values to write and the handler should
// apply them. When IsWrite is false, the handler should return the current values.
// Note that FC05 (write single coil) and FC15 (write multiple coils) both map to
// IsWrite=true — the handler cannot distinguish between them.
type CoilsRequest struct {
	ClientAddr   string
	ClientRole   string
	UnitID       uint8
	FunctionCode FunctionCode
	Addr         uint16
	Quantity     uint16
	IsWrite      bool
	Args         []bool
}

// DiscreteInputsRequest is passed to HandleDiscreteInputs for read requests (FC02).
// Discrete inputs are read-only by definition, so there is no IsWrite field.
type DiscreteInputsRequest struct {
	ClientAddr   string
	ClientRole   string
	UnitID       uint8
	FunctionCode FunctionCode
	Addr         uint16
	Quantity     uint16
}

// HoldingRegistersRequest is passed to HandleHoldingRegisters for both read (FC03)
// and write (FC06/FC16) requests.
//
// When IsWrite is true, Args contains the register values to write and the handler
// should apply them. When IsWrite is false, the handler should return the current
// values. Note that FC06 (write single register) and FC16 (write multiple registers)
// both map to IsWrite=true — the handler cannot distinguish between them.
type HoldingRegistersRequest struct {
	ClientAddr   string
	ClientRole   string
	UnitID       uint8
	FunctionCode FunctionCode
	Addr         uint16
	Quantity     uint16
	IsWrite      bool
	Args         []uint16
}

// InputRegistersRequest is passed to HandleInputRegisters for read requests (FC04).
// Input registers are read-only by definition, so there is no IsWrite field.
type InputRegistersRequest struct {
	ClientAddr   string
	ClientRole   string
	UnitID       uint8
	FunctionCode FunctionCode
	Addr         uint16
	Quantity     uint16
}

// ErrNoResponse is returned by a handler method to leave the request unanswered:
// the server sends nothing and keeps the connection, so the client runs into its
// timeout. It is how a server models a device that stays silent, such as the
// units behind a gateway that received a broadcast (unit id 0), or one that is
// simulated as unresponsive. Requests the client sent after it are served as usual.
//
// It has this meaning only when returned by a handler; no client method returns it.
var ErrNoResponse = errors.New("modbus: no response")

// RequestHandler is the interface implemented by the application to serve Modbus
// requests. Each connected client is served in its own goroutine, so handler
// methods may be called concurrently from different client goroutines.
// Implementations must be safe for concurrent use (e.g. use sync.Mutex or
// other synchronization when accessing shared state).
//
// Each handler method receives a context that is cancelled when the client
// disconnects (also while the handler is running; a client that merely shuts
// down its sending side counts as disconnected) or the server stops. A handler
// method that does not return normally is stopped there and logged at error
// level with its stack; the client receives a ServerDeviceFailure exception
// response, and the connection and the server go on.
//
// An error returned by a handler method is sent to the client as the matching
// Modbus exception (ServerDeviceFailure when there is none), with one exception:
// ErrNoResponse makes the server send nothing at all. The server calls the
// error's methods (Is, Unwrap) to choose the exception; one that does not
// return normally is logged the same way and answered with ServerDeviceFailure.
//
// Ownership. The slices in a request (Args, WriteValues, Data, Requests,
// Records) are the handler's: the server made them for this call and does not
// use them again. A slice, or a response struct, that a handler returns is read
// by the server after the handler has returned, while it encodes the response,
// and not kept after that: return a copy of shared state, not a view of it
// (h.regs[a:b]) that another call may be writing to. This holds for the
// optional handler interfaces as well.
//
// Stopping the server from a handler. A handler method may call Stop or
// Shutdown, and so may a method of ServerConfig.Metrics (ServerMetrics and
// its optional extensions: ConnectionMetrics, ConnectionLifecycleMetrics,
// ConnectionCloseMetrics, TLSHandshakeMetrics, AcceptErrorMetrics,
// ProtocolViolationMetrics), ServerConfig.AllowClient, ServerConfig.PDUTap,
// ServerConfig.Authorizer and the Logger.
// The call stops the server as a call from any other goroutine does: it stops
// accepting, cancels the context of every connection and closes the
// listener and the client sockets. Then it waits for the server's goroutines,
// the handlers running for other connections included, except the ones of the
// connection it was called on (or the accept loop, for ServerConfig.AllowClient,
// a ConnectionMetrics or AcceptErrorMetrics method or a log entry of the
// accept loop). Stop returns once they have
// ended. Shutdown also returns when its context ends, and at once when that
// context is already done: Shutdown with the handler's own context, which the
// shutdown cancels, returns context.Canceled without waiting.
//
// When the call returns, the listener and the client sockets are closed and
// every other goroutine of the server has ended. The caller's connection is
// still served on the caller's goroutine until the request in progress is
// complete: that request is not answered (its connection is closed), its
// handler is still called, with its context cancelled, when the call came
// from OnRequest, and its outcome is reported to ServerMetrics as usual. Then
// no further request of the connection is dispatched and its goroutines end;
// they may still write log entries. A Stop on any other goroutine (go
// srv.Stop() from a handler, say) waits for them too, and so for the handler
// that started it to return.
type RequestHandler interface {
	HandleCoils(ctx context.Context, req *CoilsRequest) (res []bool, err error)
	HandleDiscreteInputs(ctx context.Context, req *DiscreteInputsRequest) (res []bool, err error)
	HandleHoldingRegisters(ctx context.Context, req *HoldingRegistersRequest) (res []uint16, err error)
	HandleInputRegisters(ctx context.Context, req *InputRegistersRequest) (res []uint16, err error)
}

// MaskWriteRequest is passed to MaskWriteHandler.HandleMaskWrite for FC22
// (Mask Write Register) requests.
type MaskWriteRequest struct {
	ClientAddr   string
	ClientRole   string
	UnitID       uint8
	FunctionCode FunctionCode
	Addr         uint16
	AndMask      uint16
	OrMask       uint16
}

// MaskWriteHandler is an optional interface for serving FC22 (Mask Write Register).
// If the RequestHandler also implements MaskWriteHandler, the server dispatches
// FC22 requests to HandleMaskWrite instead of returning Illegal Function.
type MaskWriteHandler interface {
	HandleMaskWrite(ctx context.Context, req *MaskWriteRequest) error
}

// ReadWriteRegistersRequest is passed to ReadWriteHandler.HandleReadWriteRegisters
// for FC23 (Read/Write Multiple Registers) requests.
type ReadWriteRegistersRequest struct {
	ClientAddr   string
	ClientRole   string
	UnitID       uint8
	FunctionCode FunctionCode
	ReadAddr     uint16
	ReadQty      uint16
	WriteAddr    uint16
	WriteValues  []uint16
}

// ReadWriteHandler is an optional interface for serving FC23 (Read/Write Multiple
// Registers). If the RequestHandler also implements ReadWriteHandler, the server
// dispatches FC23 requests to HandleReadWriteRegisters instead of returning
// Illegal Function.
type ReadWriteHandler interface {
	HandleReadWriteRegisters(ctx context.Context, req *ReadWriteRegistersRequest) (readValues []uint16, err error)
}

// ExceptionStatusRequest is passed to ExceptionStatusHandler for FC07 requests.
type ExceptionStatusRequest struct {
	ClientAddr   string
	ClientRole   string
	UnitID       uint8
	FunctionCode FunctionCode
}

// ExceptionStatusHandler is an optional interface for serving FC07 (Read Exception
// Status). The spec labels this "Serial Line only," but it is supported on all
// transports because it can traverse gateways transparently.
type ExceptionStatusHandler interface {
	HandleExceptionStatus(ctx context.Context, req *ExceptionStatusRequest) (uint8, error)
}

// CommEventCounterRequest is passed to CommEventCounterHandler for FC0B requests.
type CommEventCounterRequest struct {
	ClientAddr   string
	ClientRole   string
	UnitID       uint8
	FunctionCode FunctionCode
}

// CommEventCounterHandler is an optional interface for serving FC0B (Get Comm Event
// Counter). The spec labels this "Serial Line only," but it is supported on all
// transports because it can traverse gateways transparently.
type CommEventCounterHandler interface {
	HandleCommEventCounter(ctx context.Context, req *CommEventCounterRequest) (*CommEventCounterResponse, error)
}

// CommEventLogRequest is passed to CommEventLogHandler for FC0C requests.
type CommEventLogRequest struct {
	ClientAddr   string
	ClientRole   string
	UnitID       uint8
	FunctionCode FunctionCode
}

// CommEventLogHandler is an optional interface for serving FC0C (Get Comm Event
// Log). The spec labels this "Serial Line only," but it is supported on all
// transports because it can traverse gateways transparently.
type CommEventLogHandler interface {
	HandleCommEventLog(ctx context.Context, req *CommEventLogRequest) (*CommEventLogResponse, error)
}

// Modbus server object.
type Server struct {
	conf    ServerConfig
	logger  *logger
	lock    sync.Mutex
	started bool
	handler RequestHandler
	metrics ServerMetrics
	// The optional extensions of metrics the server looks for on
	// every connection or request, found once by NewServer (nil: not
	// implemented).
	tlsMetrics       TLSHandshakeMetrics
	acceptMetrics    AcceptErrorMetrics
	violationMetrics ProtocolViolationMetrics
	connMetrics      ServerConnectionMetrics
	// tcpClients holds the sockets of the connected clients.
	tcpClients []net.Conn
	// tlsPending holds, oldest first, the sockets of tcpClients whose TLS
	// handshake has not completed (tcp+tls only).
	tlsPending []net.Conn
	// pendingIDs holds the ConnectionID of every socket in tlsPending, for
	// the report of one that is evicted.
	pendingIDs    map[net.Conn]ConnectionID
	transportType transportType

	// run is what the last Start created. The fields below it are copies of its
	// members, written by Start under lock and kept after the server has stopped.
	run         *serverRun
	wg          *sync.WaitGroup
	tcpListener net.Listener
	stopCancel  context.CancelFunc
	stopCtx     context.Context
}

// serverRun groups what belongs to one Start..Stop cycle. Every goroutine of a
// cycle is handed its serverRun and uses nothing else to reach the listener,
// the stop context or the WaitGroup: a Server that is started again gets a new
// one, and goroutines of the previous cycle that are still winding down (after
// a Shutdown whose context expired) cannot mix with the new cycle.
type serverRun struct {
	ctx      context.Context
	cancel   context.CancelFunc
	listener net.Listener
	// wg counts the accept loop and the client goroutines of this cycle.
	wg *sync.WaitGroup
	// goroutines maps the id of every goroutine of this cycle that runs the
	// application's code (the accept loop, each connection's goroutine and
	// its reader) to the share of wg it belongs to. Shutdown looks its caller
	// up here, so that it does not wait for itself. Guarded by Server.lock.
	goroutines map[uint64]*runShare
}

// runShare is one count of serverRun.wg: the accept loop, or one connection
// (its goroutine and the goroutine that reads its requests).
type runShare struct {
	wg   *sync.WaitGroup
	once sync.Once
	// released is set when a Shutdown called on one of the share's goroutines
	// gave the count back, so that it would not wait for itself. The share's
	// goroutines then make no further callback that the shutdown would not
	// wait for: no request is dispatched and no response is written.
	released atomic.Bool
}

// done gives the share's count back to wg, once.
func (s *runShare) done() { s.once.Do(s.wg.Done) }

// release is done for a Shutdown called on one of the share's goroutines.
func (s *runShare) release() {
	s.released.Store(true)
	s.done()
}

// join registers the calling goroutine with share, which is created (and
// counted in run.wg by the caller) when nil, and returns share.
func (ms *Server) join(run *serverRun, share *runShare) *runShare {
	if share == nil {
		share = &runShare{wg: run.wg}
	}
	id := goid.ID()
	ms.lock.Lock()
	if run.goroutines == nil {
		run.goroutines = make(map[uint64]*runShare)
	}
	run.goroutines[id] = share
	ms.lock.Unlock()
	return share
}

// leave removes the calling goroutine from run.goroutines.
func (ms *Server) leave(run *serverRun) {
	id := goid.ID()
	ms.lock.Lock()
	delete(run.goroutines, id)
	ms.lock.Unlock()
}

// Returns a new modbus server.
func NewServer(conf *ServerConfig, reqHandler RequestHandler) (
	ms *Server, err error) {
	if conf == nil {
		return nil, newConfigurationError("conf", "must not be nil")
	}
	if reqHandler == nil {
		return nil, newConfigurationError("reqHandler", "must not be nil")
	}
	var serverType string
	var splitURL []string

	ms = &Server{
		conf:    *conf,
		handler: reqHandler,
		metrics: conf.Metrics,
		wg:      &sync.WaitGroup{},
	}
	ms.tlsMetrics, _ = conf.Metrics.(TLSHandshakeMetrics)
	ms.acceptMetrics, _ = conf.Metrics.(AcceptErrorMetrics)
	ms.violationMetrics, _ = conf.Metrics.(ProtocolViolationMetrics)
	ms.connMetrics, _ = conf.Metrics.(ServerConnectionMetrics)

	splitURL = strings.SplitN(ms.conf.URL, "://", 2)
	if len(splitURL) == 2 {
		serverType = splitURL[0]
		ms.conf.URL = splitURL[1]
	}

	ms.logger = newLogger(
		fmt.Sprintf("modbus-server(%s)", ms.conf.URL), ms.conf.Logger)

	if ms.conf.URL == "" {
		ms.logger.Errorf("missing host part in URL '%s'", conf.URL)
		err = newConfigurationError("URL", "missing host")
		return
	}

	if ms.conf.FirstRequestTimeout < 0 {
		return nil, newConfigurationError("FirstRequestTimeout", "must not be negative")
	}
	if ms.conf.FrameTimeout < 0 {
		return nil, newConfigurationError("FrameTimeout", "must not be negative")
	}

	switch serverType {
	case "tcp":
		if ms.conf.Timeout == 0 {
			ms.conf.Timeout = 120 * time.Second
		}
		if ms.conf.MaxClients == 0 {
			ms.conf.MaxClients = 10
		}
		ms.transportType = modbusTCP

	case "tcp+tls":
		if ms.conf.Timeout == 0 {
			ms.conf.Timeout = 120 * time.Second
		}
		if ms.conf.MaxClients == 0 {
			ms.conf.MaxClients = 10
		}
		if ms.conf.TLSHandshakeTimeout == 0 {
			ms.conf.TLSHandshakeTimeout = defaultTLSHandshakeTimeout
		}
		if ms.conf.TLSServerCert == nil {
			ms.logger.Errorf("missing server certificate")
			err = newConfigurationError("TLSServerCert", "required for tcp+tls scheme")
			return
		}
		if ms.conf.TLSClientCAs == nil {
			ms.logger.Errorf("missing CA/client certificates")
			err = newConfigurationError("TLSClientCAs", "required for tcp+tls scheme")
			return
		}
		ms.transportType = modbusTCPOverTLS

	default:
		err = newConfigurationError("URL", fmt.Sprintf("unsupported scheme %q", serverType))
		return
	}

	return
}

// Start begins accepting client connections. It is safe to call multiple times;
// subsequent calls on an already-started server are no-ops. A server that has
// been stopped can be started again.
func (ms *Server) Start() (err error) {
	ms.lock.Lock()
	defer ms.lock.Unlock()

	if ms.started {
		return
	}

	run := &serverRun{wg: &sync.WaitGroup{}}
	run.ctx, run.cancel = context.WithCancel(context.Background())
	ms.stopCtx, ms.stopCancel = run.ctx, run.cancel

	switch ms.transportType {
	case modbusTCP, modbusTCPOverTLS:
		run.listener, err = net.Listen("tcp", ms.conf.URL)
		if err != nil {
			run.cancel()
			return
		}
		ms.tcpListener = run.listener
		// The accept loop is part of what Stop and Shutdown wait for.
		run.wg.Add(1)
		go ms.acceptTCPClients(run)

	default:
		run.cancel()
		err = ErrConfigurationError
		return
	}

	ms.run = run
	ms.wg = run.wg
	ms.started = true

	return
}

// Shutdown gracefully shuts down the server. It stops accepting new
// connections, cancels all per-connection contexts, closes client sockets,
// and waits for the accept loop and the in-flight handler goroutines to exit.
// If ctx expires before all handlers finish, Shutdown returns ctx.Err(). Use
// Stop() as a convenience wrapper that waits indefinitely.
//
// Only the first Shutdown or Stop of a Start..Stop cycle waits. A call made
// while that one is still waiting, or after it returned because its context
// ended, finds the server stopped and returns nil at once, although handler
// goroutines of the cycle may still be running; nothing else waits for them.
//
// Shutdown may be called from the application's code that the server runs: a
// RequestHandler method, a method of ServerConfig.Metrics (ServerMetrics or
// one of its optional extensions), ServerConfig.AllowClient,
// ServerConfig.PDUTap, ServerConfig.Authorizer, the Logger. It
// then waits for every goroutine of the server except the ones of the
// connection, or of the accept loop, that it was called on (see
// RequestHandler, "Stopping the server from a handler"). When ctx is already
// done once the listener and the client sockets are closed, as the context a
// handler received is (the shutdown cancels it), such a call returns ctx.Err()
// without waiting.
func (ms *Server) Shutdown(ctx context.Context) error {
	caller := goid.ID()
	ms.lock.Lock()
	if !ms.started {
		ms.lock.Unlock()
		return nil
	}
	ms.started = false
	run := ms.run
	run.cancel()

	listenErr := run.listener.Close()
	for _, sock := range ms.tcpClients {
		_ = sock.Close()
	}
	own := run.goroutines[caller]
	ms.lock.Unlock()

	if own != nil {
		// Called from the application's code on one of the cycle's goroutines:
		// do not wait for it.
		own.release()
		if err := ctx.Err(); err != nil {
			return err
		}
	}

	done := make(chan struct{})
	go func() {
		run.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return listenErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Addr returns the address the server listens on, with the port the system
// chose when the URL names port 0. It is nil before the first Start; after a
// stop it is the address the server last listened on.
func (ms *Server) Addr() net.Addr {
	ms.lock.Lock()
	defer ms.lock.Unlock()
	if ms.tcpListener == nil {
		return nil
	}
	return ms.tcpListener.Addr()
}

// Stop stops the server and blocks until all handler goroutines have exited.
// It is equivalent to Shutdown(context.Background()), and like Shutdown it
// waits only when it is the first such call of a Start..Stop cycle: a Stop
// after a Shutdown whose context ended, or while another Stop is waiting,
// returns nil at once.
//
// Called from a RequestHandler method, a method of ServerConfig.Metrics
// (ServerMetrics or one of its optional extensions),
// ServerConfig.AllowClient, ServerConfig.PDUTap, ServerConfig.Authorizer or
// the Logger,
// Stop returns once every other goroutine of the server has ended; it does
// not wait for the connection, or the accept loop, it was called on (see
// RequestHandler).
func (ms *Server) Stop() error {
	return ms.Shutdown(context.Background())
}

// ValidateServerConfig checks the server configuration and handler for errors
// without starting a server. Returns nil if valid.
func ValidateServerConfig(conf *ServerConfig, reqHandler RequestHandler) error {
	_, err := NewServer(conf, reqHandler)
	return err
}
