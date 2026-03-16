package modbus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// Modbus Role PEM OID (see R-21 of the MBAPS spec).
var modbusRoleOID asn1.ObjectIdentifier = asn1.ObjectIdentifier{
	1, 3, 6, 1, 4, 1, 50316, 802, 1,
}

// Server configuration object.
type ServerConfig struct {
	URL                 string
	Timeout             time.Duration
	MaxClients          uint
	TLSServerCert       *tls.Certificate
	TLSClientCAs        *x509.CertPool
	TLSHandshakeTimeout time.Duration
	Logger              Logger
	Metrics             ServerMetrics
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

// RequestHandler is the interface implemented by the application to serve Modbus
// requests. Each connected client is served in its own goroutine, so handler
// methods may be called concurrently from different client goroutines.
// Implementations must be safe for concurrent use (e.g. use sync.Mutex or
// other synchronization when accessing shared state).
//
// Each handler method receives a context that is cancelled when the client
// disconnects or the server stops. A panic inside any handler method is
// recovered and logged (with a stack trace); the client receives a
// ServerDeviceFailure exception response.
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
	conf          ServerConfig
	logger        *logger
	lock          sync.Mutex
	wg            sync.WaitGroup
	started       bool
	handler       RequestHandler
	metrics       ServerMetrics
	tcpListener   net.Listener
	tcpClients    []net.Conn
	transportType transportType
	stopCancel    context.CancelFunc
	stopCtx       context.Context
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
	}

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
			ms.conf.TLSHandshakeTimeout = 30 * time.Second
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
// subsequent calls on an already-started server are no-ops.
func (ms *Server) Start() (err error) {
	ms.lock.Lock()
	defer ms.lock.Unlock()

	if ms.started {
		return
	}

	ms.stopCtx, ms.stopCancel = context.WithCancel(context.Background())

	switch ms.transportType {
	case modbusTCP, modbusTCPOverTLS:
		ms.tcpListener, err = net.Listen("tcp", ms.conf.URL)
		if err != nil {
			ms.stopCancel()
			return
		}
		go ms.acceptTCPClients()

	default:
		ms.stopCancel()
		err = ErrConfigurationError
		return
	}

	ms.started = true

	return
}

// Shutdown gracefully shuts down the server. It stops accepting new
// connections, cancels all per-connection contexts, closes client sockets,
// and waits for in-flight handler goroutines to exit. If ctx expires before
// all handlers finish, Shutdown returns ctx.Err(). Use Stop() as a
// convenience wrapper that waits indefinitely.
func (ms *Server) Shutdown(ctx context.Context) error {
	ms.lock.Lock()
	if !ms.started {
		ms.lock.Unlock()
		return nil
	}
	ms.started = false
	ms.stopCancel()

	var listenErr error
	if ms.transportType == modbusTCP || ms.transportType == modbusTCPOverTLS {
		listenErr = ms.tcpListener.Close()
		for _, sock := range ms.tcpClients {
			_ = sock.Close()
		}
	}
	ms.lock.Unlock()

	done := make(chan struct{})
	go func() {
		ms.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return listenErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop stops the server and blocks until all handler goroutines have exited.
// It is equivalent to Shutdown(context.Background()).
func (ms *Server) Stop() error {
	return ms.Shutdown(context.Background())
}

// ValidateServerConfig checks the server configuration and handler for errors
// without starting a server. Returns nil if valid.
func ValidateServerConfig(conf *ServerConfig, reqHandler RequestHandler) error {
	_, err := NewServer(conf, reqHandler)
	return err
}
