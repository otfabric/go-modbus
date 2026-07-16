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

Callbacks run on the request path and must not block. Absence of a metrics
implementation is intentional for simple apps — attach one only when you have a
concrete consumer (Prometheus, statsd, …).

## Related

- Retry classification and write at-least-once: [API.md § 7](API.md#7-retry-policy)
- Error taxonomy: [ERRORS.md](ERRORS.md)
