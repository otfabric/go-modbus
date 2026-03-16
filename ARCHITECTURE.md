# Architecture

This document describes the package structure of the modbus library
and the ownership rules that govern it.

## Package layout

```
github.com/otfabric/modbus          # public API — client, server, protocol types
├── codec/                           # public codec package (numeric, text, time, …)
├── sunspec/                         # public SunSpec detection and discovery
├── internal/
│   ├── adu/                         # ADU framing: MBAP, RTU CRC, wire encoding
│   ├── transport/                   # concrete transports: TCP, RTU
│   ├── session/                     # execution engine: pool, retry, request dispatch
│   ├── protocol/                    # protocol constants, function codes, error sentinels
│   └── logging/                     # prefixed logger adapter
├── cmd/                             # CLI client (main, parse, ops, render, scan)
└── examples/
```

## Dependency graph

```
internal/adu
    ↑
internal/transport
    ↑
internal/session
    ↑
modbus (root)

internal/protocol ← modbus, codec, sunspec
internal/adu      ← codec, sunspec (byte helpers)
internal/logging  ← modbus, internal/session
```

**Forbidden imports:**

- `internal/transport` must not import `codec`
- `sunspec` must not import `internal/transport`
- `codec` must not import `internal/transport` or `internal/session`

## Ownership rules

Each subsystem has a **single owner** package.

| Subsystem | Owner | Notes |
|---|---|---|
| ADU framing & wire encoding | `internal/adu` | Leaf package — no internal deps |
| TCP / RTU transports | `internal/transport` | |
| Connection pool, retry, execute | `internal/session` | |
| Codecs (numeric, text, time, …) | `codec/` | Public subpackage; root has deprecated aliases |
| SunSpec discovery | `sunspec/` | Public subpackage; root has deprecated aliases |
| Protocol constants & errors | `internal/protocol` | Root re-exports via type aliases |
| Logging | `internal/logging` | |
| Client/server protocol API | `modbus` (root) | Uses `adu.Request`/`adu.Response` internally |

## Client architecture

`ModbusClient` is split across focused files:

| File | Concern |
|---|---|
| `client.go` | Struct, config, lifecycle (Open/Close) |
| `client_exec.go` | Request execution, metrics |
| `client_bits.go` | Coil and discrete input operations |
| `client_registers.go` | Register read/write operations |
| `client_device_id.go` | Device identification (FC43) |
| `client_diagnostics.go` | Diagnostics (FC08) |
| `client_file.go` | File records (FC20/FC21) |

### Locking model

The client mutex guards **only mutable state** (`isOpen`, `engine`, `lastResponseTransactionID`),
not the entire request lifecycle. `executeRequest` is self-locking: it snapshots the engine
under the lock, executes without holding it, and updates state under the lock on return.
`session.Engine` is the sole authority for request-level concurrency.

### Request/response flow

Client methods build `adu.Request` structs directly (no intermediate `pdu` type).
The `session.Engine` executes them on the transport and returns `adu.Response`.

## Codec architecture

All codec implementations live in the public `codec/` subpackage:

```go
import "github.com/otfabric/modbus/codec"

c, _ := codec.NewFloat32Codec(codec.Layout32_2143)
v, err := codec.DecodeRegisters(regs, c)
```

The root `modbus` package retains deprecated aliases for backward compatibility.
These will be removed in a future release.

## SunSpec architecture

SunSpec detection and discovery live in the public `sunspec/` subpackage:

```go
import "github.com/otfabric/modbus/sunspec"

result, err := sunspec.Detect(ctx, reader, &sunspec.Options{...})
```

The root `ModbusClient` provides thin convenience methods that adapt to `sunspec.Reader`.
