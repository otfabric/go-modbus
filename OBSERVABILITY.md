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
| `ConnectionMetrics` | Optional, on the `ServerMetrics` value; connections the server closed to keep within `MaxClients` or refused through `AllowClient` (`OnConnectionRejected`) |
| `ConnectionLifecycleMetrics` | Optional, on the `ServerMetrics` value; connections the server starts and stops serving (`OnConnectionOpened`, `OnConnectionClosed`) |
| `ConnectionCloseMetrics` | Optional, on the `ServerMetrics` value; why the server stopped serving a connection (`OnConnectionCloseReason`) |
| `TLSHandshakeMetrics` | Optional, on the `ServerMetrics` or `ClientMetrics` value; the outcome of every TLS handshake, failed ones included, with the peer's certificates, the negotiated version and suite, and on the server the role and why a role was unusable (`OnTLSHandshake`). Since v1.21.0 |
| `AcceptErrorMetrics` | Optional, on the `ServerMetrics` value; a failed `Accept` and the wait before the next one (`OnAcceptError`). Since v1.21.0 |
| `ProtocolViolationMetrics` | Optional, on the `ServerMetrics` value; a request that breaks the framing or the structure of its function code, with its MBAP header and what the server did (`OnProtocolViolation`). Since v1.21.0 |
| `ResponseAnomalyMetrics` | Optional, on the `ClientMetrics` value; what the client skipped or discarded while waiting for a response (`OnResponseAnomaly`). Since v1.21.0 |
| `ServerConnectionMetrics` | Optional, on the `ServerMetrics` value; the lifecycle of each tcp / tcp+tls connection (rejected, opened, closed) as one typed event with its `ConnectionID` (`OnServerConnection`). Since v1.22.0 |
| `ClientConnectionMetrics` | Optional, on the `ClientMetrics` value; every dial (with its origin, duration and error), discard (with its reason) and close of the client's connections, single or pooled, with their `ConnectionID` (`OnClientConnection`). Since v1.22.0 |

`ClientMetrics` reflects what the calling method returns (including exception and
protocol validation failures), **not** each internal retry. Implement
`AttemptMetrics` on the same value when you need per-attempt visibility.
Its `OnAttempt` reports the exchange on the link: an exception response is an
attempt with `err` nil, and the call's `OnError` carries the exception.

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
(`RejectTLSHandshakeEvicted`). It is also called for a connection
`ServerConfig.AllowClient` refused (`RejectNotAllowed`): that one is closed as
soon as it is accepted, before anything is read from it, and takes no
`MaxClients` slot. `addr` is the remote `host:port`. A connection accepted while
the server is stopping is closed without being reported. The method runs on the
accept loop, after the connection is closed, and delays the next `Accept` while
it runs. These connections are also logged at warning level, as before.

`ConnectionLifecycleMetrics.OnConnectionOpened(addr, role)` is called when the
server starts serving a `tcp` or `tcp+tls` connection (on `tcp+tls`, once its
TLS handshake has completed), with the remote `host:port` and the client's role
(empty on `tcp`); `OnConnectionClosed(addr)` is called once when it stops
serving it, after the socket is closed. Every connection reported opened is
reported closed exactly once, unless a `Shutdown` was called from one of that
connection's own callbacks: no callback runs on it after that. A connection the
server refused or closed to keep within `MaxClients` is never reported opened.
Both run on the connection's goroutine and must not block.

`ConnectionCloseMetrics.OnConnectionCloseReason(addr, reason, err)` is called
right after `OnConnectionClosed`, once for every connection the server served,
with why it ended and the error that ended it: the client closed it
(`CloseClientClosed`), no request within `ServerConfig.Timeout`
(`CloseIdleTimeout`), bytes the server cannot frame (`CloseMalformedFrame`), a
request PDU of the wrong length for its fields or byte count (since v1.20.0 after
answering it with exception 3), or a handler that returned `ErrProtocolError`
(`CloseProtocolError`), a response that could
not be written (`CloseWriteFailed`), another read error such as a reset
(`CloseReadFailed`), or `Shutdown` (`CloseServerStopped`); with the opt-in
limits set, no complete first request within `ServerConfig.FirstRequestTimeout`
(`CloseFirstRequestTimeout`) or a request not complete within
`ServerConfig.FrameTimeout` of its first byte (`CloseFrameTimeout`). A response
the client did not read in time is `CloseWriteFailed` with an error matching
`os.ErrDeadlineExceeded`. These causes were only logged up to v1.18.0. A
`tcp+tls` connection whose handshake failed was never served and is reported
neither opened nor closed; the failure is logged at warning level and, since
v1.21.0, reported to `TLSHandshakeMetrics`.

### Security-relevant facts (since v1.21.0)

The library reports what it observes; it does not decide what is an attack.
There are no severities, no event schema, no detection rules and no sinks: an
application that wants them builds them on these callbacks, with whatever
monitoring product it uses.

| Fact | Where it is reported |
|---|---|
| Connection refused at accept (`AllowClient`, `MaxClients`, `MaxClientsPerHost`) or evicted mid-handshake | `ConnectionMetrics.OnConnectionRejected` |
| TLS handshake completed or failed: version, suite, SNI, peer chain, role, unusable role | `TLSHandshakeMetrics.OnTLSHandshake` (server and client) |
| Connection opened, with the client's role; closed, and why (idle, first-request and frame timeouts, malformed frame, protocol error, write failure, reset, stop) | `ConnectionLifecycleMetrics`, `ConnectionCloseMetrics` |
| Framing or structure violation: bad MBAP length or protocol ID, wrong PDU length, contradicting fields, exception function code; the answer and close the server decided on (delivery: `ConnectionCloseMetrics`) | `ProtocolViolationMetrics.OnProtocolViolation` |
| Request refused by `ServerConfig.Authorizer` | `ServerMetrics.OnError` with an error matching `ErrNotAuthorized` |
| Function code not served, value out of range, handler error | `ServerMetrics.OnError`, `ServerConfig.PDUTap` |
| Every request and answer, raw | `ServerConfig.PDUTap` (server), `Config.PDUTap` (client) |
| Accept failure | `AcceptErrorMetrics.OnAcceptError` |
| Client: frame with another transaction ID or a non-zero protocol ID skipped; stale serial bytes discarded | `ResponseAnomalyMetrics.OnResponseAnomaly` |

**Correlation.** Every server callback about a connection carries its remote
`host:port`, which identifies the connection while it is open; the client's role
is in `OnConnectionOpened`, `TLSHandshakeInfo.Role`, `ProtocolViolation.ClientRole`
and `ServerPDUTap`. Since v1.22.0 the structured reports also carry a
`ConnectionID` (below), which stays unique after the connection closed and its
address is reused.

**Order on one connection (server).** `OnTLSHandshake` (tcp+tls), then
`OnConnectionOpened`; per request `OnRequest`, then `OnResponse` or `OnError`,
then `OnProtocolViolation` for a violation; then `OnConnectionClosed` and
`OnConnectionCloseReason`. A malformed MBAP header, which never becomes a
request, is reported to `OnProtocolViolation` right before the close.

### Connection identity and lifecycle (since v1.22.0)

Every transport connection of a client or a server has a `ConnectionID`,
allocated before it is dialled or when it is accepted, and the same in every
structured report about it: `TLSHandshakeInfo`, `ProtocolViolation`,
`AuthorizationRequest`, `ServerPDUExchange` and `ServerConnectionEvent` on the
server; `TLSHandshakeInfo`, `ResponseAnomaly`, `PDUExchange` and
`ClientConnectionEvent` on the client. Identifiers come from one counter per
process, starting at 1, and are never reused while the process runs; they mean
nothing across processes or restarts. The positional callbacks
(`OnConnectionOpened(addr, role)` and the others) are unchanged.

`ServerConnectionMetrics` reports each server connection rejected, opened and
closed as one event, with addresses, role, reject or close reason, error and
age. `ClientConnectionMetrics` reports each client connection dialled (Open and
pre-warm, on demand, re-dial; with the dial's duration and error), discarded
after a failure (transport error, suspect response, before a retry, failed
pre-warm) and closed by `Close`. A normal shutdown is `Closed`, never
`Discarded`. No event links a discarded connection with the one dialled after
it: the pool has no such relationship to report.

**Do not use `ConnectionID` as a metric label.** It has a new value for every
connection, so a label made from it creates series without bound. Use it in
logs, traces and audit records; count by the event's kind and reason.

| Fact | Where it is reported |
|---|---|
| Server connection rejected, opened, closed, with its ID | `ServerConnectionMetrics.OnServerConnection` |
| Client dial (origin, duration, error), discard (reason, error), close, with its ID | `ClientConnectionMetrics.OnClientConnection` |

**Cost.** The interfaces are found once, by type assertion, in `New` and
`NewServer`. The `ConnectionID` is allocated whether or not they are
implemented: one atomic increment per connection, carried in the dial's and
the connection's context, and copied into the reports above. Beyond that, a
value that implements none of the interfaces costs nothing more than before; `BenchmarkSecurityHooks` (`go test -run '^$' -bench SecurityHooks .`)
measures a loopback round trip with them absent and present.

A consumer that forwards these facts elsewhere must not block: hand them to a
buffered channel with a non-blocking send, and count what it drops.
[examples/security_hooks/security_hooks.go](examples/security_hooks/security_hooks.go)
does that.

A `batch` plan executed on a client makes one ordinary request per window, so
each window is one `OnRequest` and one outcome; windows a stopped batch did not
attempt are never requested and not reported. `Plan.Stats` gives the requests a
plan makes, and every `WindowResult` its `Started` and `Finished` time.

`Config.PDUTap` is not a metric: it receives the raw request and response PDUs of
every answered request (exception responses included, before the calling method
validates them) with the time the response was received, for recording what a
device answered. `ServerConfig.PDUTap` is its server counterpart: the raw
request and response PDUs of every request a server dispatches, the server's
own exception answers included, with the client's address and role, the time
the request was read and the time its answer was decided, for an audit trail or
a recording of a client. Details: [API.md § 6](API.md#6-metrics).

Callbacks run on the request path and must not block. Absence of a metrics
implementation is intentional for simple apps — attach one only when you have a
concrete consumer (Prometheus, statsd, …).

Runnable examples, each with a test: [examples/client_metrics](examples/client_metrics/client_metrics.go)
(every client interface and `Config.PDUTap`),
[examples/server_metrics](examples/server_metrics/server_metrics.go) (`ServerMetrics`, the
connection interfaces and `ServerConfig.PDUTap`) and
[examples/security_hooks](examples/security_hooks/security_hooks.go) (the server's security
hooks, the authorizer and the opt-in limits).

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
| `ConnectionLifecycleMetrics` method | `ConnectionLifecycleMetrics.OnConnectionOpened for <addr> did not return normally` (or `OnConnectionClosed`) | the server goes on as if it had returned: the connection is served |
| `ConnectionCloseMetrics.OnConnectionCloseReason` | `ConnectionCloseMetrics.OnConnectionCloseReason for <addr> did not return normally` | the server goes on as if it had returned |
| `ServerPDUTap.OnServerPDU` | `ServerPDUTap.OnServerPDU for <addr> did not return normally` | the response is sent as if it had returned |
| `ServerConfig.AllowClient` | `ServerConfig.AllowClient for <addr> did not return normally` | the connection is refused (`RejectNotAllowed`) |
| `ServerConfig.Authorizer` | `RequestAuthorizer.AuthorizeRequest for FC 0x.. from <client> did not return normally` | the request is refused with Server Device Failure; the handler is not called |
| `Is` or `Unwrap` of the authorizer's error | `the error of the authorizer for FC 0x.. from <client> did not return normally` | the request is refused with Server Device Failure |
| `TLSHandshakeMetrics.OnTLSHandshake` (server) | `TLSHandshakeMetrics.OnTLSHandshake for <addr> did not return normally` | the server goes on as if it had returned |
| `ProtocolViolationMetrics.OnProtocolViolation` | `ProtocolViolationMetrics.OnProtocolViolation for <addr> did not return normally` | the server goes on as if it had returned |
| `AcceptErrorMetrics.OnAcceptError` | `AcceptErrorMetrics.OnAcceptError did not return normally` | the accept loop goes on as if it had returned |
| private key of `TLSServerCert` (a `crypto.Signer`) | `TLS handshake with <client> did not return normally` | the handshake fails and the connection is closed |
| `ServerConnectionMetrics.OnServerConnection` | `ServerConnectionMetrics.OnServerConnection (<kind>) for <addr> did not return normally` | the server goes on as if it had returned |
| `ClientMetrics`, `AttemptMetrics`, `TLSHandshakeMetrics`, `ResponseAnomalyMetrics`, `ClientConnectionMetrics` method | `ClientMetrics.OnResponse did not return normally: …` | the request goes on as if it had returned; its result is unchanged |
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
Since v1.21.0 a `ServerMetrics` value that implements `AcceptErrorMetrics` is
also told of each, with the same error and wait.

## Related

- Retry classification and write at-least-once: [API.md § 7](API.md#7-retry-policy)
- Error taxonomy: [ERRORS.md](ERRORS.md)
