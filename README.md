# go-modbus — Modbus Protocol Library

[![Go](https://img.shields.io/badge/Go-1.21+-00ADD8?style=flat&logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE.txt)
[![Go Report Card](https://goreportcard.com/badge/github.com/otfabric/go-modbus?cache=false)](https://goreportcard.com/report/github.com/otfabric/go-modbus)
[![CI](https://github.com/otfabric/go-modbus/actions/workflows/ci.yml/badge.svg)](https://github.com/otfabric/go-modbus/actions/workflows/ci.yml)
[![Codecov](https://codecov.io/github/otfabric/go-modbus/graph/badge.svg?token=77Z0S76MTY)](https://codecov.io/github/otfabric/go-modbus)
[![Release](https://img.shields.io/github/v/release/otfabric/go-modbus?display_name=tag)](https://github.com/otfabric/go-modbus/releases)

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

- [github.com/otfabric/go-modbus](#githubcomotfabricgo-modbus)
  - [Table of Contents](#table-of-contents)
  - [API tiers](#api-tiers)
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
    - [Concurrency](#concurrency)
    - [Metrics hooks](#metrics-hooks)
    - [Client diagnostics](#client-diagnostics)
    - [Configuration grouping](#configuration-grouping)
  - [CLI client](#cli-client)
  - [Examples](#examples)
  - [Dependencies](#dependencies)
  - [License](#license)

---

## API tiers

The library is organized into five distinct API tiers, from lowest to highest level:

| Tier | Package | Purpose | Typical user |
|------|---------|---------|-------------|
| **1. Raw Modbus** | `modbus` | Direct function-code methods (`ReadCoils`, `WriteRegisters`, …) returning native Go types. Full control over unit IDs, addresses, and quantities. | Integrators who know their device's register map |
| **2. Typed Codec** | `modbus/codec` | Layout-aware encode/decode of multi-register values (`Uint32Codec`, `Float64Codec`, `AsciiCodec`, time codecs, …). Compile-time generics and runtime codec support for descriptor-driven workflows. | Applications that need typed register access |
| **3. SunSpec** | `modbus/sunspec` | SunSpec marker detection, model-chain enumeration, and device fingerprinting. Transport-level only — no point/schema decoding. | Solar/energy system integrators |
| **4. Server** | `modbus` | `RequestHandler`-based Modbus TCP/TLS server with per-connection contexts, panic recovery, optional `MaskWriteHandler` and `ReadWriteHandler` interfaces for FC22/FC23. | Simulating or embedding Modbus devices |
| **5. CLI** | `cmd/modbus-cli` | Command-line client for read/write, scanning, pinging, and codec probing. Supports `--json` for structured output. | Operators and CI/CD pipelines |

Each tier builds on the one below. For example, the Codec API uses the Raw Modbus API
internally; the CLI uses the Codec and Raw APIs. Pick the tier that matches your
use case — you never need to use a higher tier.

---

## Install

```bash
go get github.com/otfabric/go-modbus
```

Requires **Go 1.21** or later.

---

## Transport modes

The transport is selected by the `scheme://address` URL in `Config.URL`
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
> The UDP wrapper presents a stream-like interface over datagrams by buffering
> leftover bytes from partially consumed datagrams. This only works correctly when
> each request/response maps to a single datagram with no loss, reordering, or
> multiplexing. It is not recommended for high-reliability production use unless
> specifically validated against the target device(s).
>
> Standard ports: use `modbus.PortModbusTCP` (502) or `modbus.PortModbusTLS` (802) in URLs or docs; RTU over TCP has no standard port.

**Config field applicability by transport:**

| Config field | Applies to |
|---|---|
| `Speed`, `DataBits`, `Parity`, `StopBits` | RTU (serial) only |
| `DialTimeout` | TCP, TCP+TLS, UDP, RTU-over-TCP/UDP — not serial RTU |
| `TLSClientCert`, `TLSRootCAs` | TCP+TLS only |
| `MinConns`, `MaxConns` | TCP-based transports only; serial and TLS always use one connection. `MaxConns > 1` on non-poolable transports is silently clamped to 1 with a warning log. |

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
| 03 | 0x03 | Read Holding Registers | `ReadHoldingRegister`, `ReadHoldingRegisters`, `ReadRegister`, `ReadRegisters`, `ReadRegisterBytes`, `ReadRegisterBit`, `ReadRegisterBits`, `codec.ReadFromClient`, … (see API) |
| 04 | 0x04 | Read Input Registers | `ReadInputRegister`, `ReadInputRegisters`, same methods as FC03, passing `InputRegister` |
| 05 | 0x05 | Write Single Coil | `WriteCoil`, `WriteCoilRaw` |
| 06 | 0x06 | Write Single Register | `WriteRegister` |
| 07 | 0x07 | Read Exception Status | `ReadExceptionStatus` |
| 08 | 0x08 | Diagnostics | `Diagnostics`, `DiagnosticLoopback`, `DiagnosticRegister`, `BusMessageCount`, `DiagnosticForceListenOnlyMode`, `DiagnosticClearCounters`, and per-counter wrappers (see API) |
| 11 | 0x0B | Get Comm Event Counter | `GetCommEventCounter` |
| 12 | 0x0C | Get Comm Event Log | `GetCommEventLog` |
| 15 | 0x0F | Write Multiple Coils | `WriteCoils` |
| 16 | 0x10 | Write Multiple Registers | `WriteRegisters`, `WriteRegisterBytes`, `WriteRegisterBit`, `UpdateRegisterMask`, `codec.WriteToClient`, … (see API) |
| 17 | 0x11 | Report Server ID | `ReportServerID` |
| 20 | 0x14 | Read File Record | `ReadFileRecords` |
| 21 | 0x15 | Write File Record | `WriteFileRecords` |
| 22 | 0x16 | Mask Write Register | `MaskWriteRegister` (atomic server-side bit manipulation) |
| 23 | 0x17 | Read/Write Multiple Registers | `ReadWriteMultipleRegisters` |
| 24 | 0x18 | Read FIFO Queue | `ReadFIFOQueue` |
| 43/14 | 0x2B/0x0E | Read Device Identification | `ReadDeviceIdentification`, `ReadAllDeviceIdentification` |

**Note on advanced methods:**
- `WriteCoilRaw` (FC05) sends an arbitrary 16-bit payload instead of the standard
  0xFF00/0x0000 values. It is intended exclusively for vendor-specific control semantics
  (toggle, interlock, delayed activation). Compliant devices may reject non-standard
  payloads — prefer `WriteCoil` for standard operations.
- `ReadRegisterBytes` / `WriteRegisterBytes` transfer raw bytes backed by 16-bit registers.
  They are not byte-addressable — byte count must be even and maps to N/2 registers.
  Use them when the device register map represents opaque byte blobs (firmware, config blocks)
  rather than typed numeric values.

**Transport-neutral policy:** The Modbus spec labels FC07, FC08, FC11 (0x0B), and FC12
(0x0C) as "Serial Line only," but real-world Modbus TCP/UDP gateways routinely forward
these PDUs. This library supports all function codes on every transport (TCP, TLS, UDP,
RTU, RTU-over-TCP/UDP) and does not restrict any FC by transport type.

**MEI type 13 (CANopen General Reference):** FC43 sub-type 13 (0x0D) is intentionally
unsupported. It targets CANopen device profiles and has no practical use in typical Modbus
deployments. Only MEI type 14 (0x0E, Read Device Identification) is implemented.

**Device detection:** `SupportsFunction(ctx, unitID, fc)` checks a single read-style FC (FC08, FC43, FC03, FC04, FC01, FC02, FC11, FC18, FC20). Returns `(false, nil)` for probe-negative outcomes (timeout, exception, gateway failure); returns `(false, err)` for real transport errors. For richer diagnostics, `ProbeFunction(ctx, unitID, fc)` returns a `ProbeResult` with `Outcome` (supported/exception/timeout/transport error/validation failed), optional `ExceptionCode`, `ResponseFC`, `RawPayload`, and `Reason` — useful for discovery tools and field debugging of quirky devices. `SupportsDeviceIdentification(ctx, unitID)` checks FC43 (Read Device Identification). **SunSpec discovery** lives in the `sunspec` subpackage: `sunspec.DetectSunSpec`, `sunspec.ReadSunSpecModelHeaders`, and `sunspec.DiscoverSunSpec` probe for the SunSpec "SunS" marker, enumerate model chains, and combine both for fingerprinting and inventory. The library does not decode SunSpec points or schemas — only transport-level detection and model headers. See [API.md § 2.7](API.md#27-modbus-device-detection) and [API.md § 2.8](API.md#28-sunspec-discovery).

### Codec API

The library provides a **codec-first** layer for typed register read/write with explicit layout and discovery. Codec functions live in the `codec` subpackage (`import "github.com/otfabric/go-modbus/codec"`):

- **Raw vs typed:** Use **ReadRegisters** / **WriteRegisters** or **ReadRegisterBytes** / **WriteRegisterBytes** for raw transport; use **codec.ReadFromClient** / **codec.WriteToClient** with a `Codec[T]` when the type and layout are known at compile time.
- **Transport:** `codec.ReadFromClient[T]` and `codec.WriteToClient[T]` are package-level generic functions that read or write registers via a `codec.RegisterReader` / `codec.RegisterWriter` interface. The codec owns layout and interpretation. `*Client` satisfies both interfaces.
- **Layout:** `codec.RegisterLayout` describes byte order across registers (e.g. big-endian 4321 vs little-endian 2143). Use `codec.NewRegisterLayout` or common vars such as `codec.Layout32_4321`, `codec.Layout64_21436587`.
- **Codecs:** Constructors like `codec.NewUint32Codec(layout)`, `codec.NewAsciiCodec(registerCount)`, `codec.NewIPAddrCodec()`, and time codecs (e.g. `codec.NewDateTime2S2000Codec()`, `codec.NewDateTimeYMDhmsUTCCodec()`, `codec.NewDateTimeIEC870UTCCodec()`) return fixed-width `Codec[T]` instances. Numeric codecs take a layout; text and byte codecs take a register or byte count; time codecs use UTC, local, or default-UTC interpretation.
- **Discovery:** `codec.AvailableCodecDescriptors()`, `codec.CodecDescriptorsForRegisterCount`, `codec.CodecDescriptorByID`, `codec.CodecCandidatesForRegisterCount`, and `codec.FindCodecDescriptors` expose a **curated subset** of common widths for UI/CLI. The registry is not exhaustive: constructors support any valid width (e.g. `codec.NewAsciiCodec(5)` or `codec.NewBytesCodec(18)` work even if those widths are not in the discovery set).
- **Runtime codecs:** For CLI, descriptor-driven, or batch workflows where the type is not known at compile time, use `codec.RuntimeDecoder` / `codec.RuntimeEncoder` / `codec.RuntimeCodec`, `codec.RuntimeCodecByID`, `codec.ReadRuntimeFromClient`, `codec.WriteRuntimeToClient`, and **batch decode** (`codec.RuntimeDecodePlan`, `codec.ExecuteRuntimeDecodePlan` / `codec.ExecuteRuntimeDecodePlanOffline`) to read one window and decode multiple fields. See [API.md § 11.8–11.11](API.md#118-runtime-codec-api).
- **Offline:** `codec.DecodeRegisters`, `codec.EncodeRegisters`, `codec.DecodeWithDescriptor`, `codec.EncodeWithDescriptor`, `codec.ValidateRegisterSpec`, and `codec.ValidateByteSpec` work on `[]uint16` / `[]byte` for tests and tooling.

A full list of available codecs (numeric, text, bytes, network, time) with constructors and stable IDs is in **[CODECS.md](CODECS.md)**. See [API.md § 11](API.md#11-codec-api) for the full codec API reference.

### Supported Go types

| Modbus data model | Go types |
|---|---|
| Coils / discrete inputs | `bool`, `[]bool` |
| 16-bit registers | `uint16`, `[]uint16`, `int16`, `[]int16` |
| 32-bit registers (2 × 16-bit) | `uint32`, `[]uint32`, `int32`, `[]int32`, `float32`, `[]float32` |
| 48-bit registers (3 × 16-bit) | `uint64`, `[]uint64` (unsigned), `int64`, `[]int64` (signed) |
| 64-bit registers (4 × 16-bit) | `uint64`, `[]uint64`, `int64`, `[]int64`, `float64`, `[]float64` |
| Decimal limb / M10k (2–4 × 16-bit) | `uint32`, `int32`, `uint64`, `int64` (base-10000 limbs; see [CODECS.md](CODECS.md)) |
| Time (2–6 × 16-bit) | `time.Time` (s2000, YMDhms, or IEC 60870-5 CP56Time2a; see [CODECS.md § 5](CODECS.md#5-time-codecs)) |
| ASCII string (N × 16-bit) | `string` (trailing spaces stripped) |
| BCD / Packed BCD (N × 16-bit) | `string` (decimal digits; signed packed BCD and reverse byte-order variants available) |
| Raw wire bytes | `[]byte` (endianness-aware or unmodified) |
| File records | `[]FileRecordRequest` (read) / `[]FileRecord` (write) |

### Byte order and layout

Byte and word order for multi-register values are defined by the **codec**, not by client-wide settings. Use a codec with the appropriate `RegisterLayout` (e.g. `codec.NewUint32Codec(codec.Layout32_4321)` for big-endian ABCD, `codec.Layout32_2143` for CDAB). Common layout variables: `codec.Layout16_21`, `codec.Layout32_4321`, `codec.Layout32_2143`, `codec.Layout48_654321`, `codec.Layout48_214365`, `codec.Layout64_87654321`, `codec.Layout64_21436587`. Raw transport (`ReadRegisters`, `ReadRegisterBytes`, `WriteRegisters`, `WriteRegisterBytes`) returns or accepts wire-order data only; interpretation is left to the caller or to the codec API.

---

## Server

### Server supported function codes

The server dispatches decoded requests to a user-provided `RequestHandler`
implementation. All four handler methods cover the full set of supported function codes:

| FC(s) | Hex | Name | Handler method / interface | `IsWrite` |
|---|---|---|---|---|
| 01 | 0x01 | Read Coils | `HandleCoils` | `false` |
| 02 | 0x02 | Read Discrete Inputs | `HandleDiscreteInputs` | — |
| 03 | 0x03 | Read Holding Registers | `HandleHoldingRegisters` | `false` |
| 04 | 0x04 | Read Input Registers | `HandleInputRegisters` | — |
| 05 | 0x05 | Write Single Coil | `HandleCoils` | `true` |
| 06 | 0x06 | Write Single Register | `HandleHoldingRegisters` | `true` |
| 15 | 0x0F | Write Multiple Coils | `HandleCoils` | `true` |
| 16 | 0x10 | Write Multiple Registers | `HandleHoldingRegisters` | `true` |
| 07 | 0x07 | Read Exception Status | `ExceptionStatusHandler` (optional) | — |
| 11 | 0x0B | Get Comm Event Counter | `CommEventCounterHandler` (optional) | — |
| 12 | 0x0C | Get Comm Event Log | `CommEventLogHandler` (optional) | — |
| 22 | 0x16 | Mask Write Register | `MaskWriteHandler` (optional) | — |
| 23 | 0x17 | Read/Write Multiple Registers | `ReadWriteHandler` (optional) | — |

FC07, FC11, FC12, FC22 and FC23 use optional handler interfaces. If the `RequestHandler`
also implements the corresponding interface (e.g. `ExceptionStatusHandler`,
`CommEventCounterHandler`, `CommEventLogHandler`, `MaskWriteHandler`, `ReadWriteHandler`),
those FCs are dispatched accordingly; otherwise they return `Illegal Function`.

Returning a Modbus sentinel error (e.g. `ErrIllegalDataAddress`) causes the server to
send the corresponding exception code back to the client. Any other non-nil error maps
to `ServerDeviceFailure`. Unsupported FCs receive an `Illegal Function` exception.
Handler panics are recovered and logged (with full stack trace); the client receives a
`ServerDeviceFailure` exception.

Each connected client is served in its own goroutine with a per-connection context
that is cancelled when the client disconnects or the server stops. Handler methods
may be called concurrently; implementations must be safe for concurrent use. Note that
`HandleCoils` and `HandleHoldingRegisters` serve both read and write FCs — check
`IsWrite` and `FunctionCode` to distinguish. `HandleDiscreteInputs` and
`HandleInputRegisters` are read-only; they have no `IsWrite` field. All request
structs — including `MaskWriteRequest` (FC22) and `ReadWriteRegistersRequest`
(FC23) — include a `FunctionCode` field for uniform handling.

---

## Logging

Both `Config` and `ServerConfig` expose a `Logger` field. When
`nil` (the default), logging is disabled (no-op logger). Set a logger explicitly to
enable diagnostic output.

| Constructor | Behaviour |
|---|---|
| `NewStdLogger(l *log.Logger)` | Wraps a stdlib `*log.Logger`; pass `nil` for a default stdout logger |
| `NewSlogLogger(h slog.Handler)` | Wraps any `slog.Handler` (e.g. `slog.NewJSONHandler`, `slog.NewTextHandler`) |
| `NewSlogFieldLogger(h slog.Handler)` | Wraps a `slog.Handler` as a `FieldLogger` with structured key-value support |
| `NopLogger()` | Discards all output — useful in tests |

The `Logger` interface (`Debugf`/`Infof`/`Warnf`/`Errorf`) is straightforward to
implement for any custom logging library (zap, zerolog, logrus, …).

For richer observability, the library also supports `FieldLogger` — an optional extension
that adds structured key-value methods (`With`, `DebugKV`, `InfoKV`, `WarnKV`, `ErrorKV`).
When the logger assigned to `Config.Logger` implements `FieldLogger`, internal log entries
automatically use structured fields (e.g. `"component"` instead of string-prefixed
messages). Use `NewSlogFieldLogger` for an slog-backed implementation.

For trace/span propagation, `ContextLogger` is an optional extension that adds
`DebugContext`/`InfoContext`/`WarnContext`/`ErrorContext` methods accepting a
`context.Context`. `NewSlogFieldLogger` returns a logger that implements all three
interfaces (`Logger`, `FieldLogger`, `ContextLogger`).

**Nil handler safety:** `NewSlogLogger(nil)` and `NewSlogFieldLogger(nil)` return a
no-op logger instead of panicking, making it safe to pass a conditionally-nil handler.

**Debug payload logging:**  When a `Logger` is set and the `Debugf` level is active,
the transport layer logs raw TX/RX frames (e.g. `TX: 01 06 00 00 00 01 …`,
`RX: unit=0x01 fc=0x06 payload=…`). This is invaluable for troubleshooting but
carries two production considerations:

1. **Volume** — at high request rates, debug-level output can generate significant I/O.
   Enable only for short diagnostic windows or targeted unit IDs.
2. **Sensitivity** — frame payloads contain register values and coil states, which may
   represent control setpoints, process data, or configuration. Ensure debug logs are
   not exposed to unauthorized consumers in production environments.

See [API.md § 5](API.md#5-logging) for details and adapter examples.

---

## Error handling

All client methods return a typed `error`. The library uses five error categories:

| Category | Type | Use `errors.Is` / `errors.As` |
|---|---|---|
| **Configuration errors** | `*ConfigurationError` wrapping `ErrConfigurationError` | Invalid `Config` or `ServerConfig` (field, reason) |
| **Parameter errors** | `*ParameterError` wrapping `ErrUnexpectedParameters` | Invalid caller input (method, param, reason) |
| **Protocol errors** | `*ProtocolError` wrapping `ErrProtocolError` | Malformed or unexpected peer response (op, reason) |
| **Exception errors** | `*ExceptionError` wrapping a sentinel | Modbus exception responses (FC, exception code) |
| **Transport errors** | sentinel or stdlib errors | Connection / framing / timeout issues |

`New()` and `NewServer()` return typed `*ConfigurationError` values with `Field` and
`Reason`, making it easy to diagnose misconfiguration without relying on log output.
Use `errors.Is` for broad matching and `errors.As` for detailed diagnostics:

```go
var cfgErr *modbus.ConfigurationError
if errors.As(err, &cfgErr) {
    fmt.Printf("field=%s reason=%s\n", cfgErr.Field, cfgErr.Reason)
}

var paramErr *modbus.ParameterError
if errors.As(err, &paramErr) {
    fmt.Printf("method=%s param=%s reason=%s\n", paramErr.Method, paramErr.Param, paramErr.Reason)
}

var protoErr *modbus.ProtocolError
if errors.As(err, &protoErr) {
    fmt.Printf("op=%s reason=%s\n", protoErr.Op, protoErr.Reason)
}

var excErr *modbus.ExceptionError
if errors.As(err, &excErr) {
    fmt.Printf("fc=0x%02x exception=0x%02x\n", excErr.FunctionCode, excErr.ExceptionCode)
}

// Sentinel matching still works because typed errors wrap the sentinels:
if errors.Is(err, modbus.ErrConfigurationError)    { /* bad config */ }
if errors.Is(err, modbus.ErrUnexpectedParameters) { /* caller input issue */ }
if errors.Is(err, modbus.ErrProtocolError)        { /* peer misbehaved */ }
if errors.Is(err, modbus.ErrIllegalDataAddress)    { /* address does not exist */ }
```

**Config validation helpers:** Use `ValidateConfig(conf)` or
`ValidateServerConfig(conf, handler)` to check configuration without creating a
client or server — useful in CLIs and config-driven systems.

The full sentinel table is in [API.md § 4](API.md#4-errors).

See [API.md § 4](API.md#4-errors) for the full reference.

---

## Advanced features

### Retry policy

Configure automatic retry with exponential back-off on transient transport errors.
The retry classifier uses **positive classification**: only known transient errors are
retried; unknown/unclassified errors are **not** retried, preventing retry storms.

**Retried** (transient transport failures): `io.EOF`, `io.ErrUnexpectedEOF`,
`net.ErrClosed`, `net.Error` (broken pipe, reset, dial transients), and optionally
`ErrRequestTimedOut` (controlled by `RetryOnTimeout`).

**Never retried**: `ErrProtocolError`, `ErrBadCRC`, `ErrShortFrame`,
`ErrBadTransactionID`, `ErrBadUnitID`, `ErrUnknownProtocolID`, `ErrInvalidMBAPLength`,
`ErrUnexpectedParameters`, all Modbus exceptions (`*ExceptionError`),
`context.Canceled`, `context.DeadlineExceeded`, and unknown errors.

The client re-dials the transport between attempts. When pooling is enabled
(`MaxConns > 1`), retries may use a different underlying TCP connection.
Metrics are request-level, not per-attempt.

Set `Config.RetryPolicy` to one of the built-in implementations or
provide a custom `RetryPolicy` implementation. See [API.md § 7](API.md#7-retry-policy).

### Connection pool

Set `MaxConns > 1` to enable a bounded connection pool. Multiple goroutines sharing a
single `*Client` can then execute requests concurrently, each on its own
connection, without serialising on a single TCP socket.

`MinConns` controls how many connections are pre-warmed during `Open()`. Applies to
TCP-based transports only; RTU (serial) always uses a single connection. See
[API.md § 8](API.md#8-connection-pool).

### Concurrency

A `*Client` is safe for concurrent use by multiple goroutines.

- **`MaxConns` ≤ 1** (default, including RTU/serial): requests are serialized over a
  single underlying transport. Multiple goroutines may call client methods simultaneously;
  the library queues and executes them one at a time.
- **`MaxConns` > 1** (TCP-based transports only): requests may execute in parallel,
  each on its own pooled connection.

**Lifecycle guarantees:**

- `Open()` is idempotent — calling it on an already-open client is a no-op.
- `Close()` is safe to call multiple times — subsequent calls are no-ops.
- If `Close()` is called while requests are in flight, those requests may fail with a
  transport error. There is no graceful drain.
- A client can be re-opened after `Close()` by calling `Open()` again.

All server handler goroutines are fully concurrent; handler implementations must
synchronize access to shared state. `*Server` is safe to call `Stop()` or `Shutdown(ctx)`
from any goroutine. Handler panics are recovered with a full stack trace logged; the
client receives a `ServerDeviceFailure` exception.

**Graceful shutdown:** `Shutdown(ctx)` stops accepting connections, cancels all
per-connection contexts, closes sockets, and waits for handlers to exit — or returns
`ctx.Err()` if the context expires first. `Stop()` is equivalent to
`Shutdown(context.Background())` (waits indefinitely).

**Handler validation:** `NewServer()` rejects a nil `reqHandler` at construction time
with a typed `*ConfigurationError`, preventing surprising nil-pointer panics at runtime.

### Metrics hooks

Implement `ClientMetrics` and/or `ServerMetrics` and assign them to the `Metrics`
field of the respective configuration struct. Callbacks (`OnRequest`, `OnResponse`,
`OnError`, `OnTimeout`) fire synchronously on every **logical API outcome** — the
outcome reflects what the calling method actually returns, including protocol-level
validation (exception responses, payload checks, echo mismatches), not just transport
success. Retries are internal and not visible through `ClientMetrics`; must be
non-blocking.

For per-attempt visibility, the `AttemptMetrics` optional interface adds `OnAttempt`
(called after each individual transport attempt) and `OnRetryDial` (called when the
engine re-dials between retries). If `Config.Metrics` also implements `AttemptMetrics`,
these callbacks are invoked automatically. See [API.md § 6](API.md#6-metrics).

### Client diagnostics

`client.Info()` returns a `ClientInfo` snapshot with:

- `IsOpen` — whether the client has an active transport
- `Endpoint` — the resolved target address
- `Transport` — the `TransportKind` (rtu, tcp, tcp+tls, …)
- `PoolEnabled` — whether the connection pool is active
- `MaxConns` — the configured maximum connection count

Safe for concurrent use. Useful for health checks and dashboards.

### Configuration grouping

For larger setups, use `NewConfig(TransportConfig, ExecutionConfig, ObservabilityConfig)`
to build a `Config` from clearly grouped sub-configurations instead of a flat struct
literal:

```go
cfg := modbus.NewConfig(
    modbus.TransportConfig{URL: "tcp://plc:502", DialTimeout: 5 * time.Second},
    modbus.ExecutionConfig{Timeout: 3 * time.Second, MaxConns: 4},
    modbus.ObservabilityConfig{Logger: myLogger, Metrics: myMetrics},
)
```

The flat `Config` struct remains fully supported and backward-compatible.

---

## CLI client

A command-line Modbus client is included in `cmd/modbus-cli/`:

```bash
go build -o modbus-cli ./cmd/modbus-cli/
./modbus-cli --help
```

Usage:

```bash
./modbus-cli 
A modbus command line interface client for quick interaction with modbus
devices (e.g. for probing or troubleshooting).

Operations are given as trailing arguments using a colon-separated DSL.

Available operations:

  rc:<addr>[+qty]                 Read coils
  rdi:<addr>[+qty]                Read discrete inputs
  rh:<type>:<addr>[+qty]          Read holding registers
  ri:<type>:<addr>[+qty]          Read input registers
  wc:<addr>:<true|false>          Write coil
  wr:<type>:<addr>:<value>        Write register
  scan:<target>                   Scan address space
  ping:<count>[:<interval>]       Ping device
  sleep:<duration>                Pause execution
  suid:<id> / sid:<id>            Set unit ID for subsequent operations
  repeat                          Restart all operations from the beginning
  date                            Print current date and time

Register types (rh/ri/wr):
  uint16, int16, uint32, int32, float32, uint64, int64, float64, bytes
  (wr also accepts: string)

Scan targets:
  c/coils, di/discreteInputs, h/hr/holding/holdingRegisters,
  i/ir/input/inputRegisters, s/sid

Supported transports:
  rtu:///path/to/device           Modbus RTU (serial)
  rtuovertcp://host:port          RTU over TCP
  rtuoverudp://host:port          RTU over UDP
  tcp://host:port                 Modbus TCP (MBAP)
  tcp+tls://host:port             Modbus TCP over TLS (requires --cert, --key, --ca)
  udp://host:port                 Modbus TCP over UDP

Register endianness and word order:
  Use --endianness <big|little> (default: big, per Modbus spec).
  For multi-register values (32/64-bit), use --word-order <highfirst|lowfirst>
  (default: highfirst, i.e. most significant word first).

Usage:
  modbus-cli [flags] operation [operation...]
  modbus-cli [command]

Examples:
  # Read 6 uint32 holding registers and 11 coils, then set coil 3
  modbus-cli --target tcp://10.100.0.10:502 rh:uint32:0x100+5 rc:0+10 wc:3:true

  # Serial RTU: read, write, switch unit ID, loop forever
  modbus-cli --target rtu:///dev/ttyUSB0 --speed 19200 \
    suid:2 rh:uint16:0+7 wr:uint16:0x2:0x0605 \
    suid:3 ri:int16:0+1 sleep:1s repeat

  # Scan all register types
  modbus-cli --target tcp://somehost:502 scan:hr scan:ir scan:di scan:coils

  # TLS mutual authentication
  modbus-cli --target tcp+tls://securehost:802 \
    --cert client.cert.pem --key client.key.pem --ca ca.cert.pem \
    rh:uint32:0x3000

  # Ping a device 10 times with 500ms interval
  modbus-cli --target tcp://somehost:502 ping:10:500ms

  # Generate shell completion (bash, zsh, fish, powershell)
  modbus-cli completion bash > /etc/bash_completion.d/modbus-cli
  modbus-cli completion zsh > "${fpath[1]}/_modbus-cli"

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  version     Print build version information

Flags:
      --ca string           TLS CA/server certificate path
      --cert string         TLS client certificate path
      --data-bits uint      number of data bits per character (rtu) (default 8)
      --endianness string   register endianness: big, little (default "big")
      --fail-fast           stop on first operation error
  -h, --help                help for modbus-cli
      --json                output as line-delimited JSON
      --key string          TLS client key path
      --parity string       parity bit: none, even, odd (rtu) (default "none")
      --speed uint          serial bus speed in bps (rtu) (default 19200)
      --stop-bits uint      stop bits: 0 (auto), 1, 2 (rtu)
      --target string       target device to connect to (e.g. tcp://somehost:502)
      --timeout string      request timeout (e.g. 3s, 500ms) (default "3s")
      --unit-id uint        unit/slave ID (0-255) (default 1)
      --word-order string   word order: highfirst|hf, lowfirst|lf (default "highfirst")

Use "modbus-cli [command] --help" for more information about a command.
```

Use `--json` for machine-readable line-delimited JSON output (one JSON object per
result), suitable for piping into `jq` or other tools. Scan and ping operations
also produce structured JSON when `--json` is set.

```bash
./modbus-cli --target tcp://10.0.0.1:502 --json rh:uint16:0x100+3
```

Use `--fail-fast` to stop execution on the first operation error. Without it, the CLI
runs all operations and exits with a non-zero status code if any failed.

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

- [github.com/otfabric/go-serial](https://github.com/otfabric/go-serial) — serial port access for RTU mode

---

## License

MIT.
