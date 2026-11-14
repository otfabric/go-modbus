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
`NewSlogLogger(nil)` / `NewSlogFieldLogger(nil)` return a no-op logger.

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
| `ConnectionMetrics` | Optional, on the `ServerMetrics` value; connections the server closed to keep within `MaxClients` (`OnConnectionRejected`) |

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

A `Diagnostics` call with more than 250 bytes of data is an `OnRequest`, then an
`OnError` with its `*ParameterError`; nothing is sent, so `AttemptMetrics` is not
called.

`ConnectionMetrics.OnConnectionRejected(addr, reason)` is called once for every
connection the server closes to keep within `ServerConfig.MaxClients`: one it
accepted when every slot was taken (`RejectMaxClients`), and on `tcp+tls` one
whose TLS handshake had not completed, closed to make room for a newer one
(`RejectTLSHandshakeEvicted`). `addr` is the remote `host:port`. A connection
accepted while the server is stopping is closed without being reported. The
method runs on the accept loop, after the connection is closed, and delays the
next `Accept` while it runs. These connections are also logged at warning
level, as before.

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

### Callbacks that do not return normally

Every call of the application's code is guarded where the library makes it. A
call that does not return normally is stopped there, logged at error level with
the value it ended with and its stack, and handled as an error of that call; the
goroutine, and with it the process, goes on. The library holds none of its
locks when it logs.

| Callback | Logged as | Handled as |
|---|---|---|
| `RequestHandler` method | `panic in handler for FC 0x.. from <client>` (the v1.15.3 text, kept for log filters) | the client gets a Server Device Failure exception |
| `ServerMetrics` method | `panic in ServerMetrics.OnRequest for FC 0x.. from <client>` (the v1.15.3 text) | the request is answered as if the method had returned |
| `Is` or `Unwrap` of a handler's error | `the error of the handler for FC 0x.. from <client> did not return normally` | the client gets a Server Device Failure exception |
| `ConnectionMetrics.OnConnectionRejected` | `ConnectionMetrics.OnConnectionRejected for <addr> did not return normally` | the server goes on as if it had returned |
| private key of `TLSServerCert` (a `crypto.Signer`) | `TLS handshake with <client> did not return normally` | the handshake fails and the connection is closed |
| `ClientMetrics`, `AttemptMetrics` method | `ClientMetrics.OnResponse did not return normally: …` | the request goes on as if it had returned; its result is unchanged |
| `PDUTap.OnPDU` | `PDUTap.OnPDU did not return normally: …` | the call goes on as if it had returned; its result is unchanged |
| `RetryPolicy.ShouldRetry` | `RetryPolicy.ShouldRetry did not return normally: …` | no retry: the call returns the error of its attempt |
| `Config.Dialer` | `Config.Dialer did not return normally: …` | the dial fails with an error that says so (`Open` returns it, or the request that dialled) |
| method of a `Config.Dialer` connection | `net.Conn.Read (Config.Dialer) did not return normally: …` | that method fails with an error that says so: the request fails and the connection is not used again; `RemoteAddr`, `LocalAddr` return nil; `Close` returns the error |
| private key of `TLSClientCert` | `TLS handshake did not return normally: …` | the dial fails |
| `Logger` method (and `FieldLogger.With`) | `<component> [error]: Logger.Debugf did not return normally: …`, through the same Logger's `Errorf` | the entry is lost; if `Errorf` does not return normally either, so is the report. A `FieldLogger` whose `With` fails is used through its plain methods |

On v1.15.3 only the first two rows were guarded: a client callback that did not
return normally reached the caller of the client method, and on the server, and
on the client's own goroutines (the interrupt of a cancelled request), it ended
the process.

Not guarded: the methods of an error a `Config.Dialer` or its connection returns
(`errors.Is` and `errors.As` call them on the caller's goroutine), and the
interfaces of `batch`, `sunspec` and `codec`, which run synchronously on the
caller's goroutine. A call that ends its goroutine with `runtime.Goexit` cannot
be stopped.

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
