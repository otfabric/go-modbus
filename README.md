# modbus — Modbus Protocol Library

[![Go](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE.txt)
[![Go Report Card](https://goreportcard.com/badge/github.com/otfabric/modbus?cache=false)](https://goreportcard.com/report/github.com/otfabric/modbus)
[![CI](https://github.com/otfabric/modbus/actions/workflows/ci.yml/badge.svg)](https://github.com/otfabric/modbus/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/otfabric/modbus?display_name=tag)](https://github.com/otfabric/modbus/releases)

A production-ready Go implementation of the Modbus application protocol, providing both **client** and **server** capabilities. No C dependencies, no CGo — just Go.

The library exposes a high-level, idiomatic Go API for both client and server roles,
working with native Go types across all supported transports. Every request carries a
`context.Context` for cancellation and deadline propagation.
Advanced features — connection pooling, automatic retries, structured logging, and
metrics hooks — are built in.

> For the complete type signatures, configuration options, and runnable examples see
> **[API.md](API.md)**.

---

## Table of Contents

- [github.com/otfabric/modbus](#githubcomotfabricmodbus)
  - [Table of Contents](#table-of-contents)
  - [Install](#install)
  - [Transport modes](#transport-modes)
  - [Client](#client)
  - [Client supported function codes](#client-supported-function-codes)
  - [Codec API](#codec-api)
  - [Supported Go types](#supported-go-types)
  - [Byte order and layout](#byte-order-and-layout)
  - [Server](#server)
    - [Server supported function codes](#server-supported-function-codes)
  - [Logging](#logging)
  - [Error handling](#error-handling)
  - [Advanced features](#advanced-features)
    - [Retry policy](#retry-policy)
    - [Connection pool](#connection-pool)
    - [Metrics hooks](#metrics-hooks)
  - [CLI client](#cli-client)
  - [Examples](#examples)
  - [Dependencies](#dependencies)
  - [License](#license)

---

## Install

```bash
go get github.com/otfabric/modbus
```

Requires **Go 1.21** or later.

---

## Transport modes

The transport is selected by the `scheme://address` URL in `ClientConfiguration.URL`
or `ServerConfiguration.URL`.

| Scheme | Transport | Client | Server |
|---|---|:---:|:---:|
| `tcp://<host:port>` | Modbus TCP (MBAP) | ✓ | ✓ |
| `tcp+tls://<host:port>` | Modbus TCP over TLS (MBAPS / Modbus Security) | ✓ | ✓ |
| `udp://<host:port>` | Modbus TCP framing over UDP | ✓ | — |
| `rtu://<device>` | Modbus RTU over serial (RS-232 / RS-485) | ✓ | — |
| `rtuovertcp://<host:port>` | Modbus RTU framing tunnelled over TCP | ✓ | — |
| `rtuoverudp://<host:port>` | Modbus RTU framing tunnelled over UDP | ✓ | — |

> **Note:** UDP transports are not part of the official Modbus specification. Both
> MBAP-over-UDP (`udp://`) and RTU-over-UDP (`rtuoverudp://`) are provided because
> different vendors use different framing conventions. When unsure, try both.
>
> Standard ports: use `modbus.PortModbusTCP` (502) or `modbus.PortModbusTLS` (802) in URLs or docs; RTU over TCP has no standard port.

---

## Client

### Client supported function codes

All client methods accept a `context.Context` as their first argument and a
`unitID uint8` (slave / unit ID) as their second, enabling per-request deadline and
cancellation control independent of the connection lifecycle.

| FC | Hex | Name | Client method(s) |
|---|---|---|---|
| 01 | 0x01 | Read Coils | `ReadCoil`, `ReadCoils` |
| 02 | 0x02 | Read Discrete Inputs | `ReadDiscreteInput`, `ReadDiscreteInputs` |
| 03 | 0x03 | Read Holding Registers | `ReadRegister`, `ReadRegisters`, `ReadRawBytes`, `ReadRegisterBit`, `ReadRegisterBits`, `ReadWithCodec`, … (see API) |
| 04 | 0x04 | Read Input Registers | same methods as FC03, passing `InputRegister` |
| 05 | 0x05 | Write Single Coil | `WriteCoil`, `WriteCoilValue` |
| 06 | 0x06 | Write Single Register | `WriteRegister` |
| 15 | 0x0F | Write Multiple Coils | `WriteCoils` |
| 16 | 0x10 | Write Multiple Registers | `WriteRegisters`, `WriteRawBytes`, `WriteRegisterBit`, `UpdateRegisterMask`, `WriteWithCodec`, … (see API) |
| 20 | 0x14 | Read File Record | `ReadFileRecords` |
| 21 | 0x15 | Write File Record | `WriteFileRecords` |
| 08 | 0x08 | Diagnostics | `Diagnostics` |
| 11 | 0x11 | Report Server ID | `ReportServerId` |
| 23 | 0x17 | Read/Write Multiple Registers | `ReadWriteMultipleRegisters` |
| 24 | 0x18 | Read FIFO Queue | `ReadFIFOQueue` |
| 43/14 | 0x2B/0x0E | Read Device Identification | `ReadDeviceIdentification`, `ReadAllDeviceIdentification` |

**Device detection:** `HasUnitReadFunction(ctx, unitID, fc)` checks a single read-style FC (FC08, FC43, FC03, FC04, FC01, FC02, FC11, FC18, FC20). `HasUnitIdentifyFunction(ctx, unitID)` checks FC43 (Read Device Identification). **SunSpec discovery:** `DetectSunSpec(ctx, opts)` probes candidate base addresses for the SunSpec "SunS" marker; `ReadSunSpecModelHeaders(ctx, opts, base)` enumerates the model chain (ID and length only); `DiscoverSunSpec(ctx, opts)` does both in one call for fingerprinting and inventory. The library does not decode SunSpec points or schemas — only transport-level detection and model headers. See [API.md § 2.7](API.md#27-modbus-device-detection) and [API.md § 2.8](API.md#28-sunspec-discovery).

### Codec API

The library provides a **codec-first** layer for typed register read/write with explicit layout and discovery:

- **Raw vs typed:** Use **ReadRegisters** / **WriteRegisters** or **ReadRawBytes** / **WriteRawBytes** for raw transport; use **ReadWithCodec** / **WriteWithCodec** with a `Codec[T]` when the type and layout are known at compile time.
- **Transport:** `ReadWithCodec[T]` and `WriteWithCodec[T]` are package-level generic functions that read or write registers using a `Decoder[T]` or `Encoder[T]`. The codec owns layout and interpretation.
- **Layout:** `RegisterLayout` describes byte order across registers (e.g. big-endian 4321 vs little-endian 2143). Use `NewRegisterLayout` or common vars such as `Layout32_4321`, `Layout64_21436587`.
- **Codecs:** Constructors like `NewUint32Codec(layout)`, `NewAsciiCodec(registerCount)`, `NewIPAddrCodec()` return fixed-width `Codec[T]` instances. Numeric codecs take a layout; text and byte codecs take a register or byte count.
- **Discovery:** `AvailableCodecDescriptors()`, `CodecDescriptorsForRegisterCount`, `CodecDescriptorByID`, `CodecCandidatesForRegisterCount`, and `FindCodecDescriptors` expose a **curated subset** of common widths for UI/CLI. The registry is not exhaustive: constructors support any valid width (e.g. `NewAsciiCodec(5)` or `NewBytesCodec(18)` work even if those widths are not in the discovery set).
- **Runtime codecs:** For CLI, descriptor-driven, or batch workflows where the type is not known at compile time, use `RuntimeDecoder` / `RuntimeEncoder` / `RuntimeCodec`, `RuntimeCodecByID`, `ReadWithRuntimeCodec`, `WriteWithRuntimeCodec`, and **batch decode** (`RuntimeDecodePlan`, `ExecuteRuntimeDecodePlan` / `ExecuteRuntimeDecodePlanOffline`) to read one window and decode multiple fields. See [API.md § 11.8–11.11](API.md#118-runtime-codec-api).
- **Offline:** `DecodeRegisters`, `EncodeRegisters`, `DecodeWithDescriptor`, `EncodeWithDescriptor`, `ValidateRegisterSpec`, and `ValidateByteSpec` work on `[]uint16` / `[]byte` for tests and tooling.

A full list of available codecs (numeric, text, bytes, network) with constructors and stable IDs is in **[CODECS.md](CODECS.md)**. See [API.md § 11](API.md#11-codec-api) for the full codec API reference.

### Supported Go types

| Modbus data model | Go types |
|---|---|
| Coils / discrete inputs | `bool`, `[]bool` |
| 16-bit registers | `uint16`, `[]uint16`, `int16`, `[]int16` |
| 32-bit registers (2 × 16-bit) | `uint32`, `[]uint32`, `int32`, `[]int32`, `float32`, `[]float32` |
| 48-bit registers (3 × 16-bit) | `uint64`, `[]uint64` (unsigned), `int64`, `[]int64` (signed) |
| 64-bit registers (4 × 16-bit) | `uint64`, `[]uint64`, `int64`, `[]int64`, `float64`, `[]float64` |
| Decimal limb / M10k (2–4 × 16-bit) | `uint32`, `int32`, `uint64`, `int64` (base-10000 limbs; see [CODECS.md](CODECS.md)) |
| ASCII string (N × 16-bit) | `string` (trailing spaces stripped) |
| BCD / Packed BCD (N × 16-bit) | `string` (decimal digits; signed packed BCD and reverse byte-order variants available) |
| Raw wire bytes | `[]byte` (endianness-aware or unmodified) |
| File records | `[]FileRecordRequest` (read) / `[]FileRecord` (write) |

### Byte order and layout

Byte and word order for multi-register values are defined by the **codec**, not by client-wide settings. Use a codec with the appropriate `RegisterLayout` (e.g. `NewUint32Codec(Layout32_4321)` for big-endian ABCD, `Layout32_2143` for CDAB). Common layout variables: `Layout16_21`, `Layout32_4321`, `Layout32_2143`, `Layout48_654321`, `Layout48_214365`, `Layout64_87654321`, `Layout64_21436587`. Raw transport (`ReadRegisters`, `ReadRawBytes`, `WriteRegisters`, `WriteRawBytes`) returns or accepts wire-order data only; interpretation is left to the caller or to the codec API.

---

## Server

### Server supported function codes

The server dispatches decoded requests to a user-provided `RequestHandler`
implementation. All four handler methods cover the full set of supported function codes:

| FC(s) | Hex | Name | Handler method | `IsWrite` |
|---|---|---|---|---|
| 01 | 0x01 | Read Coils | `HandleCoils` | `false` |
| 02 | 0x02 | Read Discrete Inputs | `HandleDiscreteInputs` | — |
| 03 | 0x03 | Read Holding Registers | `HandleHoldingRegisters` | `false` |
| 04 | 0x04 | Read Input Registers | `HandleInputRegisters` | — |
| 05 | 0x05 | Write Single Coil | `HandleCoils` | `true` |
| 06 | 0x06 | Write Single Register | `HandleHoldingRegisters` | `true` |
| 15 | 0x0F | Write Multiple Coils | `HandleCoils` | `true` |
| 16 | 0x10 | Write Multiple Registers | `HandleHoldingRegisters` | `true` |

Returning a Modbus sentinel error (e.g. `ErrIllegalDataAddress`) causes the server to
send the corresponding exception code back to the client. Any other non-nil error maps
to `ServerDeviceFailure`.

---

## Logging

Both `ClientConfiguration` and `ServerConfiguration` expose a `Logger` field. When
`nil`, the library writes through `slog.Default()` — the Go standard structured logger.

| Constructor | Behaviour |
|---|---|
| `NewStdLogger(l *log.Logger)` | Wraps a stdlib `*log.Logger`; pass `nil` for a default stdout logger |
| `NewSlogLogger(h slog.Handler)` | Wraps any `slog.Handler` (e.g. `slog.NewJSONHandler`, `slog.NewTextHandler`) |
| `NopLogger()` | Discards all output — useful in tests |

The `Logger` interface is straightforward to implement for any custom logging library
(zap, zerolog, logrus, …). See [API.md § 5](API.md#5-logging) for details and an
adapter example.

---

## Error handling

All client methods return a typed `error`. The library defines sentinel errors that can
be tested with `errors.Is`:

| Sentinel | Cause |
|---|---|
| `ErrConfigurationError` | Invalid configuration passed to `NewClient` / `NewServer` |
| `ErrClientNotOpen` | Request made before `Open()` or after `Close()` |
| `ErrRequestTimedOut` | Request exceeded its deadline or configured timeout |
| `ErrIllegalFunction` | Modbus exception 0x01 |
| `ErrIllegalDataAddress` | Modbus exception 0x02 |
| `ErrIllegalDataValue` | Modbus exception 0x03 |
| `ErrServerDeviceFailure` | Modbus exception 0x04 |
| `ErrAcknowledge` | Modbus exception 0x05 |
| `ErrServerDeviceBusy` | Modbus exception 0x06 |
| `ErrMemoryParityError` | Modbus exception 0x08 |
| `ErrGWPathUnavailable` | Modbus exception 0x0A |
| `ErrGWTargetFailedToRespond` | Modbus exception 0x0B |
| `ErrBadCRC` | RTU CRC mismatch |
| `ErrShortFrame` | Frame too short to decode |
| `ErrProtocolError` | Malformed or unexpected response |
| `ErrBadUnitId` | Response unit ID does not match request |
| `ErrBadTransactionId` | TCP transaction ID mismatch (MBAP) |
| `ErrUnknownProtocolId` | Non-zero MBAP protocol identifier |
| `ErrInvalidMBAPLength` | MBAP length &lt; 2 or &gt; 254 (TCP) |
| `ErrUnexpectedParameters` | Invalid arguments passed to a client method |
| `ErrSunSpecModelChainInvalid` | Malformed or non-progressing SunSpec model chain |
| `ErrSunSpecModelChainLimitExceeded` | SunSpec model chain exceeded `MaxAddressSpan` |
| `ErrCodecRegisterCount` | Register count does not match codec |
| `ErrCodecLayout` | Invalid layout for codec |
| `ErrCodecValue` | Invalid value for encode/decode |
| `ErrCodecByteCount` | Byte count does not match codec |
| `ErrEncodingError` | Codec encoding/byte validation error |

When the remote device sends a Modbus exception response, the error is additionally
wrapped in `*ExceptionError`, which carries the raw `FunctionCode` and `ExceptionCode`
bytes. `errors.As` gives access to those fields while `errors.Is` still works against
the sentinel:

```go
var excErr *modbus.ExceptionError
if errors.As(err, &excErr) {
    fmt.Printf("fc=0x%02x exception=0x%02x\n", excErr.FunctionCode, excErr.ExceptionCode)
}
if errors.Is(err, modbus.ErrIllegalDataAddress) {
    // address does not exist on this device
}
```

See [API.md § 4](API.md#4-errors) for the full reference.

---

## Advanced features

### Retry policy

Configure automatic retry with exponential back-off on transient errors. The client
re-dials the transport between attempts (or replaces the failed pool connection when
pooling is enabled).

Set `ClientConfiguration.RetryPolicy` to one of the built-in implementations or
provide a custom `RetryPolicy` implementation. See [API.md § 7](API.md#7-retry-policy).

### Connection pool

Set `MaxConns > 1` to enable a bounded connection pool. Multiple goroutines sharing a
single `*ModbusClient` can then execute requests concurrently, each on its own
connection, without serialising on a single TCP socket.

`MinConns` controls how many connections are pre-warmed during `Open()`. Applies to
TCP-based transports only; RTU (serial) always uses a single connection. See
[API.md § 8](API.md#8-connection-pool).

### Metrics hooks

Implement `ClientMetrics` and/or `ServerMetrics` and assign them to the `Metrics`
field of the respective configuration struct. Callbacks (`OnRequest`, `OnResponse`,
`OnError`, `OnTimeout`) fire synchronously on every request outcome and must be
non-blocking. See [API.md § 6](API.md#6-metrics).

---

## CLI client

A command-line Modbus client is included in `cmd/modbus-cli.go`:

```bash
go build -o modbus-cli cmd/modbus-cli.go
./modbus-cli --help
```

---

## Examples

| File | Description |
|---|---|
| [examples/tcp_server.go](examples/tcp_server.go) | Modbus TCP server with an in-memory `RequestHandler` |
| [examples/tls_server.go](examples/tls_server.go) | MBAPS (Modbus over TLS) server with client certificate authentication |
| [examples/tls_client.go](examples/tls_client.go) | MBAPS client with mutual TLS |

For the full public API reference — all types, method signatures, configuration
details, and annotated examples — see **[API.md](API.md)**.

---

## Dependencies

- [github.com/goburrow/serial](https://github.com/goburrow/serial) — serial port access for RTU mode

---

## License

MIT.
