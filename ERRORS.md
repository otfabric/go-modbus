# Errors: otfabric/go-modbus

How go-modbus reports failures. Full sentinel lists and typed-error fields:
[API.md § 4](API.md#4-errors). Codec errors: [API.md § 4 Codec errors](API.md#codec-errors).

Modbus has two related but distinct failure surfaces, both returned as Go `error`:

1. **Library / transport errors** — configuration, parameters, framing, timeouts,
   connection failures (`*ConfigurationError`, `*ParameterError`, `*ProtocolError`,
   CRC/short-frame sentinels, `net`/`io` errors, …).
2. **Wire Modbus exceptions** — the peer answered with an exception PDU
   (`*ExceptionError` wrapping `ErrIllegalFunction`, `ErrIllegalDataAddress`, …).

Do not treat a protocol framing error the same as a device exception: the former
usually means a bad or unexpected response on the link; the latter is a well-formed
Modbus exception from the unit.

## Quick reference

| Situation | How it is reported |
|-----------|--------------------|
| Bad `Config` / `ServerConfig` | `*ConfigurationError` (`errors.Is` → `ErrConfigurationError`) |
| Invalid method arguments | `*ParameterError` (`errors.Is` → `ErrUnexpectedParameters`) |
| Request before `Open` / after `Close`, or interrupted by `Close` | `ErrClientNotOpen` (never while the client is open) |
| Function code not in `Config.AllowedFunctionCodes` | error wrapping `ErrFunctionCodeNotAllowed` (`errors.Is`); nothing is sent, the client stays usable |
| Configured timeout, or context deadline expiring in flight | `ErrRequestTimedOut` |
| Context already cancelled or expired when the call starts | `ctx.Err()` (`context.Canceled` / `context.DeadlineExceeded`); nothing is sent |
| Context cancelled while the request is in flight | `context.Canceled` (`errors.Is`); returns promptly |
| Connection lost, refused or reset | stdlib error (`io.EOF`, `*net.OpError`, …); the next request reconnects |
| Malformed / unexpected peer response | `*ProtocolError` (`errors.Is` → `ErrProtocolError`) |
| RTU CRC / ASCII LRC / short frame | `ErrBadCRC`, `ErrBadLRC`, `ErrShortFrame` |
| Peer Modbus exception (0x01–0x0B) | `*ExceptionError` (`errors.Is` → matching `ErrIllegal…` sentinel) |
| Codec encode/decode failure | `codec` package sentinels / typed errors |
| `batch.Compile` / `Decode` given invalid items or data | `*batch.ValidationError` |
| `batch` `Plan.Execute` that did not read every window | result **and** `*batch.ExecError` (`errors.Is` → `batch.ErrIncomplete`, the first window error, and the reason the batch stopped) |

Prefer:

```go
import (
    "errors"

    "github.com/otfabric/go-modbus"
)

_, err := client.ReadRegisters(ctx, 1, 0x9000, 10, modbus.HoldingRegister)
if err != nil {
    var exc *modbus.ExceptionError
    if errors.As(err, &exc) {
        // well-formed exception from the device
        _ = exc.FunctionCode
        _ = exc.ExceptionCode
    }
    if errors.Is(err, modbus.ErrIllegalDataAddress) {
        // address does not exist (works through *ExceptionError)
    }
    if errors.Is(err, modbus.ErrProtocolError) {
        // peer misbehaved / unexpected framing
    }
    if errors.Is(err, modbus.ErrRequestTimedOut) {
        // transport timeout
    }
    return err
}
```

## Sentinels

Every exported sentinel of the module. All match through `errors.Is`, including
through a typed error or a wrapping `fmt.Errorf`.

### Exception sentinels

The peer answered with an exception PDU; `*ExceptionError` wraps the sentinel of
the code. A `RequestHandler` returns the same sentinels to make the server answer
with that exception.

| Sentinel | Code | Meaning |
|---|---|---|
| `ErrIllegalFunction` | 0x01 | The unit does not serve this function code |
| `ErrIllegalDataAddress` | 0x02 | The address range does not exist on the unit |
| `ErrIllegalDataValue` | 0x03 | A value in the request is not acceptable. The server also answers with it when a request's quantity or byte count is out of range |
| `ErrServerDeviceFailure` | 0x04 | The unit failed while performing the action. The server answers with it when a handler returns an unmapped error or a response that does not fit |
| `ErrAcknowledge` | 0x05 | The unit accepted a long-running request and is still processing it |
| `ErrServerDeviceBusy` | 0x06 | The unit is busy; send the request again later |
| `ErrMemoryParityError` | 0x08 | The unit found a parity error reading extended file memory |
| `ErrGWPathUnavailable` | 0x0A | A gateway has no path to the target unit |
| `ErrGWTargetFailedToRespond` | 0x0B | A gateway got no answer from the target unit. `Probe*` treats it like a timeout |

### Framing and response validation

These mean the response on the link could not be trusted. None is retried by the
built-in retry policies. `ErrBadCRC`, `ErrBadLRC`, `ErrShortFrame` and
`ErrProtocolError` are described in the quick reference above.

| Sentinel | Meaning |
|---|---|
| `ErrBadUnitID` | The response carries a different unit ID from the request |
| `ErrBadTransactionID` | MBAP responses kept arriving with the wrong transaction ID. Single mismatches are logged and skipped; this is returned when mismatches and unknown protocol IDs together reach the anomaly limit of the request |
| `ErrUnknownProtocolID` | MBAP frames kept arriving with a protocol ID other than 0, up to the same limit |
| `ErrInvalidMBAPLength` | A received MBAP length field is outside the valid range, or a request's payload would not fit an MBAP length |

### Transport and server

| Sentinel | Meaning |
|---|---|
| `ErrSerialPortNotOpen` | A read or write on a serial port that was never opened or was closed. The client treats it as a lost link |
| `ErrNoResponse` | Returned **by a `RequestHandler`**, not by the client: the server sends nothing and keeps the connection, so the client runs into its timeout. It models a silent device |

### SunSpec discovery

| Sentinel | Meaning |
|---|---|
| `ErrSunSpecModelChainInvalid` | The chain cannot exist: a model runs past register 0xFFFF, leaves no room for the next header, or has length 0 without being the end model. Models read before the fault are returned with it |
| `ErrSunSpecModelChainLimitExceeded` | The chain passed `MaxAddressSpan`. Models read so far are returned with it |

### Package `codec`

| Sentinel | Meaning |
|---|---|
| `codec.ErrCodecRegisterCount` | The register slice has the wrong length for the codec (also unwrapped from `*codec.CodecRegisterCountError`). A batch decode item whose window is too short reports it too |
| `codec.ErrCodecLayout` | The layout is not valid for the codec, or a descriptor has none (`*codec.CodecLayoutError`) |
| `codec.ErrCodecValue` | The value cannot be encoded or decoded, or a codec parameter is out of range (`*codec.CodecValueError`) |
| `codec.ErrCodecByteCount` | The byte slice has the wrong length for the codec (`*codec.CodecByteCountError`) |
| `codec.ErrUnknownCodec` | The registry has no codec with this ID, or a decimal limb order in the ID is not known |
| `codec.ErrEncodingError` | The input to a byte permutation has the wrong length |
| `codec.ErrInvalidLayout` | `NewRegisterLayout` got a register count outside 1..4, the wrong number of positions, or an out-of-range or repeated position |

## Categories

| Category | Type | Match with |
|----------|------|------------|
| Configuration | `*ConfigurationError` | `errors.Is(…, ErrConfigurationError)` / `errors.As` |
| Parameters | `*ParameterError` | `errors.Is(…, ErrUnexpectedParameters)` / `errors.As` |
| Protocol | `*ProtocolError` | `errors.Is(…, ErrProtocolError)` / `errors.As` |
| Exception | `*ExceptionError` | `errors.Is(…, ErrIllegalDataAddress)` etc. / `errors.As` |
| Transport / framing | sentinels or stdlib | `errors.Is` / plain `error` |

`ValidateConfig` / `ValidateServerConfig` run the same config checks without
creating a client or server.

## Errors and the connection

On `tcp://` and `tcp+tls://` a timeout, a cancelled request, a framing or
protocol error and a transport error all end the connection the request ran on:
it is closed, and the next request dials a new one (a pooled connection is
discarded). A Modbus exception does not. The client itself stays open through
all of them; only `Close` makes it return `ErrClientNotOpen`. The serial-framed
transports and `udp` keep their link unless it is down. See
[API.md § 2.2](API.md#22-lifecycle).

## Batch execution

`batch` `Plan.Execute` sorts window errors by what they say about the link. A
Modbus exception, or a function code `Config.AllowedFunctionCodes` refuses, fails
only its window. A timeout, transport error or protocol error ends the batch: the
remaining windows are `NotAttempted`, with an error wrapping
`batch.ErrNotAttempted` and that error (`Options.ContinueOnTransportError` reads
them anyway). A cancelled or expired context always ends the batch. Every window
and item keeps its own `State` and `Err`; see
[API.md § 12](API.md#12-batch-reads-package-batch).

## Retries

Built-in retry policies classify by error only. Protocol errors, parameter errors,
and Modbus exceptions are **not** retried. Transient transport failures may be.
See [API.md § 7](API.md#7-retry-policy) (including write at-least-once semantics).
