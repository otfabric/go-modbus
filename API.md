# `github.com/otfabric/go-modbus` — Public API Reference

This document covers every exported type, function, and interface in the library.
All examples assume `import "github.com/otfabric/go-modbus"`.

---

## Table of Contents

1. [Transport modes and URL schemes](#1-transport-modes-and-url-schemes)
2. [Client](#2-client)
   - [Configuration](#21-config)
   - [Lifecycle](#22-lifecycle)
   - [Read operations](#23-read-operations)
   - [Write operations](#24-write-operations)
   - [Advanced register operations (FC20/21/23/24)](#25-advanced-register-operations-fc20212324)
   - [Device identification (FC43)](#26-device-identification-fc43)
   - [Modbus device detection](#27-modbus-device-detection)
   - [SunSpec discovery](#28-sunspec-discovery)
   - [Serial-line FCs and Diagnostics (FC07/FC08/FC0B/FC0C/FC11)](#29-serial-line-function-codes-and-diagnostics-fc07fc08fc0bfc0cfc11)
3. [Server](#3-server)
   - [Configuration](#31-serverconfig)
   - [Lifecycle](#32-lifecycle)
   - [RequestHandler interface](#33-requesthandler-interface)
   - [Request types](#34-request-types)
   - [Optional handler interfaces (FC07, FC0B, FC0C, FC22, FC23)](#35-optional-handler-interfaces-fc07-fc0b-fc0c-fc22-fc23)
4. [Errors](#4-errors)
5. [Logging](#5-logging)
6. [Metrics](#6-metrics)
7. [Retry policy](#7-retry-policy)
8. [Connection pool](#8-connection-pool)
9. [TLS helpers](#9-tls-helpers)
10. [Type constants](#10-type-constants)
11. [Codec API](#11-codec-api)

---

## 1. Transport modes and URL schemes

The `URL` field in both `Config` and `ServerConfig` encodes the
transport type and the address using the `<scheme>://<address>` format.

| Scheme | Transport | Client | Server |
|---|---|---|---|
| `rtu://<device>` | Modbus RTU over serial | ✓ | — |
| `rtuovertcp://<host:port>` | Modbus RTU framing over TCP | ✓ | — |
| `rtuoverudp://<host:port>` | Modbus RTU framing over UDP | ✓ | — |
| `tcp://<host:port>` | Modbus TCP (MBAP) | ✓ | ✓ |
| `tcp+tls://<host:port>` | Modbus TCP over TLS (MBAPS) | ✓ | ✓ |
| `udp://<host:port>` | Modbus TCP framing over UDP | ✓ | — |

**Standard ports:** `PortModbusTCP` (502) and `PortModbusTLS` (802) are package constants for use in URLs or documentation. Modbus RTU over TCP has no standard port.

---

## 2. Client

### 2.1 `Config`

```go
type Config struct {
    // URL encodes the transport type and target address (required).
    // Examples: "tcp://plc.local:502", "rtu:///dev/ttyUSB0", "tcp+tls://plc.local:802"
    URL string

    // Speed is the serial baud rate (rtu only). Default: 19200.
    Speed uint

    // DataBits is the number of data bits per character (rtu only). Default: 8.
    DataBits uint

    // Parity is the serial parity mode (rtu only). Default: ParityNone.
    Parity Parity

    // StopBits is the number of serial stop bits (rtu only).
    // Default: 2 when ParityNone, 1 otherwise.
    StopBits uint

    // Timeout is the per-request I/O deadline. If 0, a sensible default is applied:
    // 300 ms for RTU, 1 s for all TCP/UDP modes.
    Timeout time.Duration

    // DialTimeout is the maximum time to establish a connection (TCP dial, TLS
    // handshake, UDP dial). 0 uses a sensible default: 5 s for TCP/UDP, 15 s for TLS.
    // Does not apply to serial (RTU) transports.
    DialTimeout time.Duration

    // TLSClientCert is the client-side TLS certificate and private key (tcp+tls only).
    // Required: mutual TLS authentication is mandatory per the MBAPS spec.
    TLSClientCert *tls.Certificate

    // TLSRootCAs contains CAs (or leaf certs for pinning) used to verify the server
    // certificate (tcp+tls only). Required.
    TLSRootCAs *x509.CertPool

    // Logger is the sink for log output. If nil (default), logging is disabled
    // (no-op logger). Build a value with NewStdLogger, NewSlogLogger, or NopLogger.
    Logger Logger

    // RetryPolicy controls automatic retry of failed requests.
    // Nil (default) is equivalent to NoRetry().
    RetryPolicy RetryPolicy

    // Metrics receives callbacks for every request outcome.
    // Nil (default) disables collection.
    Metrics ClientMetrics

    // MinConns is the number of connections pre-warmed during Open().
    // Applies to TCP-based transports only. 0 = no pre-warming.
    MinConns int

    // MaxConns is the pool size. 0 or 1 = single connection (default).
    // Values > 1 enable the connection pool for concurrent goroutines.
    // Applies to TCP-based transports only.
    MaxConns int
}
```

**Configuration grouping** — for larger setups, use grouped sub-structs:

```go
type TransportConfig struct {
    URL, Speed, DataBits, Parity, StopBits, DialTimeout, TLSClientCert, TLSRootCAs
}

type ExecutionConfig struct {
    Timeout, RetryPolicy, MinConns, MaxConns
}

type ObservabilityConfig struct {
    Logger, Metrics
}

func NewConfig(tc TransportConfig, ec ExecutionConfig, oc ObservabilityConfig) Config
```

### 2.2 Lifecycle

```go
func New(conf Config) (*Client, error)
func ValidateConfig(conf Config) error
func NewConfig(tc TransportConfig, ec ExecutionConfig, oc ObservabilityConfig) Config
func (mc *Client) Open() error
func (mc *Client) Close() error
func (mc *Client) Info() ClientInfo
func (mc *Client) LastObservedTransactionID() uint16
```

`New` validates the URL and configuration but does **not** open a network
connection. It returns typed `*ConfigurationError` values on failure.
`ValidateConfig` runs the same validation without creating a client — useful
for CLIs and config-driven systems. `NewConfig` builds a `Config` from grouped
sub-configurations — a convenience alternative to the flat struct literal.
Call `Open` to establish the transport. `Open` is idempotent — calling it on an
already-open client is a no-op. `Close` closes all connections (or drains the
pool when `MaxConns > 1`).

**Config auto-correction:** `MaxConns > 1` on non-poolable transports (RTU, TCP+TLS)
is silently clamped to 1 with a warning log message.

`Info` returns a `ClientInfo` snapshot:

```go
type ClientInfo struct {
    IsOpen      bool          // active transport/connection
    Endpoint    string        // resolved target address
    Transport   TransportKind // "rtu", "tcp", "tcp+tls", "udp", "rtuovertcp", "rtuoverudp"
    PoolEnabled bool          // true when MaxConns > 1 AND transport supports pooling
    MaxConns    int           // configured maximum connections
}
```

`LastObservedTransactionID` returns the MBAP transaction ID of the last successful
TCP response; it is 0 for RTU and other non-TCP transports. In pooled/concurrent
use (`MaxConns > 1`), this is a shared diagnostic value and is not correlated to
any specific request.

```go
client, err := modbus.New(modbus.Config{
    URL:     "tcp://192.168.1.10:502",
    Timeout: 2 * time.Second,
})
if err != nil {
    log.Fatal(err)
}
if err := client.Open(); err != nil {
    log.Fatal(err)
}
defer client.Close()
```

**TLS client:**

```go
cert, _ := tls.LoadX509KeyPair("client.crt", "client.key")
pool, _ := modbus.LoadCertPool("ca.crt")

client, err := modbus.New(modbus.Config{
    URL:           "tcp+tls://plc.local:802",
    TLSClientCert: &cert,
    TLSRootCAs:    pool,
    Timeout:       5 * time.Second,
})
```

### 2.3 Read operations

All read methods share the same signature preamble:

```go
func (mc *Client) <Method>(ctx context.Context, unitID uint8, addr uint16, ...) (..., error)
```

`ctx` propagates cancellation and deadlines. If the context carries a deadline it
overrides the configured `Timeout`. `unitID` is the Modbus slave/unit ID (1–247;
255 is broadcast).

#### Coils and discrete inputs

```go
// FC01 — read one coil
func (mc *Client) ReadCoil(ctx context.Context, unitID uint8, addr uint16) (bool, error)

// FC01 — read multiple coils (quantity ≤ 2000)
func (mc *Client) ReadCoils(ctx context.Context, unitID uint8, addr uint16, quantity uint16) ([]bool, error)

// FC02 — read one discrete input
func (mc *Client) ReadDiscreteInput(ctx context.Context, unitID uint8, addr uint16) (bool, error)

// FC02 — read multiple discrete inputs (quantity ≤ 2000)
func (mc *Client) ReadDiscreteInputs(ctx context.Context, unitID uint8, addr uint16, quantity uint16) ([]bool, error)
```

```go
coils, err := client.ReadCoils(ctx, 1, 0x0000, 8)
// coils[0] = coil at address 0, coils[7] = coil at address 7
```

#### 16-bit registers

```go
// FC03/FC04 — read one register as uint16
func (mc *Client) ReadRegister(ctx context.Context, unitID uint8, addr uint16, regType RegType) (uint16, error)

// FC03/FC04 — read multiple registers as []uint16 (quantity ≤ 125)
func (mc *Client) ReadRegisters(ctx context.Context, unitID uint8, addr uint16, quantity uint16, regType RegType) ([]uint16, error)
```

`regType` is `HoldingRegister` (FC03) or `InputRegister` (FC04).

```go
val, err := client.ReadRegister(ctx, 1, 0x1000, modbus.HoldingRegister)
```

For typed reads (32/64-bit, float, ASCII, BCD, IP, etc.) use the **codec API**: `codec.ReadFromClient[T](mc, ctx, unitID, addr, regType, dec)`. See [§ 11 Codec API](#11-codec-api).

#### Convenience aliases

```go
func (mc *Client) ReadHoldingRegister(ctx context.Context, unitID uint8, addr uint16) (uint16, error)
func (mc *Client) ReadHoldingRegisters(ctx context.Context, unitID uint8, addr uint16, quantity uint16) ([]uint16, error)
func (mc *Client) ReadInputRegister(ctx context.Context, unitID uint8, addr uint16) (uint16, error)
func (mc *Client) ReadInputRegisters(ctx context.Context, unitID uint8, addr uint16, quantity uint16) ([]uint16, error)
```

These are convenience wrappers for `ReadRegister` and `ReadRegisters` with the `RegType` pre-set.

#### Raw bytes

**Transport convenience helpers** — no interpretation or codec; for typed values use the codec API.

```go
// FC03/FC04 — read registers as bytes in wire order (no reordering)
func (mc *Client) ReadRegisterBytes(ctx context.Context, unitID uint8, addr uint16, byteCount uint16, regType RegType) ([]byte, error)
```

**byteCount** is the number of bytes to read (the library reads `ceil(byteCount/2)` registers). To read N registers, pass `byteCount = N*2`. Bytes are returned in wire order. Odd byteCount is valid (e.g. byteCount 3 reads 2 registers and returns exactly 3 bytes, with the trailing padding byte trimmed).

#### Bitfield / masked register operations

Many devices expose booleans and enums inside holding (or input) registers rather than coils. The following helpers read or update individual bits or bit ranges without clobbering adjacent bits.

```go
// FC03/FC04 — read one bit from a register (bitIndex 0 = LSB, 15 = MSB)
func (mc *Client) ReadRegisterBit(ctx context.Context, unitID uint8, addr uint16, bitIndex uint8, regType RegType) (bool, error)

// FC03/FC04 — read count bits from one register starting at bitIndex (count 1–16, bitIndex+count ≤ 16)
func (mc *Client) ReadRegisterBits(ctx context.Context, unitID uint8, addr uint16, bitIndex, count uint8, regType RegType) ([]bool, error)

// FC03 + FC16 — read register, set or clear one bit, write back (holding registers only)
func (mc *Client) WriteRegisterBit(ctx context.Context, unitID uint8, addr uint16, bitIndex uint8, value bool) error

// FC03 + FC16 — read-modify-write: newVal = (old & ^mask) | (value & mask) (holding registers only)
func (mc *Client) UpdateRegisterMask(ctx context.Context, unitID uint8, addr uint16, mask, value uint16) error
```

- **ReadRegisterBit** — Reads one register and returns `(reg>>bitIndex)&1 != 0`. Use for status bits, alarm bits, or single enum bits. `bitIndex > 15` returns `ErrUnexpectedParameters`.
- **ReadRegisterBits** — Reads one register and returns a slice of `count` booleans from `bitIndex` upward. Use for multi-bit mode enums. Invalid `count` or `bitIndex+count > 16` returns `ErrUnexpectedParameters`.
- **WriteRegisterBit** — Read-modify-write: reads the holding register, sets or clears the bit at `bitIndex`, writes back. Other bits unchanged. `bitIndex > 15` returns `ErrUnexpectedParameters`.
- **UpdateRegisterMask** — Read-modify-write: only the bits set in `mask` are updated to the corresponding bits in `value`; all other bits are preserved. Use for control words and mode fields without affecting adjacent bits.

#### Atomic mask write (FC22)

```go
// FC22 (0x16) — server-side atomic read-modify-write: result = (current AND andMask) OR (orMask AND NOT andMask)
func (mc *Client) MaskWriteRegister(ctx context.Context, unitID uint8, addr uint16, andMask, orMask uint16) error
```

Unlike `WriteRegisterBit` and `UpdateRegisterMask` (which do client-side read-modify-write using FC03+FC16), `MaskWriteRegister` is a single atomic operation on the server. Use it when concurrent clients or asynchronous device firmware may modify the same register, and atomicity matters.

**Example — set bit 3 without disturbing other bits:**

```go
err := client.MaskWriteRegister(ctx, 1, 0x0010,
    0xFFFF,  // AND mask: keep all bits
    0x0008,  // OR mask:  set bit 3
)
```

**Example — clear bits 4–7:**

```go
err := client.MaskWriteRegister(ctx, 1, 0x0010,
    0xFF0F,  // AND mask: clear bits 4–7
    0x0000,  // OR mask:  no bits to set
)
```

### 2.4 Write operations

#### Coils

```go
// FC05 — write one coil (true → 0xFF00, false → 0x0000)
func (mc *Client) WriteCoil(ctx context.Context, unitID uint8, addr uint16, value bool) error

// FC05 — write one coil with an arbitrary 16-bit payload (non-standard; use sparingly)
func (mc *Client) WriteCoilRaw(ctx context.Context, unitID uint8, addr uint16, payload uint16) error

// FC15 — write multiple coils (quantity ≤ 1968)
func (mc *Client) WriteCoils(ctx context.Context, unitID uint8, addr uint16, values []bool) error
```

#### 16-bit registers

```go
// FC06 — write one 16-bit register
func (mc *Client) WriteRegister(ctx context.Context, unitID uint8, addr uint16, value uint16) error

// FC16 — write multiple 16-bit registers (quantity ≤ 123)
func (mc *Client) WriteRegisters(ctx context.Context, unitID uint8, addr uint16, values []uint16) error
```

For typed writes (32/64-bit, float, ASCII, BCD, IP, etc.) use the **codec API**: `codec.WriteToClient[T](mc, ctx, unitID, addr, value, enc)`. See [§ 11 Codec API](#11-codec-api).

#### Raw bytes

**Transport convenience helpers** — no interpretation or codec; for typed values use the codec API.

```go
// FC16 — write bytes into registers in wire order (no reordering)
func (mc *Client) WriteRegisterBytes(ctx context.Context, unitID uint8, addr uint16, values []byte) error
```

Odd-length byte slices are zero-padded to the next register boundary (implicit register-boundary handling).

### 2.5 Advanced register operations (FC20/21/23/24)

#### Read/Write Multiple Registers — FC23

Executes a combined write-then-read in a single Modbus transaction. The write
operation is always performed on the server side before the read. Both addresses
are holding registers. Request and response use raw `[]uint16` register values.

```go
// FC23 — write writeValues starting at writeAddr, then read readQty registers
// starting at readAddr, atomically.
// readQty  ≤ 125 (0x7D), len(writeValues) ≤ 121 (0x79)
func (mc *Client) ReadWriteMultipleRegisters(
    ctx         context.Context,
    unitID      uint8,
    readAddr    uint16,
    readQty     uint16,
    writeAddr   uint16,
    writeValues []uint16,
) ([]uint16, error)
```

```go
// Atomically write 3 configuration registers and read back 6 status registers.
result, err := client.ReadWriteMultipleRegisters(ctx, 1,
    0x0100, 6,                            // read 6 regs from 0x0100
    0x0200, []uint16{0x01, 0x02, 0x03},   // write 3 regs at 0x0200
)
```

#### Read FIFO Queue — FC24

Reads the contents of a FIFO queue of holding registers. `addr` is the FIFO
Pointer Address (the count register). The queue count register is returned first
by the server; the library strips it and returns only the queued data registers.

The server returns an exception (`ErrIllegalDataValue`) if the queue has more
than 31 entries.

```go
// FC24 — read the FIFO queue at the given pointer address.
// Returns up to 31 uint16 values (queue count ≤ 31).
func (mc *Client) ReadFIFOQueue(
    ctx    context.Context,
    unitID uint8,
    addr   uint16,
) ([]uint16, error)
```

```go
queue, err := client.ReadFIFOQueue(ctx, 1, 0x04DE)
if err != nil {
    log.Fatal(err)
}
fmt.Printf("FIFO contains %d entries\n", len(queue))
```

#### Read File Records — FC20

Reads one or more groups of registers from a file on the remote device.
Each file contains up to 10 000 records (0–9999); records are addressed by
file number and record number within the file.

```go
// FileRecordRequest describes one sub-request for ReadFileRecords.
type FileRecordRequest struct {
    FileNumber   uint16 // file number (1–0xFFFF)
    RecordNumber uint16 // starting record within the file (0–9999 / 0x270F)
    RecordLength uint16 // number of 16-bit registers to read (≥ 1)
}

// FC20 — read one or more groups of file records.
// Returns one []uint16 slice per sub-request, in the same order.
// Register data is returned in big-endian wire order.
func (mc *Client) ReadFileRecords(
    ctx      context.Context,
    unitID   uint8,
    requests []FileRecordRequest,
) ([][]uint16, error)
```

```go
// Read 2 registers from file 4, record 1, and
// 2 registers from file 3, record 9, in one round-trip.
results, err := client.ReadFileRecords(ctx, 1, []modbus.FileRecordRequest{
    {FileNumber: 4, RecordNumber: 1, RecordLength: 2},
    {FileNumber: 3, RecordNumber: 9, RecordLength: 2},
})
if err != nil {
    log.Fatal(err)
}
fmt.Println("file 4 record 1:", results[0]) // e.g. [0x0DFE 0x0020]
fmt.Println("file 3 record 9:", results[1]) // e.g. [0x33CD 0x0040]
```

#### Write File Records — FC21

Writes one or more groups of registers to a file on the remote device.
The response is an echo of the entire request; the library validates the echo
before returning.

```go
// FileRecord describes one sub-request for WriteFileRecords.
// The record length is implied by len(Data).
type FileRecord struct {
    FileNumber   uint16   // file number (1–0xFFFF)
    RecordNumber uint16   // starting record within the file (0–9999 / 0x270F)
    Data         []uint16 // register values to write (len ≥ 1)
}

// FC21 — write one or more groups of file records.
// Register values are encoded as big-endian uint16 on the wire.
func (mc *Client) WriteFileRecords(
    ctx     context.Context,
    unitID  uint8,
    records []FileRecord,
) error
```

```go
// Write 3 registers to file 4, starting at record 7.
err := client.WriteFileRecords(ctx, 1, []modbus.FileRecord{
    {
        FileNumber:   4,
        RecordNumber: 7,
        Data:         []uint16{0x06AF, 0x04BE, 0x100D},
    },
})
if err != nil {
    log.Fatal(err)
}
```

---

### 2.6 Device identification (FC43)

Device identification (FC43 / MEI 0x0E) exposes three categories of objects:

- **Basic** (mandatory): VendorName, ProductCode, MajorMinorRevision
- **Regular** (optional): Basic + VendorUrl, ProductName, ModelName, UserApplicationName
- **Extended** (optional): Regular + private/vendor objects (object IDs 0x80–0xFF)

Use **ReadAllDeviceIdentification** to fetch everything the device supports in one call; use **ReadDeviceIdentification** when you need a specific category or a single object.

#### ReadAllDeviceIdentification — get all available identification

```go
func (mc *Client) ReadAllDeviceIdentification(
    ctx    context.Context,
    unitID uint8,
) (*DeviceIdentification, error)
```

Requests the Extended category; the device responds with all objects it implements (basic, regular, and/or extended, per its conformity level). Automatically pages through `MoreFollows`. Prefer this when you want a complete snapshot.

```go
di, err := client.ReadAllDeviceIdentification(ctx, 1)
if err != nil {
    log.Fatal(err)
}
fmt.Printf("Conformity level: 0x%02x\n", di.ConformityLevel)
for _, obj := range di.Objects {
    fmt.Printf("%s = %s\n", obj.Name, obj.Value)
}
```

#### ReadDeviceIdentification — category or single object

```go
func (mc *Client) ReadDeviceIdentification(
    ctx         context.Context,
    unitID      uint8,
    category    DeviceIDCategory,
    startObject DeviceIDObjectID,
) (*DeviceIdentification, error)
```

Sends a FC43 / MEI 0x0E request for a specific category or one object. Pages through `MoreFollows` and returns all objects for that request.

**Read device ID code constants:**

| Constant | Value | Category |
|----------|-------|----------|
| `modbus.DeviceIDBasic` | `0x01` | Basic (VendorName, ProductCode, MajorMinorRevision) |
| `modbus.DeviceIDRegular` | `0x02` | Regular (+ VendorUrl, ProductName, ModelName, UserApplicationName) |
| `modbus.DeviceIDExtended` | `0x03` | Extended (+ private objects 0x80–0xFF) |
| `modbus.DeviceIDIndividual` | `0x04` | Single object (set `startObject` to desired ID) |

For stream access (Basic/Regular/Extended), pass `startObject` as `0x00` to start from the first object. For Individual, pass the desired object ID. If you request a higher category than the device supports, it responds at its actual conformity level.

**Response types:**

```go
type DeviceIDCategory uint8

const (
    DeviceIDBasic      DeviceIDCategory = 0x01
    DeviceIDRegular    DeviceIDCategory = 0x02
    DeviceIDExtended   DeviceIDCategory = 0x03
    DeviceIDIndividual DeviceIDCategory = 0x04
)

type DeviceIDObjectID uint8

type DeviceIdentification struct {
    Category        DeviceIDCategory
    ConformityLevel uint8
    MoreFollows     bool
    NextObjectID    DeviceIDObjectID
    Objects         []DeviceIdentificationObject
}

type DeviceIdentificationObject struct {
    ID    DeviceIDObjectID
    Name  string
    Value string
}
```

**Example — basic only:**

```go
di, err := client.ReadDeviceIdentification(ctx, 1, modbus.DeviceIDBasic, 0x00)
if err != nil {
    log.Fatal(err)
}
for _, obj := range di.Objects {
    fmt.Printf("%s = %s\n", obj.Name, obj.Value)
}
```

**Example — single object (individual access):**

```go
di, err := client.ReadDeviceIdentification(ctx, 1, modbus.DeviceIDIndividual, 0x04)
if err != nil {
    log.Fatal(err)
}
// di.Objects has one element: ProductName (0x04)
```

#### Conformity level helpers

```go
// SupportsStreamAccess reports whether the device conformity level indicates
// support for stream access (Basic, Regular, or Extended categories).
// True for conformity levels 0x01, 0x02, 0x03, 0x81, 0x82, 0x83.
func (di *DeviceIdentification) SupportsStreamAccess() bool

// SupportsIndividualAccess reports whether the device conformity level
// indicates support for individual object access (category 0x04).
// True for conformity levels 0x81, 0x82, 0x83.
func (di *DeviceIdentification) SupportsIndividualAccess() bool
```

#### Validation rules (FC43/14)

The client enforces the following Modbus-spec validation on FC43 responses:

- **Conformity level** must be one of `0x01, 0x02, 0x03, 0x81, 0x82, 0x83`. Other values
  produce a `*ProtocolError`.
- When **MoreFollows** is `0x00` (no continuation), **NextObjectID** must also be `0x00`.
- For **Individual access** (category `0x04`): `MoreFollows` must be `0x00` and exactly one
  object must be returned.
- Requesting an unknown object ID via stream access causes the device to restart from
  object 0; individual access for an unknown ID returns `ErrIllegalDataAddress`.

#### MEI type 13 (CANopen)

FC43 sub-type 13 (0x0D) is intentionally unsupported. It targets CANopen device profiles
and has no practical use in typical Modbus deployments. Only MEI type 14 (0x0E, Read
Device Identification) is implemented.

---

### 2.7 Modbus device detection

**SupportsFunction** probes the given unit with a single read-style function code and returns whether the unit responded with a structurally valid Modbus response (normal or exception). Supported FCs: FC08, FC43, FC03, FC04, FC01, FC02, FC11, FC18, FC20. For any other FC returns `(false, ErrUnexpectedParameters)`. Use after **Open()**.

Error classification: returns `(false, nil)` for expected probe-negative outcomes (timeout, Modbus exception, gateway target failure); returns `(false, err)` for real transport/client errors (broken socket, bad CRC, bad unit ID, client not open). Context cancellation returns `(false, ctx.Err())`.

```go
func (mc *Client) SupportsFunction(ctx context.Context, unitID uint8, fc FunctionCode) (bool, error)
```

**SupportsDeviceIdentification** reports whether the unit supports Read Device Identification (FC43). Equivalent to `SupportsFunction(ctx, unitID, FCEncapsulatedInterface)`. Use after **Open()**.

```go
func (mc *Client) SupportsDeviceIdentification(ctx context.Context, unitID uint8) (bool, error)
```

**ProbeFunction** provides detailed probe diagnostics. Unlike `SupportsFunction`,
it never returns a non-nil error for expected probe-negative outcomes — those are
captured in the `ProbeResult`. Only context cancellation and unsupported probe FCs
return a non-nil error.

```go
func (mc *Client) ProbeFunction(ctx context.Context, unitID uint8, fc FunctionCode) (ProbeResult, error)

type ProbeOutcome uint8

const (
    ProbeSupported        ProbeOutcome = iota // valid normal response
    ProbeException                            // Modbus exception
    ProbeTimeout                              // no response (timeout/gateway)
    ProbeTransportError                       // broken socket, corruption
    ProbeValidationFailed                     // response received but invalid
)

type ProbeResult struct {
    Outcome       ProbeOutcome
    Supported     bool          // true only when Outcome == ProbeSupported
    ExceptionCode ExceptionCode // set when Outcome == ProbeException
    Err           error         // underlying error for Timeout/TransportError
    ResponseFC    FunctionCode  // response FC (set when a response was received)
    RawPayload    []byte        // raw response payload (non-exception responses)
    Reason        string        // short explanation for non-success outcomes
}
```

---

### 2.8 SunSpec discovery

Transport-level **read-only** SunSpec discovery helpers live in the `sunspec` subpackage (`import "github.com/otfabric/go-modbus/sunspec"`). They detect the SunSpec "SunS" marker, probe candidate base addresses, and enumerate the model chain (model ID and length only). These APIs do not modify device state and do **not** implement point decoding, scale factors, or schema-driven parsing; that belongs in a higher-level SunSpec library.

See the `sunspec` package documentation for the full API (`sunspec.DetectSunSpec`, `sunspec.ReadSunSpecModelHeaders`, `sunspec.DiscoverSunSpec`).

---

### 2.9 Serial-line function codes and Diagnostics (FC07/FC08/FC0B/FC0C/FC11)

> **Transport-neutral policy:** The Modbus spec labels FC07, FC08, FC0B, and FC0C as
> "Serial Line only," but real-world gateways routinely forward these PDUs over TCP/UDP.
> This library supports all function codes on every transport and does not restrict any FC
> by transport type.

#### Read Exception Status (FC 0x07)

Reads the eight exception status outputs (coils) from the device. Returns a single byte
where each bit represents one exception coil (vendor-defined meaning).

```go
func (mc *Client) ReadExceptionStatus(ctx context.Context, unitID uint8) (status uint8, err error)
```

**Example:**

```go
status, err := client.ReadExceptionStatus(ctx, 1)
if err != nil {
    log.Fatal(err)
}
// status bits 0-7 represent vendor-defined exception coils
```

#### Get Comm Event Counter (FC 0x0B)

Reads the device status and event counter for successful message completions on the
serial line. Useful for determining whether the device has processed a message.

```go
func (mc *Client) GetCommEventCounter(
    ctx    context.Context,
    unitID uint8,
) (*CommEventCounterResponse, error)
```

```go
type CommEventCounterResponse struct {
    Status     uint16
    EventCount uint16
}
```

**Example:**

```go
cr, err := client.GetCommEventCounter(ctx, 1)
if err != nil {
    log.Fatal(err)
}
// cr.Status: 0x0000 = not busy, 0xFFFF = busy
// cr.EventCount: increments on each successful message completion
```

#### Get Comm Event Log (FC 0x0C)

Reads the device status, event count, message count, and a variable-length event log
from the device.

```go
func (mc *Client) GetCommEventLog(
    ctx    context.Context,
    unitID uint8,
) (*CommEventLogResponse, error)
```

```go
type CommEventLogResponse struct {
    Status       uint16
    EventCount   uint16
    MessageCount uint16
    Events       []byte
}
```

**Example:**

```go
cl, err := client.GetCommEventLog(ctx, 1)
if err != nil {
    log.Fatal(err)
}
// cl.Events contains 0-64 event bytes (most recent first)
```

#### Diagnostics (FC 0x08)

#### Diagnostics (FC 0x08)

Sends a Diagnostics request with a sub-function code and optional data. The response echoes the sub-function and returns sub-function-specific data. Use **DiagnosticSubFunction** constants for well-known sub-functions.

```go
func (mc *Client) Diagnostics(
    ctx        context.Context,
    unitID     uint8,
    subFunction DiagnosticSubFunction,
    data       []byte,
) (*DiagnosticResponse, error)
```

**Sub-function type and constants:**

```go
type DiagnosticSubFunction uint16

const (
    DiagReturnQueryData                   DiagnosticSubFunction = 0x0000 // loopback
    DiagRestartCommunications             DiagnosticSubFunction = 0x0001
    DiagReturnDiagnosticRegister          DiagnosticSubFunction = 0x0002
    DiagChangeASCIIInputDelimiter         DiagnosticSubFunction = 0x0003
    DiagForceListenOnlyMode               DiagnosticSubFunction = 0x0004
    DiagClearCountersAndDiagnosticReg     DiagnosticSubFunction = 0x000A
    DiagReturnBusMessageCount             DiagnosticSubFunction = 0x000B
    DiagReturnBusCommunicationErrorCount  DiagnosticSubFunction = 0x000C
    DiagReturnBusExceptionErrorCount      DiagnosticSubFunction = 0x000D
    DiagReturnServerMessageCount          DiagnosticSubFunction = 0x000E
    DiagReturnServerNoResponseCount       DiagnosticSubFunction = 0x000F
    DiagReturnServerNAKCount              DiagnosticSubFunction = 0x0010
    DiagReturnServerBusyCount             DiagnosticSubFunction = 0x0011
    DiagReturnBusCharacterOverrunCount    DiagnosticSubFunction = 0x0012
    DiagClearOverrunCounterAndFlag        DiagnosticSubFunction = 0x0014
)
```

`DiagnosticSubFunction` has a `String()` method for logging. Raw `uint16` values can be cast to `DiagnosticSubFunction` for reserved or vendor sub-functions.

```go
type DiagnosticResponse struct {
    SubFunction DiagnosticSubFunction // echoed from request
    Data        []byte                // sub-function-specific response data
}
```

**Example — Return Query Data (loopback):**

```go
dr, err := client.Diagnostics(ctx, 1, modbus.DiagReturnQueryData, []byte{0x12, 0x34})
if err != nil {
    log.Fatal(err)
}
// dr.SubFunction == modbus.DiagReturnQueryData, dr.Data is the echoed request data
```

**Example — Return Diagnostic Register:**

```go
dr, err := client.Diagnostics(ctx, 1, modbus.DiagReturnDiagnosticRegister, nil)
if err != nil {
    log.Fatal(err)
}
// dr.Data contains 2 bytes (diagnostic register value, big-endian)
```

#### Report Server ID (FC 0x11)

Requests the device-specific server ID, run indicator status, and optional additional data.

```go
func (mc *Client) ReportServerID(ctx context.Context, unitID uint8) (*ReportServerIDResponse, error)
```

```go
type ReportServerIDResponse struct {
    Data               []byte
    RunIndicatorStatus *bool  // parsed from last byte: true=ON (0xFF), false=OFF (0x00); nil if not present
}
```

**Example:**

```go
rs, err := client.ReportServerID(ctx, 1)
if err != nil {
    log.Fatal(err)
}
// rs.Data contains server ID and optional additional data; rs.RunIndicatorStatus indicates ON/OFF when present
```

#### Convenience helpers

```go
// FC08 sub-function 0x0000 — loopback test; returns the echoed value.
func (mc *Client) DiagnosticLoopback(ctx context.Context, unitID uint8, value uint16) (uint16, error)

// FC08 sub-function 0x0002 — read the diagnostic register.
func (mc *Client) DiagnosticRegister(ctx context.Context, unitID uint8) (uint16, error)

// FC08 sub-function 0x0004 — force the device into listen-only mode (no responses).
// No response is expected; the method returns nil on success.
func (mc *Client) DiagnosticForceListenOnlyMode(ctx context.Context, unitID uint8) error

// FC08 sub-function 0x000A — clear all counters and the diagnostic register.
func (mc *Client) DiagnosticClearCounters(ctx context.Context, unitID uint8) error

// FC08 sub-function 0x000B — read the bus message count.
func (mc *Client) BusMessageCount(ctx context.Context, unitID uint8) (uint16, error)

// FC08 counter sub-functions — each returns a single uint16 counter value.
func (mc *Client) DiagnosticBusCommunicationErrorCount(ctx context.Context, unitID uint8) (uint16, error)
func (mc *Client) DiagnosticBusExceptionErrorCount(ctx context.Context, unitID uint8) (uint16, error)
func (mc *Client) DiagnosticServerMessageCount(ctx context.Context, unitID uint8) (uint16, error)
func (mc *Client) DiagnosticServerNoResponseCount(ctx context.Context, unitID uint8) (uint16, error)
func (mc *Client) DiagnosticServerNAKCount(ctx context.Context, unitID uint8) (uint16, error)
func (mc *Client) DiagnosticServerBusyCount(ctx context.Context, unitID uint8) (uint16, error)
func (mc *Client) DiagnosticBusCharacterOverrunCount(ctx context.Context, unitID uint8) (uint16, error)
```

All counter wrappers validate that the response contains exactly 2 data bytes and return a
`*ProtocolError` if the payload is malformed.

---

## 3. Server

### 3.1 `ServerConfig`

```go
type ServerConfig struct {
    // URL defines where to listen. e.g. "tcp://[::]:502", "tcp+tls://[::]:802"
    URL string

    // Timeout is the idle session timeout. Connections idle for longer are closed.
    // Default: 120 s.
    Timeout time.Duration

    // MaxClients limits concurrent client connections. Default: 10.
    MaxClients uint

    // TLSServerCert is the server certificate and private key (tcp+tls only). Required.
    TLSServerCert *tls.Certificate

    // TLSClientCAs contains CAs (or leaf certs) used to verify client certificates
    // (tcp+tls only). Required.
    TLSClientCAs *x509.CertPool

    // TLSHandshakeTimeout is the maximum time for the TLS handshake. Default: 30 s.
    TLSHandshakeTimeout time.Duration

    // Logger is the sink for log output. If nil, slog.Default() is used.
    Logger Logger

    // Metrics receives callbacks for every request handled by the server.
    // Nil (default) disables collection.
    Metrics ServerMetrics
}
```

### 3.2 Lifecycle

```go
func NewServer(conf *ServerConfig, handler RequestHandler) (*Server, error)
func ValidateServerConfig(conf *ServerConfig, handler RequestHandler) error

func (ms *Server) Start() error
func (ms *Server) Shutdown(ctx context.Context) error
func (ms *Server) Stop() error
```

`NewServer` validates the configuration and handler (handler must be non-nil).
`ValidateServerConfig` runs the same validation without creating a server.
`Start` binds the listener and begins accepting connections. `Shutdown(ctx)` stops
accepting, cancels per-connection contexts, closes sockets, and blocks until all
handler goroutines exit or `ctx` expires (returning `ctx.Err()`). `Stop()` is
equivalent to `Shutdown(context.Background())` — it blocks indefinitely.

**Supported function codes:** The server currently handles the following 8 FCs:
FC01 (Read Coils), FC02 (Read Discrete Inputs), FC03 (Read Holding Registers),
FC04 (Read Input Registers), FC05 (Write Single Coil), FC06 (Write Single Register),
FC15 (Write Multiple Coils), FC16 (Write Multiple Registers). Any other FC receives
an `Illegal Function` exception response. Advanced FCs (FC08, FC20/21, FC22, FC23,
FC24, FC43) are not supported on the server side.

**Panic recovery:** Handler panics are caught by the server. A panic in any handler
method results in a `ServerDeviceFailure` exception response and a log entry; the
client goroutine continues processing subsequent requests.

```go
server, err := modbus.NewServer(&modbus.ServerConfig{
    URL:        "tcp://[::]:502",
    MaxClients: 20,
}, &myHandler{})

if err != nil {
    log.Fatal(err)
}
if err := server.Start(); err != nil {
    log.Fatal(err)
}

// bounded graceful shutdown on SIGINT
<-sigCh
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
defer cancel()
if err := server.Shutdown(ctx); err != nil {
    log.Printf("shutdown: %v", err)
}
```

### 3.3 `RequestHandler` interface

Implement this interface and pass it to `NewServer`. Each connected client is
served in its own goroutine, so handler methods may be called concurrently from
different client goroutines. Implementations must be safe for concurrent use
(e.g. use `sync.Mutex` or other synchronization when accessing shared state).

A panic inside any handler method is recovered and logged with a full stack
trace; the client receives a `ServerDeviceFailure` exception response.

```go
type RequestHandler interface {
    HandleCoils(ctx context.Context, req *CoilsRequest) ([]bool, error)
    HandleDiscreteInputs(ctx context.Context, req *DiscreteInputsRequest) ([]bool, error)
    HandleHoldingRegisters(ctx context.Context, req *HoldingRegistersRequest) ([]uint16, error)
    HandleInputRegisters(ctx context.Context, req *InputRegistersRequest) ([]uint16, error)
}
```

**Return values:**
- Return `nil` error and the requested data slice to send a positive response.
- Return a Modbus sentinel error (e.g. `modbus.ErrIllegalDataAddress`) to send a
  specific exception response to the client.
- Return any other non-nil error to send an exception response with code
  `ServerDeviceFailure`.
- For **write** requests (`IsWrite == true`), the returned data slice is ignored;
  only the error value is used.

```go
type myHandler struct {
    coils [65536]bool
    regs  [65536]uint16
}

func (h *myHandler) HandleCoils(ctx context.Context, req *modbus.CoilsRequest) ([]bool, error) {
    if req.IsWrite {
        for i, v := range req.Args {
            h.coils[req.Addr+uint16(i)] = v
        }
        return nil, nil
    }
    out := make([]bool, req.Quantity)
    for i := range out {
        out[i] = h.coils[req.Addr+uint16(i)]
    }
    return out, nil
}

func (h *myHandler) HandleDiscreteInputs(ctx context.Context, req *modbus.DiscreteInputsRequest) ([]bool, error) {
    out := make([]bool, req.Quantity)
    for i := range out {
        out[i] = h.coils[req.Addr+uint16(i)]
    }
    return out, nil
}

func (h *myHandler) HandleHoldingRegisters(ctx context.Context, req *modbus.HoldingRegistersRequest) ([]uint16, error) {
    if req.IsWrite {
        for i, v := range req.Args {
            h.regs[req.Addr+uint16(i)] = v
        }
        return nil, nil
    }
    out := make([]uint16, req.Quantity)
    for i := range out {
        out[i] = h.regs[req.Addr+uint16(i)]
    }
    return out, nil
}

func (h *myHandler) HandleInputRegisters(ctx context.Context, req *modbus.InputRegistersRequest) ([]uint16, error) {
    return h.HandleHoldingRegisters(ctx, &modbus.HoldingRegistersRequest{
        ClientAddr: req.ClientAddr,
        UnitId:     req.UnitId,
        Addr:       req.Addr,
        Quantity:   req.Quantity,
    })
}
```

### 3.4 Request types

```go
type CoilsRequest struct {
    ClientAddr   string       // source IP address of the client
    ClientRole   string       // role from the client TLS certificate (tcp+tls only)
    UnitID       uint8        // target unit / slave ID
    FunctionCode FunctionCode // FC01, FC05, or FC15
    Addr         uint16       // first coil address
    Quantity     uint16       // number of consecutive coils
    IsWrite      bool         // true for FC05/FC15 (writes)
    Args         []bool       // coil values for write requests (nil for reads)
}

type DiscreteInputsRequest struct {
    ClientAddr   string
    ClientRole   string
    UnitID       uint8
    FunctionCode FunctionCode // FC02
    Addr         uint16
    Quantity     uint16
}

type HoldingRegistersRequest struct {
    ClientAddr   string
    ClientRole   string
    UnitID       uint8
    FunctionCode FunctionCode // FC03, FC06, or FC16
    Addr         uint16
    Quantity     uint16
    IsWrite      bool         // true for FC06/FC16 (writes)
    Args         []uint16     // register values for write requests (nil for reads)
}

type InputRegistersRequest struct {
    ClientAddr   string
    ClientRole   string
    UnitID       uint8
    FunctionCode FunctionCode // FC04
    Addr         uint16
    Quantity     uint16
}
```

### 3.5 Optional handler interfaces (FC07, FC0B, FC0C, FC22, FC23)

FC07 (Read Exception Status), FC0B (Get Comm Event Counter), FC0C (Get Comm Event Log),
FC22 (Mask Write Register) and FC23 (Read/Write Multiple Registers) use optional handler
interfaces. If the `RequestHandler` also implements the corresponding interface, the
server dispatches the FC to it; otherwise the server returns `Illegal Function`.

#### Serial-line function codes (FC07, FC0B, FC0C)

```go
type ExceptionStatusHandler interface {
    HandleExceptionStatus(ctx context.Context, req *ExceptionStatusRequest) (status uint8, err error)
}

type ExceptionStatusRequest struct {
    ClientAddr string
    ClientRole string
    UnitID     uint8
}

type CommEventCounterHandler interface {
    HandleCommEventCounter(ctx context.Context, req *CommEventCounterRequest) (status uint16, eventCount uint16, err error)
}

type CommEventCounterRequest struct {
    ClientAddr string
    ClientRole string
    UnitID     uint8
}

type CommEventLogHandler interface {
    HandleCommEventLog(ctx context.Context, req *CommEventLogRequest) (status uint16, eventCount uint16, messageCount uint16, events []byte, err error)
}

type CommEventLogRequest struct {
    ClientAddr string
    ClientRole string
    UnitID     uint8
}
```

These handlers follow the same transport-neutral policy as the client: they are dispatched
regardless of transport type, since gateways commonly forward these PDUs over TCP/UDP.

#### Register manipulation (FC22, FC23)

```go
type MaskWriteHandler interface {
    HandleMaskWrite(ctx context.Context, req *MaskWriteRequest) error
}

type MaskWriteRequest struct {
    ClientAddr   string
    ClientRole   string
    UnitID       uint8
    FunctionCode FunctionCode // FC22
    Addr         uint16
    AndMask      uint16
    OrMask       uint16
}

type ReadWriteHandler interface {
    HandleReadWriteRegisters(ctx context.Context, req *ReadWriteRegistersRequest) (readValues []uint16, err error)
}

type ReadWriteRegistersRequest struct {
    ClientAddr   string
    ClientRole   string
    UnitID       uint8
    FunctionCode FunctionCode // FC23
    ReadAddr     uint16
    ReadQty      uint16
    WriteAddr    uint16
    WriteValues  []uint16
}
```

---

## 4. Errors

The library uses sentinel `error` variables. Use `errors.Is` to test for specific
conditions and `errors.As` to access structured exception details.

### Sentinel errors

```go
var (
    ErrConfigurationError      // invalid configuration passed to New/NewServer
    ErrClientNotOpen           // request before Open() or after Close()
    ErrRequestTimedOut         // request exceeded deadline or configured timeout
    ErrIllegalFunction         // Modbus exception 0x01
    ErrIllegalDataAddress      // Modbus exception 0x02
    ErrIllegalDataValue        // Modbus exception 0x03
    ErrServerDeviceFailure     // Modbus exception 0x04
    ErrAcknowledge             // Modbus exception 0x05
    ErrServerDeviceBusy        // Modbus exception 0x06
    ErrMemoryParityError       // Modbus exception 0x08
    ErrGWPathUnavailable       // Modbus exception 0x0A
    ErrGWTargetFailedToRespond // Modbus exception 0x0B
    ErrBadCRC                  // RTU CRC mismatch
    ErrShortFrame              // frame too short to decode
    ErrProtocolError           // malformed response
    ErrBadUnitID               // response unit ID does not match request
    ErrBadTransactionID        // TCP transaction ID mismatch
    ErrUnknownProtocolID       // non-zero MBAP protocol identifier
    ErrInvalidMBAPLength      // MBAP length &lt; 2 or &gt; 254 (error may wrap value)
    ErrUnexpectedParameters          // invalid arguments passed to a client method
    ErrSunSpecModelChainInvalid      // malformed or non-progressing SunSpec model chain
    ErrSunSpecModelChainLimitExceeded // SunSpec model chain exceeded MaxAddressSpan
)
```

For Modbus TCP, the MBAP length field (unit_id + function_code + payload) must be between 2 and 254 per the spec; otherwise the transport returns an error wrapping `ErrInvalidMBAPLength` (the received length is included in the error message).

### `ExceptionError` — structured exception details

When a remote device responds with a Modbus exception, the error is wrapped in
`*ExceptionError`. It implements `errors.Is` against its `Sentinel` field, so the
usual `errors.Is(err, modbus.ErrIllegalDataAddress)` pattern works even through
`errors.As`.

```go
type ExceptionError struct {
    FunctionCode  FunctionCode  // originating FC (high bit cleared)
    ExceptionCode ExceptionCode // Modbus exception code (0x01–0x0B)
    Sentinel      error         // one of the Err* sentinels above
}
```

```go
_, err := client.ReadRegisters(ctx, 1, 0x9000, 10, modbus.HoldingRegister)
if err != nil {
    var excErr *modbus.ExceptionError
    if errors.As(err, &excErr) {
        fmt.Printf("device exception: fc=0x%02x code=0x%02x\n",
            excErr.FunctionCode, excErr.ExceptionCode)
    }
    if errors.Is(err, modbus.ErrIllegalDataAddress) {
        // address 0x9000 does not exist on this device
    }
}
```

### `ProtocolError` — typed protocol diagnostics

Every protocol-level anomaly (bad byte count, truncated payload, echo mismatch,
pagination inconsistency, unexpected FC, etc.) returns a `*ProtocolError` that
wraps `ErrProtocolError` with the operation name and a human-readable reason.

```go
type ProtocolError struct {
    Op     string // e.g. "ReadDeviceIdentification", "checkResponseFC", "extractByteCountPayload"
    Reason string // e.g. "byte count 5 does not match payload length 3"
}
```

`errors.Is(err, modbus.ErrProtocolError)` still works because `ProtocolError`
implements `Unwrap() error { return ErrProtocolError }`. Use `errors.As` for
the detailed diagnostic message:

```go
var protoErr *modbus.ProtocolError
if errors.As(err, &protoErr) {
    log.Printf("protocol error in %s: %s", protoErr.Op, protoErr.Reason)
}
```

### `ConfigurationError` — typed constructor diagnostics

`New()` and `NewServer()` return `*ConfigurationError` wrapping `ErrConfigurationError`
with the specific field and reason. `errors.Is(err, modbus.ErrConfigurationError)` still
works; use `errors.As` for detailed diagnostics.

```go
type ConfigurationError struct {
    Field  string // e.g. "URL", "TLSClientCert", "reqHandler"
    Reason string // e.g. "required for tcp+tls scheme", "must not be nil"
}
```

```go
var cfgErr *modbus.ConfigurationError
if errors.As(err, &cfgErr) {
    log.Printf("config field %q: %s", cfgErr.Field, cfgErr.Reason)
}
```

`ValidateConfig(conf)` and `ValidateServerConfig(conf, handler)` run the same
validation without creating a client or server — useful for CLIs and config files.

### `ParameterError` — typed validation diagnostics

Every public method that receives invalid arguments returns a `*ParameterError`
that wraps `ErrUnexpectedParameters` with the method name, parameter name, and reason.

```go
type ParameterError struct {
    Method string // e.g. "ReadRegisters", "ReadFileRecords"
    Param  string // e.g. "quantity", "bitIndex"
    Reason string // e.g. "must be 1..125, got 0"
}
```

`errors.Is(err, modbus.ErrUnexpectedParameters)` still works. Use `errors.As`
for detailed diagnostics:

```go
var paramErr *modbus.ParameterError
if errors.As(err, &paramErr) {
    log.Printf("%s: parameter %q: %s", paramErr.Method, paramErr.Param, paramErr.Reason)
}
```

### Codec errors

Codec error sentinels and typed errors live in the `codec` subpackage (`import "github.com/otfabric/go-modbus/codec"`). Use `errors.Is` and `errors.As` to inspect them.

```go
// codec package sentinels
var (
    codec.ErrCodecRegisterCount  // register count mismatch
    codec.ErrCodecByteCount      // byte count mismatch
    codec.ErrCodecLayout         // invalid layout
    codec.ErrCodecValue          // invalid value for encode/decode
    codec.ErrUnknownCodec        // unknown codec ID
    codec.ErrEncodingError       // byte-count or encoding validation
)

// Typed errors (errors.As for Codec, Expected, Actual, Layout, Reason)
type codec.CodecRegisterCountError struct { Codec string; Expected RegisterSpec; Actual uint16 }
type codec.CodecLayoutError struct { Codec string; Layout RegisterLayout; Reason string }
type codec.CodecByteCountError struct { Codec string; Expected ByteSpec; Actual uint16 }
type codec.CodecValueError struct { Codec string; Reason string }
```

---

## 5. Logging

Both `Config` and `ServerConfig` accept a `Logger` interface.
When the field is `nil` (default), logging is disabled (no-op logger).

```go
type Logger interface {
    Debugf(format string, args ...any)
    Infof(format string, args ...any)
    Warnf(format string, args ...any)
    Errorf(format string, args ...any)
}
```

### Structured logging (FieldLogger)

For richer observability, the library supports `FieldLogger` — an optional extension
of `Logger`. When the value assigned to `Config.Logger` also implements `FieldLogger`,
internal log entries use structured key-value fields (e.g. `"component"`) instead of
string-prefixed messages.

```go
type FieldLogger interface {
    Logger
    With(keysAndValues ...any) FieldLogger
    DebugKV(msg string, keysAndValues ...any)
    InfoKV(msg string, keysAndValues ...any)
    WarnKV(msg string, keysAndValues ...any)
    ErrorKV(msg string, keysAndValues ...any)
}
```

### Context-aware logging (ContextLogger)

For trace/span propagation and request-scoped log fields, `ContextLogger` is an
optional extension that adds context-aware methods.

```go
type ContextLogger interface {
    Logger
    DebugContext(ctx context.Context, msg string, keysAndValues ...any)
    InfoContext(ctx context.Context, msg string, keysAndValues ...any)
    WarnContext(ctx context.Context, msg string, keysAndValues ...any)
    ErrorContext(ctx context.Context, msg string, keysAndValues ...any)
}
```

`NewSlogFieldLogger` returns a value that implements all three interfaces: `Logger`,
`FieldLogger`, and `ContextLogger`.

### Built-in constructors

```go
// Wrap a stdlib *log.Logger. Pass nil for a default stdout logger.
func NewStdLogger(l *log.Logger) Logger

// Wrap any slog.Handler (slog.NewJSONHandler, slog.NewTextHandler, etc.)
// Nil handler returns a no-op logger.
func NewSlogLogger(h slog.Handler) Logger

// Wrap a slog.Handler as a FieldLogger + ContextLogger with full structured KV support.
// Nil handler returns a no-op field logger.
func NewSlogFieldLogger(h slog.Handler) FieldLogger

// Discard all log output (useful in tests).
func NopLogger() Logger
```

### Examples

```go
// stdout, text format
conf.Logger = modbus.NewStdLogger(nil)

// JSON to stderr using slog
conf.Logger = modbus.NewSlogLogger(
    slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}),
)

// Structured JSON with field-based logging
conf.Logger = modbus.NewSlogFieldLogger(
    slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}),
)

// silent
conf.Logger = modbus.NopLogger()

// custom implementation (e.g. zap)
type zapAdapter struct{ l *zap.SugaredLogger }
func (a *zapAdapter) Debugf(f string, args ...any) { a.l.Debugf(f, args...) }
func (a *zapAdapter) Infof(f string, args ...any)  { a.l.Infof(f, args...) }
func (a *zapAdapter) Warnf(f string, args ...any)  { a.l.Warnf(f, args...) }
func (a *zapAdapter) Errorf(f string, args ...any) { a.l.Errorf(f, args...) }

conf.Logger = &zapAdapter{l: zapLogger.Sugar()}
```

---

## 6. Metrics

Attach metric callbacks via the `Metrics` field of `Config` or
`ServerConfig`. All methods fire at the **request level** (not per-attempt —
retries are internal to the client). All methods are called synchronously;
implementations must be **non-blocking** (e.g. increment an atomic counter, send
on a buffered channel).

### `ClientMetrics`

Callbacks reflect **logical API outcomes** — the result the calling method actually
returns. This means exception responses, protocol validation failures, echo mismatches,
and byte-count errors are correctly reported as errors, not false successes.

```go
type ClientMetrics interface {
    // Called once before the first attempt of a logical request.
    OnRequest(unitID uint8, functionCode FunctionCode)

    // Called when the logical request succeeds (all protocol validation passed).
    OnResponse(unitID uint8, functionCode FunctionCode, duration time.Duration)

    // Called when the logical request fails with a non-timeout error
    // (including protocol validation, exception responses, echo mismatches).
    OnError(unitID uint8, functionCode FunctionCode, duration time.Duration, err error)

    // Called when the logical request fails due to a timeout.
    OnTimeout(unitID uint8, functionCode FunctionCode, duration time.Duration)
}
```

### `AttemptMetrics` (optional)

For per-attempt visibility into retries and reconnects, implement `AttemptMetrics`
on the same value assigned to `Config.Metrics`. If `Metrics` also implements
`AttemptMetrics`, the library calls its methods for every individual attempt and
dial within a retried request.

```go
type AttemptMetrics interface {
    // Called after each transport attempt (including the first).
    // attempt is 0-based. err is nil on success.
    OnAttempt(unitID uint8, functionCode FunctionCode, attempt int, duration time.Duration, err error)

    // Called when the engine re-dials the transport between retries.
    // attempt is the retry attempt that triggered the dial. err is nil on success.
    OnRetryDial(attempt int, duration time.Duration, err error)
}
```

### `ServerMetrics`

```go
type ServerMetrics interface {
    // Called before invoking the handler.
    OnRequest(unitID uint8, functionCode uint8)

    // Called after the handler returns without error.
    OnResponse(unitID uint8, functionCode uint8, duration time.Duration)

    // Called when the handler returns an error.
    OnError(unitID uint8, functionCode uint8, duration time.Duration, err error)
}
```

### Example — Prometheus-style counters

```go
type promMetrics struct {
    requests  atomic.Uint64
    responses atomic.Uint64
    errors    atomic.Uint64
    timeouts  atomic.Uint64
}

func (m *promMetrics) OnRequest(uint8, uint8)                              { m.requests.Add(1) }
func (m *promMetrics) OnResponse(uint8, uint8, time.Duration)              { m.responses.Add(1) }
func (m *promMetrics) OnError(uint8, uint8, time.Duration, error)          { m.errors.Add(1) }
func (m *promMetrics) OnTimeout(uint8, uint8, time.Duration)               { m.timeouts.Add(1) }

conf.Metrics = &promMetrics{}
```

---

## 7. Retry policy

Control retry behaviour with the `RetryPolicy` field of `Config`.

```go
type RetryPolicy interface {
    // ShouldRetry is called after each failed attempt.
    // attempt is zero-based (0 = first failure).
    // Return (true, delay) to retry after delay, or (false, 0) to stop.
    ShouldRetry(attempt int, err error) (bool, time.Duration)
}
```

### Built-in policies

```go
// No retries (default when RetryPolicy is nil).
func NoRetry() RetryPolicy

// Exponential back-off with common settings.
// delay = base × 2^attempt, capped at maxDelay. Stops after maxAttempts retries.
// maxAttempts = 0 means unlimited (always pair with a context deadline).
func ExponentialBackoff(base, maxDelay time.Duration, maxAttempts int) RetryPolicy

// Full control via ExponentialBackoffConfig.
func NewExponentialBackoff(cfg ExponentialBackoffConfig) RetryPolicy

type ExponentialBackoffConfig struct {
    BaseDelay      time.Duration // default 100 ms
    MaxDelay       time.Duration // default 30 s
    MaxAttempts    int           // 0 = unlimited
    RetryOnTimeout bool          // default false: timeouts are not retried
}
```

### Example

```go
// Retry up to 4 times with 200 ms → 400 ms → 800 ms → 1.6 s back-off.
conf.RetryPolicy = modbus.ExponentialBackoff(200*time.Millisecond, 5*time.Second, 4)

// Retry indefinitely (capped at 10 s between attempts), also retrying timeouts.
conf.RetryPolicy = modbus.NewExponentialBackoff(modbus.ExponentialBackoffConfig{
    BaseDelay:      500 * time.Millisecond,
    MaxDelay:       10 * time.Second,
    MaxAttempts:    0,
    RetryOnTimeout: true,
})
```

### Retryability

The built-in `ExponentialBackoff` policy only retries errors that are classified as
transient transport failures. The `IsRetryable(err, retryTimeout)` classifier (in the
`session` package, used internally) uses **positive classification**: only known
transient errors are retried. Unknown/unclassified errors are **not** retried, to
prevent hiding bugs or creating retry storms in production.

**Retried** (transient transport failures): `io.EOF`, `io.ErrUnexpectedEOF`, broken
pipe, connection reset, dial failures, `net.ErrClosed`, and (when `RetryOnTimeout` is
true) `ErrRequestTimedOut`.

**Never retried**: `context.Canceled`, `context.DeadlineExceeded`, `ErrClientNotOpen`,
`ErrConfigurationError`, `ErrProtocolError`, `ErrBadCRC`, `ErrShortFrame`,
`ErrBadTransactionID`, `ErrBadUnitID`, `ErrUnknownProtocolID`, `ErrInvalidMBAPLength`,
`ErrUnexpectedParameters`, all Modbus exception responses (`*ExceptionError`), and
**unknown/unclassified errors**.

Custom `RetryPolicy` implementations may use different rules by inspecting the error
directly.

On each retry the client automatically:
1. Closes the failed connection.
2. Sleeps for the policy-specified delay, releasing the lock so other goroutines
   are not blocked.
3. Dials a fresh connection before the next attempt.

When a connection pool is active (`MaxConns > 1`), a retry may use a different
underlying TCP connection. Metrics are request-level, not per-attempt — retries
are internal to the engine.

---

## 8. Connection pool

Enable a connection pool to allow concurrent goroutines to share a single
`*Client` without serialising on a single connection.

```go
client, _ := modbus.New(modbus.Config{
    URL:      "tcp://plc.local:502",
    MinConns: 2,   // pre-warm 2 connections during Open()
    MaxConns: 8,   // pool up to 8 concurrent connections
})
client.Open()
```

- Applies to all TCP-based transports (`tcp`, `rtuovertcp`, `rtuoverudp`, `udp`).
- RTU (serial) always uses a single connection; pooling is silently ignored.
- When the pool is at capacity and all connections are in use, goroutines block
  until one is returned, until the context is cancelled, or until the pool is closed.
- Failed connections are discarded; the pool dials replacements lazily on the next
  `acquire` call.
- `Close()` drains and closes all idle pool connections and wakes any goroutines
  blocked waiting for an idle connection.

---

## 9. TLS helpers

```go
// LoadCertPool reads PEM-encoded certificates from filePath into a *x509.CertPool.
// Accepts files containing multiple concatenated certificates.
func LoadCertPool(filePath string) (*x509.CertPool, error)
```

```go
// Load a server certificate for NewServer
cert, err := tls.LoadX509KeyPair("server.crt", "server.key")
pool, err := modbus.LoadCertPool("client-ca.crt")

server, err := modbus.NewServer(&modbus.ServerConfig{
    URL:           "tcp+tls://[::]:802",
    TLSServerCert: &cert,
    TLSClientCAs:  pool,
}, handler)
```

---

## 10. Type constants

### `Parity`

Used in `Config.Parity` (RTU only).

| Constant | Value | Description |
|---|---|---|
| `ParityNone` | 0 | No parity bit |
| `ParityEven` | 1 | Even parity |
| `ParityOdd` | 2 | Odd parity |

### `Endianness` and `WordOrder`

`Endianness` (`BigEndian`, `LittleEndian`) and `WordOrder` (`HighWordFirst`, `LowWordFirst`) are type constants used when mapping layout names (e.g. in CLI or descriptor tooling) to `RegisterLayout`. Byte and word order for typed read/write are defined by the codec’s `RegisterLayout`, not by client-wide state.

### `RegType`

Distinguishes holding from input registers on read calls.

| Constant | Function code |
|---|---|
| `HoldingRegister` | FC03 (read) / FC06, FC16 (write) |
| `InputRegister` | FC04 (read only) |

### `FunctionCode` and `ExceptionCode`

Protocol function and exception codes are strongly typed. Use `FunctionCode` and
`ExceptionCode` (and the exported constants below) instead of raw bytes when
inspecting `ExceptionError` or implementing metrics.

**Exported function code constants:** `FCReadCoils`, `FCReadDiscreteInputs`,
`FCReadHoldingRegisters`, `FCReadInputRegisters`, `FCWriteSingleCoil`,
`FCWriteSingleRegister`, `FCWriteMultipleCoils`, `FCWriteMultipleRegisters`,
`FCDiagnostics`, `FCReportServerID`, `FCReadFileRecord`, `FCWriteFileRecord`,
`FCMaskWriteRegister`, `FCReadWriteMultipleRegs`, `FCReadFIFOQueue`,
`FCEncapsulatedInterface`.

**MEI type (FC43):** `MEIReadDeviceIdentification`.

**FunctionCode helpers:** `IsException()` (MSB set), `Base()` (strip exception bit), `String()` (e.g. `"Read Holding Registers (0x03)"` or `"Read Holding Registers Exception (0x83)"`), `Valid()` (known FC after stripping exception bit), `KnownFunctionCodes()` (slice of base FCs), `ParseFunctionCode(byte)` (validate raw byte, return `FunctionCode` or error).

**ExceptionCode helpers:** `String()` (e.g. `"Illegal Data Address (0x02)"`), `ToError()` (sentinel or `fmt.Errorf` for unknown).

**ExceptionError:** `Error()` returns a readable message like `"Read Holding Registers (0x03): Illegal Data Address (0x02)"` using the above `String()` methods.

### SunSpec constants

Exported constants for SunSpec marker detection, end-of-chain detection, and default probe addresses. These allow callers that process raw register data (e.g. strategies parsing `ScanResult.Data`) to use the canonical values without duplication.

| Constant | Type | Value | Description |
|---|---|---|---|
| `SunSpecMarkerReg0` | `uint16` | `0x5375` | First register of "SunS" marker (`'S'<<8 \| 'u'`) |
| `SunSpecMarkerReg1` | `uint16` | `0x6E53` | Second register of "SunS" marker (`'n'<<8 \| 'S'`) |
| `SunSpecEndModelID` | `uint16` | `0xFFFF` | Model ID indicating end of SunSpec model chain |
| `SunSpecEndModelLength` | `uint16` | `0` | Model length for end-of-chain sentinel |

| Variable | Type | Value | Description |
|---|---|---|---|
| `SunSpecDefaultBaseAddresses` | `[]uint16` | `{0, 40000, 50000, 1, 39999, 40001, 49999, 50001}` | Default candidate base addresses for SunSpec probe |

---

## 11. Codec API

The codec API provides typed register read/write with explicit layout. Transport remains register-native; codecs own interpretation. All codec instances are fixed-width (parameterized at construction).

For a **list and description of all built-in codecs** (numeric, text, bytes, network) with constructors, layouts, and stable IDs, see **[CODECS.md](CODECS.md)**.

### 11.1 RegisterLayout and common layouts

`RegisterLayout` describes how bytes of a multi-register value are permuted across Modbus registers. Positions are 1-based: 1 = least-significant byte, highest = most-significant byte. Layouts are immutable.

```go
func NewRegisterLayout(registerCount uint16, positions ...uint8) (RegisterLayout, error)
func MustNewRegisterLayout(registerCount uint16, positions ...uint8) RegisterLayout
func (l RegisterLayout) RegisterCount() uint16
func (l RegisterLayout) BytePositions() []uint8  // copy; do not mutate
func (l RegisterLayout) String() string          // e.g. "4321", "21436587"
```

**Common layout variables (fully explicit byte-position notation):**

| Name | Registers | Typical use |
|------|-----------|--------------|
| `Layout16_21`, `Layout16_12` | 1 | 16-bit byte order |
| `Layout32_4321`, `Layout32_3412`, `Layout32_2143`, `Layout32_1234` | 2 | 32-bit (four canonical variants) |
| `Layout48_654321`, `Layout48_563412`, `Layout48_214365`, `Layout48_123456` | 3 | 48-bit (four variants) |
| `Layout64_87654321`, `Layout64_78563412`, `Layout64_21436587`, `Layout64_12345678` | 4 | 64-bit (four variants) |

`ErrInvalidLayout` is returned when positions are invalid (wrong length, duplicate, or out of range).

### 11.2 Codec interfaces

```go
type RegisterSpec struct { Count uint16 }
type ByteSpec struct { Count uint16 }

type Decoder[T any] interface {
    ID() string
    Name() string
    RegisterSpec() RegisterSpec
    ByteSpec() ByteSpec
    DecodeRegisters(regs []uint16) (T, error)
}

type Encoder[T any] interface {
    ID() string
    Name() string
    RegisterSpec() RegisterSpec
    ByteSpec() ByteSpec
    EncodeRegisters(value T) ([]uint16, error)
}

type Codec[T any] interface {
    Decoder[T]
    Encoder[T]
}
```

`ID()` is stable for discovery and tests; `Name()` is human-readable.

### 11.3 Transport: codec.ReadFromClient, codec.WriteToClient

These live in the `codec` subpackage (`import "github.com/otfabric/go-modbus/codec"`). They are **package-level** generic functions (Go methods cannot have type parameters). They accept a `codec.RegisterReader` / `codec.RegisterWriter` interface — `*modbus.Client` satisfies both. Wire order is big-endian per register; layout is defined by the codec.

```go
// codec.RegisterReader / codec.RegisterWriter interfaces:
type RegisterReader interface {
    ReadRegisters(ctx context.Context, unitID uint8, addr uint16, quantity uint16, regType RegType) ([]uint16, error)
}
type RegisterWriter interface {
    WriteRegisters(ctx context.Context, unitID uint8, addr uint16, values []uint16) error
}

func codec.ReadFromClient[T any](
    r RegisterReader,
    ctx context.Context,
    unitID uint8,
    addr uint16,
    regType RegType,
    dec Decoder[T],
) (T, error)

func codec.WriteToClient[T any](
    w RegisterWriter,
    ctx context.Context,
    unitID uint8,
    addr uint16,
    value T,
    enc Encoder[T],
) error
```

If `dec.RegisterSpec().Count == 0`, `ReadFromClient` returns a `*CodecRegisterCountError`.

**Convenience wrappers for uint32:**

```go
func codec.ReadUint32FromClient(r RegisterReader, ctx context.Context, unitID uint8, addr uint16, regType RegType, layout RegisterLayout) (uint32, error)
func codec.WriteUint32ToClient(w RegisterWriter, ctx context.Context, unitID uint8, addr uint16, v uint32, layout RegisterLayout) error
```

### 11.4 Offline helpers

For tests and tooling on raw register/byte data:

```go
func DecodeRegisters[T any](regs []uint16, codec Decoder[T]) (T, error)
func EncodeRegisters[T any](value T, codec Encoder[T]) ([]uint16, error)
func ValidateRegisterSpec(spec RegisterSpec, regs []uint16, codecID string) error
func ValidateByteSpec(spec ByteSpec, b []byte, codecID string) error
```

`ValidateRegisterSpec` returns `*CodecRegisterCountError` on mismatch; `ValidateByteSpec` returns `*CodecByteCountError`. Use `codec.ID()` for the `codecID` argument.

### 11.5 Codec constructors

**Numeric (layout required):** Each returns `(Codec[T], error)` or `Codec[T]` for `Must` variants.

```go
func NewUint16Codec(layout RegisterLayout) (Codec[uint16], error)
func MustNewUint16Codec(layout RegisterLayout) Codec[uint16]
func NewInt16Codec(layout RegisterLayout) (Codec[int16], error)
func MustNewInt16Codec(layout RegisterLayout) Codec[int16]
func NewUint32Codec(layout RegisterLayout) (Codec[uint32], error)
func MustNewUint32Codec(layout RegisterLayout) Codec[uint32]
func NewInt32Codec(layout RegisterLayout) (Codec[int32], error)
func MustNewInt32Codec(layout RegisterLayout) Codec[int32]
func NewUint48Codec(layout RegisterLayout) (Codec[uint64], error)
func MustNewUint48Codec(layout RegisterLayout) Codec[uint64]
func NewInt48Codec(layout RegisterLayout) (Codec[int64], error)
func MustNewInt48Codec(layout RegisterLayout) Codec[int64]
func NewUint64Codec(layout RegisterLayout) (Codec[uint64], error)
func MustNewUint64Codec(layout RegisterLayout) Codec[uint64]
func NewInt64Codec(layout RegisterLayout) (Codec[int64], error)
func MustNewInt64Codec(layout RegisterLayout) Codec[int64]
func NewFloat32Codec(layout RegisterLayout) (Codec[float32], error)
func MustNewFloat32Codec(layout RegisterLayout) Codec[float32]
func NewFloat64Codec(layout RegisterLayout) (Codec[float64], error)
func MustNewFloat64Codec(layout RegisterLayout) Codec[float64]
```

**Sign-magnitude 16-bit:** Special-purpose legacy encoding; one register; bit 15 = sign, bits 0–14 = magnitude. **Not two's complement.** Independent of `RegisterLayout`. Family `integer`, ID `int16_sign_magnitude`. See [CODECS.md](CODECS.md) for details.

```go
func NewInt16SignMagnitudeCodec() Codec[int16]
```

**Decimal limb (M10k) — family `decimal_limb`:** Each register holds one base-10000 limb. Unsigned: limbs 0..9999. Signed: only the **most-significant limb** is signed (−9999..9999); others 0..9999; MS limb is `int16(reg)` on wire. Order is given by **DecimalLimbOrder**. Schneider: low_to_high ↔ 2143, 21-65, 21-87; high_to_low ↔ 4321, 65-21, 87-21.

```go
type DecimalLimbOrder uint8
const (
    DecimalLimbLowToHigh  DecimalLimbOrder = 1  // first reg = LSB limb
    DecimalLimbHighToLow DecimalLimbOrder = 2  // first reg = MSB limb
)
func (DecimalLimbOrder) String() string  // "low_to_high" | "high_to_low" | "unknown"

func NewUint32M10kCodec(order DecimalLimbOrder) (Codec[uint32], error)
func MustNewUint32M10kCodec(order DecimalLimbOrder) Codec[uint32]
func NewInt32M10kCodec(order DecimalLimbOrder) (Codec[int32], error)
func MustNewInt32M10kCodec(order DecimalLimbOrder) Codec[int32]
func NewUint48M10kCodec(order DecimalLimbOrder) (Codec[uint64], error)
func MustNewUint48M10kCodec(order DecimalLimbOrder) Codec[uint64]
func NewInt48M10kCodec(order DecimalLimbOrder) (Codec[int64], error)
func MustNewInt48M10kCodec(order DecimalLimbOrder) Codec[int64]
func NewUint64M10kCodec(order DecimalLimbOrder) (Codec[uint64], error)
func MustNewUint64M10kCodec(order DecimalLimbOrder) Codec[uint64]
func NewInt64M10kCodec(order DecimalLimbOrder) (Codec[int64], error)
func MustNewInt64M10kCodec(order DecimalLimbOrder) Codec[int64]
```

Stable IDs: `uint32_m10k/order:low_to_high`, `uint32_m10k/order:high_to_low`, `int32_m10k/order:...`, and similarly for 48/64.

**Text (register count = width in registers):**

```go
func NewAsciiCodec(registerCount uint16) (Codec[string], error)
func NewAsciiFixedCodec(registerCount uint16) (Codec[string], error)
func NewAsciiReverseCodec(registerCount uint16) (Codec[string], error)
func NewUTF16BECodec(registerCount uint16) (Codec[string], error)
func NewUTF16LECodec(registerCount uint16) (Codec[string], error)
func NewBCDCodec(registerCount uint16) (Codec[string], error)
func NewPackedBCDCodec(registerCount uint16) (Codec[string], error)
func NewSignedPackedBCDCodec(registerCount uint16) (Codec[string], error)
func NewPackedBCDReverseCodec(registerCount uint16) (Codec[string], error)
```

UTF-16: full width preserved on decode; embedded NULs survive; encode right-pads with NUL. Signed packed BCD: decode accepts trailing nibble 0xC/0xD/0xF = negative; encode uses 0xC only for negative. See [CODECS.md](CODECS.md) for full contracts.

**Bytes and network (fixed size or byte count):** `NewBytesCodec` and `NewUint8SliceCodec` require **even** byte count (register-backed). IPv6 codec rejects IPv4 addresses.

```go
func NewBytesCodec(byteCount uint16) (Codec[[]byte], error)
func NewUint8SliceCodec(byteCount uint16) (Codec[[]uint8], error)
func NewIPAddrCodec() Codec[net.IP]
func NewIPv6AddrCodec() Codec[net.IP]
func NewEUI48Codec() Codec[net.HardwareAddr]
func NewEUI64Codec() Codec[net.HardwareAddr]
```

**Time codecs — family `time`, value kind `time`:** Epoch (seconds since 2000-01-01 UTC), calendar YMDhms (6 registers), and IEC 60870-5 CP56Time2a (4 registers, 7 bytes). Unspecified-timezone codecs use UTC as the canonical interpretation for deterministic behaviour. See [CODECS.md § 5 Time codecs](CODECS.md#5-time-codecs).

```go
func NewDateTime2S2000Codec() Codec[time.Time]   // 2 regs, uint32 seconds since 2000
func NewDateTime3S2000Codec() Codec[time.Time]   // 3 regs, 48-bit seconds since 2000
func NewDateTimeYMDhmsUTCCodec() Codec[time.Time]   // 6 regs: Y,M,D,h,m,s (UTC)
func NewDateTimeYMDhmsLocalCodec() Codec[time.Time] // 6 regs (local time)
func NewDateTimeYMDhmsCodec() Codec[time.Time]      // 6 regs naive Y/M/D/h/m/s; library interprets in UTC
func NewDateTimeIEC870UTCCodec() Codec[time.Time]   // 4 regs CP56Time2a (UTC)
func NewDateTimeIEC870LocalCodec() Codec[time.Time] // 4 regs CP56Time2a (local)
func NewDateTimeIEC870Codec() Codec[time.Time]      // 4 regs CP56Time2a; timezone-unspecified, library interprets in UTC
```

Stable IDs: `datetime2_s2000`, `datetime3_s2000`, `datetime_ymdhms_utc`, `datetime_ymdhms_local`, `datetime_ymdhms`, `datetime_iec870_utc`, `datetime_iec870_local`, `datetime_iec870`.

### 11.6 Discovery (registry)

Descriptors are derived from the registration table; returned descriptors are deep-copied. Discovery exposes a **curated subset** of widths (e.g. text/UTF-16: 1, 2, 3, 4, 6, 8, 12, 16, 20, 32, 48, 64 registers; bytes: 2, 4, 6, 8, 10, 12, 14, 16, 20, 24, 32, 48, 64 bytes). Constructors accept any valid width; not every width appears in the registry.

```go
type CodecDescriptor struct {
    ID           string
    Name         string
    Family       CodecFamily
    ValueKind    CodecValueKind
    RegisterSpec RegisterSpec
    ByteSpec     ByteSpec
    Layouts      []RegisterLayoutDescriptor  // nil for layout-less codecs
}

type CodecCandidate struct {
    CodecID    string
    LayoutName string
}

type CodecQuery struct {
    RegisterCount uint16
    ByteCount     uint16
    Family        CodecFamily
    ValueKind     CodecValueKind
}

func AvailableCodecDescriptors() []CodecDescriptor
func CodecDescriptorsForRegisterCount(count uint16) []CodecDescriptor
func CodecDescriptorsForByteCount(count uint16) []CodecDescriptor
func CodecDescriptorByID(id string) (CodecDescriptor, bool)
func CodecCandidatesForRegisterCount(count uint16) []CodecCandidate
func CodecCandidatesForByteCount(count uint16) []CodecCandidate
func FindCodecDescriptors(q CodecQuery) []CodecDescriptor
```

`CodecFamily` and `CodecValueKind` have `String()` methods. `CodecCandidate.CodecID` equals the corresponding `CodecDescriptor.ID`.

### 11.7 Codec errors (summary)

All codec error sentinels and typed errors live in the `codec` subpackage. Sentinels: `codec.ErrCodecRegisterCount`, `codec.ErrCodecLayout`, `codec.ErrCodecValue`, `codec.ErrCodecByteCount`, `codec.ErrUnknownCodec`, `codec.ErrEncodingError`. Typed: `*codec.CodecRegisterCountError`, `*codec.CodecLayoutError`, `*codec.CodecByteCountError`, `*codec.CodecValueError`; all implement `Unwrap()` to the appropriate sentinel. See [§ 4](#4-errors).

### 11.8 Runtime codec API

For CLI tools, descriptor-driven workflows, and batch decode plans the library provides **type-erased** runtime codec interfaces. They do not replace typed `Codec[T]`; use them when the concrete type is not known at compile time.

**Interfaces:**

```go
type RuntimeDecoder interface {
    ID() string
    Name() string
    RegisterSpec() RegisterSpec
    ByteSpec() ByteSpec
    ValueKind() CodecValueKind
    DecodeRegistersAny(regs []uint16) (any, error)
}

type RuntimeEncoder interface {
    ID() string
    Name() string
    RegisterSpec() RegisterSpec
    ByteSpec() ByteSpec
    ValueKind() CodecValueKind
    EncodeRegistersAny(value any) ([]uint16, error)
}

type RuntimeCodec interface {
    RuntimeDecoder
    RuntimeEncoder
}
```

**Adapters** — wrap typed codecs as runtime interfaces:

```go
func AsRuntimeDecoder[T any](d Decoder[T], kind CodecValueKind) RuntimeDecoder
func AsRuntimeEncoder[T any](e Encoder[T], kind CodecValueKind) RuntimeEncoder
func AsRuntimeCodec[T any](c Codec[T], kind CodecValueKind) RuntimeCodec
```

**Package-level offline helpers** (work on `[]uint16`; do not use transport):

```go
func DecodeRegistersAny(regs []uint16, codec RuntimeDecoder) (any, error)
func EncodeRegistersAny(value any, codec RuntimeEncoder) ([]uint16, error)
```

`EncodeRegistersAny` returns a `*CodecValueError` if the value type does not match the codec; it does not panic.

### 11.9 Runtime registry and discovery

Instantiate a `RuntimeCodec` from a descriptor or a stable ID (e.g. from discovery or CLI). All built-in descriptors can be instantiated; discovery returns only codecs that are valid for the given width.

```go
func RuntimeCodecFromDescriptor(desc CodecDescriptor) (RuntimeCodec, error)
func RuntimeCodecByID(id string) (RuntimeCodec, bool, error)
func MustRuntimeCodecByID(id string) RuntimeCodec  // panics if id unknown

func RuntimeCodecsForRegisterCount(count uint16) ([]RuntimeCodec, error)
func RuntimeCodecsForByteCount(count uint16) ([]RuntimeCodec, error)
func FindRuntimeCodecs(q CodecQuery) ([]RuntimeCodec, error)
```

IDs follow the same scheme as descriptors (e.g. `uint32/layout:4321`, `ascii/registers:4`, `ip_addr`). `RuntimeCodecsForRegisterCount` and `RuntimeCodecsForByteCount` return only codecs whose register/byte count matches; `FindRuntimeCodecs` filters by `CodecQuery` and returns instantiated runtime codecs.

### 11.10 Runtime transport and descriptor helpers

**Client-bound** (perform a Modbus read or write using a runtime codec, in the `codec` subpackage):

```go
func codec.ReadRuntimeFromClient(r RegisterReader, ctx context.Context, unitID uint8, addr uint16, regType RegType, dec RuntimeDecoder) (any, error)
func codec.WriteRuntimeToClient(w RegisterWriter, ctx context.Context, unitID uint8, addr uint16, value any, enc RuntimeEncoder) error
```

**Offline** (decode/encode using a descriptor only; useful when you have a descriptor from discovery but no codec instance):

```go
func codec.DecodeWithDescriptor(regs []uint16, desc CodecDescriptor) (any, error)
func codec.EncodeWithDescriptor(value any, desc CodecDescriptor) ([]uint16, error)
```

These instantiate a runtime codec from the descriptor internally. Type mismatch on encode returns `*CodecValueError`.

### 11.11 Batch decode plan

Read one register window (single Modbus request) and decode multiple fields with different codecs. Useful for device maps and CLI “read block” commands.

**Types:**

```go
type ReadWindow struct {
    Addr     uint16
    Quantity uint16
    RegType  RegType
}

type RuntimeDecodeItem struct {
    Name     string
    Offset   uint16        // register offset within the window
    Codec    RuntimeDecoder
    Metadata map[string]any
}

type RuntimeDecodePlan struct {
    Window ReadWindow
    Items  []RuntimeDecodeItem
}

type RuntimeDecodedValue struct {
    Name          string
    CodecID       string
    ValueKind     CodecValueKind
    Offset        uint16
    RegisterCount uint16
    Value         any
    Error         error
}

type RuntimeDecodeResult struct {
    Addr      uint16
    Quantity  uint16
    RegType   RegType
    Registers []uint16
    Values    []RuntimeDecodedValue
}
```

**Validation and execution:**

```go
func ValidateRuntimeDecodePlan(plan RuntimeDecodePlan) error
func ExecuteRuntimeDecodePlan(mc *Client, ctx context.Context, unitID uint8, plan RuntimeDecodePlan) (*RuntimeDecodeResult, error)
func ExecuteRuntimeDecodePlanOffline(regs []uint16, plan RuntimeDecodePlan) (*RuntimeDecodeResult, error)
```

`ValidateRuntimeDecodePlan` returns a `*RuntimePlanValidationError` when the window is invalid (quantity not 1–125, address overflow), items have empty or duplicate names, a codec is nil, or an item’s offset and register count would extend past the window. The executors perform one read (or use the provided slice) and decode each item; per-item decode failures are recorded in `RuntimeDecodedValue.Error` and do not abort the whole plan.

