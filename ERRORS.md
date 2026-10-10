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
| Configured timeout, or context deadline expiring in flight | `ErrRequestTimedOut` |
| Context already cancelled or expired when the call starts | `ctx.Err()` (`context.Canceled` / `context.DeadlineExceeded`); nothing is sent |
| Context cancelled while the request is in flight | `context.Canceled` (`errors.Is`); returns promptly |
| Connection lost, refused or reset | stdlib error (`io.EOF`, `*net.OpError`, …); the next request reconnects |
| Malformed / unexpected peer response | `*ProtocolError` (`errors.Is` → `ErrProtocolError`) |
| RTU CRC / ASCII LRC / short frame | `ErrBadCRC`, `ErrBadLRC`, `ErrShortFrame` |
| Peer Modbus exception (0x01–0x0B) | `*ExceptionError` (`errors.Is` → matching `ErrIllegal…` sentinel) |
| Codec encode/decode failure | `codec` package sentinels / typed errors |

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

## Retries

Built-in retry policies classify by error only. Protocol errors, parameter errors,
and Modbus exceptions are **not** retried. Transient transport failures may be.
See [API.md § 7](API.md#7-retry-policy) (including write at-least-once semantics).
