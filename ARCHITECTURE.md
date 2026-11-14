# Architecture

This document describes the package structure of the modbus library
and the ownership rules that govern it.

## Package layout

```
github.com/otfabric/go-modbus          # public API — client, server, protocol types
├── batch/                           # public batch reads (planning, sequential execution, results)
├── codec/                           # public codec package (numeric, text, time, …)
├── sunspec/                         # public SunSpec detection and discovery
├── internal/
│   ├── adu/                         # ADU framing: MBAP, RTU CRC, ASCII LRC, wire encoding
│   ├── transport/                   # concrete transports: TCP, RTU, ASCII
│   ├── session/                     # execution engine: pool, retry, request dispatch
│   ├── protocol/                    # protocol constants, function codes, error sentinels
│   ├── benchserver/                 # benchmark server: delay per request, request and byte counts
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

modbus (root), codec ← batch
modbus (root)       ← internal/benchserver (benchmarks only)

internal/protocol ← modbus, codec, sunspec
internal/adu      ← codec, sunspec (byte helpers)
internal/logging  ← modbus, internal/session
```

**Forbidden imports:**

- `internal/transport` must not import `codec`
- `sunspec` must not import `internal/transport`
- `codec` must not import `internal/transport` or `internal/session`
- The root package must not import `batch` or `internal/benchserver`; `batch` uses the public API only

## Ownership rules

Each subsystem has a **single owner** package.

| Subsystem | Owner | Notes |
|---|---|---|
| ADU framing & wire encoding | `internal/adu` | Leaf package — no internal deps |
| TCP / RTU / ASCII transports | `internal/transport` | |
| Connection pool, retry, execute | `internal/session` | |
| Codecs (numeric, text, time, …) | `codec/` | Public subpackage. The root package imports it for `RuntimeDecodePlan` and has no aliases of its types |
| SunSpec discovery | `sunspec/` | Public subpackage. The root package does not import it; it only re-exports the two SunSpec error sentinels from `internal/protocol` |
| Batch-read planning | `batch/` | Public subpackage; plans, executes sequentially and decodes; no scheduling, caching or goroutines |
| Protocol constants & errors | `internal/protocol` | Root re-exports via type aliases |
| Logging | `internal/logging` | |
| Client/server protocol API | `modbus` (root) | Uses `adu.Request`/`adu.Response` internally |

## Client architecture

`Client` is split across focused files:

| File | Concern |
|---|---|
| `client.go` | Struct, config, lifecycle (Open/Close), dialling (`Config.Dialer` or `net.Dialer`, TLS handshake) |
| `client_exec.go` | Request execution, metrics |
| `client_bits.go` | Coil and discrete input operations |
| `client_registers.go` | Register read/write operations |
| `client_device_id.go` | Device identification (FC43) |
| `client_diagnostics.go` | Diagnostics (FC08) |
| `client_file.go` | File records (FC20/FC21) |

The server is split the same way:

| File | Concern |
|---|---|
| `server.go` | Struct, config, lifecycle, the handler interfaces for the data model and FC07/0B/0C |
| `server_transport.go` | Per-connection loop, dispatch by function code, exceptions, metrics |
| `server_bits.go`, `server_registers.go` | FC01–06, FC15/16, FC22/23 |
| `server_device_id.go` | Device identification (FC43) |
| `server_functions.go` | FC08, FC17, FC20/21 and FC24 |

Every function code the client implements has a server handler; the e2e suite
enforces it (`TestE2E_EveryClientFunctionIsServed`).

### Locking model

The client mutex guards **only mutable state** (`clientState`: `engine`, which is nil
while the client is not open, and `lastResponseTransactionID`), not the entire request
lifecycle. `executeRequest` is self-locking: it snapshots the engine under the lock,
executes without holding it, and updates state under the lock on return.
`session.Engine` is the sole authority for request-level concurrency. No lock and no
turn is held while a `PDUTap` or a `ClientMetrics` outcome hook runs; `AttemptMetrics`
hooks run while the request holds its connection.

On the server, `Server.lock` guards `started`, the connection lists and the fields of
the current run; it is never held while a handler or a `ServerMetrics` hook runs. Each
`Start` creates a `serverRun` (stop context, listener, WaitGroup) that the accept loop
and the connection goroutines of that cycle are counted in; `Shutdown` waits for that
WaitGroup, which is why it cannot be waited for from a handler or a hook.

### Request/response flow

Client methods build `adu.Request` structs directly (no intermediate `pdu` type).
The `session.Engine` executes them on the transport and returns `adu.Response`.

### Dialling

`session.Config.Dial` takes a context. `Open` passes `context.Background()`;
a re-dial during a request (single connection or pool) passes the request's
context. `Client.dialSessionTransport` bounds it by the dial timeout and opens
the network connection through `Config.Dialer` (or `net.Dialer`); `tcp+tls`
then runs `tls.Client(...).HandshakeContext` under the same deadline. Serial
transports open their device and ignore the context.

## Codec architecture

All codec implementations live in the public `codec/` subpackage:

```go
import "github.com/otfabric/go-modbus/codec"

c, _ := codec.NewFloat32Codec(codec.Layout32_2143)
v, err := codec.DecodeRegisters(regs, c)
```

The root `modbus` package has no aliases of the codec types: import `codec` for them.

## SunSpec architecture

SunSpec detection and discovery live in the public `sunspec/` subpackage:

```go
import "github.com/otfabric/go-modbus/sunspec"

result, err := sunspec.Detect(ctx, reader, &sunspec.Options{...})
```

`sunspec` reads through its one-method `Reader` interface. `*modbus.Client` does not
implement it: an application adapts the client in a few lines, as
`examples/sunspec_detect` and [API.md § 2.8](API.md#28-sunspec-discovery) show.
