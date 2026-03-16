# `github.com/otfabric/modbus` — Public API Reference

This document covers every exported type, function, and interface in the library.
All examples assume `import "github.com/otfabric/modbus"`.

---

## Table of Contents

1. [Transport modes and URL schemes](#1-transport-modes-and-url-schemes)
2. [Client](#2-client)
   - [Configuration](#21-clientconfiguration)
   - [Lifecycle](#22-lifecycle)
   - [Read operations](#23-read-operations)
   - [Write operations](#24-write-operations)
   - [Advanced register operations (FC20/21/23/24)](#25-advanced-register-operations-fc20212324)
   - [Device identification (FC43)](#26-device-identification-fc43)
   - [Modbus device detection](#27-modbus-device-detection)
   - [SunSpec discovery](#28-sunspec-discovery)
   - [Diagnostics and Report Server ID (FC08/0x11)](#29-diagnostics-and-report-server-id-fc080x11)
3. [Server](#3-server)
   - [Configuration](#31-serverconfiguration)
   - [Lifecycle](#32-lifecycle)
   - [RequestHandler interface](#33-requesthandler-interface)
   - [Request types](#34-request-types)
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

The `URL` field in both `ClientConfiguration` and `ServerConfiguration` encodes the
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

### 2.1 `ClientConfiguration`

```go
type ClientConfiguration struct {
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

    // TLSClientCert is the client-side TLS certificate and private key (tcp+tls only).
    // Required: mutual TLS authentication is mandatory per the MBAPS spec.
    TLSClientCert *tls.Certificate

    // TLSRootCAs contains CAs (or leaf certs for pinning) used to verify the server
    // certificate (tcp+tls only). Required.
    TLSRootCAs *x509.CertPool

    // Logger is the sink for log output. If nil, slog.Default() is used.
    // Build a value with NewStdLogger, NewSlogLogger, or NopLogger.
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

### 2.2 Lifecycle

```go
func NewClient(conf *ClientConfiguration) (*ModbusClient, error)
func (mc *ModbusClient) Open() error
func (mc *ModbusClient) Close() error
func (mc *ModbusClient) LastTransactionID() uint16
```

`NewClient` validates the URL and configuration but does **not** open a network
connection. Call `Open` to establish the transport. `Open` is idempotent — calling
it on an already-open client is a no-op. `Close` closes all connections (or drains
the pool when `MaxConns > 1`). `LastTransactionID` returns the MBAP transaction ID of the last successful TCP response; it is 0 for RTU and other non-TCP transports. Useful for diagnostics and correlating with packet captures.

```go
client, err := modbus.NewClient(&modbus.ClientConfiguration{
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

client, err := modbus.NewClient(&modbus.ClientConfiguration{
    URL:           "tcp+tls://plc.local:802",
    TLSClientCert: &cert,
    TLSRootCAs:    pool,
    Timeout:       5 * time.Second,
})
```

### 2.3 Read operations

All read methods share the same signature preamble:

```go
func (mc *ModbusClient) <Method>(ctx context.Context, unitID uint8, addr uint16, ...) (..., error)
```

`ctx` propagates cancellation and deadlines. If the context carries a deadline it
overrides the configured `Timeout`. `unitID` is the Modbus slave/unit ID (1–247;
255 is broadcast).

#### Coils and discrete inputs

```go
// FC01 — read one coil
func (mc *ModbusClient) ReadCoil(ctx context.Context, unitID uint8, addr uint16) (bool, error)

// FC01 — read multiple coils (quantity ≤ 2000)
func (mc *ModbusClient) ReadCoils(ctx context.Context, unitID uint8, addr uint16, quantity uint16) ([]bool, error)

// FC02 — read one discrete input
func (mc *ModbusClient) ReadDiscreteInput(ctx context.Context, unitID uint8, addr uint16) (bool, error)

// FC02 — read multiple discrete inputs (quantity ≤ 2000)
func (mc *ModbusClient) ReadDiscreteInputs(ctx context.Context, unitID uint8, addr uint16, quantity uint16) ([]bool, error)
```

```go
coils, err := client.ReadCoils(ctx, 1, 0x0000, 8)
// coils[0] = coil at address 0, coils[7] = coil at address 7
```

#### 16-bit registers

```go
// FC03/FC04 — read one register as uint16
func (mc *ModbusClient) ReadRegister(ctx context.Context, unitID uint8, addr uint16, regType RegType) (uint16, error)

// FC03/FC04 — read multiple registers as []uint16 (quantity ≤ 125)
func (mc *ModbusClient) ReadRegisters(ctx context.Context, unitID uint8, addr uint16, quantity uint16, regType RegType) ([]uint16, error)
```

`regType` is `HoldingRegister` (FC03) or `InputRegister` (FC04).

```go
val, err := client.ReadRegister(ctx, 1, 0x1000, modbus.HoldingRegister)
```

For typed reads (32/64-bit, float, ASCII, BCD, IP, etc.) use the **codec API**: `ReadWithCodec[T](mc, ctx, unitID, addr, regType, codec)`. See [§ 11 Codec API](#11-codec-api).

#### Raw bytes

**Transport convenience helpers** — no interpretation or codec; for typed values use the codec API.

```go
// FC03/FC04 — read registers as bytes in wire order (no reordering)
func (mc *ModbusClient) ReadRawBytes(ctx context.Context, unitID uint8, addr uint16, quantity uint16, regType RegType) ([]byte, error)
```

**Quantity** is the number of bytes to read (the library reads `ceil(quantity/2)` registers). To read N registers, pass `quantity = N*2`. Bytes are returned in wire order. Odd quantity is valid (e.g. quantity 3 reads 2 registers and returns exactly 3 bytes, with the trailing padding byte trimmed).

#### Bitfield / masked register operations

Many devices expose booleans and enums inside holding (or input) registers rather than coils. The following helpers read or update individual bits or bit ranges without clobbering adjacent bits.

```go
// FC03/FC04 — read one bit from a register (bitIndex 0 = LSB, 15 = MSB)
func (mc *ModbusClient) ReadRegisterBit(ctx context.Context, unitID uint8, addr uint16, bitIndex uint8, regType RegType) (bool, error)

// FC03/FC04 — read count bits from one register starting at bitIndex (count 1–16, bitIndex+count ≤ 16)
func (mc *ModbusClient) ReadRegisterBits(ctx context.Context, unitID uint8, addr uint16, bitIndex, count uint8, regType RegType) ([]bool, error)

// FC03 + FC16 — read register, set or clear one bit, write back (holding registers only)
func (mc *ModbusClient) WriteRegisterBit(ctx context.Context, unitID uint8, addr uint16, bitIndex uint8, value bool) error

// FC03 + FC16 — read-modify-write: newVal = (old & ^mask) | (value & mask) (holding registers only)
func (mc *ModbusClient) UpdateRegisterMask(ctx context.Context, unitID uint8, addr uint16, mask, value uint16) error
```

- **ReadRegisterBit** — Reads one register and returns `(reg>>bitIndex)&1 != 0`. Use for status bits, alarm bits, or single enum bits. `bitIndex > 15` returns `ErrUnexpectedParameters`.
- **ReadRegisterBits** — Reads one register and returns a slice of `count` booleans from `bitIndex` upward. Use for multi-bit mode enums. Invalid `count` or `bitIndex+count > 16` returns `ErrUnexpectedParameters`.
- **WriteRegisterBit** — Read-modify-write: reads the holding register, sets or clears the bit at `bitIndex`, writes back. Other bits unchanged. `bitIndex > 15` returns `ErrUnexpectedParameters`.
- **UpdateRegisterMask** — Read-modify-write: only the bits set in `mask` are updated to the corresponding bits in `value`; all other bits are preserved. Use for control words and mode fields without affecting adjacent bits.

### 2.4 Write operations

#### Coils

```go
// FC05 — write one coil (true → 0xFF00, false → 0x0000)
func (mc *ModbusClient) WriteCoil(ctx context.Context, unitID uint8, addr uint16, value bool) error

// FC05 — write one coil with an arbitrary 16-bit payload (non-standard; use sparingly)
func (mc *ModbusClient) WriteCoilValue(ctx context.Context, unitID uint8, addr uint16, payload uint16) error

// FC15 — write multiple coils (quantity ≤ 1968)
func (mc *ModbusClient) WriteCoils(ctx context.Context, unitID uint8, addr uint16, values []bool) error
```

#### 16-bit registers

```go
// FC06 — write one 16-bit register
func (mc *ModbusClient) WriteRegister(ctx context.Context, unitID uint8, addr uint16, value uint16) error

// FC16 — write multiple 16-bit registers (quantity ≤ 123)
func (mc *ModbusClient) WriteRegisters(ctx context.Context, unitID uint8, addr uint16, values []uint16) error
```

For typed writes (32/64-bit, float, ASCII, BCD, IP, etc.) use the **codec API**: `WriteWithCodec[T](mc, ctx, unitID, addr, value, codec)`. See [§ 11 Codec API](#11-codec-api).

#### Raw bytes

**Transport convenience helpers** — no interpretation or codec; for typed values use the codec API.

```go
// FC16 — write bytes into registers in wire order (no reordering)
func (mc *ModbusClient) WriteRawBytes(ctx context.Context, unitID uint8, addr uint16, values []byte) error
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
func (mc *ModbusClient) ReadWriteMultipleRegisters(
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
func (mc *ModbusClient) ReadFIFOQueue(
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
func (mc *ModbusClient) ReadFileRecords(
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
func (mc *ModbusClient) WriteFileRecords(
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
func (mc *ModbusClient) ReadAllDeviceIdentification(
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
func (mc *ModbusClient) ReadDeviceIdentification(
    ctx              context.Context,
    unitID           uint8,
    readDeviceIdCode uint8,
    objectId         uint8,
) (*DeviceIdentification, error)
```

Sends a FC43 / MEI 0x0E request for a specific category or one object. Pages through `MoreFollows` and returns all objects for that request.

**Read device ID code constants:**

| Constant | Value | Category |
|----------|-------|----------|
| `modbus.ReadDeviceIdBasic` | `0x01` | Basic (VendorName, ProductCode, MajorMinorRevision) |
| `modbus.ReadDeviceIdRegular` | `0x02` | Regular (+ VendorUrl, ProductName, ModelName, UserApplicationName) |
| `modbus.ReadDeviceIdExtended` | `0x03` | Extended (+ private objects 0x80–0xFF) |
| `modbus.ReadDeviceIdIndividual` | `0x04` | Single object (set `objectId` to desired ID) |

For stream access (Basic/Regular/Extended), pass `objectId` as `0x00` to start from the first object. For Individual, pass the desired object ID. If you request a higher category than the device supports, it responds at its actual conformity level.

**Response types:**

```go
type DeviceIdentification struct {
    ReadDeviceIdCode uint8   // echo of requested (or actual) category
    ConformityLevel  uint8   // 0x01/0x02/0x03 or 0x81/0x82/0x83 (stream + individual)
    MoreFollows      uint8   // 0x00 = last page, 0xFF = more pages
    NextObjectId     uint8   // next object to request when MoreFollows == 0xFF
    Objects          []DeviceIdentificationObject
}

type DeviceIdentificationObject struct {
    Id    uint8  // object identifier (0x00–0x06 standard, 0x80–0xFF vendor)
    Name  string // human-readable label (e.g. "VendorName", "Extended")
    Value string // decoded UTF-8 value
}
```

**Example — basic only:**

```go
di, err := client.ReadDeviceIdentification(ctx, 1, modbus.ReadDeviceIdBasic, 0x00)
if err != nil {
    log.Fatal(err)
}
for _, obj := range di.Objects {
    fmt.Printf("%s = %s\n", obj.Name, obj.Value)
}
```

**Example — single object (individual access):**

```go
di, err := client.ReadDeviceIdentification(ctx, 1, modbus.ReadDeviceIdIndividual, 0x04)
if err != nil {
    log.Fatal(err)
}
// di.Objects has one element: ProductName (0x04)
```

---

### 2.7 Modbus device detection

**HasUnitReadFunction** probes the given unit with a single read-style function code and returns whether the unit responded with a structurally valid Modbus response (normal or exception). Supported FCs: FC08, FC43, FC03, FC04, FC01, FC02, FC11, FC18, FC20. For any other FC returns `(false, ErrUnexpectedParameters)`. Use after **Open()**.

```go
func (mc *ModbusClient) HasUnitReadFunction(ctx context.Context, unitID uint8, fc FunctionCode) (bool, error)
```

**HasUnitIdentifyFunction** reports whether the unit supports Read Device Identification (FC43). Equivalent to `HasUnitReadFunction(ctx, unitID, FCEncapsulatedInterface)`. Use after **Open()**.

```go
func (mc *ModbusClient) HasUnitIdentifyFunction(ctx context.Context, unitID uint8) (bool, error)
```

---

### 2.8 SunSpec discovery

The library provides transport-level **read-only** SunSpec discovery helpers: detect the SunSpec "SunS" marker, probe candidate base addresses, and enumerate the model chain (model ID and length only). These APIs do not modify device state. They do **not** implement point decoding, scale factors, or schema-driven parsing; that belongs in a higher-level SunSpec library.

**Defaults when `opts` is nil:** `RegType = HoldingRegister`, `BaseAddresses` = `SunSpecDefaultBaseAddresses` (official candidates 0, 40000, 50000 plus adjacent offsets 1, 39999, 40001, 49999, 50001 for 0-based/1-based compatibility), `MaxModels = 256`. UnitID zero is treated as 1. "Not SunSpec" is **not** an error: detection returns `Detected: false` with `error == nil`. Reaching **MaxModels** stops enumeration and returns the models collected so far **without error** (normal truncation). Invalid options (unsupported RegType, empty BaseAddresses) return `ErrUnexpectedParameters`. UnitID may be 0–255 (e.g. when the device is behind a Modbus gateway).

#### Types

```go
type SunSpecOptions struct {
    UnitID         uint8    // Modbus slave/unit ID (0–255); gateway use supported
    RegType        RegType  // default HoldingRegister
    BaseAddresses  []uint16 // default: 0, 40000, 50000 + adjacent 1, 39999, 40001, 49999, 50001
    MaxModels      int      // guard; 0 = 256
    MaxAddressSpan uint16   // max (end − base) for model chain; 0 = no limit
}

type SunSpecProbeAttempt struct {
    BaseAddress uint16
    RegType     RegType
    Registers   []uint16 // len 2 when read succeeded
    Matched     bool
    Error       error
}

type SunSpecDetectionResult struct {
    Detected    bool
    UnitID      uint8
    RegType     RegType
    BaseAddress uint16
    Marker      [2]uint16
    Attempts    []SunSpecProbeAttempt
}

type SunSpecModelHeader struct {
    ID           uint16
    Length       uint16   // payload length in registers (excl. 2-reg header)
    StartAddress uint16
    EndAddress   uint16
    NextAddress  uint16
    HeaderRaw    [2]uint16
    IsEndModel   bool
}

type SunSpecDiscoveryResult struct {
    Detection SunSpecDetectionResult
    Models    []SunSpecModelHeader
}
```

#### DetectSunSpec

Probes each candidate base address: reads 2 registers and checks for the SunSpec marker (reg 0 = 0x5375, reg 1 = 0x6E53). Returns a structured result; only returns a non-nil error for invalid options, context cancellation, or inability to produce a result. Uses the same request path as other client methods (lock per read, retries, metrics).

```go
func (mc *ModbusClient) DetectSunSpec(ctx context.Context, opts *SunSpecOptions) (*SunSpecDetectionResult, error)
```

#### ReadSunSpecModelHeaders

Walks the model chain starting at `baseAddress + 2`, reading 2 registers per model (ID, length). Stops at the end model (ID 0xFFFF, length 0) or when guards trigger. **Reaching MaxModels** stops enumeration and returns the models collected so far **without error**. Returns **partial model results** plus `ErrSunSpecModelChainInvalid` for malformed or non-progressing chains (e.g. length 0 with ID ≠ 0xFFFF, or `baseAddress+2` overflow), or `ErrSunSpecModelChainLimitExceeded` when the chain exceeds `MaxAddressSpan`. Uses **big-endian** for marker and headers. Uses the same request path as other client methods (lock per read, retries, metrics).

```go
func (mc *ModbusClient) ReadSunSpecModelHeaders(
    ctx context.Context,
    opts *SunSpecOptions,
    baseAddress uint16,
) ([]SunSpecModelHeader, error)
```

#### DiscoverSunSpec

Convenience: runs DetectSunSpec and, if detected, ReadSunSpecModelHeaders. Single call for fingerprinting and inventory.

```go
func (mc *ModbusClient) DiscoverSunSpec(ctx context.Context, opts *SunSpecOptions) (*SunSpecDiscoveryResult, error)
```

**Example — detect only:**

```go
res, err := client.DetectSunSpec(ctx, &modbus.SunSpecOptions{UnitID: 1})
if err != nil {
    return err
}
if !res.Detected {
    fmt.Println("not sunspec")
    return nil
}
fmt.Printf("sunspec at base %d\n", res.BaseAddress)
```

**Example — detect and list models:**

```go
disc, err := client.DiscoverSunSpec(ctx, &modbus.SunSpecOptions{UnitID: 1})
if err != nil {
    return err
}
if !disc.Detection.Detected {
    fmt.Println("not sunspec")
    return nil
}
for _, m := range disc.Models {
    if m.IsEndModel {
        fmt.Printf("END at %d\n", m.StartAddress)
        continue
    }
    fmt.Printf("model=%d start=%d end=%d len=%d\n",
        m.ID, m.StartAddress, m.EndAddress, m.Length)
}
```

---

### 2.9 Diagnostics and Report Server ID (FC08/0x11)

#### Diagnostics (FC 0x08)

Sends a Diagnostics request with a sub-function code and optional data. The response echoes the sub-function and returns sub-function-specific data. Use **DiagnosticSubFunction** constants for well-known sub-functions.

```go
func (mc *ModbusClient) Diagnostics(
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
func (mc *ModbusClient) ReportServerId(ctx context.Context, unitID uint8) (*ReportServerIdResponse, error)
```

```go
type ReportServerIdResponse struct {
    ByteCount uint8  // number of following bytes
    Data      []byte // server ID, run indicator (0x00=OFF, 0xFF=ON), optional additional data
}
```

**Example:**

```go
rs, err := client.ReportServerId(ctx, 1)
if err != nil {
    log.Fatal(err)
}
// rs.Data[0] often is server ID byte; last byte often is run indicator; layout is device-specific
```

---

## 3. Server

### 3.1 `ServerConfiguration`

```go
type ServerConfiguration struct {
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
func NewServer(conf *ServerConfiguration, reqHandler RequestHandler) (*ModbusServer, error)
func (ms *ModbusServer) Start() error
func (ms *ModbusServer) Stop() error
```

`NewServer` validates the configuration. `Start` binds the listener and begins
accepting connections. `Stop` closes the listener, closes all open client
connections, and blocks until every in-flight handler goroutine has exited
(backed by a `sync.WaitGroup`).

```go
server, err := modbus.NewServer(&modbus.ServerConfiguration{
    URL:        "tcp://[::]:502",
    MaxClients: 20,
}, &myHandler{})
if err != nil {
    log.Fatal(err)
}
if err := server.Start(); err != nil {
    log.Fatal(err)
}

// graceful shutdown on SIGINT
<-sigCh
server.Stop()
```

### 3.3 `RequestHandler` interface

Implement this interface and pass it to `NewServer`. All four methods receive the
request context (currently `context.Background()`) and a request struct.

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
    ClientAddr string   // source IP address of the client
    ClientRole string   // role from the client TLS certificate (tcp+tls only)
    UnitId     uint8    // target unit / slave ID
    Addr       uint16   // first coil address
    Quantity   uint16   // number of consecutive coils
    IsWrite    bool     // true for FC05/FC15 (writes)
    Args       []bool   // coil values for write requests (nil for reads)
}

type DiscreteInputsRequest struct {
    ClientAddr string
    ClientRole string
    UnitId     uint8
    Addr       uint16
    Quantity   uint16
}

type HoldingRegistersRequest struct {
    ClientAddr string
    ClientRole string
    UnitId     uint8
    Addr       uint16
    Quantity   uint16
    IsWrite    bool     // true for FC06/FC16 (writes)
    Args       []uint16 // register values for write requests (nil for reads)
}

type InputRegistersRequest struct {
    ClientAddr string
    ClientRole string
    UnitId     uint8
    Addr       uint16
    Quantity   uint16
}
```

---

## 4. Errors

The library uses sentinel `error` variables. Use `errors.Is` to test for specific
conditions and `errors.As` to access structured exception details.

### Sentinel errors

```go
var (
    ErrConfigurationError      // invalid configuration passed to NewClient/NewServer
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
    ErrBadUnitId               // response unit ID does not match request
    ErrBadTransactionId        // TCP transaction ID mismatch
    ErrUnknownProtocolId       // non-zero MBAP protocol identifier
    ErrInvalidMBAPLength      // MBAP length &lt; 2 or &gt; 254 (error may wrap value)
    ErrUnexpectedParameters          // invalid arguments passed to a client method
    ErrSunSpecModelChainInvalid      // malformed or non-progressing SunSpec model chain
    ErrSunSpecModelChainLimitExceeded // SunSpec model chain exceeded MaxAddressSpan
    ErrCodecRegisterCount            // register count does not match codec
    ErrCodecByteCount                // byte count does not match codec
    ErrCodecLayout                   // invalid layout for codec
    ErrCodecValue                    // invalid value for encode/decode
    ErrUnknownCodec                  // unknown codec ID (e.g. runtime registry lookup)
    ErrEncodingError                 // codec encoding/byte validation error
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

### Codec errors

Codec operations use the same sentinels and add typed errors for diagnostics. Use `errors.As` to inspect them.

```go
var (
    ErrCodecRegisterCount  // register count mismatch
    ErrCodecByteCount      // byte count mismatch
    ErrCodecLayout        // invalid layout
    ErrCodecValue         // invalid value for encode/decode
    ErrUnknownCodec       // unknown codec ID
    ErrEncodingError      // byte-count or encoding validation
)

// Typed errors (errors.As for Codec, Expected, Actual, Layout, Reason)
type CodecRegisterCountError struct { Codec string; Expected RegisterSpec; Actual uint16 }
type CodecLayoutError struct { Codec string; Layout RegisterLayout; Reason string }
type CodecByteCountError struct { Codec string; Expected ByteSpec; Actual uint16 }
type CodecValueError struct { Codec string; Reason string }
```

---

## 5. Logging

Both `ClientConfiguration` and `ServerConfiguration` accept a `Logger` interface.
When the field is `nil`, the library uses `slog.Default()`.

```go
type Logger interface {
    Debugf(format string, args ...any)
    Infof(format string, args ...any)
    Warnf(format string, args ...any)
    Errorf(format string, args ...any)
}
```

### Built-in constructors

```go
// Wrap a stdlib *log.Logger. Pass nil for a default stdout logger.
func NewStdLogger(l *log.Logger) Logger

// Wrap any slog.Handler (slog.NewJSONHandler, slog.NewTextHandler, etc.)
func NewSlogLogger(h slog.Handler) Logger

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

Attach metric callbacks via the `Metrics` field of `ClientConfiguration` or
`ServerConfiguration`. All methods are called synchronously; implementations must
be **non-blocking** (e.g. increment an atomic counter, send on a buffered channel).

### `ClientMetrics`

```go
type ClientMetrics interface {
    // Called before the first attempt.
    OnRequest(unitID uint8, functionCode uint8)

    // Called after a successful round-trip (including any retry delays).
    OnResponse(unitID uint8, functionCode uint8, duration time.Duration)

    // Called when a request ultimately fails with a non-timeout error.
    OnError(unitID uint8, functionCode uint8, duration time.Duration, err error)

    // Called when a request ultimately fails due to a timeout.
    OnTimeout(unitID uint8, functionCode uint8, duration time.Duration)
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

Control retry behaviour with the `RetryPolicy` field of `ClientConfiguration`.

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

On each retry the client automatically:
1. Closes the failed connection.
2. Sleeps for the policy-specified delay, releasing the lock so other goroutines
   are not blocked.
3. Dials a fresh connection before the next attempt.

---

## 8. Connection pool

Enable a connection pool to allow concurrent goroutines to share a single
`*ModbusClient` without serialising on a single connection.

```go
conf := &modbus.ClientConfiguration{
    URL:      "tcp://plc.local:502",
    MinConns: 2,   // pre-warm 2 connections during Open()
    MaxConns: 8,   // pool up to 8 concurrent connections
}
client, _ := modbus.NewClient(conf)
client.Open()
```

- Applies to all TCP-based transports (`tcp`, `rtuovertcp`, `rtuoverudp`, `udp`).
- RTU (serial) always uses a single connection; pooling is silently ignored.
- When the pool is at capacity and all connections are in use, goroutines block
  until one is returned, or until the context is cancelled.
- Failed connections are discarded; the pool dials replacements lazily on the next
  `acquire` call.
- `Close()` drains and closes all idle pool connections.

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

server, err := modbus.NewServer(&modbus.ServerConfiguration{
    URL:           "tcp+tls://[::]:802",
    TLSServerCert: &cert,
    TLSClientCAs:  pool,
}, handler)
```

---

## 10. Type constants

### `Parity`

Used in `ClientConfiguration.Parity` (RTU only).

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

### 11.3 Transport: ReadWithCodec, WriteWithCodec

These are **package-level** generic functions (Go methods cannot have type parameters). They use wire order (big-endian per register); layout is defined by the codec.

```go
func ReadWithCodec[T any](
    mc *ModbusClient,
    ctx context.Context,
    unitID uint8,
    addr uint16,
    regType RegType,
    codec Decoder[T],
) (T, error)

func WriteWithCodec[T any](
    mc *ModbusClient,
    ctx context.Context,
    unitID uint8,
    addr uint16,
    value T,
    codec Encoder[T],
) error
```

If `codec.RegisterSpec().Count == 0`, `ReadWithCodec` returns a `*CodecRegisterCountError`.

**Convenience wrappers for uint32:**

```go
func ReadUint32WithLayout(mc *ModbusClient, ctx context.Context, unitID uint8, addr uint16, regType RegType, layout RegisterLayout) (uint32, error)
func WriteUint32WithLayout(mc *ModbusClient, ctx context.Context, unitID uint8, addr uint16, v uint32, layout RegisterLayout) error
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

Sentinels: `ErrCodecRegisterCount`, `ErrCodecLayout`, `ErrCodecValue`, `ErrCodecByteCount`, `ErrUnknownCodec`, `ErrEncodingError`. Typed: `*CodecRegisterCountError`, `*CodecLayoutError`, `*CodecByteCountError`, `*CodecValueError`; all implement `Unwrap()` to the appropriate sentinel. See [§ 4](#4-errors).

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

**Client-bound** (perform a Modbus read or write using a runtime codec):

```go
func ReadWithRuntimeCodec(mc *ModbusClient, ctx context.Context, unitID uint8, addr uint16, regType RegType, codec RuntimeDecoder) (any, error)
func WriteWithRuntimeCodec(mc *ModbusClient, ctx context.Context, unitID uint8, addr uint16, value any, codec RuntimeEncoder) error
```

**Offline** (decode/encode using a descriptor only; useful when you have a descriptor from discovery but no codec instance):

```go
func DecodeWithDescriptor(regs []uint16, desc CodecDescriptor) (any, error)
func EncodeWithDescriptor(value any, desc CodecDescriptor) ([]uint16, error)
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
func ExecuteRuntimeDecodePlan(mc *ModbusClient, ctx context.Context, unitID uint8, plan RuntimeDecodePlan) (*RuntimeDecodeResult, error)
func ExecuteRuntimeDecodePlanOffline(regs []uint16, plan RuntimeDecodePlan) (*RuntimeDecodeResult, error)
```

`ValidateRuntimeDecodePlan` returns a `*RuntimePlanValidationError` when the window is invalid (quantity not 1–125, address overflow), items have empty or duplicate names, a codec is nil, or an item’s offset and register count would extend past the window. The executors perform one read (or use the provided slice) and decode each item; per-item decode failures are recorded in `RuntimeDecodedValue.Error` and do not abort the whole plan.

