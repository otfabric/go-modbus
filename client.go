// SPDX-License-Identifier: MIT

package modbus

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/otfabric/go-modbus/internal/adu"
	"github.com/otfabric/go-modbus/internal/session"
	inttrans "github.com/otfabric/go-modbus/internal/transport"
)

type Parity uint
type WordOrder uint

// Endianness for 16-bit wire encoding (re-exported from internal/adu).
type Endianness = adu.Endianness

const (
	ParityNone Parity = 0
	ParityEven Parity = 1
	ParityOdd  Parity = 2

	// endianness of 16-bit registers.
	BigEndian    = adu.BigEndian
	LittleEndian = adu.LittleEndian

	// word order of 32-bit registers.
	HighWordFirst WordOrder = 1
	LowWordFirst  WordOrder = 2
)

// Modbus client configuration object.
type Config struct {
	// URL sets the client mode and target location in the form
	// <mode>://<serial device or host:port> e.g. tcp://plc:502
	URL string
	// Speed sets the serial link speed (in bps, rtu only)
	Speed uint
	// DataBits sets the number of bits per serial character (rtu only)
	DataBits uint
	// Parity sets the serial link parity mode (rtu only)
	Parity Parity
	// StopBits sets the number of serial stop bits (rtu only)
	StopBits uint
	// Timeout sets the request timeout value
	Timeout time.Duration
	// DialTimeout sets the maximum time for establishing a connection (TCP dial,
	// TLS handshake, UDP dial). Zero uses a sensible default (5s for TCP/UDP,
	// 15s for TLS). Does not apply to serial (RTU) transports.
	DialTimeout time.Duration
	// TLSClientCert sets the client-side TLS key pair (tcp+tls only)
	TLSClientCert *tls.Certificate
	// TLSRootCAs sets the list of CA certificates used to authenticate
	// the server (tcp+tls only). Leaf (i.e. server) certificates can also
	// be used in case of self-signed certs, or if cert pinning is required.
	TLSRootCAs *x509.CertPool
	// Logger provides a custom sink for log messages.
	// If nil (default), logging is disabled (no-op logger).
	// Use NewStdLogger, NewSlogLogger, or NopLogger to build a value.
	Logger Logger

	// RetryPolicy controls whether and how failed requests are retried.
	// A nil RetryPolicy (the default) is equivalent to NoRetry() — errors are
	// returned to the caller immediately without any retry attempt.
	// Use ExponentialBackoff or NewExponentialBackoff to configure automatic retries.
	// On retry the client closes and re-dials the transport before each attempt;
	// when a connection pool is configured only the failed connection is replaced.
	RetryPolicy RetryPolicy

	// Metrics receives callbacks for every request outcome.
	// A nil Metrics (the default) disables metric collection.
	Metrics ClientMetrics

	// MinConns is the number of connections pre-warmed during Open().
	// Applies only to TCP-based transports (tcp, rtuovertcp, rtuoverudp, udp).
	// Zero disables pre-warming.
	MinConns int

	// MaxConns is the maximum number of concurrent connections maintained by the
	// internal connection pool. When > 1, multiple goroutines sharing a single
	// Client can execute requests concurrently, each on its own connection.
	// Applies only to TCP-based transports. Zero and 1 both mean a single connection
	// (no pool). Values greater than 1 allocate a pool of up to MaxConns connections.
	MaxConns int
}

// TransportConfig groups transport-related settings. Use with NewConfig
// for a structured alternative to the flat Config literal.
type TransportConfig struct {
	URL           string
	Speed         uint
	DataBits      uint
	Parity        Parity
	StopBits      uint
	DialTimeout   time.Duration
	TLSClientCert *tls.Certificate
	TLSRootCAs    *x509.CertPool
}

// ExecutionConfig groups execution and retry settings. Use with NewConfig
// for a structured alternative to the flat Config literal.
type ExecutionConfig struct {
	Timeout     time.Duration
	RetryPolicy RetryPolicy
	MinConns    int
	MaxConns    int
}

// ObservabilityConfig groups logging and metrics settings. Use with NewConfig
// for a structured alternative to the flat Config literal.
type ObservabilityConfig struct {
	Logger  Logger
	Metrics ClientMetrics
}

// NewConfig builds a Config from grouped sub-configurations. This is a
// convenience alternative to the flat Config literal for larger setups.
func NewConfig(tc TransportConfig, ec ExecutionConfig, oc ObservabilityConfig) Config {
	return Config{
		URL:           tc.URL,
		Speed:         tc.Speed,
		DataBits:      tc.DataBits,
		Parity:        tc.Parity,
		StopBits:      tc.StopBits,
		DialTimeout:   tc.DialTimeout,
		TLSClientCert: tc.TLSClientCert,
		TLSRootCAs:    tc.TLSRootCAs,
		Timeout:       ec.Timeout,
		RetryPolicy:   ec.RetryPolicy,
		MinConns:      ec.MinConns,
		MaxConns:      ec.MaxConns,
		Logger:        oc.Logger,
		Metrics:       oc.Metrics,
	}
}

// clientState holds the mutable fields of Client, guarded by lock.
type clientState struct {
	engine                    *session.Engine
	lastResponseTransactionID uint16
}

// Modbus client object.
type Client struct {
	conf          Config
	endpoint      string
	logger        *logger
	lock          sync.Mutex
	state         clientState
	transportType transportType
}

// New creates, configures and returns a modbus client object.
func New(conf Config) (mc *Client, err error) {
	var clientType string
	var splitURL []string

	mc = &Client{
		conf: conf,
	}

	splitURL = strings.SplitN(mc.conf.URL, "://", 2)
	if len(splitURL) == 2 {
		clientType = splitURL[0]
		mc.endpoint = splitURL[1]
	}

	mc.logger = newLogger(
		fmt.Sprintf("modbus-client(%s)", mc.endpoint), conf.Logger)

	switch clientType {
	case "rtu":
		// set useful defaults
		if mc.conf.Speed == 0 {
			mc.conf.Speed = 19200
		}

		// note: the "modbus over serial line v1.02" document specifies an
		// 11-bit character frame, with even parity and 1 stop bit as default,
		// and mandates the use of 2 stop bits when no parity is used.
		// This stack defaults to 8/N/2 as most devices seem to use no parity,
		// but giving 8/N/1, 8/E/1 and 8/O/1 a shot may help with serial
		// issues.
		if mc.conf.DataBits == 0 {
			mc.conf.DataBits = 8
		}

		if mc.conf.StopBits == 0 {
			if mc.conf.Parity == ParityNone {
				mc.conf.StopBits = 2
			} else {
				mc.conf.StopBits = 1
			}
		}

		if mc.conf.Timeout == 0 {
			mc.conf.Timeout = 300 * time.Millisecond
		}

		mc.transportType = modbusRTU

	case "rtuovertcp":
		if mc.conf.Speed == 0 {
			mc.conf.Speed = 19200
		}

		if mc.conf.Timeout == 0 {
			mc.conf.Timeout = 1 * time.Second
		}

		mc.transportType = modbusRTUOverTCP

	case "rtuoverudp":
		if mc.conf.Speed == 0 {
			mc.conf.Speed = 19200
		}

		if mc.conf.Timeout == 0 {
			mc.conf.Timeout = 1 * time.Second
		}

		mc.transportType = modbusRTUOverUDP

	case "tcp":
		if mc.conf.Timeout == 0 {
			mc.conf.Timeout = 1 * time.Second
		}

		mc.transportType = modbusTCP

	case "tcp+tls":
		if mc.conf.Timeout == 0 {
			mc.conf.Timeout = 1 * time.Second
		}

		// expect a client-side certificate for mutual auth as the
		// modbus/mpab protocol has no inherent auth facility.
		// (see requirements R-08 and R-19 of the MBAPS spec)
		if mc.conf.TLSClientCert == nil {
			mc.logger.Errorf("missing client certificate")
			err = newConfigurationError("TLSClientCert", "required for tcp+tls scheme")
			return
		}

		// expect a CertPool object containing at least 1 CA or
		// leaf certificate to validate the server-side cert
		if mc.conf.TLSRootCAs == nil {
			mc.logger.Errorf("missing CA/server certificate")
			err = newConfigurationError("TLSRootCAs", "required for tcp+tls scheme")
			return
		}

		mc.transportType = modbusTCPOverTLS

	case "udp":
		if mc.conf.Timeout == 0 {
			mc.conf.Timeout = 1 * time.Second
		}

		mc.transportType = modbusTCPOverUDP

	default:
		if len(splitURL) != 2 {
			mc.logger.Errorf("missing client type in URL '%s'", conf.URL)
			err = newConfigurationError("URL", fmt.Sprintf("missing scheme in %q", conf.URL))
		} else {
			mc.logger.Errorf("unsupported client type '%s'", clientType)
			err = newConfigurationError("URL", fmt.Sprintf("unsupported scheme %q", clientType))
		}
		return
	}

	if mc.conf.MaxConns > 1 && !mc.supportsPooling() {
		mc.logger.Warningf("MaxConns %d has no effect on %s transport (pooling not supported); using single connection",
			mc.conf.MaxConns, clientType)
		mc.conf.MaxConns = 1
		mc.conf.MinConns = 0
	}

	if mc.conf.MinConns > mc.conf.MaxConns && mc.conf.MaxConns > 0 {
		mc.logger.Warningf("MinConns (%d) > MaxConns (%d), clamping MinConns to MaxConns",
			mc.conf.MinConns, mc.conf.MaxConns)
		mc.conf.MinConns = mc.conf.MaxConns
	}

	return
}

// ValidateConfig checks the configuration for errors without creating a
// connection. It returns nil if the configuration is valid. Use this in CLIs
// or config-driven systems to validate early.
func ValidateConfig(conf Config) error {
	_, err := New(conf)
	return err
}

// Open opens the underlying transport (network socket or serial line).
// If MaxConns > 1 and the transport supports pooling (tcp, rtuovertcp, rtuoverudp, udp — not tcp+tls),
// a connection pool pre-warmed with MinConns connections is created; subsequent requests draw from the
// pool and may execute concurrently. For serial and tcp+tls, a single transport is used.
//
// Open is idempotent: calling it on an already-open client is a no-op and returns nil.
// A client can be re-opened after Close to establish a new connection.
func (mc *Client) Open() (err error) {
	mc.lock.Lock()
	if mc.state.engine != nil {
		mc.lock.Unlock()
		return
	}
	mc.lock.Unlock()

	usePool := mc.conf.MaxConns > 1 && mc.supportsPooling()

	var obs session.AttemptObserver
	if am, ok := mc.conf.Metrics.(AttemptMetrics); ok {
		obs = &attemptBridge{am: am}
	}

	eng := session.NewEngine(session.Config{
		Dial:     mc.dialSessionTransport,
		UsePool:  usePool,
		MinConns: mc.conf.MinConns,
		MaxConns: mc.conf.MaxConns,
		Retry:    mc.conf.RetryPolicy,
		Logger:   mc.conf.Logger,
		Attempts: obs,
	})

	if err = eng.Open(); err != nil {
		return
	}

	mc.lock.Lock()
	defer mc.lock.Unlock()
	if mc.state.engine != nil {
		_ = eng.Close()
		return
	}
	mc.state.engine = eng
	return
}

func (mc *Client) dialTimeout() time.Duration {
	if mc.conf.DialTimeout > 0 {
		return mc.conf.DialTimeout
	}
	if mc.transportType == modbusTCPOverTLS {
		return 15 * time.Second
	}
	return 5 * time.Second
}

// dialSessionTransport dials and returns a new internal transport for the session engine.
// Safe to call without mc.lock because it only reads immutable config fields set at construction time.
func (mc *Client) dialSessionTransport() (session.Transport[*adu.Request, *adu.Response], error) {
	switch mc.transportType {
	case modbusRTU:
		spw := newSerialPortWrapper(&serialPortConfig{
			Device:   mc.endpoint,
			Speed:    mc.conf.Speed,
			DataBits: mc.conf.DataBits,
			Parity:   mc.conf.Parity,
			StopBits: mc.conf.StopBits,
		})
		if err := spw.Open(); err != nil {
			return nil, err
		}
		return inttrans.NewRTU(
			spw, mc.endpoint, mc.conf.Speed, mc.conf.Timeout,
			newLogger(fmt.Sprintf("rtu-transport(%s)", mc.endpoint), mc.conf.Logger)), nil

	case modbusRTUOverTCP:
		sock, err := net.DialTimeout("tcp", mc.endpoint, mc.dialTimeout())
		if err != nil {
			return nil, err
		}
		return inttrans.NewRTU(
			sock, mc.endpoint, mc.conf.Speed, mc.conf.Timeout,
			newLogger(fmt.Sprintf("rtu-transport(%s)", mc.endpoint), mc.conf.Logger)), nil

	case modbusRTUOverUDP:
		sock, err := net.DialTimeout("udp", mc.endpoint, mc.dialTimeout())
		if err != nil {
			return nil, err
		}
		usw, err := newUDPSockWrapper(sock)
		if err != nil {
			_ = sock.Close()
			return nil, err
		}
		return inttrans.NewRTU(
			usw, mc.endpoint, mc.conf.Speed, mc.conf.Timeout,
			newLogger(fmt.Sprintf("rtu-transport(%s)", mc.endpoint), mc.conf.Logger)), nil

	case modbusTCP:
		sock, err := net.DialTimeout("tcp", mc.endpoint, mc.dialTimeout())
		if err != nil {
			return nil, err
		}
		return inttrans.NewTCP(
			sock, mc.conf.Timeout,
			newLogger(fmt.Sprintf("tcp-transport(%s)", sock.RemoteAddr()), mc.conf.Logger)), nil

	case modbusTCPOverTLS:
		sock, err := tls.DialWithDialer(
			&net.Dialer{Deadline: time.Now().Add(mc.dialTimeout())},
			"tcp", mc.endpoint,
			&tls.Config{
				Certificates: []tls.Certificate{*mc.conf.TLSClientCert},
				RootCAs:      mc.conf.TLSRootCAs,
				MinVersion:   tls.VersionTLS12,
			})
		if err != nil {
			return nil, err
		}
		if err = sock.Handshake(); err != nil {
			_ = sock.Close()
			return nil, err
		}
		return inttrans.NewTCP(
			newTLSSockWrapper(sock), mc.conf.Timeout,
			newLogger(fmt.Sprintf("tcp-transport(%s)", sock.RemoteAddr()), mc.conf.Logger)), nil

	case modbusTCPOverUDP:
		sock, err := net.DialTimeout("udp", mc.endpoint, mc.dialTimeout())
		if err != nil {
			return nil, err
		}
		usw, err := newUDPSockWrapper(sock)
		if err != nil {
			_ = sock.Close()
			return nil, err
		}
		return inttrans.NewTCP(
			usw, mc.conf.Timeout,
			newLogger(fmt.Sprintf("tcp-transport(%s)", sock.RemoteAddr()), mc.conf.Logger)), nil

	default:
		return nil, ErrConfigurationError
	}
}

// Close closes the underlying transport (or connection pool).
// It is safe to call Close multiple times; subsequent calls are no-ops.
//
// If requests are in flight when Close is called, they may fail with a
// transport error. There is no graceful drain — the underlying connections
// are closed immediately. After Close returns, the client can be re-opened
// by calling Open again.
func (mc *Client) Close() (err error) {
	mc.lock.Lock()
	defer mc.lock.Unlock()

	if mc.state.engine != nil {
		err = mc.state.engine.Close()
		mc.state.engine = nil
	}
	return
}

// LastObservedTransactionID returns the most recently observed MBAP transaction ID
// on this client instance. For RTU and other non-TCP transports it is always 0.
// In pooled/concurrent use (MaxConns > 1), this is a shared diagnostic value and
// is not correlated to any specific request.
func (mc *Client) LastObservedTransactionID() uint16 {
	mc.lock.Lock()
	defer mc.lock.Unlock()
	return mc.state.lastResponseTransactionID
}

// TransportKind describes the transport protocol in use by a Client.
type TransportKind string

const (
	TransportRTU        TransportKind = "rtu"
	TransportRTUOverTCP TransportKind = "rtuovertcp"
	TransportRTUOverUDP TransportKind = "rtuoverudp"
	TransportTCP        TransportKind = "tcp"
	TransportTCPOverTLS TransportKind = "tcp+tls"
	TransportTCPOverUDP TransportKind = "udp"
)

// ClientInfo contains read-only diagnostic information about a Client's
// current state and configuration.
type ClientInfo struct {
	// IsOpen is true when the client has an active transport/connection.
	IsOpen bool
	// Endpoint is the resolved target address (host:port or serial device path).
	Endpoint string
	// Transport identifies the transport protocol.
	Transport TransportKind
	// PoolEnabled is true when the client uses a connection pool (MaxConns > 1).
	PoolEnabled bool
	// MaxConns is the configured maximum connection count.
	MaxConns int
}

// Info returns a snapshot of the client's current diagnostic state.
// It is safe for concurrent use.
func (mc *Client) Info() ClientInfo {
	mc.lock.Lock()
	defer mc.lock.Unlock()
	return ClientInfo{
		IsOpen:      mc.state.engine != nil,
		Endpoint:    mc.endpoint,
		Transport:   mc.transportKind(),
		PoolEnabled: mc.conf.MaxConns > 1 && mc.supportsPooling(),
		MaxConns:    mc.conf.MaxConns,
	}
}

func (mc *Client) supportsPooling() bool {
	return mc.transportType == modbusTCP ||
		mc.transportType == modbusRTUOverTCP ||
		mc.transportType == modbusRTUOverUDP ||
		mc.transportType == modbusTCPOverUDP
}

func (mc *Client) transportKind() TransportKind {
	switch mc.transportType {
	case modbusRTU:
		return TransportRTU
	case modbusRTUOverTCP:
		return TransportRTUOverTCP
	case modbusRTUOverUDP:
		return TransportRTUOverUDP
	case modbusTCP:
		return TransportTCP
	case modbusTCPOverTLS:
		return TransportTCPOverTLS
	case modbusTCPOverUDP:
		return TransportTCPOverUDP
	default:
		return ""
	}
}

// attemptBridge adapts the public AttemptMetrics interface to the internal
// session.AttemptObserver interface.
type attemptBridge struct{ am AttemptMetrics }

func (ab *attemptBridge) OnAttempt(unitID uint8, fc byte, attempt int, d time.Duration, err error) {
	ab.am.OnAttempt(unitID, FunctionCode(fc), attempt, d, err)
}

func (ab *attemptBridge) OnRetryDial(attempt int, d time.Duration, err error) {
	ab.am.OnRetryDial(attempt, d, err)
}
