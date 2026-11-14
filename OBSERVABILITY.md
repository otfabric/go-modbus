# Observability: otfabric/go-modbus

Logging and metrics for the Modbus client and server.

Constructors, interfaces, and examples: [API.md § 5](API.md#5-logging) (logging),
[API.md § 6](API.md#6-metrics) (metrics).

## Logging (silent by default)

Both `Config.Logger` and `ServerConfig.Logger` are **nil by default** — logging is
disabled (no-op). Nothing is written unless you pass a logger explicitly.

```go
client, err := modbus.New(modbus.Config{
    URL:    "tcp://plc:502",
    Logger: modbus.NewSlogLogger(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
        Level: slog.LevelDebug,
    })),
})
```

Built-in constructors: `NewStdLogger`, `NewSlogLogger`, `NewSlogFieldLogger`,
`NopLogger()`. Optional extensions:

- **`FieldLogger`** — structured key-value methods (`With`, `DebugKV`, …)
- **`ContextLogger`** — context-aware methods for trace/span propagation

`NewSlogFieldLogger` implements `Logger`, `FieldLogger`, and `ContextLogger`.
`NewSlogLogger(nil)` / `NewSlogFieldLogger(nil)` return a no-op logger (no panic).

### Debug frame payloads

When a logger is set and debug is enabled, the transport may log raw TX/RX frames.
Useful for troubleshooting; high volume and potentially sensitive (register/coil
values). Prefer short diagnostic windows in production.

## Metrics

Pluggable synchronous callbacks via `Config.Metrics` / `ServerConfig.Metrics`:

| Interface | Scope |
|-----------|--------|
| `ClientMetrics` | Logical API outcome (`OnRequest`, `OnResponse`, `OnError`, `OnTimeout`) |
| `AttemptMetrics` | Optional; per transport attempt / re-dial (`OnAttempt`, `OnRetryDial`) |
| `ServerMetrics` | Per handled request on the server |

`ClientMetrics` reflects what the calling method returns (including exception and
protocol validation failures), **not** each internal retry. Implement
`AttemptMetrics` on the same value when you need per-attempt visibility.

A request refused by `Config.AllowedFunctionCodes` is reported like any other
failure: `OnRequest`, then `OnError` with an error wrapping
`ErrFunctionCodeNotAllowed`. It is never sent, so `AttemptMetrics` is not called.

`OnRetryDial` reports every re-dial of a single connection: between retry
attempts and, with attempt 0, when a request starts without a usable connection
(the previous one was lost, or dropped after a timeout on `tcp`/`tcp+tls`). Its
`err` is the dial error, wrapped as described in [ERRORS.md](ERRORS.md) (it also
covers a `Config.Dialer` that failed or returned no connection); its duration
includes the TLS handshake. A
request cancelled through its context is an `OnError` with `context.Canceled`; an
expired deadline is an `OnTimeout`. On the server, a request for a function code
no handler serves is an `OnError` with `ErrIllegalFunction`, and a request a
handler leaves unanswered (`ErrNoResponse`, for example Force Listen Only Mode)
is an `OnError` with `ErrNoResponse`.

A `batch` plan executed on a client makes one ordinary request per window, so
each window is one `OnRequest` and one outcome; windows a stopped batch did not
attempt are never requested and not reported. `Plan.Stats` gives the requests a
plan makes, and every `WindowResult` its `Started` and `Finished` time.

`Config.PDUTap` is not a metric: it receives the raw request and response PDUs of
every answered request (exception responses included, before the calling method
validates them) with the time the response was received, for recording what a
device answered. Details: [API.md § 6](API.md#6-metrics).

Callbacks run on the request path and must not block. Absence of a metrics
implementation is intentional for simple apps — attach one only when you have a
concrete consumer (Prometheus, statsd, …).

### Callbacks that panic

| Callback | A panic in it |
|---|---|
| `ServerMetrics` method | recovered and logged at error level with a stack trace (`panic in ServerMetrics.OnRequest for FC 0x.. from <client>`); the request is answered as if the method had returned |
| `RequestHandler` method | recovered and logged the same way; the client gets a Server Device Failure exception |
| `ClientMetrics`, `AttemptMetrics`, `PDUTap` method | not recovered: it reaches the caller of the client method; the client stays usable |
| `Logger` method | not recovered: on a client it reaches the caller, on a server it ends the process |

`ServerMetrics.OnRequest` sees the function code before the server has checked
it, so a table indexed by function code needs room for all 256 values.

### Accept errors

A server whose `Accept` fails (other than because it is stopping) logs a warning
with the error and the wait before the next attempt, `failed to accept client
connection (next attempt in 5ms): …`: 5 ms after the first failure, doubling to
at most 1 s, so a listener that keeps failing produces about one line a second.
There is no metrics hook for accept errors.

## Related

- Retry classification and write at-least-once: [API.md § 7](API.md#7-retry-policy)
- Error taxonomy: [ERRORS.md](ERRORS.md)
