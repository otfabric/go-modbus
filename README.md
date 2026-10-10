# go-modbus — Modbus TCP, RTU, ASCII & TLS Library for Go (Golang)

[![Go Version](https://img.shields.io/badge/Go-1.23%2B-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Go Reference](https://pkg.go.dev/badge/github.com/otfabric/go-modbus.svg)](https://pkg.go.dev/github.com/otfabric/go-modbus)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![CI Status](https://github.com/otfabric/go-modbus/actions/workflows/ci.yml/badge.svg)](https://github.com/otfabric/go-modbus/actions/workflows/ci.yml)
[![Interop](https://github.com/otfabric/go-modbus/actions/workflows/interop.yml/badge.svg)](https://github.com/otfabric/go-modbus/actions/workflows/interop.yml)
[![Code Coverage](https://codecov.io/gh/otfabric/go-modbus/graph/badge.svg)](https://codecov.io/gh/otfabric/go-modbus)
[![Latest Release](https://img.shields.io/github/v/release/otfabric/go-modbus?label=release)](https://github.com/otfabric/go-modbus/releases)

**go-modbus is a production-ready Modbus client and server library for Go.** It supports Modbus TCP, Modbus RTU and Modbus ASCII (RS-232/RS-485), Modbus over TLS (MBAPS), UDP, RTU-over-TCP, RTU-over-UDP and ASCII-over-TCP, in pure Go with no CGO.

Use it to talk to PLCs, energy meters, solar inverters (SunSpec), sensors and other industrial devices from SCADA, industrial IoT (IIoT), energy-management and automation software, or to simulate Modbus devices for testing.

Beyond basic register reads and writes, go-modbus adds what production systems need: typed register codecs with explicit byte/word order, connection pooling, automatic retries, `context.Context` cancellation, structured logging, metrics hooks and a CLI.

**Quick links:** [Quick start](#quick-start) · [Comparison](#go-modbus-vs-other-go-modbus-libraries) · [Install](#install) · [API reference](API.md) · [CLI](#modbus-cli) · [FAQ](#faq)

---

## Why go-modbus?

- **Every common transport** — Modbus TCP, RTU and ASCII (serial), TLS (MBAPS / Modbus Security), UDP, RTU-over-TCP, RTU-over-UDP and ASCII-over-TCP.
- **Client and server** — Build Modbus masters (clients) and slaves (servers) with the same library.
- **Broad function-code coverage** — Coils, discrete inputs, holding/input registers, mask write, read/write multiple, FIFO queue, file records, diagnostics and device identification (FC43/14).
- **Typed register codecs** — Read and write `uint16`–`uint64`, `float32/64`, ASCII strings, BCD, IP addresses and timestamps, with per-value byte and word order for vendor-specific register layouts.
- **SunSpec discovery** — Detect SunSpec devices and enumerate model chains (solar inverters, meters, storage).
- **Built for concurrency** — `context.Context` on every call, a bounded connection pool and a `*Client` that is safe for concurrent use.
- **Resilient** — Configurable retry policy with exponential back-off and a typed error taxonomy for `errors.Is` / `errors.As`.
- **Observable** — Structured logging (`log/slog` adapters) and metrics hooks.
- **CLI included** — `modbus-cli` for probing, scanning and troubleshooting devices, with `--json` output.
- **Pure Go** — No CGO or native dependencies.

> For complete type signatures, configuration options and runnable examples see
> **[API.md](API.md)**. Error taxonomy: **[ERRORS.md](ERRORS.md)**. Logging and
> metrics: **[OBSERVABILITY.md](OBSERVABILITY.md)**.

---

## Quick start

Install the library:

```bash
go get github.com/otfabric/go-modbus
```

Read four holding registers from unit 1 over Modbus TCP:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/otfabric/go-modbus"
)

func main() {
	client, err := modbus.NewClient(modbus.NewConfig(
		modbus.TransportConfig{URL: "tcp://192.168.1.10:502", DialTimeout: 5 * time.Second},
		modbus.ExecutionConfig{Timeout: 3 * time.Second},
		modbus.ObservabilityConfig{},
	))
	if err != nil {
		log.Fatal(err)
	}
	if err := client.Open(); err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	regs, err := client.ReadRegisters(ctx, 1, 100, 4, modbus.HoldingRegister)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(regs)
}
```

Prefer the command line? Probe a device without writing any code:

```bash
modbus-cli --target tcp://192.168.1.10:502 rh:uint16:100+4
```

Switching transport is a one-line change to the URL (`rtu:///dev/ttyUSB0`, `tcp+tls://host:802`, `udp://host:502`, …). See [Transport modes](#transport-modes).

---

## go-modbus vs other Go Modbus libraries

Several good Go Modbus libraries exist. This table shows where go-modbus differs from [simonvetter/modbus](https://github.com/simonvetter/modbus) and [goburrow/modbus](https://github.com/goburrow/modbus) so you can pick the right one for your project.

| Capability | otfabric/go-modbus | simonvetter/modbus | goburrow/modbus |
|---|:---:|:---:|:---:|
| Modbus TCP client | ✅ | ✅ | ✅ |
| Modbus RTU client (serial) | ✅ | ✅ | ✅ |
| Modbus ASCII client | ✅ | — | ✅ |
| Modbus TCP server | ✅ | ✅ | — |
| Modbus over TLS (MBAPS) | ✅ | ✅ | — |
| UDP transport | ✅ | ✅ | — |
| RTU-over-TCP / RTU-over-UDP | ✅ | ✅ | — |
| Typed reads/writes (`uint32`, `float32`, …) | ✅ | ✅ | — |
| Per-value byte/word order codecs (BCD, ASCII, time, IP, …) | ✅ | client-wide setting | — |
| Device identification, diagnostics, file records | ✅ | — | — |
| `context.Context` cancellation and deadlines | ✅ | — | — |
| Connection pooling | ✅ | — | — |
| Automatic retries with back-off | ✅ | — | — |
| Structured logging (`log/slog`) | ✅ | stdlib `log` | stdlib `log` |
| Metrics hooks | ✅ | — | — |
| SunSpec discovery | ✅ | — | — |
| CLI tool | ✅ | ✅ | — |
| Pure Go (no CGO) | ✅ | ✅ | ✅ |

✅ = documented feature · — = not documented / not available. Based on each project's public documentation as of October 2026. If you spot an inaccuracy, please [open an issue](https://github.com/otfabric/go-modbus/issues) and we will correct it.

**Choose go-modbus if** you need a Modbus server and client in one library, per-request `context` control, concurrent access to a device, retries, observability, vendor-specific register layouts or SunSpec discovery.

**Choose a smaller library if** you only need a minimal client with a tiny API surface.

<!--
Benchmarks (add once measured; avoid publishing numbers you have not reproduced):

| Scenario | go-modbus | simonvetter/modbus | goburrow/modbus |
|---|---|---|---|
| Sequential FC03 reads, 125 registers (ops/s) | | | |
| Allocations per read (allocs/op, B/op) | | | |
| 32 goroutines, shared client (ops/s) | | | |
| 32 goroutines, MaxConns=8 pool (ops/s) | | | |
| Reconnect after dropped connection (ms) | | | |

Run against the same local server on the same machine; publish the benchmark code under bench/ and state Go version, OS and CPU.
-->

---

## Use cases

- **PLC and SCADA integration** — Poll registers and coils from Modbus TCP/RTU PLCs and gateways.
- **Industrial IoT gateways** — Bridge Modbus devices to MQTT, databases or cloud platforms with pooled, cancellable, retried requests.
- **Energy and solar monitoring** — Read meters and inverters, and discover SunSpec model chains.
- **Device simulation and testing** — Run a Modbus TCP or TLS server in Go to simulate devices in unit and CI tests.
- **Field diagnostics** — Scan address spaces, ping devices and decode registers with `modbus-cli`.

---

## Table of Contents

- [Why go-modbus?](#why-go-modbus)
- [Quick start](#quick-start)
- [go-modbus vs other Go Modbus libraries](#go-modbus-vs-other-go-modbus-libraries)
- [Use cases](#use-cases)
- [Install](#install)
- [API tiers](#api-tiers)
- [Transport modes](#transport-modes)
- [Client](#client)
- [Server](#server)
- [Logging](#logging)
- [Error handling](#error-handling)
- [Advanced features](#advanced-features)
- [modbus-cli](#modbus-cli)
- [Examples](#examples)
- [Interop tests](#interop-tests)
- [FAQ](#faq)
- [Project structure](#project-structure)
- [Contributing](#contributing)
- [Dependencies](#dependencies)
- [License](#license)

---

## Install

```bash
go get github.com/otfabric/go-modbus
```

Requires **Go 1.23** or later.

---

## API tiers

The library is organized into five distinct API tiers, from lowest to highest level:

| Tier | Package | Purpose | Typical user |
|------|---------|---------|-------------|
| **1. Raw Modbus** | `modbus` | Direct function-code methods (`ReadCoils`, `WriteRegisters`, …) returning native Go types. Full control over unit IDs, addresses, and quantities. | Integrators who know their device's register map |
| **2. Typed Codec** | `modbus/codec` | Layout-aware encode/decode of multi-register values (`Uint32Codec`, `Float64Codec`, `AsciiCodec`, time codecs, …). Compile-time generics and runtime codec support for descriptor-driven workflows. | Applications that need typed register access |
| **3. SunSpec** | `modbus/sunspec` | SunSpec marker detection, model-chain enumeration, and device fingerprinting. Transport-level only — no point/schema decoding. | Solar/energy system integrators |
| **4. Server** | `modbus` | `RequestHandler`-based Modbus TCP/TLS server with per-connection contexts, panic recovery, optional `MaskWriteHandler`, `ReadWriteHandler`, and `DeviceIdentificationHandler` interfaces for FC22/FC23/FC43. | Simulating or embedding Modbus devices |
| **5. CLI** | `cmd/modbus-cli` | Command-line client for read/write, scanning, pinging, and codec probing. Supports `--json` for structured output. | Operators and CI/CD pipelines |

Each tier builds on the one below. For example, the Codec API uses the Raw Modbus API
internally; the CLI uses the Codec and Raw APIs. Pick the tier that matches your
use case — you never need to use a higher tier.

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
| `ascii://<device>` | Modbus ASCII over serial (RS-232 / RS-485) | ✓ | — |
| `asciiovertcp://<host:port>` | Modbus ASCII framing tunnelled over TCP | ✓ | — |

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
| `Speed`, `DataBits`, `Parity`, `StopBits` | Serial (`rtu://`, `ascii://`) only. Defaults: 19200 bps, 8 data bits (RTU) or 7 (ASCII), no parity, 2 stop bits without parity or 1 with |
| `DialTimeout` | TCP, TCP+TLS, UDP, RTU-over-TCP/UDP, ASCII-over-TCP — not serial |
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

**Device detection:** `SupportsFunction(ctx, unitID, fc)` checks a single read-style FC (FC08, FC43, FC03, FC04, FC01, FC02, FC11, FC18, FC20). Returns `(true, nil)` for a valid normal response or an exception that shows the function was recognised (for example Illegal Data Address), `(false, nil)` for probe-negative outcomes (timeout, Illegal Function, gateway failure), and `(false, err)` for real transport errors. For richer diagnostics, `ProbeFunction(ctx, unitID, fc)` returns a `ProbeResult` with `Outcome` (supported/exception/timeout/transport error/validation failed), optional `ExceptionCode`, `ResponseFC`, `RawPayload`, and `Reason` — useful for discovery tools and field debugging of quirky devices. `SupportsDeviceIdentification(ctx, unitID)` checks FC43 (Read Device Identification). **SunSpec discovery** lives in the `sunspec` subpackage: `sunspec.DetectSunSpec`, `sunspec.ReadSunSpecModelHeaders`, and `sunspec.DiscoverSunSpec` probe for the SunSpec "SunS" marker, enumerate model chains, and combine both for fingerprinting and inventory. The library does not decode SunSpec points or schemas — only transport-level detection and model headers. See [API.md § 2.7](API.md#27-modbus-device-detection) and [API.md § 2.8](API.md#28-sunspec-discovery).

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
| 43/14 | 0x2B/0x0E | Read Device Identification | `DeviceIdentificationHandler` (optional) | — |

FC07, FC11, FC12, FC22, FC23 and FC43 use optional handler interfaces. If the `RequestHandler`
also implements the corresponding interface (e.g. `ExceptionStatusHandler`,
`CommEventCounterHandler`, `CommEventLogHandler`, `MaskWriteHandler`, `ReadWriteHandler`,
`DeviceIdentificationHandler`), those FCs are dispatched accordingly; otherwise they return
`Illegal Function`.

For FC43 (Read Device Identification), the handler returns the full set of
`DeviceIdentificationObject` values it implements plus a conformity level; the server owns
all MEI framing, category filtering (basic/regular/extended/individual), and
MoreFollows/NextObjectID pagination automatically. Only MEI type 14 (0x0E) is supported;
other MEI types (e.g. CANopen 0x0D) return `Illegal Function`.

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

Both `Config` and `ServerConfig` expose a `Logger` field. When `nil` (the default),
logging is disabled. Use `NewStdLogger`, `NewSlogLogger`, `NewSlogFieldLogger`, or
`NopLogger()` to enable output. Optional `FieldLogger` / `ContextLogger` extensions
add structured fields and context-aware methods.

Debug-level transport logging can include raw TX/RX frame payloads — useful for
troubleshooting, but sensitive and high-volume in production.

See [OBSERVABILITY.md](OBSERVABILITY.md) and [API.md § 5](API.md#5-logging) for
constructors, adapters, and examples.

---

## Error handling

All client methods return a typed `error`. Distinguish **library/transport**
failures from well-formed peer **Modbus exceptions** (`*ExceptionError`). Use
`errors.Is` / `errors.As` against the five categories (configuration, parameter,
protocol, exception, transport).

See **[ERRORS.md](ERRORS.md)** for the quick reference and
[API.md § 4](API.md#4-errors) for the full sentinel and typed-error tables.

---

## Advanced features

### Retry policy

Configure automatic retry with exponential back-off on transient transport errors
(`Config.RetryPolicy`; nil / `NoRetry()` is the default). The classifier uses
**positive classification**: only known transient transport errors are retried.

Retries apply equally to read and write function codes. If a request may already
have been written to the wire and the response is lost, a retry can deliver a
write **at least once**. Prefer no retries (or application-level idempotency) for
non-idempotent writes.

See [API.md § 7](API.md#7-retry-policy) for classification tables, built-in
policies, and write-safety details.

### Connection pool

Set `MaxConns > 1` to enable a bounded connection pool so concurrent goroutines
can share a `*Client` without serialising on one TCP socket. `MinConns`
pre-warms connections during `Open()`. TCP-based transports only; RTU always
uses a single connection. See [API.md § 8](API.md#8-connection-pool).

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

Implement `ClientMetrics` and/or `ServerMetrics` on the `Metrics` config field.
Callbacks fire synchronously on every **logical API outcome** (what the calling
method returns), not per internal retry. Optional `AttemptMetrics` adds
per-attempt / re-dial visibility. Callbacks must be non-blocking.

See [OBSERVABILITY.md](OBSERVABILITY.md) and [API.md § 6](API.md#6-metrics)
for interfaces and examples.

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

## modbus-cli

A command-line Modbus client is included in `cmd/modbus-cli/`:

```bash
go build -o modbus-cli ./cmd/modbus-cli/
./modbus-cli --help
```

<details>
<summary>Full <code>modbus-cli</code> reference: operations, flags and examples</summary>

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
  ascii:///path/to/device         Modbus ASCII (serial)
  asciiovertcp://host:port        ASCII over TCP
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

</details>


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

## Interop tests

The `interop` package tests go-modbus against five independent Modbus TCP
implementations, [libmodbus](https://github.com/stephane/libmodbus) (C),
[PyModbus](https://github.com/pymodbus-dev/pymodbus) (Python),
[digitalpetri/modbus](https://github.com/digitalpetri/modbus) (Java),
[NModbus](https://github.com/NModbus/NModbus) (C#) and
[tokio-modbus](https://github.com/slowtec/tokio-modbus) (Rust), packaged as
container images by [otfabric/modbus-interop](https://github.com/otfabric/modbus-interop).

```sh
make interop    # needs Docker; about five minutes
```

| Direction | What is checked |
|-----------|-----------------|
| go-modbus client → reference servers | FC01 to FC04 over every table of the reference device, at the block boundaries and with the largest request of each function, value by value; FC05, FC06, FC15, FC16, FC22 and FC23 read back on a second connection, together with everything that must not have changed; FC43/14 in every category; exceptions 1, 2 and 11; two thousand requests on one connection; sixteen connections at once and a pooled client |
| reference clients → go-modbus server | 92 operations per client against a go-modbus device that serves the same fixture: every operation the client has, up to 1000 requests on one connection, raw PDUs, exceptions 1, 2, 3 and 11. Each must have the outcome the specification prescribes, a write must leave the device in exactly the expected state, and the client's result document must equal, field by field, what it gets from a reference server |

The images are the only thing shared with modbus-interop; nothing of it is
cloned or built here. Scenarios, expected values and assertions live in this
repository.

This version is qualified against modbus-interop v0.1.0 (libmodbus v3.2.0,
PyModbus 3.15.0, digitalpetri/modbus 2.1.6, NModbus 3.0.83, tokio-modbus
0.17.0). The five images are named in `interop/harness.go`, the Makefile and
the Interop workflow, which runs on every push and pull request. To test
against another build, set `MODBUS_INTEROP_<ADAPTER>_IMAGE` (`LIBMODBUS`,
`PYMODBUS`, `DIGITALPETRI`, `NMODBUS`, `TOKIOMODBUS`); to run some stacks
only, `MODBUS_INTEROP_ADAPTERS=libmodbus,pymodbus`.

The references do not all support the same. The suite asks each image what
it declares (`print-capabilities`) and skips, rather than fails, what an
image lacks:

| | libmodbus | PyModbus | digitalpetri | NModbus | tokio-modbus |
|---|:---:|:---:|:---:|:---:|:---:|
| FC01 to FC06, FC15, FC16, FC23, exceptions, both directions | ✅ | ✅ | ✅ | ✅ | ✅ |
| FC22, both directions | ✅ | ✅ | ✅ | — no FC22 | ✅ |
| FC43/14, reference as server | — no FC43 | ✅ | — no FC43 | — no FC43 | ✅ |
| FC43/14, reference as client | — | ✅ | raw PDU only | ✅ | ✅ |

What is implemented, how the server answers invalid requests and what the
suite found is in [INTEROPERABILITY.md](INTEROPERABILITY.md).

---

## FAQ

### How do I read Modbus registers in Go?

Create a client with a `tcp://`, `rtu://` or other transport URL, call `Open()`, then use `ReadRegisters` (raw `[]uint16`) or a typed codec from the `codec` package. See the [Quick start](#quick-start) and [Client](#client).

### Does go-modbus support Modbus TCP, RTU, ASCII and TLS?

Yes. Modbus TCP, serial RTU and ASCII (RS-232/RS-485), Modbus TCP over TLS (MBAPS), UDP, RTU-over-TCP, RTU-over-UDP and ASCII-over-TCP are supported by the client. The server supports TCP and TLS. See [Transport modes](#transport-modes).

### Can I run a Modbus server in Go for testing or simulation?

Yes. Implement the `RequestHandler` interface and start a TCP or TLS server. Runnable samples are in [Examples](#examples).

### How do I handle byte order and word order (endianness)?

Layout is defined per value by the codec, for example `codec.NewUint32Codec(codec.Layout32_2143)` for CDAB word order. This lets one client read devices and registers that use different layouts. See [Byte order and layout](#byte-order-and-layout).

### Does go-modbus support SunSpec solar inverters?

go-modbus detects the SunSpec marker and enumerates model chains for fingerprinting and inventory. It does not decode SunSpec points or schemas. See the `sunspec` package.

### Is the client safe for concurrent use?

Yes. By default requests are serialized over one connection. Set `MaxConns > 1` on TCP-based transports to run requests in parallel over a bounded connection pool. See [Concurrency](#concurrency).

### Should I enable retries for writes?

Be careful. If a write reaches the device but the response is lost, a retry can deliver the write more than once. Prefer no retries, or application-level idempotency, for non-idempotent writes. See [Retry policy](#retry-policy).

### Does it need CGO?

No. go-modbus is pure Go. Serial access uses [otfabric/go-serial](https://github.com/otfabric/go-serial).

---

## Project structure

<details>
<summary>Repository layout and package ownership</summary>

```
go-modbus/
├── .                  Public API — client, server, config, retry, metrics, errors
├── codec/             Typed encode/decode for multi-register values
├── sunspec/           SunSpec marker detection and model-chain discovery
├── internal/
│   ├── adu/           ADU framing (MBAP, RTU CRC, ASCII LRC, wire encoding)
│   ├── transport/     TCP / RTU / ASCII / UDP transports
│   ├── session/       Execution engine (pool, retry, dispatch)
│   ├── protocol/      Function codes, limits, shared sentinels
│   └── logging/       Prefixed logger adapter
├── cmd/modbus-cli/    Command-line client
├── interop/           Tests against libmodbus, PyModbus, digitalpetri, NModbus and
│                      tokio-modbus (build tag "interop", needs Docker)
├── examples/          Runnable TCP/TLS server and client samples
├── spec/              Protocol notes / reference material
├── testdata/          Fuzz corpora and test fixtures
├── API.md             Full public API reference
├── ARCHITECTURE.md    Package ownership and dependency rules
├── ERRORS.md          Error taxonomy
├── INTEROPERABILITY.md  Protocol coverage and verification against other stacks
├── OBSERVABILITY.md   Logging and metrics
├── CODECS.md          Codec design notes
└── RELEASE.md         Release history
```

Root package files are split by concern (`client_*.go`, `server_*.go`, …). Ownership
and import rules are in [ARCHITECTURE.md](ARCHITECTURE.md).

</details>

---

## Contributing

Bug reports, device-compatibility notes and pull requests are welcome. Please [open an issue](https://github.com/otfabric/go-modbus/issues) to discuss larger changes first. Package ownership and import rules are in [ARCHITECTURE.md](ARCHITECTURE.md). See [CONTRIBUTING.md](CONTRIBUTING.md) for the development setup and guidelines.

Please report security vulnerabilities privately, as described in [SECURITY.md](SECURITY.md), not in a public issue.

If go-modbus is useful to you, a star on GitHub helps other Go developers find it.

---

## Dependencies

- [github.com/otfabric/go-serial](https://github.com/otfabric/go-serial) — serial port access for RTU and ASCII modes

---

## License

This project is licensed under the MIT License. See [LICENSE](./LICENSE).