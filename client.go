package modbus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

type Parity uint
type RegType uint
type Endianness uint
type WordOrder uint

const (
	ParityNone Parity = 0
	ParityEven Parity = 1
	ParityOdd  Parity = 2

	HoldingRegister RegType = 0
	InputRegister   RegType = 1

	// endianness of 16-bit registers.
	BigEndian    Endianness = 1
	LittleEndian Endianness = 2

	// word order of 32-bit registers.
	HighWordFirst WordOrder = 1
	LowWordFirst  WordOrder = 2
)

// Modbus client configuration object.
type ClientConfiguration struct {
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
	// TLSClientCert sets the client-side TLS key pair (tcp+tls only)
	TLSClientCert *tls.Certificate
	// TLSRootCAs sets the list of CA certificates used to authenticate
	// the server (tcp+tls only). Leaf (i.e. server) certificates can also
	// be used in case of self-signed certs, or if cert pinning is required.
	TLSRootCAs *x509.CertPool
	// Logger provides a custom sink for log messages.
	// If nil, the slog default logger (slog.Default()) is used.
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
	// ModbusClient can execute requests concurrently, each on its own connection.
	// Applies only to TCP-based transports. Zero and 1 both mean a single connection
	// (no pool). Values greater than 1 allocate a pool of up to MaxConns connections.
	MaxConns int
}

// Modbus client object.
type ModbusClient struct {
	conf                      ClientConfiguration
	logger                    *logger
	lock                      sync.Mutex
	isOpen                    bool
	transport                 transport
	transportType             transportType
	pool                      *connPool // non-nil when MaxConns > 1 and transport uses pool (tcp/rtuovertcp/rtuoverudp/udp)
	lastResponseTransactionID uint16    // last MBAP transaction ID from response (TCP only; 0 for RTU)
}

// DeviceIdentificationObject represents one object from an FC43/MEI response.
type DeviceIdentificationObject struct {
	Id    uint8
	Name  string
	Value string
}

// DeviceIdentification groups all decoded data from an FC43/MEI response.
type DeviceIdentification struct {
	ReadDeviceIdCode uint8
	ConformityLevel  uint8
	MoreFollows      uint8
	NextObjectId     uint8
	Objects          []DeviceIdentificationObject
}

// DiagnosticSubFunction is the two-byte sub-function code for Diagnostics (FC 0x08).
// Use the constants below for well-known sub-functions; raw uint16 values are valid.
type DiagnosticSubFunction uint16

const (
	DiagReturnQueryData                  DiagnosticSubFunction = 0x0000 // Loopback request data
	DiagRestartCommunications            DiagnosticSubFunction = 0x0001
	DiagReturnDiagnosticRegister         DiagnosticSubFunction = 0x0002
	DiagChangeASCIIInputDelimiter        DiagnosticSubFunction = 0x0003
	DiagForceListenOnlyMode              DiagnosticSubFunction = 0x0004
	DiagClearCountersAndDiagnosticReg    DiagnosticSubFunction = 0x000A
	DiagReturnBusMessageCount            DiagnosticSubFunction = 0x000B
	DiagReturnBusCommunicationErrorCount DiagnosticSubFunction = 0x000C
	DiagReturnBusExceptionErrorCount     DiagnosticSubFunction = 0x000D
	DiagReturnServerMessageCount         DiagnosticSubFunction = 0x000E
	DiagReturnServerNoResponseCount      DiagnosticSubFunction = 0x000F
	DiagReturnServerNAKCount             DiagnosticSubFunction = 0x0010
	DiagReturnServerBusyCount            DiagnosticSubFunction = 0x0011
	DiagReturnBusCharacterOverrunCount   DiagnosticSubFunction = 0x0012
	DiagClearOverrunCounterAndFlag       DiagnosticSubFunction = 0x0014
)

// String returns a short name for the sub-function for logging and debugging.
func (s DiagnosticSubFunction) String() string {
	switch s {
	case DiagReturnQueryData:
		return "ReturnQueryData"
	case DiagRestartCommunications:
		return "RestartCommunications"
	case DiagReturnDiagnosticRegister:
		return "ReturnDiagnosticRegister"
	case DiagChangeASCIIInputDelimiter:
		return "ChangeASCIIInputDelimiter"
	case DiagForceListenOnlyMode:
		return "ForceListenOnlyMode"
	case DiagClearCountersAndDiagnosticReg:
		return "ClearCountersAndDiagnosticReg"
	case DiagReturnBusMessageCount:
		return "ReturnBusMessageCount"
	case DiagReturnBusCommunicationErrorCount:
		return "ReturnBusCommunicationErrorCount"
	case DiagReturnBusExceptionErrorCount:
		return "ReturnBusExceptionErrorCount"
	case DiagReturnServerMessageCount:
		return "ReturnServerMessageCount"
	case DiagReturnServerNoResponseCount:
		return "ReturnServerNoResponseCount"
	case DiagReturnServerNAKCount:
		return "ReturnServerNAKCount"
	case DiagReturnServerBusyCount:
		return "ReturnServerBusyCount"
	case DiagReturnBusCharacterOverrunCount:
		return "ReturnBusCharacterOverrunCount"
	case DiagClearOverrunCounterAndFlag:
		return "ClearOverrunCounterAndFlag"
	default:
		return fmt.Sprintf("DiagnosticSubFunction(0x%04X)", uint16(s))
	}
}

// DiagnosticResponse is the response from Diagnostics (FC 0x08). SubFunction is
// echoed from the request; Data is the sub-function-specific data (e.g. loopback
// data, diagnostic register value).
type DiagnosticResponse struct {
	SubFunction DiagnosticSubFunction
	Data        []byte
}

// ReportServerIdResponse is the response from Report Server ID (FC 0x11).
// ByteCount is the number of following bytes; Data contains device-specific
// server ID, run indicator status (0x00 = OFF, 0xFF = ON), and optional additional data.
type ReportServerIdResponse struct {
	ByteCount uint8
	Data      []byte
}

// FileRecordRequest describes one sub-request for ReadFileRecords (FC20).
// Each sub-request reads a contiguous slice of registers from a single file.
type FileRecordRequest struct {
	FileNumber   uint16 // file number (1–0xFFFF)
	RecordNumber uint16 // starting record number within the file (0–0x270F)
	RecordLength uint16 // number of 16-bit registers to read (≥ 1)
}

// FileRecord describes one sub-request for WriteFileRecords (FC21).
// Each record writes a contiguous slice of registers to a single file.
// The record length is implied by len(Data).
type FileRecord struct {
	FileNumber   uint16   // file number (1–0xFFFF)
	RecordNumber uint16   // starting record number within the file (0–0x270F)
	Data         []uint16 // register values to write (len gives record length)
}

// objectDescription returns a descriptive label for known device ID object IDs.
func objectDescription(id byte) string {
	switch {
	case id == 0x00:
		return "VendorName"
	case id == 0x01:
		return "ProductCode"
	case id == 0x02:
		return "MajorMinorRevision"
	case id == 0x03:
		return "VendorUrl"
	case id == 0x04:
		return "ProductName"
	case id == 0x05:
		return "ModelName"
	case id == 0x06:
		return "UserApplicationName"
	case id >= 0x07 && id <= 0x7F:
		return "Reserved"
	case id >= 0x80:
		return "Extended"
	default:
		return ""
	}
}

// NewClient creates, configures and returns a modbus client object.
func NewClient(conf *ClientConfiguration) (mc *ModbusClient, err error) {
	if conf == nil {
		return nil, ErrConfigurationError
	}
	var clientType string
	var splitURL []string

	mc = &ModbusClient{
		conf: *conf,
	}

	splitURL = strings.SplitN(mc.conf.URL, "://", 2)
	if len(splitURL) == 2 {
		clientType = splitURL[0]
		mc.conf.URL = splitURL[1]
	}

	mc.logger = newLogger(
		fmt.Sprintf("modbus-client(%s)", mc.conf.URL), conf.Logger)

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
			err = ErrConfigurationError
			return
		}

		// expect a CertPool object containing at least 1 CA or
		// leaf certificate to validate the server-side cert
		if mc.conf.TLSRootCAs == nil {
			mc.logger.Errorf("missing CA/server certificate")
			err = ErrConfigurationError
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
			mc.logger.Errorf("missing client type in URL '%s'", mc.conf.URL)
		} else {
			mc.logger.Errorf("unsupported client type '%s'", clientType)
		}
		err = ErrConfigurationError
		return
	}

	return
}

// Opens the underlying transport (network socket or serial line).
// If MaxConns > 1 and the transport uses the pool (tcp, rtuovertcp, rtuoverudp, udp — not tcp+tls), a connection pool pre-warmed
// with MinConns connections is created; subsequent requests draw from the pool and
// may execute concurrently. For serial and tcp+tls, a single transport is used.
func (mc *ModbusClient) Open() (err error) {
	mc.lock.Lock()
	defer mc.lock.Unlock()

	if mc.isOpen {
		// already open — idempotent no-op
		return
	}

	// Connection pool: MaxConns > 1 for tcp, rtuovertcp, rtuoverudp, udp (not tcp+tls).
	usePool := mc.conf.MaxConns > 1 &&
		(mc.transportType == modbusTCP ||
			mc.transportType == modbusRTUOverTCP ||
			mc.transportType == modbusRTUOverUDP ||
			mc.transportType == modbusTCPOverUDP)

	if usePool {
		mc.pool, err = newConnPool(
			mc.conf.MinConns, mc.conf.MaxConns, mc.dialTransport, mc.logger)
		if err != nil {
			return
		}
		mc.transport = nil
	} else {
		mc.transport, err = mc.dialTransport()
		if err != nil {
			return
		}
		mc.pool = nil
	}

	mc.isOpen = true
	return
}

// dialTransport dials and returns a single new transport.
// Callers must hold mc.lock (or otherwise ensure exclusive access to mc.conf).
// It is used both by Open() and by the connection pool factory.
func (mc *ModbusClient) dialTransport() (t transport, err error) {
	var spw *serialPortWrapper
	var sock net.Conn

	switch mc.transportType {
	case modbusRTU:
		// create a serial port wrapper object
		spw = newSerialPortWrapper(&serialPortConfig{
			Device:   mc.conf.URL,
			Speed:    mc.conf.Speed,
			DataBits: mc.conf.DataBits,
			Parity:   mc.conf.Parity,
			StopBits: mc.conf.StopBits,
		})

		// open the serial device
		err = spw.Open()
		if err != nil {
			return
		}

		// discard potentially stale serial data
		discard(spw)

		// create the RTU transport
		t = newRTUTransport(
			spw, mc.conf.URL, mc.conf.Speed, mc.conf.Timeout, mc.conf.Logger)

	case modbusRTUOverTCP:
		// connect to the remote host
		sock, err = net.DialTimeout("tcp", mc.conf.URL, 5*time.Second)
		if err != nil {
			return
		}

		// discard potentially stale serial data
		discard(sock)

		// create the RTU transport
		t = newRTUTransport(
			sock, mc.conf.URL, mc.conf.Speed, mc.conf.Timeout, mc.conf.Logger)

	case modbusRTUOverUDP:
		// open a socket to the remote host (note: no actual connection is
		// being made as UDP is connection-less)
		sock, err = net.DialTimeout("udp", mc.conf.URL, 5*time.Second)
		if err != nil {
			return
		}

		// create the RTU transport, wrapping the UDP socket in
		// an adapter to allow the transport to read the stream of
		// packets byte per byte
		t = newRTUTransport(
			newUDPSockWrapper(sock),
			mc.conf.URL, mc.conf.Speed, mc.conf.Timeout, mc.conf.Logger)

	case modbusTCP:
		// connect to the remote host
		sock, err = net.DialTimeout("tcp", mc.conf.URL, 5*time.Second)
		if err != nil {
			return
		}

		// create the TCP transport
		t = newTCPTransport(sock, mc.conf.Timeout, mc.conf.Logger)

	case modbusTCPOverTLS:
		// connect to the remote host with TLS
		sock, err = tls.DialWithDialer(
			&net.Dialer{
				Deadline: time.Now().Add(15 * time.Second),
			}, "tcp", mc.conf.URL,
			&tls.Config{
				Certificates: []tls.Certificate{
					*mc.conf.TLSClientCert,
				},
				RootCAs: mc.conf.TLSRootCAs,
				// mandate TLS 1.2 or higher (see R-01 of the MBAPS spec)
				MinVersion: tls.VersionTLS12,
			})
		if err != nil {
			return
		}

		// force the TLS handshake
		err = sock.(*tls.Conn).Handshake()
		if err != nil {
			_ = sock.Close()
			return
		}

		// create the TCP transport, wrapping the TLS socket in
		// an adapter to work around write timeouts corrupting internal
		// state (see https://pkg.go.dev/crypto/tls#Conn.SetWriteDeadline)
		t = newTCPTransport(
			newTLSSockWrapper(sock), mc.conf.Timeout, mc.conf.Logger)

	case modbusTCPOverUDP:
		// open a socket to the remote host (note: no actual connection is
		// being made as UDP is connection-less)
		sock, err = net.DialTimeout("udp", mc.conf.URL, 5*time.Second)
		if err != nil {
			return
		}

		// create the TCP transport, wrapping the UDP socket in
		// an adapter to allow the transport to read the stream of
		// packets byte per byte
		t = newTCPTransport(
			newUDPSockWrapper(sock), mc.conf.Timeout, mc.conf.Logger)

	default:
		// should never happen
		err = ErrConfigurationError
	}

	return
}

// Closes the underlying transport (or connection pool).
func (mc *ModbusClient) Close() (err error) {
	mc.lock.Lock()
	defer mc.lock.Unlock()

	if mc.pool != nil {
		err = mc.pool.closeAll()
		mc.pool = nil
	}
	if mc.transport != nil {
		if cerr := mc.transport.Close(); err == nil {
			err = cerr
		}
		mc.transport = nil
	}
	mc.isOpen = false
	return
}

// LastTransactionID returns the most recently observed MBAP transaction ID on this client instance.
// For RTU and other non-TCP transports it is always 0. In pooled/concurrent use (MaxConns > 1),
// this is a shared diagnostic value and is not request-specific.
func (mc *ModbusClient) LastTransactionID() uint16 {
	mc.lock.Lock()
	defer mc.lock.Unlock()
	return mc.lastResponseTransactionID
}

// Reads multiple coils (function code 01).
func (mc *ModbusClient) ReadCoils(ctx context.Context, unitID uint8, addr uint16, quantity uint16) (values []bool, err error) {
	mc.lock.Lock()
	defer mc.lock.Unlock()

	values, err = mc.readBools(ctx, unitID, addr, quantity, false)

	return
}

// Reads a single coil (function code 01).
func (mc *ModbusClient) ReadCoil(ctx context.Context, unitID uint8, addr uint16) (value bool, err error) {
	mc.lock.Lock()
	defer mc.lock.Unlock()

	var values []bool

	values, err = mc.readBools(ctx, unitID, addr, 1, false)
	if err == nil {
		value = values[0]
	}

	return
}

// Reads multiple discrete inputs (function code 02).
func (mc *ModbusClient) ReadDiscreteInputs(ctx context.Context, unitID uint8, addr uint16, quantity uint16) (values []bool, err error) {
	mc.lock.Lock()
	defer mc.lock.Unlock()

	values, err = mc.readBools(ctx, unitID, addr, quantity, true)

	return
}

// Reads a single discrete input (function code 02).
func (mc *ModbusClient) ReadDiscreteInput(ctx context.Context, unitID uint8, addr uint16) (value bool, err error) {
	mc.lock.Lock()
	defer mc.lock.Unlock()

	var values []bool

	values, err = mc.readBools(ctx, unitID, addr, 1, true)
	if err == nil {
		value = values[0]
	}

	return
}

// Reads multiple 16-bit registers (function code 03 or 04).
func (mc *ModbusClient) ReadRegisters(ctx context.Context, unitID uint8, addr uint16, quantity uint16, regType RegType) (values []uint16, err error) {
	mc.lock.Lock()
	defer mc.lock.Unlock()

	var mbPayload []byte

	// read quantity uint16 registers, as bytes
	mbPayload, err = mc.readRegisters(ctx, unitID, addr, quantity, regType)
	if err != nil {
		return
	}

	// decode payload bytes as uint16s
	values = bytesToUint16s(BigEndian, mbPayload)

	return
}

// Reads a single 16-bit register (function code 03 or 04).
func (mc *ModbusClient) ReadRegister(ctx context.Context, unitID uint8, addr uint16, regType RegType) (value uint16, err error) {
	mc.lock.Lock()
	defer mc.lock.Unlock()

	var mbPayload []byte

	// read 1 uint16 register, as bytes
	mbPayload, err = mc.readRegisters(ctx, unitID, addr, 1, regType)
	if err == nil {
		value = bytesToUint16s(BigEndian, mbPayload)[0]
	}

	return
}

// ReadRawBytes is a transport convenience helper: it reads one or more 16-bit
// registers (FC03/FC04) as raw bytes. quantity is the number of bytes to read
// (the library reads ceil(quantity/2) registers). No interpretation or reordering
// is applied; bytes are returned exactly as on the wire. For typed interpretation
// use ReadWithCodec or ReadWithRuntimeCodec. Odd quantity is valid: the library
// reads ceil(quantity/2) registers and returns exactly quantity bytes, trimming
// the final padding byte when needed.
func (mc *ModbusClient) ReadRawBytes(ctx context.Context, unitID uint8, addr uint16, quantity uint16, regType RegType) (values []byte, err error) {
	mc.lock.Lock()
	defer mc.lock.Unlock()

	values, err = mc.readBytes(ctx, unitID, addr, quantity, regType)

	return
}

const maxRegisterBitIndex = 15

// ReadRegisterBit reads one register (FC03/FC04) and returns the value of the bit at bitIndex (0 = LSB, 15 = MSB).
// Useful for status bits, alarm bits, and enums packed in a single register.
func (mc *ModbusClient) ReadRegisterBit(ctx context.Context, unitID uint8, addr uint16, bitIndex uint8, regType RegType) (bool, error) {
	if bitIndex > maxRegisterBitIndex {
		return false, ErrUnexpectedParameters
	}
	reg, err := mc.ReadRegister(ctx, unitID, addr, regType)
	if err != nil {
		return false, err
	}
	return (reg>>bitIndex)&1 != 0, nil
}

// ReadRegisterBits reads one register (FC03/FC04) and returns count bits starting at bitIndex (0 = LSB).
// count must be 1–16 and bitIndex+count must not exceed 16. Useful for multi-bit fields (e.g. mode enums).
func (mc *ModbusClient) ReadRegisterBits(ctx context.Context, unitID uint8, addr uint16, bitIndex, count uint8, regType RegType) ([]bool, error) {
	if count == 0 || count > 16 || bitIndex > maxRegisterBitIndex || uint16(bitIndex)+uint16(count) > 16 {
		return nil, ErrUnexpectedParameters
	}
	reg, err := mc.ReadRegister(ctx, unitID, addr, regType)
	if err != nil {
		return nil, err
	}
	out := make([]bool, count)
	for i := uint8(0); i < count; i++ {
		out[i] = (reg>>(bitIndex+i))&1 != 0
	}
	return out, nil
}

// WriteRegisterBit reads the register at addr (FC03), sets or clears the bit at bitIndex (0 = LSB, 15 = MSB),
// and writes the result back (FC16). Other bits are unchanged. Only holding registers are written.
func (mc *ModbusClient) WriteRegisterBit(ctx context.Context, unitID uint8, addr uint16, bitIndex uint8, value bool) error {
	if bitIndex > maxRegisterBitIndex {
		return ErrUnexpectedParameters
	}
	mc.lock.Lock()
	defer mc.lock.Unlock()
	mbPayload, err := mc.readRegisters(ctx, unitID, addr, 1, HoldingRegister)
	if err != nil {
		return err
	}
	reg := bytesToUint16s(BigEndian, mbPayload)[0]
	if value {
		reg |= 1 << bitIndex
	} else {
		reg &^= 1 << bitIndex
	}
	return mc.writeRegisters(ctx, unitID, addr, uint16ToBytes(BigEndian, reg))
}

// UpdateRegisterMask performs a client-side read-modify-write on a single holding register:
// newVal = (old & ^mask) | (value & mask). It uses FC03 then FC16, not protocol FC22 (Mask Write Register).
// Only the bits set in mask are updated; others are preserved. Useful for control words and mode fields without clobbering adjacent bits.
func (mc *ModbusClient) UpdateRegisterMask(ctx context.Context, unitID uint8, addr uint16, mask, value uint16) error {
	mc.lock.Lock()
	defer mc.lock.Unlock()
	mbPayload, err := mc.readRegisters(ctx, unitID, addr, 1, HoldingRegister)
	if err != nil {
		return err
	}
	old := bytesToUint16s(BigEndian, mbPayload)[0]
	newVal := (old & ^mask) | (value & mask)
	return mc.writeRegisters(ctx, unitID, addr, uint16ToBytes(BigEndian, newVal))
}

// ReadDeviceIdentification reads device identification objects using FC43 / MEI type 0x0E.
// It automatically pages through MoreFollows and returns all objects for the requested category.
//
// readDeviceIdCode selects the category (use constants from this package):
//   - ReadDeviceIdBasic (0x01): VendorName, ProductCode, MajorMinorRevision (mandatory)
//   - ReadDeviceIdRegular (0x02): Basic + VendorUrl, ProductName, ModelName, UserApplicationName
//   - ReadDeviceIdExtended (0x03): Regular + private/vendor objects (0x80–0xFF)
//   - ReadDeviceIdIndividual (0x04): single object by objectId (objectId must be set)
//
// For objectId use 0x00 to start from the first object (stream access); for Individual,
// pass the desired object ID. The device responds at its conformity level if a higher
// category is requested (e.g. requesting Extended on a basic-only device returns Basic).
func (mc *ModbusClient) ReadDeviceIdentification(ctx context.Context, unitID uint8, readDeviceIdCode uint8, objectId uint8) (di *DeviceIdentification, err error) {
	var req *pdu
	var res *pdu
	var offset int
	var objId uint8
	var objLen uint8
	var objCount int
	var allObjs []DeviceIdentificationObject
	var nextObjId uint8
	var conformityLevel uint8
	var readDeviceIdCodeResp uint8

	mc.lock.Lock()
	defer mc.lock.Unlock()

	if readDeviceIdCode < 0x01 || readDeviceIdCode > 0x04 {
		err = ErrUnexpectedParameters
		mc.logger.Errorf("unexpected read device id code (%v)", readDeviceIdCode)
		return
	}

	nextObjId = objectId

	for {
		req = &pdu{
			unitID:       unitID,
			functionCode: FCEncapsulatedInterface,
			payload:      []byte{byte(MEIReadDeviceIdentification), readDeviceIdCode, nextObjId},
		}

		res, err = mc.executeRequest(ctx, req)
		if err != nil {
			return
		}

		switch res.functionCode {
		case req.functionCode:
			if len(res.payload) < 6 {
				err = ErrProtocolError
				return
			}

			if res.payload[0] != byte(MEIReadDeviceIdentification) {
				err = ErrProtocolError
				return
			}

			readDeviceIdCodeResp = res.payload[1]
			conformityLevel = res.payload[2]
			objCount = int(res.payload[5])
			offset = 6

			for i := 0; i < objCount; i++ {
				if offset+2 > len(res.payload) {
					err = ErrProtocolError
					return
				}

				objId = res.payload[offset]
				objLen = res.payload[offset+1]
				offset += 2

				if offset+int(objLen) > len(res.payload) {
					err = ErrProtocolError
					return
				}

				allObjs = append(allObjs, DeviceIdentificationObject{
					Id:    objId,
					Name:  objectDescription(objId),
					Value: string(res.payload[offset : offset+int(objLen)]),
				})

				offset += int(objLen)
			}

			if offset != len(res.payload) {
				err = ErrProtocolError
				return
			}

			// MoreFollows == 0xFF: the device has more objects; loop with NextObjectId.
			if res.payload[3] == 0xff {
				nextObjId = res.payload[4]
				continue
			}

			// MoreFollows == 0x00: final page — assemble and return.
			di = &DeviceIdentification{
				ReadDeviceIdCode: readDeviceIdCodeResp,
				ConformityLevel:  conformityLevel,
				MoreFollows:      res.payload[3],
				NextObjectId:     res.payload[4],
				Objects:          allObjs,
			}
			return

		case FunctionCode(uint8(req.functionCode) | 0x80):
			if len(res.payload) != 1 {
				err = ErrProtocolError
				return
			}

			err = mapExceptionCodeToError(req.functionCode, ExceptionCode(res.payload[0]))
			return

		default:
			err = ErrProtocolError
			mc.logger.Warningf("unexpected response code (%v)", res.functionCode)
			return
		}
	}
}

// ReadAllDeviceIdentification reads all device identification the unit supports:
// basic, regular, and extended (FC43 / MEI 0x0E). It requests the Extended category
// (ReadDeviceIdExtended); the device responds with all objects it implements, up to
// its conformity level. Use this when you want a single, complete snapshot of
// device identification without calling ReadDeviceIdentification multiple times.
func (mc *ModbusClient) ReadAllDeviceIdentification(ctx context.Context, unitID uint8) (*DeviceIdentification, error) {
	return mc.ReadDeviceIdentification(ctx, unitID, ReadDeviceIdExtended, 0x00)
}

// detectionProbe is one entry in the probe set used by HasUnitReadFunction.
type detectionProbe struct {
	fc       FunctionCode
	payload  []byte
	validate func(req, res *pdu) bool
}

// isValidModbusException returns true when res is a well-formed Modbus exception:
// function code equals req FC | 0x80 and payload is a single byte in the valid
// exception code range (0x01–0x0B).
func isValidModbusException(req, res *pdu) bool {
	return res.functionCode == FunctionCode(uint8(req.functionCode)|0x80) &&
		len(res.payload) == 1 &&
		res.payload[0] >= 0x01 && res.payload[0] <= 0x0b
}

// allDetectionProbes returns the full ordered probe table.
// Each probe carries its own structural validator so that detection is
// function-aware and rejects non-Modbus traffic (e.g. TCP echo services,
// HTTP on port 502, random binary protocols).
func allDetectionProbes() []detectionProbe {
	return []detectionProbe{
		// FC08 Diagnostics: sub-function 0x0000 (Return Query Data) with test data.
		// Only exception responses count as positive detection; a normal loopback
		// echo is indistinguishable from a TCP echo service at the PDU level.
		{
			fc:      FCDiagnostics,
			payload: []byte{0x00, 0x00, 0x12, 0x34},
			validate: func(req, res *pdu) bool {
				return isValidModbusException(req, res)
			},
		},
		// FC43 Read Device Identification (Basic category, starting at object 0).
		{
			fc:      FCEncapsulatedInterface,
			payload: []byte{byte(MEIReadDeviceIdentification), ReadDeviceIdBasic, 0x00},
			validate: func(req, res *pdu) bool {
				if isValidModbusException(req, res) {
					return true
				}
				// Normal FC43: MEI + readCode + conformity + moreFollows + nextObjId + objCount = 6 min.
				return res.functionCode == req.functionCode && len(res.payload) >= 6
			},
		},
		// FC03 Read Holding Registers (addr 0, qty 1).
		{
			fc:      FCReadHoldingRegisters,
			payload: append(uint16ToBytes(BigEndian, 0), uint16ToBytes(BigEndian, 1)...),
			validate: func(req, res *pdu) bool {
				if isValidModbusException(req, res) {
					return true
				}
				// Normal FC03: byte-count (2) + 2 data bytes = 3 bytes.
				return res.functionCode == req.functionCode &&
					len(res.payload) == 3 && res.payload[0] == 2
			},
		},
		// FC04 Read Input Registers (addr 0, qty 1).
		{
			fc:      FCReadInputRegisters,
			payload: append(uint16ToBytes(BigEndian, 0), uint16ToBytes(BigEndian, 1)...),
			validate: func(req, res *pdu) bool {
				if isValidModbusException(req, res) {
					return true
				}
				// Normal FC04: byte-count (2) + 2 data bytes = 3 bytes.
				return res.functionCode == req.functionCode &&
					len(res.payload) == 3 && res.payload[0] == 2
			},
		},
		// FC01 Read Coils (addr 0, qty 1).
		{
			fc:      FCReadCoils,
			payload: append(uint16ToBytes(BigEndian, 0), uint16ToBytes(BigEndian, 1)...),
			validate: func(req, res *pdu) bool {
				if isValidModbusException(req, res) {
					return true
				}
				// Normal FC01: byte-count (1) + 1 data byte = 2 bytes.
				return res.functionCode == req.functionCode &&
					len(res.payload) == 2 && res.payload[0] == 1
			},
		},
		// FC02 Read Discrete Inputs (addr 0, qty 1).
		{
			fc:      FCReadDiscreteInputs,
			payload: append(uint16ToBytes(BigEndian, 0), uint16ToBytes(BigEndian, 1)...),
			validate: func(req, res *pdu) bool {
				if isValidModbusException(req, res) {
					return true
				}
				// Normal FC02: byte-count (1) + 1 data byte = 2 bytes.
				return res.functionCode == req.functionCode &&
					len(res.payload) == 2 && res.payload[0] == 1
			},
		},
		// FC11 Report Server ID (no request data).
		{
			fc:      FCReportServerID,
			payload: nil,
			validate: func(req, res *pdu) bool {
				if isValidModbusException(req, res) {
					return true
				}
				// Normal FC11: byte count (1) + data; spec has at least server ID + run indicator (2 bytes).
				// Reject echo (which would be [unitID, 0x11], so byte count 1 and len 2).
				if res.functionCode != req.functionCode || len(res.payload) < 2 {
					return false
				}
				byteCount := res.payload[0]
				return int(byteCount) == len(res.payload)-1 && byteCount >= 2
			},
		},
		// FC18 Read FIFO Queue (FIFO pointer addr 0).
		{
			fc:      FCReadFIFOQueue,
			payload: uint16ToBytes(BigEndian, 0),
			validate: func(req, res *pdu) bool {
				if isValidModbusException(req, res) {
					return true
				}
				// Normal FC18: byte count (2) + FIFO count (2) + data.
				return res.functionCode == req.functionCode && len(res.payload) >= 4
			},
		},
		// FC20 Read File Record (one sub-request: file 1, record 0, length 1).
		{
			fc:      FCReadFileRecord,
			payload: []byte{7, 0x06, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01}, // byte count 7; refType 6, file 1, rec 0, len 1
			validate: func(req, res *pdu) bool {
				if isValidModbusException(req, res) {
					return true
				}
				// Normal FC20: byte count (1) + refType (1) + data (2*recordLen). Our probe expects one record → 4 bytes.
				// Reject echo (8 bytes, payload[0]==7) by requiring the expected response size.
				if res.functionCode != req.functionCode || len(res.payload) < 4 {
					return false
				}
				byteCount := res.payload[0]
				return int(byteCount) == len(res.payload)-1 && byteCount == 3
			},
		},
	}
}

// getProbeForFC returns the detection probe for the given function code, if defined.
func getProbeForFC(fc FunctionCode) (detectionProbe, bool) {
	for _, p := range allDetectionProbes() {
		if p.fc == fc {
			return p, true
		}
	}
	return detectionProbe{}, false
}

// runOneProbe runs a single detection probe. Caller must hold mc.lock.
// Returns (true, nil) on valid response, (false, nil) on timeout/invalid, (false, err) on context/transport error.
func (mc *ModbusClient) runOneProbe(ctx context.Context, unitID uint8, p detectionProbe) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}
	req := &pdu{unitID: unitID, functionCode: p.fc, payload: p.payload}
	res, err := mc.executeRequest(ctx, req)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false, err
		}
		return false, nil
	}
	return p.validate(req, res), nil
}

// HasUnitReadFunction probes the given unit with a single read-style function code and returns whether
// the unit responded with a structurally valid Modbus response (normal or exception). Use after Open().
// Only FCs that have a detection probe are supported: FC08, FC43, FC03, FC04, FC01, FC02, FC11, FC18, FC20.
// For an unsupported fc, returns (false, ErrUnexpectedParameters).
func (mc *ModbusClient) HasUnitReadFunction(ctx context.Context, unitID uint8, fc FunctionCode) (bool, error) {
	mc.lock.Lock()
	defer mc.lock.Unlock()
	p, ok := getProbeForFC(fc)
	if !ok {
		return false, ErrUnexpectedParameters
	}
	return mc.runOneProbe(ctx, unitID, p)
}

// HasUnitIdentifyFunction reports whether the given unit supports Read Device Identification (FC43).
// It is equivalent to HasUnitReadFunction(ctx, unitID, FCEncapsulatedInterface). Use after Open().
func (mc *ModbusClient) HasUnitIdentifyFunction(ctx context.Context, unitID uint8) (bool, error) {
	return mc.HasUnitReadFunction(ctx, unitID, FCEncapsulatedInterface)
}

// Writes a single coil (function code 05).
func (mc *ModbusClient) WriteCoil(ctx context.Context, unitID uint8, addr uint16, value bool) (err error) {
	var payload uint16

	mc.lock.Lock()
	defer mc.lock.Unlock()

	if value {
		payload = 0xff00
	} else {
		payload = 0x0000
	}

	err = mc.writeCoil(ctx, unitID, addr, payload)

	return
}

// Sends a write coil request (function code 05) with a specific payload
// value instead of the standard 0xff00 (true) or 0x0000 (false).
// This is a violation of the modbus spec and should almost never be necessary,
// but a handful of vendors seem to be hiding various DO/coil control modes
// behind it (e.g. toggle, interlock, delayed open/close, etc.).
func (mc *ModbusClient) WriteCoilValue(ctx context.Context, unitID uint8, addr uint16, payload uint16) (err error) {
	mc.lock.Lock()
	defer mc.lock.Unlock()

	err = mc.writeCoil(ctx, unitID, addr, payload)

	return
}

// Writes multiple coils (function code 15).
func (mc *ModbusClient) WriteCoils(ctx context.Context, unitID uint8, addr uint16, values []bool) (err error) {
	var req *pdu
	var res *pdu
	var quantity uint16
	var encodedValues []byte

	mc.lock.Lock()
	defer mc.lock.Unlock()

	quantity = uint16(len(values))
	if quantity == 0 {
		err = ErrUnexpectedParameters
		mc.logger.Error("quantity of coils is 0")
		return
	}

	if quantity > maxWriteCoils {
		err = ErrUnexpectedParameters
		mc.logger.Errorf("quantity of coils exceeds %v", maxWriteCoils)
		return
	}

	if uint32(addr)+uint32(quantity)-1 > 0xffff {
		err = ErrUnexpectedParameters
		mc.logger.Error("end coil address is past 0xffff")
		return
	}

	encodedValues = encodeBools(values)

	// create and fill in the request object
	req = &pdu{
		unitID:       unitID,
		functionCode: FCWriteMultipleCoils,
	}

	// start address
	req.payload = uint16ToBytes(BigEndian, addr)
	// quantity
	req.payload = append(req.payload, uint16ToBytes(BigEndian, quantity)...)
	// byte count
	req.payload = append(req.payload, byte(len(encodedValues)))
	// payload
	req.payload = append(req.payload, encodedValues...)

	// run the request across the transport and wait for a response
	res, err = mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	// validate the response code
	switch res.functionCode {
	case req.functionCode:
		// expect 4 bytes (2 byte of address + 2 bytes of quantity)
		if len(res.payload) != 4 ||
			// bytes 1-2 should be the base coil address
			bytesToUint16(BigEndian, res.payload[0:2]) != addr ||
			// bytes 3-4 should be the quantity of coils
			bytesToUint16(BigEndian, res.payload[2:4]) != quantity {
			err = ErrProtocolError
			return
		}

	case FunctionCode(uint8(req.functionCode) | 0x80):
		if len(res.payload) != 1 {
			err = ErrProtocolError
			return
		}

		err = mapExceptionCodeToError(req.functionCode, ExceptionCode(res.payload[0]))

	default:
		err = ErrProtocolError
		mc.logger.Warningf("unexpected response code (%v)", res.functionCode)
	}

	return
}

// Writes a single 16-bit register (function code 06).
func (mc *ModbusClient) WriteRegister(ctx context.Context, unitID uint8, addr uint16, value uint16) (err error) {
	var req *pdu
	var res *pdu

	mc.lock.Lock()
	defer mc.lock.Unlock()

	// create and fill in the request object
	req = &pdu{
		unitID:       unitID,
		functionCode: FCWriteSingleRegister,
	}

	// register address
	req.payload = uint16ToBytes(BigEndian, addr)
	// register value
	req.payload = append(req.payload, uint16ToBytes(BigEndian, value)...)

	// run the request across the transport and wait for a response
	res, err = mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	// validate the response code
	switch res.functionCode {
	case req.functionCode:
		// expect 4 bytes (2 byte of address + 2 bytes of value)
		if len(res.payload) != 4 ||
			// bytes 1-2 should be the register address
			bytesToUint16(BigEndian, res.payload[0:2]) != addr ||
			// bytes 3-4 should be the value
			bytesToUint16(BigEndian, res.payload[2:4]) != value {
			err = ErrProtocolError
			return
		}

	case FunctionCode(uint8(req.functionCode) | 0x80):
		if len(res.payload) != 1 {
			err = ErrProtocolError
			return
		}

		err = mapExceptionCodeToError(req.functionCode, ExceptionCode(res.payload[0]))

	default:
		err = ErrProtocolError
		mc.logger.Warningf("unexpected response code (%v)", res.functionCode)
	}

	return
}

// Writes multiple 16-bit registers (function code 16).
func (mc *ModbusClient) WriteRegisters(ctx context.Context, unitID uint8, addr uint16, values []uint16) (err error) {
	mc.lock.Lock()
	defer mc.lock.Unlock()

	var payload []byte

	// turn registers to bytes
	for _, value := range values {
		payload = append(payload, uint16ToBytes(BigEndian, value)...)
	}

	err = mc.writeRegisters(ctx, unitID, addr, payload)

	return
}

// WriteRawBytes is a transport convenience helper: it writes raw bytes into
// holding registers (FC16). No interpretation or reordering is applied. For
// typed values use WriteWithCodec or WriteWithRuntimeCodec. Odd-length slices
// are zero-padded to the next register boundary (implicit register-boundary handling).
func (mc *ModbusClient) WriteRawBytes(ctx context.Context, unitID uint8, addr uint16, values []byte) (err error) {
	mc.lock.Lock()
	defer mc.lock.Unlock()

	err = mc.writeBytes(ctx, unitID, addr, values)

	return
}

// Performs a combined read/write in a single Modbus transaction (function code 23).
// The write is executed on the server before the read.
// writeValues are encoded as big-endian (wire order).
// The returned slice contains the registers read, also as big-endian.
//
// Limits (per spec):
//
//	readQty:  1–125 (0x7D)
//	writeQty: 1–121 (0x79), implied by len(writeValues)
func (mc *ModbusClient) ReadWriteMultipleRegisters(ctx context.Context, unitID uint8, readAddr, readQty, writeAddr uint16, writeValues []uint16) (values []uint16, err error) {
	var req *pdu
	var res *pdu
	var writeQty uint16

	mc.lock.Lock()
	defer mc.lock.Unlock()

	writeQty = uint16(len(writeValues))

	if readQty == 0 {
		err = ErrUnexpectedParameters
		mc.logger.Error("read quantity of registers is 0")
		return
	}
	if readQty > maxRWReadRegs {
		err = ErrUnexpectedParameters
		mc.logger.Errorf("read quantity of registers (%v) exceeds maximum of %v", readQty, maxRWReadRegs)
		return
	}
	if writeQty == 0 {
		err = ErrUnexpectedParameters
		mc.logger.Error("write quantity of registers is 0")
		return
	}
	if writeQty > maxRWWriteRegs {
		err = ErrUnexpectedParameters
		mc.logger.Errorf("write quantity of registers (%v) exceeds maximum of %v", writeQty, maxRWWriteRegs)
		return
	}
	if uint32(readAddr)+uint32(readQty)-1 > 0xffff {
		err = ErrUnexpectedParameters
		mc.logger.Error("read end register address is past 0xffff")
		return
	}
	if uint32(writeAddr)+uint32(writeQty)-1 > 0xffff {
		err = ErrUnexpectedParameters
		mc.logger.Error("write end register address is past 0xffff")
		return
	}

	req = &pdu{
		unitID:       unitID,
		functionCode: FCReadWriteMultipleRegs,
	}

	// read starting address
	req.payload = uint16ToBytes(BigEndian, readAddr)
	// quantity to read
	req.payload = append(req.payload, uint16ToBytes(BigEndian, readQty)...)
	// write starting address
	req.payload = append(req.payload, uint16ToBytes(BigEndian, writeAddr)...)
	// quantity to write
	req.payload = append(req.payload, uint16ToBytes(BigEndian, writeQty)...)
	// write byte count (2 bytes per register)
	req.payload = append(req.payload, byte(writeQty*2))
	// write register values
	for _, v := range writeValues {
		req.payload = append(req.payload, uint16ToBytes(BigEndian, v)...)
	}

	res, err = mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	switch res.functionCode {
	case req.functionCode:
		// response: 1 byte byte-count + readQty*2 bytes of register data
		if len(res.payload) != 1+2*int(readQty) {
			err = ErrProtocolError
			return
		}
		if uint(res.payload[0]) != 2*uint(readQty) {
			err = ErrProtocolError
			return
		}
		values = bytesToUint16s(BigEndian, res.payload[1:])

	case FunctionCode(uint8(req.functionCode) | 0x80):
		if len(res.payload) != 1 {
			err = ErrProtocolError
			return
		}
		err = mapExceptionCodeToError(req.functionCode, ExceptionCode(res.payload[0]))

	default:
		err = ErrProtocolError
		mc.logger.Warningf("unexpected response code (%v)", res.functionCode)
	}

	return
}

// Reads the contents of a FIFO queue of holding registers (function code 24).
// addr is the FIFO Pointer Address (the count register); registers are returned
// as big-endian uint16 values exactly as they arrive from the device.
// The FIFO queue may contain at most 31 registers; an exception response is
// returned by the server if the count exceeds 31.
func (mc *ModbusClient) ReadFIFOQueue(ctx context.Context, unitID uint8, addr uint16) (values []uint16, err error) {
	var req *pdu
	var res *pdu
	var byteCount uint16
	var fifoCount uint16

	mc.lock.Lock()
	defer mc.lock.Unlock()

	req = &pdu{
		unitID:       unitID,
		functionCode: FCReadFIFOQueue,
		payload:      uint16ToBytes(BigEndian, addr),
	}

	res, err = mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	switch res.functionCode {
	case req.functionCode:
		// response: 2 bytes byte-count  + 2 bytes fifo-count + fifoCount*2 bytes data
		if len(res.payload) < 4 {
			err = ErrProtocolError
			return
		}
		byteCount = bytesToUint16(BigEndian, res.payload[0:2])
		fifoCount = bytesToUint16(BigEndian, res.payload[2:4])

		if fifoCount > maxFIFOCount {
			err = ErrProtocolError
			mc.logger.Errorf("server returned FIFO count %v, exceeds maximum of %v", fifoCount, maxFIFOCount)
			return
		}
		// byteCount covers fifoCount word (2 bytes) + register data (fifoCount*2 bytes)
		if int(byteCount) != 2+2*int(fifoCount) {
			err = ErrProtocolError
			return
		}
		if len(res.payload) != 2+int(byteCount) {
			err = ErrProtocolError
			return
		}
		values = bytesToUint16s(BigEndian, res.payload[4:])

	case FunctionCode(uint8(req.functionCode) | 0x80):
		if len(res.payload) != 1 {
			err = ErrProtocolError
			return
		}
		err = mapExceptionCodeToError(req.functionCode, ExceptionCode(res.payload[0]))

	default:
		err = ErrProtocolError
		mc.logger.Warningf("unexpected response code (%v)", res.functionCode)
	}

	return
}

// Diagnostics sends a Diagnostics request (FC 0x08). subFunction selects the
// diagnostic (use DiagnosticSubFunction constants). data is optional request
// data (sub-function-specific; use nil or empty for none). The response echoes
// the sub-function and returns sub-function-specific data.
func (mc *ModbusClient) Diagnostics(ctx context.Context, unitID uint8, subFunction DiagnosticSubFunction, data []byte) (dr *DiagnosticResponse, err error) {
	var req *pdu
	var res *pdu

	mc.lock.Lock()
	defer mc.lock.Unlock()

	req = &pdu{
		unitID:       unitID,
		functionCode: FCDiagnostics,
		payload:      uint16ToBytes(BigEndian, uint16(subFunction)),
	}
	if len(data) > 0 {
		req.payload = append(req.payload, data...)
	}

	res, err = mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	switch res.functionCode {
	case req.functionCode:
		if len(res.payload) < 2 {
			err = ErrProtocolError
			return
		}
		dr = &DiagnosticResponse{
			SubFunction: DiagnosticSubFunction(bytesToUint16(BigEndian, res.payload[0:2])),
			Data:        res.payload[2:],
		}
	case FunctionCode(uint8(req.functionCode) | 0x80):
		if len(res.payload) != 1 {
			err = ErrProtocolError
			return
		}
		err = mapExceptionCodeToError(req.functionCode, ExceptionCode(res.payload[0]))
	default:
		err = ErrProtocolError
		mc.logger.Warningf("unexpected response code (%v)", res.functionCode)
	}
	return
}

// ReportServerId requests the Report Server ID (FC 0x11). The response contains
// device-specific server ID, run indicator status, and optional additional data.
func (mc *ModbusClient) ReportServerId(ctx context.Context, unitID uint8) (rs *ReportServerIdResponse, err error) {
	var req *pdu
	var res *pdu

	mc.lock.Lock()
	defer mc.lock.Unlock()

	req = &pdu{
		unitID:       unitID,
		functionCode: FCReportServerID,
	}

	res, err = mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	switch res.functionCode {
	case req.functionCode:
		if len(res.payload) < 1 {
			err = ErrProtocolError
			return
		}
		byteCount := res.payload[0]
		if len(res.payload) != 1+int(byteCount) {
			err = ErrProtocolError
			return
		}
		rs = &ReportServerIdResponse{
			ByteCount: byteCount,
			Data:      append([]byte(nil), res.payload[1:1+byteCount]...),
		}
	case FunctionCode(uint8(req.functionCode) | 0x80):
		if len(res.payload) != 1 {
			err = ErrProtocolError
			return
		}
		err = mapExceptionCodeToError(req.functionCode, ExceptionCode(res.payload[0]))
	default:
		err = ErrProtocolError
		mc.logger.Warningf("unexpected response code (%v)", res.functionCode)
	}
	return
}

// Reads one or more groups of file records (function code 20).
// Each FileRecordRequest selects a contiguous run of registers from one file.
// The returned slice has one []uint16 entry per request, in the same order.
// Register data is returned as big-endian uint16 values as received from the device.
//
// Spec limits:
//
//	FileNumber   must be 1–0xFFFF
//	RecordNumber must be 0–0x270F (decimal 0–9999)
//	Total request byte count must not exceed 0xF5 (at most 35 sub-requests)
func (mc *ModbusClient) ReadFileRecords(ctx context.Context, unitID uint8, requests []FileRecordRequest) (records [][]uint16, err error) {
	var req *pdu
	var res *pdu

	mc.lock.Lock()
	defer mc.lock.Unlock()

	if len(requests) == 0 {
		err = ErrUnexpectedParameters
		mc.logger.Error("no file record requests provided")
		return
	}

	// validate all sub-requests and compute the byte count
	// each sub-request encodes as 7 bytes: refType(1) + fileNum(2) + recNum(2) + recLen(2)
	byteCount := len(requests) * 7
	if byteCount > maxFileByteCount {
		err = ErrUnexpectedParameters
		mc.logger.Errorf("too many sub-requests: byte count %v exceeds maximum of 0x%02X", byteCount, maxFileByteCount)
		return
	}

	for i, r := range requests {
		if r.FileNumber == 0 {
			err = ErrUnexpectedParameters
			mc.logger.Errorf("sub-request %d: file number 0 is not allowed (must be 1–0xFFFF)", i)
			return
		}
		if r.RecordNumber > 0x270F {
			err = ErrUnexpectedParameters
			mc.logger.Errorf("sub-request %d: record number %v exceeds 0x270F (9999)", i, r.RecordNumber)
			return
		}
		if r.RecordLength == 0 {
			err = ErrUnexpectedParameters
			mc.logger.Errorf("sub-request %d: record length is 0", i)
			return
		}
	}

	// build the request PDU
	req = &pdu{
		unitID:       unitID,
		functionCode: FCReadFileRecord,
		payload:      []byte{byte(byteCount)},
	}
	for _, r := range requests {
		req.payload = append(req.payload, 0x06) // reference type must be 6
		req.payload = append(req.payload, uint16ToBytes(BigEndian, r.FileNumber)...)
		req.payload = append(req.payload, uint16ToBytes(BigEndian, r.RecordNumber)...)
		req.payload = append(req.payload, uint16ToBytes(BigEndian, r.RecordLength)...)
	}

	res, err = mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	switch res.functionCode {
	case req.functionCode:
		// response layout: [respDataLen(1)] [sub-resp1] [sub-resp2] ...
		// each sub-response: [fileRespLen(1)] [refType=0x06(1)] [data(recordLen*2)]
		if len(res.payload) < 1 {
			err = ErrProtocolError
			return
		}
		respDataLen := int(res.payload[0])
		if len(res.payload) != 1+respDataLen {
			err = ErrProtocolError
			return
		}

		offset := 1
		for i, r := range requests {
			// need at least fileRespLen + refType byte
			if offset+2 > len(res.payload) {
				err = ErrProtocolError
				mc.logger.Errorf("sub-response %d: truncated payload", i)
				return
			}
			fileRespLen := int(res.payload[offset])
			offset++
			if res.payload[offset] != 0x06 {
				err = ErrProtocolError
				mc.logger.Errorf("sub-response %d: unexpected reference type 0x%02x (expected 0x06)", i, res.payload[offset])
				return
			}
			offset++

			// fileRespLen includes the refType byte; data follows for the rest
			dataLen := fileRespLen - 1
			expectedDataLen := int(r.RecordLength) * 2
			if dataLen != expectedDataLen {
				err = ErrProtocolError
				mc.logger.Errorf("sub-response %d: expected %v data bytes, got %v", i, expectedDataLen, dataLen)
				return
			}
			if offset+dataLen > len(res.payload) {
				err = ErrProtocolError
				mc.logger.Errorf("sub-response %d: data truncated", i)
				return
			}
			records = append(records, bytesToUint16s(BigEndian, res.payload[offset:offset+dataLen]))
			offset += dataLen
		}

		// all bytes should be consumed
		if offset != len(res.payload) {
			err = ErrProtocolError
			return
		}

	case FunctionCode(uint8(req.functionCode) | 0x80):
		if len(res.payload) != 1 {
			err = ErrProtocolError
			return
		}
		err = mapExceptionCodeToError(req.functionCode, ExceptionCode(res.payload[0]))

	default:
		err = ErrProtocolError
		mc.logger.Warningf("unexpected response code (%v)", res.functionCode)
	}

	return
}

// Writes one or more groups of file records (function code 21).
// Each FileRecord specifies the target file, starting record number, and
// the register values to write. The normal response is an echo of the request.
// Register data is encoded as big-endian uint16 values on the wire.
//
// Spec limits:
//
//	FileNumber   must be 1–0xFFFF
//	RecordNumber must be 0–0x270F (decimal 0–9999)
//	Total request data length must be in the range 0x09–0xFB
func (mc *ModbusClient) WriteFileRecords(ctx context.Context, unitID uint8, records []FileRecord) (err error) {
	var req *pdu
	var res *pdu

	mc.lock.Lock()
	defer mc.lock.Unlock()

	if len(records) == 0 {
		err = ErrUnexpectedParameters
		mc.logger.Error("no file records provided")
		return
	}

	// compute total request data length:
	// each sub-request: refType(1) + fileNum(2) + recNum(2) + recLen(2) + data(len(Data)*2)
	reqDataLen := 0
	for _, r := range records {
		reqDataLen += 7 + 2*len(r.Data)
	}
	if reqDataLen > maxFileReqDataLen {
		err = ErrUnexpectedParameters
		mc.logger.Errorf("request data length %v exceeds maximum of 0x%02X", reqDataLen, maxFileReqDataLen)
		return
	}

	for i, r := range records {
		if r.FileNumber == 0 {
			err = ErrUnexpectedParameters
			mc.logger.Errorf("record %d: file number 0 is not allowed (must be 1–0xFFFF)", i)
			return
		}
		if r.RecordNumber > 0x270F {
			err = ErrUnexpectedParameters
			mc.logger.Errorf("record %d: record number %v exceeds 0x270F (9999)", i, r.RecordNumber)
			return
		}
		if len(r.Data) == 0 {
			err = ErrUnexpectedParameters
			mc.logger.Errorf("record %d: Data slice is empty, nothing to write", i)
			return
		}
	}

	// build the request PDU
	req = &pdu{
		unitID:       unitID,
		functionCode: FCWriteFileRecord,
		payload:      []byte{byte(reqDataLen)},
	}
	for _, r := range records {
		req.payload = append(req.payload, 0x06) // reference type must be 6
		req.payload = append(req.payload, uint16ToBytes(BigEndian, r.FileNumber)...)
		req.payload = append(req.payload, uint16ToBytes(BigEndian, r.RecordNumber)...)
		req.payload = append(req.payload, uint16ToBytes(BigEndian, uint16(len(r.Data)))...)
		for _, v := range r.Data {
			req.payload = append(req.payload, uint16ToBytes(BigEndian, v)...)
		}
	}

	res, err = mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	switch res.functionCode {
	case req.functionCode:
		// the normal response is an echo of the entire request payload
		if len(res.payload) != len(req.payload) {
			err = ErrProtocolError
			mc.logger.Errorf("response length %v does not match request length %v",
				len(res.payload), len(req.payload))
			return
		}
		if int(res.payload[0]) != reqDataLen {
			err = ErrProtocolError
			mc.logger.Errorf("response data length byte 0x%02x does not match expected 0x%02x",
				res.payload[0], reqDataLen)
			return
		}
		for i := range req.payload {
			if res.payload[i] != req.payload[i] {
				err = ErrProtocolError
				mc.logger.Errorf("response echo mismatch at byte %d: expected 0x%02x, got 0x%02x",
					i, req.payload[i], res.payload[i])
				return
			}
		}

	case FunctionCode(uint8(req.functionCode) | 0x80):
		if len(res.payload) != 1 {
			err = ErrProtocolError
			return
		}
		err = mapExceptionCodeToError(req.functionCode, ExceptionCode(res.payload[0]))

	default:
		err = ErrProtocolError
		mc.logger.Warningf("unexpected response code (%v)", res.functionCode)
	}

	return
}

/*** unexported methods ***/
// Reads one or multiple 16-bit registers (function code 03 or 04) as bytes (wire order, no swap).
func (mc *ModbusClient) readBytes(ctx context.Context, unitID uint8, addr uint16, quantity uint16, regType RegType) (values []byte, err error) {
	var regCount = (quantity / 2) + (quantity % 2)
	values, err = mc.readRegisters(ctx, unitID, addr, regCount, regType)
	if err != nil {
		return
	}
	if quantity%2 == 1 {
		values = values[0 : len(values)-1]
	}
	return
}

// Writes the given slice of bytes to 16-bit registers (wire order, no swap).
func (mc *ModbusClient) writeBytes(ctx context.Context, unitID uint8, addr uint16, values []byte) (err error) {
	if len(values)%2 == 1 {
		values = append(values, 0x00)
	}
	return mc.writeRegisters(ctx, unitID, addr, values)
}

// Reads and returns quantity booleans.
// Digital inputs are read if di is true, otherwise coils are read.
// Callers must hold mc.lock before invoking this method.
func (mc *ModbusClient) readBools(ctx context.Context, unitID uint8, addr uint16, quantity uint16, di bool) (values []bool, err error) {
	var req *pdu
	var res *pdu
	var expectedLen int

	if quantity == 0 {
		err = ErrUnexpectedParameters
		mc.logger.Error("quantity of coils/discrete inputs is 0")
		return
	}

	if quantity > maxReadCoils {
		err = ErrUnexpectedParameters
		mc.logger.Errorf("quantity of coils/discrete inputs exceeds %v", maxReadCoils)
		return
	}

	if uint32(addr)+uint32(quantity)-1 > 0xffff {
		err = ErrUnexpectedParameters
		mc.logger.Error("end coil/discrete input address is past 0xffff")
		return
	}

	// create and fill in the request object
	req = &pdu{
		unitID: unitID,
	}

	if di {
		req.functionCode = FCReadDiscreteInputs
	} else {
		req.functionCode = FCReadCoils
	}

	// start address
	req.payload = uint16ToBytes(BigEndian, addr)
	// quantity
	req.payload = append(req.payload, uint16ToBytes(BigEndian, quantity)...)

	// run the request across the transport and wait for a response
	res, err = mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	// validate the response code
	switch res.functionCode {
	case req.functionCode:
		// expect a payload of 1 byte (byte count) + 1 byte for 8 coils/discrete inputs)
		expectedLen = 1
		expectedLen += int(quantity) / 8
		if quantity%8 != 0 {
			expectedLen++
		}

		if len(res.payload) != expectedLen {
			err = ErrProtocolError
			return
		}

		// validate the byte count field
		if int(res.payload[0])+1 != expectedLen {
			err = ErrProtocolError
			return
		}

		// turn bits into a bool slice
		values = decodeBools(quantity, res.payload[1:])

	case FunctionCode(uint8(req.functionCode) | 0x80):
		if len(res.payload) != 1 {
			err = ErrProtocolError
			return
		}

		err = mapExceptionCodeToError(req.functionCode, ExceptionCode(res.payload[0]))

	default:
		err = ErrProtocolError
		mc.logger.Warningf("unexpected response code (%v)", res.functionCode)
	}

	return
}

// Reads and returns quantity registers of type regType, as bytes.
// Callers must hold mc.lock before invoking this method.
func (mc *ModbusClient) readRegisters(ctx context.Context, unitID uint8, addr uint16, quantity uint16, regType RegType) (bytes []byte, err error) {
	var req *pdu
	var res *pdu

	// create and fill in the request object
	req = &pdu{
		unitID: unitID,
	}

	switch regType {
	case HoldingRegister:
		req.functionCode = FCReadHoldingRegisters
	case InputRegister:
		req.functionCode = FCReadInputRegisters
	default:
		err = ErrUnexpectedParameters
		mc.logger.Errorf("unexpected register type (%v)", regType)
		return
	}

	if quantity == 0 {
		err = ErrUnexpectedParameters
		mc.logger.Error("quantity of registers is 0")
		return
	}

	if quantity > maxReadRegisters {
		err = ErrUnexpectedParameters
		mc.logger.Errorf("quantity of registers exceeds %v", maxReadRegisters)
		return
	}

	if uint32(addr)+uint32(quantity)-1 > 0xffff {
		err = ErrUnexpectedParameters
		mc.logger.Error("end register address is past 0xffff")
		return
	}

	// start address
	req.payload = uint16ToBytes(BigEndian, addr)
	// quantity
	req.payload = append(req.payload, uint16ToBytes(BigEndian, quantity)...)

	// run the request across the transport and wait for a response
	res, err = mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	// validate the response code
	switch res.functionCode {
	case req.functionCode:
		// make sure the payload length is what we expect
		// (1 byte of length + 2 bytes per register)
		if len(res.payload) != 1+2*int(quantity) {
			err = ErrProtocolError
			return
		}

		// validate the byte count field
		// (2 bytes per register * number of registers)
		if uint(res.payload[0]) != 2*uint(quantity) {
			err = ErrProtocolError
			return
		}

		// remove the byte count field from the returned slice
		bytes = res.payload[1:]

	case FunctionCode(uint8(req.functionCode) | 0x80):
		if len(res.payload) != 1 {
			err = ErrProtocolError
			return
		}

		err = mapExceptionCodeToError(req.functionCode, ExceptionCode(res.payload[0]))

	default:
		err = ErrProtocolError
		mc.logger.Warningf("unexpected response code (%v)", res.functionCode)
	}

	return
}

// Writes a single coil (function code 05) using the specified payload.
func (mc *ModbusClient) writeCoil(ctx context.Context, unitID uint8, addr uint16, payload uint16) (err error) {
	var req *pdu
	var res *pdu

	// create and fill in the request object
	req = &pdu{
		unitID:       unitID,
		functionCode: FCWriteSingleCoil,
	}

	// coil address
	req.payload = uint16ToBytes(BigEndian, addr)
	// payload (coil value)
	req.payload = append(req.payload, uint16ToBytes(BigEndian, payload)...)

	// run the request across the transport and wait for a response
	res, err = mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	// validate the response code
	switch res.functionCode {
	case req.functionCode:
		// expect 4 bytes (2 byte of address + 2 bytes of value)
		if len(res.payload) != 4 ||
			// bytes 1-2 should be the coil address
			bytesToUint16(BigEndian, res.payload[0:2]) != addr ||
			// bytes 3-4 should be an echo of the coil value
			bytesToUint16(BigEndian, res.payload[2:4]) != payload {
			err = ErrProtocolError
			return
		}

	case FunctionCode(uint8(req.functionCode) | 0x80):
		if len(res.payload) != 1 {
			err = ErrProtocolError
			return
		}

		err = mapExceptionCodeToError(req.functionCode, ExceptionCode(res.payload[0]))

	default:
		err = ErrProtocolError
		mc.logger.Warningf("unexpected response code (%v)", res.functionCode)
	}

	return
}

// Writes multiple registers starting from base address addr.
// Register values are passed as bytes, each value being exactly 2 bytes.
// Callers must hold mc.lock before invoking this method.
func (mc *ModbusClient) writeRegisters(ctx context.Context, unitID uint8, addr uint16, values []byte) (err error) {
	var req *pdu
	var res *pdu
	var payloadLength uint16
	var quantity uint16

	payloadLength = uint16(len(values))
	quantity = payloadLength / 2

	if quantity == 0 {
		err = ErrUnexpectedParameters
		mc.logger.Error("quantity of registers is 0")
		return
	}

	if quantity > maxWriteRegisters {
		err = ErrUnexpectedParameters
		mc.logger.Errorf("quantity of registers exceeds %v", maxWriteRegisters)
		return
	}

	if uint32(addr)+uint32(quantity)-1 > 0xffff {
		err = ErrUnexpectedParameters
		mc.logger.Error("end register address is past 0xffff")
		return
	}

	// create and fill in the request object
	req = &pdu{
		unitID:       unitID,
		functionCode: FCWriteMultipleRegisters,
	}

	// base address
	req.payload = uint16ToBytes(BigEndian, addr)
	// quantity of registers (2 bytes per register)
	req.payload = append(req.payload, uint16ToBytes(BigEndian, quantity)...)
	// byte count
	req.payload = append(req.payload, byte(payloadLength))
	// registers value
	req.payload = append(req.payload, values...)

	// run the request across the transport and wait for a response
	res, err = mc.executeRequest(ctx, req)
	if err != nil {
		return
	}

	// validate the response code
	switch res.functionCode {
	case req.functionCode:
		// expect 4 bytes (2 byte of address + 2 bytes of quantity)
		if len(res.payload) != 4 ||
			// bytes 1-2 should be the base register address
			bytesToUint16(BigEndian, res.payload[0:2]) != addr ||
			// bytes 3-4 should be the quantity of registers (2 bytes per register)
			bytesToUint16(BigEndian, res.payload[2:4]) != quantity {
			err = ErrProtocolError
			return
		}

	case FunctionCode(uint8(req.functionCode) | 0x80):
		if len(res.payload) != 1 {
			err = ErrProtocolError
			return
		}

		err = mapExceptionCodeToError(req.functionCode, ExceptionCode(res.payload[0]))

	default:
		err = ErrProtocolError
		mc.logger.Warningf("unexpected response code (%v)", res.functionCode)
	}

	return
}

// readRegistersForCodec reads count registers and returns them as []uint16 in wire order.
// Used by ReadWithCodec and ReadWithRuntimeCodec so lock/read/convert logic lives in one place.
func (mc *ModbusClient) readRegistersForCodec(ctx context.Context, unitID uint8, addr uint16, count uint16, regType RegType) ([]uint16, error) {
	mc.lock.Lock()
	defer mc.lock.Unlock()
	mbPayload, err := mc.readRegisters(ctx, unitID, addr, count, regType)
	if err != nil {
		return nil, err
	}
	return bytesToUint16s(BigEndian, mbPayload), nil
}

// writeRegistersForCodec writes regs to the given address in wire order.
// Used by WriteWithCodec and WriteWithRuntimeCodec so lock/write logic lives in one place.
func (mc *ModbusClient) writeRegistersForCodec(ctx context.Context, unitID uint8, addr uint16, regs []uint16) error {
	mc.lock.Lock()
	defer mc.lock.Unlock()
	payload := uint16sToBytes(BigEndian, regs)
	return mc.writeRegisters(ctx, unitID, addr, payload)
}

// executeRequest sends req and returns the response, transparently applying the
// configured RetryPolicy and reporting outcomes to ClientMetrics.
//
// For pool transports (MaxConns > 1), the main lock is temporarily released
// during network I/O so that goroutines sharing this client can use different
// pool connections concurrently. For single transports, the lock is held for
// the full request duration (existing behaviour).
//
// Callers must hold mc.lock on entry; the lock is guaranteed to be held on return.
func (mc *ModbusClient) executeRequest(ctx context.Context, req *pdu) (res *pdu, err error) {
	policy := mc.conf.RetryPolicy
	metrics := mc.conf.Metrics
	start := time.Now()

	if metrics != nil {
		metrics.OnRequest(req.unitID, req.functionCode)
	}

	for attempt := 0; ; attempt++ {
		res, err = mc.executeOnce(ctx, req)
		if err == nil {
			if metrics != nil {
				metrics.OnResponse(req.unitID, req.functionCode, time.Since(start))
			}
			return
		}

		// determine whether to retry
		var retry bool
		var delay time.Duration
		if policy != nil {
			retry, delay = policy.ShouldRetry(attempt, err)
		}

		if !retry {
			break
		}

		mc.logger.Debugf("retrying request (attempt %d, delay %v): %v", attempt+1, delay, err)

		// For single-transport mode, close the connection so the next attempt
		// dials a fresh one. Pool mode needs no explicit close — discard()
		// already decremented the total so acquire() can dial a replacement.
		if mc.pool == nil {
			if mc.transport != nil {
				_ = mc.transport.Close()
				mc.transport = nil
				mc.isOpen = false
			}
		}

		// sleep while honouring context cancellation;
		// release the lock so other goroutines are not blocked during the wait.
		if delay > 0 {
			mc.lock.Unlock()
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				mc.lock.Lock()
				err = ctx.Err()
				if metrics != nil {
					metrics.OnTimeout(req.unitID, req.functionCode, time.Since(start))
				}
				return
			case <-timer.C:
			}
			mc.lock.Lock()
		}

		// reconnect the single transport (pool dials lazily on acquire)
		if mc.pool == nil && !mc.isOpen {
			var reconnErr error
			mc.transport, reconnErr = mc.dialTransport()
			if reconnErr != nil {
				mc.logger.Errorf("reconnect failed (attempt %d): %v", attempt+1, reconnErr)
				break
			}
			mc.isOpen = true
		}
	}

	// report the final error
	if metrics != nil {
		if errors.Is(err, ErrRequestTimedOut) || errors.Is(err, context.DeadlineExceeded) {
			metrics.OnTimeout(req.unitID, req.functionCode, time.Since(start))
		} else {
			metrics.OnError(req.unitID, req.functionCode, time.Since(start), err)
		}
	}

	return
}

// executeOnce performs a single transport round-trip without retry.
// For pool transports the main lock is temporarily released during I/O.
// Callers must hold mc.lock on entry; the lock is guaranteed held on return.
func (mc *ModbusClient) executeOnce(ctx context.Context, req *pdu) (res *pdu, err error) {
	if mc.pool == nil && mc.transport == nil {
		return nil, ErrClientNotOpen
	}
	if mc.pool != nil {
		// release the main lock for the duration of network I/O so that
		// goroutines sharing this client can use different pool connections concurrently.
		mc.lock.Unlock()
		res, err = mc.pool.execute(ctx, req)
		mc.lock.Lock()
	} else {
		res, err = mc.transport.ExecuteRequest(ctx, req)
	}

	if err != nil {
		// map i/o timeouts to ErrRequestTimedOut
		if os.IsTimeout(err) {
			err = ErrRequestTimedOut
		}
		return
	}

	mc.lastResponseTransactionID = res.responseTransactionID

	// make sure the source unit id matches that of the request
	if (res.functionCode&0x80) == 0x00 && res.unitID != req.unitID {
		err = ErrBadUnitId
		return
	}
	// accept errors from gateway devices (using special unit id #255)
	if (res.functionCode&0x80) == 0x80 &&
		(res.unitID != req.unitID && res.unitID != 0xff) {
		err = ErrBadUnitId
		return
	}

	return
}
