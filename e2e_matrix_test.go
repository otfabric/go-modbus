// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// End-to-end feature matrix: our client against our server over real loopback
// sockets. Unless a line says otherwise, every test runs on both tcp:// and
// tcp+tls:// and asserts both sides: what the client returned and what the
// server handler received (or what the device holds afterwards).
//
// "existing:" names a test outside the e2e_* files that already covers the
// feature end to end; it is listed instead of being duplicated.
//
// Client method / feature                       -> test
//
// Bits
//   ReadCoils, ReadDiscreteInputs (values, bit packing at 1..2000, limits 0/2001,
//     address 65535, server-side limit enforcement) -> TestE2E_Bits_ReadPackingAndLimits
//   ReadCoil, ReadDiscreteInput                  -> TestE2E_Bits_ReadPackingAndLimits
//   WriteCoil, WriteCoilRaw (standard and non-standard payloads), WriteCoils
//     (1..1968, neighbours untouched, 0/1969, address 65535) -> TestE2E_Bits_Write
//   exceptions for every bit method              -> TestE2E_Bits_Exceptions
//   existing: TestB2B_Coils_RoundTrip, TestB2B_DiscreteInputs_Read,
//     TestB2B_Property_ReadWriteDifferential, TestTCPServerCoilsAndDiscreteInputs
//
// Registers
//   ReadRegisters, ReadRegister, ReadRegisterBytes, ReadHoldingRegister(s),
//     ReadInputRegister(s) (limits 0/125/126, address 65535) -> TestE2E_Registers_Read
//   WriteRegister, WriteRegisters, WriteRegisterBytes (limits 0/123/124, odd byte
//     counts, address 65535)                     -> TestE2E_Registers_Write
//   quantities beyond uint16                     -> TestE2E_Registers_HugeWriteIsParameterError
//   ReadRegisterBit, ReadRegisterBits, WriteRegisterBit, UpdateRegisterMask
//                                                -> TestE2E_Registers_BitHelpers
//   MaskWriteRegister (FC22)                     -> TestE2E_Registers_MaskWrite
//   ReadWriteMultipleRegisters (FC23: limits 125/121, write-before-read,
//     address 65535)                             -> TestE2E_Registers_ReadWriteMultiple
//   FC22/FC23 against a handler without them     -> TestE2E_Registers_OptionalHandlersMissing
//   existing: TestB2B_HoldingRegisters_RoundTrip, TestB2B_InputRegisters_Read,
//     TestB2B_MaskWriteRegister, TestB2B_ReadWriteMultipleRegisters_WriteBeforeRead,
//     TestB2B_BoundaryLimits, TestTCPServerHoldingAndInputRegisters
//
// Typed codecs through the client (package codec)
//   ReadFromClient, WriteToClient, ReadUint32FromClient, WriteUint32ToClient:
//     16/32/48/64-bit integers, floats, every layout family, decimal limbs,
//     strings, BCD, UTF-16, bytes, addresses, time -> TestE2E_Codec_TypedRoundTrip
//   ReadRuntimeFromClient, WriteRuntimeToClient for every registered codec
//     descriptor                                 -> TestE2E_Codec_EveryDescriptor
//   codec errors and server exceptions through the helpers -> TestE2E_Codec_Errors
//
// Device identification (FC43 / MEI 0x0E)
//   ReadDeviceIdentification basic/regular/extended/individual, start object,
//     ReadAllDeviceIdentification                -> TestE2E_DeviceIdentification_Categories
//   derived conformity level, empty object set   -> TestE2E_DeviceIdentification_DerivedConformity
//   multi-response pagination (30 pages)         -> TestE2E_DeviceIdentification_Pagination
//   more than 32 pages                           -> TestE2E_DeviceIdentification_ManyPages
//   object too large for one response            -> TestE2E_DeviceIdentification_OversizedObject
//   handler without device identification        -> TestE2E_DeviceIdentification_NotImplemented
//   existing: TestB2B_DeviceIdentification, TestB2B_Property_DeviceIDReassembly,
//     TestServerFC43_* (tcp only)
//
// Serial-line function codes served over TCP
//   ReadExceptionStatus (FC07), GetCommEventCounter (FC0B), GetCommEventLog
//     (FC0C, 0..64 events)                       -> TestE2E_SerialLineFunctions
//   event log too large for one response         -> TestE2E_SerialLineFunctions_OversizedEventLog (tcp)
//   handler without them                         -> TestE2E_SerialLineFunctions_NotImplemented
//   existing: TestServerFC07_*, TestServerFC0B_*, TestServerFC0C_* (tcp only)
//
// Function codes the server does not serve
//   Diagnostics and its 13 wrappers (FC08), ReportServerID (FC11),
//     ReadFileRecords (FC14), WriteFileRecords (FC15), ReadFIFOQueue (FC18):
//     Illegal Function, connection stays usable  -> TestE2E_UnservedFunctionCodes
//   existing: TestB2B_UnsupportedFunctionCodes (raw frames)
//
// Probes
//   SupportsFunction, SupportsDeviceIdentification, ProbeFunction: supported,
//     unsupported, exception, timeout, dropped connection, no probe defined
//                                                -> TestE2E_Probes
//
// sunspec package
//   Detect, ReadModelHeaders, Discover on a map in holding and in input
//     registers at several base addresses, MaxModels, MaxAddressSpan
//                                                -> TestE2E_SunSpec_DetectAndDiscover
//   non-SunSpec device, end of address space, broken chains, unit IDs,
//     exceptions, invalid options, cancelled context -> TestE2E_SunSpec_EdgesAndErrors
//
// Server behaviour
//   unit ID filtering by handlers, gateway exceptions -> TestE2E_Server_UnitIDFiltering
//   every handler error -> client error, for every dispatched function code
//                                                -> TestE2E_Server_ExceptionMatrix
//   handler panic -> Server Device Failure, server keeps serving
//                                                -> TestE2E_Server_HandlerPanic
//   handler breaking its contract                -> TestE2E_Server_MisbehavingHandler
//   ClientAddr passed to handlers                -> TestE2E_Server_ClientAddr
//   ClientRole from the TLS client certificate   -> TestE2E_TLS_MutualAuthAndRoles,
//                                                   TestE2E_TLS_NoRoleOnPlainTCP (tcp)
//   MaxClients (N+1th rejected, slot freed)      -> TestE2E_Server_MaxClients
//   connection context cancelled on disconnect   -> TestE2E_Server_HandlerContext_ClientDisconnect
//   ... while a handler is in flight             -> TestE2E_Server_HandlerContext_ClientDisconnectInFlight
//   context cancelled on stop, Shutdown with an in-flight handler
//                                                -> TestE2E_Server_ShutdownWithInFlightHandler
//   Start/Stop, replacement server on the same address -> TestE2E_Server_RestartOnSameAddress
//   Stop then Start of one Server instance       -> TestE2E_Server_RestartSameInstance (tcp)
//   idle Timeout, client with and without RetryPolicy -> TestE2E_Server_IdleTimeout
//   slow handler versus idle Timeout             -> TestE2E_Server_IdleTimeoutDoesNotCutResponses
//   existing: TestB2B_ExceptionAgreement, TestB2B_Adversarial_ServerBranches,
//     TestTCPServerWithConcurrentConnections, TestServerStart_LifecycleEdgeCases,
//     TestServerShutdown_ContextExpiresWhileHandlerRuns, TestServerStop_ConcurrentWithAccept
//
// Client behaviour
//   Open/Close idempotence, reopen, ErrClientNotOpen for every method, Info,
//     LastObservedTransactionID                  -> TestE2E_Client_OpenCloseLifecycle
//   Open without a server                        -> TestE2E_Client_OpenWithoutServer
//   request Timeout, late response skipped       -> TestE2E_Client_RequestTimeout
//   context deadline versus Timeout              -> TestE2E_Client_ContextDeadline
//   context cancellation in flight               -> TestE2E_Client_ContextCancelInFlight
//   context cancelled before the call            -> TestE2E_Client_ContextAlreadyCancelled
//   Close during a request                       -> TestE2E_Client_CloseDuringRequest
//   DialTimeout versus Timeout                   -> TestE2E_Client_DialTimeout (see note 1)
//   pool: MinConns pre-warm, MaxConns concurrency, waiting callers, Close
//                                                -> TestE2E_Client_ConnectionPool (tcp, see note 2)
//   no pool: requests serialised on one connection -> TestE2E_Client_SingleConnectionSerialises
//   RetryPolicy: dropped connection, exhaustion, exceptions, AttemptMetrics
//                                                -> TestE2E_Client_RetryPolicy
//   RetryOnTimeout                               -> TestE2E_Client_RetryOnTimeout
//   retry with a pool                            -> TestE2E_Client_RetryWithPool (tcp)
//   pool after a server restart                  -> TestE2E_Client_PoolAfterServerRestart (tcp)
//   retry after a failed reconnect               -> TestE2E_Client_RetryRecoversAfterFailedRedial
//
// Metrics and logging
//   ClientMetrics and ServerMetrics: one outcome per request for every method,
//     NewConfig                                  -> TestE2E_Metrics_OneOutcomePerRequest
//   success, exception, timeout, dropped connection on both sides
//                                                -> TestE2E_Metrics_Outcomes
//   unknown function code in ServerMetrics       -> TestE2E_Metrics_ServerReportsUnservedFunctionAsError
//   custom Logger receives the frames            -> TestE2E_Logging_Frames
//   NewStdLogger, NewSlogLogger, NewSlogFieldLogger, NopLogger -> TestE2E_Logging_StockAdapters
//
// TLS (tcp+tls only)
//   mutual authentication, roles, wrong role     -> TestE2E_TLS_MutualAuthAndRoles
//   client certificate not trusted by the server -> TestE2E_TLS_ClientCertNotTrusted
//   server certificate not trusted by the client, wrong server name
//                                                -> TestE2E_TLS_ServerCertNotTrusted
//   TLS 1.2 minimum on both sides                -> TestE2E_TLS_MinimumVersion
//   plain client against TLS server and vice versa -> TestE2E_TLS_TransportMismatch
//   TLSHandshakeTimeout                          -> TestE2E_TLS_HandshakeTimeout
//   LoadCertPool                                 -> TestE2E_TLS_LoadCertPool
//   existing: TestTLSServer, TestTCPoverTLSClient, TestTLSClientOnServerTimeout
//
// Not covered end to end, and why:
//   1. DialTimeout on tcp://: a loopback connect either succeeds or is refused at
//      once, so only the TLS handshake part of DialTimeout can be made to expire.
//   2. Connection pooling on tcp+tls://: the client does not pool that transport
//      (MaxConns is forced to 1), which TestE2E_Client_SingleConnectionSerialises asserts.
//   3. rtu://, ascii://, rtuovertcp://, rtuoverudp://, asciiovertcp:// and udp://:
//      the server only listens on tcp:// and tcp+tls://.
//   4. Successful FC08, FC11, FC14, FC15 and FC18 exchanges: the server has no
//      handler interface for them (covered against scripted peers elsewhere).
//   5. Two Modbus Role extensions in one client certificate: crypto/x509 rejects
//      such a certificate during the handshake, before the server sees it.
//   6. MBAP transaction ID wrap-around: it takes 65536 requests on one connection.

const (
	// e2eUnit is the unit ID the e2e device serves.
	e2eUnit uint8 = 1
	// e2eSpace is the size of every table of the e2e device: the full Modbus
	// address space, so that protocol limits and the end of the address range
	// can be exercised without the device itself getting in the way.
	e2eSpace = 0x10000
	// e2eWait bounds every "this must happen" wait in the suite.
	e2eWait = 5 * time.Second
)

// e2eCall is one request as seen by a server handler.
type e2eCall struct {
	Kind       string
	FC         FunctionCode
	UnitID     uint8
	Addr       uint16
	Quantity   uint16
	IsWrite    bool
	Bools      []bool
	Words      []uint16
	AndMask    uint16
	OrMask     uint16
	WriteAddr  uint16
	MEIType    MEIType
	Category   DeviceIDCategory
	ObjectID   DeviceIDObjectID
	ClientAddr string
	ClientRole string
}

// e2eDevice is an in-memory Modbus device implementing RequestHandler and every
// optional handler interface. It records every request it receives and lets a
// test intercept requests through a hook. It is safe for concurrent use.
type e2eDevice struct {
	mu       sync.Mutex
	unit     uint8
	coils    []bool
	discrete []bool
	holding  []uint16
	input    []uint16

	objects    []DeviceIdentificationObject
	conformity uint8

	excStatus    uint8
	commStatus   uint16
	eventCount   uint16
	messageCount uint16
	events       []byte

	calls []e2eCall
	hook  func(ctx context.Context, c e2eCall) error
}

func e2eNewDevice() *e2eDevice {
	return &e2eDevice{
		unit:         e2eUnit,
		coils:        make([]bool, e2eSpace),
		discrete:     make([]bool, e2eSpace),
		holding:      make([]uint16, e2eSpace),
		input:        make([]uint16, e2eSpace),
		objects:      regularDeviceIDObjects(),
		conformity:   0x83,
		excStatus:    0xA5,
		commStatus:   0xFFFF,
		eventCount:   0x0102,
		messageCount: 0x0304,
		events:       []byte{0x20, 0x00, 0x40},
	}
}

// setHook installs fn, which runs for every request before the device acts on
// it. A non-nil error is returned to the server as the handler error.
func (d *e2eDevice) setHook(fn func(ctx context.Context, c e2eCall) error) {
	d.mu.Lock()
	d.hook = fn
	d.mu.Unlock()
}

func (d *e2eDevice) setUnit(unit uint8) {
	d.mu.Lock()
	d.unit = unit
	d.mu.Unlock()
}

// takeCalls returns the requests received since the last call and forgets them.
func (d *e2eDevice) takeCalls() []e2eCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := d.calls
	d.calls = nil
	return out
}

func (d *e2eDevice) callCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

func (d *e2eDevice) holdingAt(addr, n int) []uint16 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]uint16(nil), d.holding[addr:addr+n]...)
}

func (d *e2eDevice) coilsAt(addr, n int) []bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]bool(nil), d.coils[addr:addr+n]...)
}

func (d *e2eDevice) setHolding(addr int, vals ...uint16) {
	d.mu.Lock()
	copy(d.holding[addr:], vals)
	d.mu.Unlock()
}

func (d *e2eDevice) setInput(addr int, vals ...uint16) {
	d.mu.Lock()
	copy(d.input[addr:], vals)
	d.mu.Unlock()
}

func (d *e2eDevice) setCoils(addr int, vals ...bool) {
	d.mu.Lock()
	copy(d.coils[addr:], vals)
	d.mu.Unlock()
}

func (d *e2eDevice) setDiscrete(addr int, vals ...bool) {
	d.mu.Lock()
	copy(d.discrete[addr:], vals)
	d.mu.Unlock()
}

// enter records the call, runs the hook and applies the unit ID filter.
func (d *e2eDevice) enter(ctx context.Context, c e2eCall) error {
	d.mu.Lock()
	d.calls = append(d.calls, c)
	hook := d.hook
	unit := d.unit
	d.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, c); err != nil {
			return err
		}
	}
	if c.UnitID != unit {
		return ErrGWPathUnavailable
	}
	return nil
}

func (d *e2eDevice) HandleCoils(ctx context.Context, req *CoilsRequest) ([]bool, error) {
	err := d.enter(ctx, e2eCall{
		Kind: "coils", FC: req.FunctionCode, UnitID: req.UnitID, Addr: req.Addr, Quantity: req.Quantity,
		IsWrite: req.IsWrite, Bools: append([]bool(nil), req.Args...),
		ClientAddr: req.ClientAddr, ClientRole: req.ClientRole,
	})
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if req.IsWrite {
		copy(d.coils[req.Addr:], req.Args)
	}
	return append([]bool(nil), d.coils[int(req.Addr):int(req.Addr)+int(req.Quantity)]...), nil
}

func (d *e2eDevice) HandleDiscreteInputs(ctx context.Context, req *DiscreteInputsRequest) ([]bool, error) {
	err := d.enter(ctx, e2eCall{
		Kind: "discrete", FC: req.FunctionCode, UnitID: req.UnitID, Addr: req.Addr, Quantity: req.Quantity,
		ClientAddr: req.ClientAddr, ClientRole: req.ClientRole,
	})
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]bool(nil), d.discrete[int(req.Addr):int(req.Addr)+int(req.Quantity)]...), nil
}

func (d *e2eDevice) HandleHoldingRegisters(ctx context.Context, req *HoldingRegistersRequest) ([]uint16, error) {
	err := d.enter(ctx, e2eCall{
		Kind: "holding", FC: req.FunctionCode, UnitID: req.UnitID, Addr: req.Addr, Quantity: req.Quantity,
		IsWrite: req.IsWrite, Words: append([]uint16(nil), req.Args...),
		ClientAddr: req.ClientAddr, ClientRole: req.ClientRole,
	})
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if req.IsWrite {
		copy(d.holding[req.Addr:], req.Args)
	}
	return append([]uint16(nil), d.holding[int(req.Addr):int(req.Addr)+int(req.Quantity)]...), nil
}

func (d *e2eDevice) HandleInputRegisters(ctx context.Context, req *InputRegistersRequest) ([]uint16, error) {
	err := d.enter(ctx, e2eCall{
		Kind: "input", FC: req.FunctionCode, UnitID: req.UnitID, Addr: req.Addr, Quantity: req.Quantity,
		ClientAddr: req.ClientAddr, ClientRole: req.ClientRole,
	})
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]uint16(nil), d.input[int(req.Addr):int(req.Addr)+int(req.Quantity)]...), nil
}

func (d *e2eDevice) HandleMaskWrite(ctx context.Context, req *MaskWriteRequest) error {
	err := d.enter(ctx, e2eCall{
		Kind: "mask", FC: req.FunctionCode, UnitID: req.UnitID, Addr: req.Addr,
		AndMask: req.AndMask, OrMask: req.OrMask,
		ClientAddr: req.ClientAddr, ClientRole: req.ClientRole,
	})
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.holding[req.Addr] = (d.holding[req.Addr] & req.AndMask) | (req.OrMask &^ req.AndMask)
	return nil
}

func (d *e2eDevice) HandleReadWriteRegisters(ctx context.Context, req *ReadWriteRegistersRequest) ([]uint16, error) {
	err := d.enter(ctx, e2eCall{
		Kind: "rw", FC: req.FunctionCode, UnitID: req.UnitID, Addr: req.ReadAddr, Quantity: req.ReadQty,
		WriteAddr: req.WriteAddr, Words: append([]uint16(nil), req.WriteValues...),
		ClientAddr: req.ClientAddr, ClientRole: req.ClientRole,
	})
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	copy(d.holding[req.WriteAddr:], req.WriteValues)
	return append([]uint16(nil), d.holding[int(req.ReadAddr):int(req.ReadAddr)+int(req.ReadQty)]...), nil
}

func (d *e2eDevice) HandleDeviceIdentification(ctx context.Context, req *DeviceIdentificationRequest) (*DeviceIdentificationResponse, error) {
	err := d.enter(ctx, e2eCall{
		Kind: "devid", FC: req.FunctionCode, UnitID: req.UnitID,
		MEIType: req.MEIType, Category: req.Category, ObjectID: req.ObjectID,
		ClientAddr: req.ClientAddr, ClientRole: req.ClientRole,
	})
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return &DeviceIdentificationResponse{
		ConformityLevel: d.conformity,
		Objects:         append([]DeviceIdentificationObject(nil), d.objects...),
	}, nil
}

func (d *e2eDevice) HandleExceptionStatus(ctx context.Context, req *ExceptionStatusRequest) (uint8, error) {
	err := d.enter(ctx, e2eCall{
		Kind: "excstatus", FC: req.FunctionCode, UnitID: req.UnitID,
		ClientAddr: req.ClientAddr, ClientRole: req.ClientRole,
	})
	if err != nil {
		return 0, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.excStatus, nil
}

func (d *e2eDevice) HandleCommEventCounter(ctx context.Context, req *CommEventCounterRequest) (*CommEventCounterResponse, error) {
	err := d.enter(ctx, e2eCall{
		Kind: "commcounter", FC: req.FunctionCode, UnitID: req.UnitID,
		ClientAddr: req.ClientAddr, ClientRole: req.ClientRole,
	})
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return &CommEventCounterResponse{Status: d.commStatus, EventCount: d.eventCount}, nil
}

func (d *e2eDevice) HandleCommEventLog(ctx context.Context, req *CommEventLogRequest) (*CommEventLogResponse, error) {
	err := d.enter(ctx, e2eCall{
		Kind: "commlog", FC: req.FunctionCode, UnitID: req.UnitID,
		ClientAddr: req.ClientAddr, ClientRole: req.ClientRole,
	})
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return &CommEventLogResponse{
		Status:       d.commStatus,
		EventCount:   d.eventCount,
		MessageCount: d.messageCount,
		Events:       append([]byte(nil), d.events...),
	}, nil
}

// e2eBasicOnly exposes only the four mandatory handler methods of an e2eDevice,
// i.e. a device that implements none of the optional handler interfaces.
type e2eBasicOnly struct{ d *e2eDevice }

func (b e2eBasicOnly) HandleCoils(ctx context.Context, req *CoilsRequest) ([]bool, error) {
	return b.d.HandleCoils(ctx, req)
}

func (b e2eBasicOnly) HandleDiscreteInputs(ctx context.Context, req *DiscreteInputsRequest) ([]bool, error) {
	return b.d.HandleDiscreteInputs(ctx, req)
}

func (b e2eBasicOnly) HandleHoldingRegisters(ctx context.Context, req *HoldingRegistersRequest) ([]uint16, error) {
	return b.d.HandleHoldingRegisters(ctx, req)
}

func (b e2eBasicOnly) HandleInputRegisters(ctx context.Context, req *InputRegistersRequest) ([]uint16, error) {
	return b.d.HandleInputRegisters(ctx, req)
}

// --- PKI ---------------------------------------------------------------------

// e2eCA is a throwaway certificate authority.
type e2eCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

// e2ePKIData holds the certificates shared by the suite. They are generated at
// run time so that the suite does not depend on the expiry of embedded test
// certificates.
type e2ePKIData struct {
	ca         *e2eCA
	otherCA    *e2eCA
	serverCert tls.Certificate
	clientCert tls.Certificate
}

var (
	e2ePKIOnce sync.Once
	e2ePKIVal  *e2ePKIData
	e2ePKIErr  error
	e2eSerial  int64 = 1000
	e2eSerialM sync.Mutex
)

func e2eNextSerial() *big.Int {
	e2eSerialM.Lock()
	defer e2eSerialM.Unlock()
	e2eSerial++
	return big.NewInt(e2eSerial)
}

func e2eNewCA(cn string) (*e2eCA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          e2eNextSerial(),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &e2eCA{cert: cert, key: key, pool: pool}, nil
}

// issue returns a leaf certificate signed by the CA. server selects a server
// (SAN localhost/127.0.0.1) or a client certificate; roles adds a Modbus Role
// extension (at most one: crypto/x509 refuses certificates with duplicate
// extensions, so the server's duplicate-role check cannot be reached through a
// real handshake).
func (ca *e2eCA) issue(cn string, server bool, roles ...string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: e2eNextSerial(),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	if server {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.DNSNames = []string{"localhost"}
		tmpl.IPAddresses = []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	for _, role := range roles {
		// The Modbus Role is a UTF8String; a "printable:" prefix encodes it as
		// a PrintableString instead, which the server must not accept.
		params := "utf8"
		if rest, ok := strings.CutPrefix(role, "printable:"); ok {
			role, params = rest, "printable"
		}
		val, err := asn1.MarshalWithParams(role, params)
		if err != nil {
			return tls.Certificate{}, err
		}
		tmpl.ExtraExtensions = append(tmpl.ExtraExtensions, pkix.Extension{Id: modbusRoleOID, Value: val})
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

func e2ePKI(t *testing.T) *e2ePKIData {
	t.Helper()
	e2ePKIOnce.Do(func() {
		p := &e2ePKIData{}
		if p.ca, e2ePKIErr = e2eNewCA("e2e test CA"); e2ePKIErr != nil {
			return
		}
		if p.otherCA, e2ePKIErr = e2eNewCA("e2e other CA"); e2ePKIErr != nil {
			return
		}
		if p.serverCert, e2ePKIErr = p.ca.issue("e2e server", true); e2ePKIErr != nil {
			return
		}
		if p.clientCert, e2ePKIErr = p.ca.issue("e2e client", false, "operator"); e2ePKIErr != nil {
			return
		}
		e2ePKIVal = p
	})
	if e2ePKIErr != nil {
		t.Fatalf("e2e PKI: %v", e2ePKIErr)
	}
	return e2ePKIVal
}

func e2eIssue(t *testing.T, ca *e2eCA, cn string, server bool, roles ...string) *tls.Certificate {
	t.Helper()
	c, err := ca.issue(cn, server, roles...)
	if err != nil {
		t.Fatalf("issue %s: %v", cn, err)
	}
	return &c
}

// --- client/server pair ------------------------------------------------------

// e2eOpts customises e2eStart.
type e2eOpts struct {
	// server mutates the server configuration before NewServer.
	server func(*ServerConfig)
	// client mutates the client configuration before New.
	client func(*Config)
	// noOpen leaves the client closed.
	noOpen bool
}

// e2ePair is a started server plus a client configured to reach it.
type e2ePair struct {
	t          *testing.T
	kind       string
	server     *Server
	client     *Client
	hostPort   string
	clientConf Config
}

// e2eClientURL returns the client URL for a server listening on hostPort.
// tcp+tls connects through "localhost" so that the server certificate validates.
func e2eClientURL(kind, hostPort string) string {
	if kind == "tcp+tls" {
		_, port, _ := net.SplitHostPort(hostPort)
		return "tcp+tls://localhost:" + port
	}
	return kind + "://" + hostPort
}

// e2eServerConfig returns a server configuration for kind listening on listen.
func e2eServerConfig(t *testing.T, kind, listen string) ServerConfig {
	t.Helper()
	conf := ServerConfig{URL: kind + "://" + listen, MaxClients: 16}
	if kind == "tcp+tls" {
		pki := e2ePKI(t)
		conf.TLSServerCert = &pki.serverCert
		conf.TLSClientCAs = pki.ca.pool
	}
	return conf
}

// e2eClientConfig returns a client configuration for kind reaching hostPort.
func e2eClientConfig(t *testing.T, kind, hostPort string) Config {
	t.Helper()
	conf := Config{URL: e2eClientURL(kind, hostPort), Timeout: 3 * time.Second}
	if kind == "tcp+tls" {
		pki := e2ePKI(t)
		conf.TLSClientCert = &pki.clientCert
		conf.TLSRootCAs = pki.ca.pool
	}
	return conf
}

// e2eStart starts a server for kind ("tcp" or "tcp+tls") on a free loopback port
// and returns it together with a client. Both are torn down with the test.
func e2eStart(t *testing.T, kind string, handler RequestHandler, opts e2eOpts) *e2ePair {
	t.Helper()
	sconf := e2eServerConfig(t, kind, "127.0.0.1:0")
	if opts.server != nil {
		opts.server(&sconf)
	}
	server, err := NewServer(&sconf, handler)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = server.Stop() })

	p := &e2ePair{t: t, kind: kind, server: server, hostPort: server.tcpListener.Addr().String()}
	p.clientConf = e2eClientConfig(t, kind, p.hostPort)
	if opts.client != nil {
		opts.client(&p.clientConf)
	}
	p.client = p.newClient(nil)
	if !opts.noOpen {
		if err := p.client.Open(); err != nil {
			t.Fatalf("Open: %v", err)
		}
	}
	return p
}

// newClient returns another (closed) client for the pair's server, using the
// pair's client configuration optionally modified by mut.
func (p *e2ePair) newClient(mut func(*Config)) *Client {
	p.t.Helper()
	conf := p.clientConf
	if mut != nil {
		mut(&conf)
	}
	c, err := New(conf)
	if err != nil {
		p.t.Fatalf("New: %v", err)
	}
	p.t.Cleanup(func() { _ = c.Close() })
	return c
}

// conns returns the number of client connections the server currently tracks.
func (p *e2ePair) conns() int { return e2eServerConns(p.server) }

func e2eServerConns(s *Server) int {
	s.lock.Lock()
	defer s.lock.Unlock()
	return len(s.tcpClients)
}

// e2eEventually polls cond until it holds, failing the test after e2eWait.
func e2eEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(e2eWait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// e2eFreeAddr returns a loopback host:port that was free a moment ago.
func e2eFreeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// --- observability recorders --------------------------------------------------

// e2eEvent is one metrics callback.
type e2eEvent struct {
	Kind    string
	Unit    uint8
	FC      FunctionCode
	Attempt int
	Err     error
}

func (e e2eEvent) String() string {
	return fmt.Sprintf("%s(unit=%d fc=0x%02X attempt=%d err=%v)", e.Kind, e.Unit, uint8(e.FC), e.Attempt, e.Err)
}

// e2eMetrics records ClientMetrics / ServerMetrics callbacks.
type e2eMetrics struct {
	mu     sync.Mutex
	events []e2eEvent
}

func (m *e2eMetrics) add(e e2eEvent) {
	m.mu.Lock()
	m.events = append(m.events, e)
	m.mu.Unlock()
}

func (m *e2eMetrics) OnRequest(unitID uint8, fc FunctionCode) {
	m.add(e2eEvent{Kind: "request", Unit: unitID, FC: fc})
}

func (m *e2eMetrics) OnResponse(unitID uint8, fc FunctionCode, _ time.Duration) {
	m.add(e2eEvent{Kind: "response", Unit: unitID, FC: fc})
}

func (m *e2eMetrics) OnError(unitID uint8, fc FunctionCode, _ time.Duration, err error) {
	m.add(e2eEvent{Kind: "error", Unit: unitID, FC: fc, Err: err})
}

func (m *e2eMetrics) OnTimeout(unitID uint8, fc FunctionCode, _ time.Duration) {
	m.add(e2eEvent{Kind: "timeout", Unit: unitID, FC: fc})
}

// take returns the events recorded since the last call and forgets them.
func (m *e2eMetrics) take() []e2eEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.events
	m.events = nil
	return out
}

func (m *e2eMetrics) len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events)
}

// e2eAttemptMetrics additionally records AttemptMetrics callbacks.
type e2eAttemptMetrics struct{ e2eMetrics }

func (m *e2eAttemptMetrics) OnAttempt(unitID uint8, fc FunctionCode, attempt int, _ time.Duration, err error) {
	m.add(e2eEvent{Kind: "attempt", Unit: unitID, FC: fc, Attempt: attempt, Err: err})
}

func (m *e2eAttemptMetrics) OnRetryDial(attempt int, _ time.Duration, err error) {
	m.add(e2eEvent{Kind: "retrydial", Attempt: attempt, Err: err})
}

// e2eKinds renders events as "kind:fc" strings for compact comparisons.
func e2eKinds(events []e2eEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = fmt.Sprintf("%s:%02X", e.Kind, uint8(e.FC))
	}
	return out
}

// e2eLogger records formatted log lines, prefixed with their level.
type e2eLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *e2eLogger) add(level, format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, level+" "+fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *e2eLogger) Debugf(format string, args ...any) { l.add("D", format, args...) }
func (l *e2eLogger) Infof(format string, args ...any)  { l.add("I", format, args...) }
func (l *e2eLogger) Warnf(format string, args ...any)  { l.add("W", format, args...) }
func (l *e2eLogger) Errorf(format string, args ...any) { l.add("E", format, args...) }

// has reports whether a recorded line contains every one of subs.
func (l *e2eLogger) has(subs ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		ok := true
		for _, s := range subs {
			if !strings.Contains(line, s) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

func (l *e2eLogger) dump() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}
