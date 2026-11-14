# Limitations

What go-modbus does not do, or does differently from what one might expect.
What it implements, function by function, is in
[INTEROPERABILITY.md](INTEROPERABILITY.md).

## Scope not implemented

- **Server transports.** The server listens on Modbus TCP and TLS only. RTU,
  ASCII, UDP and the tunnelled serial framings are client-only.
- **Vendor and user-defined function codes.** The client has no call that
  sends an arbitrary PDU, and the server answers them with exception 1
  (illegal function).
- **FC43 sub-type 13.** The CANopen general reference is not supported
  ([API.md](API.md)); only MEI type 14 is.
- **SunSpec points.** The `sunspec` package detects the marker and lists model
  headers. It does not decode points or schemas: that is
  [go-sunspec](https://github.com/otfabric/go-sunspec).
- **CP56Time2a flags.** The codec ignores the status bits on decode and writes
  them as zero ([CODECS.md](CODECS.md)).

## Verification

- Run against eight independent implementations in CI (libmodbus, PyModbus,
  digitalpetri/modbus, NModbus, tokio-modbus, j2mod, jsmodbus,
  modbus-esp8266) and a ninth, modbus4j, from a local build only, because
  its image is GPL-3.0 and not published. Versions and gaps are in
  [INTEROPERABILITY.md](INTEROPERABILITY.md).
- Both directions on Modbus TCP. The client only on Modbus UDP (j2mod,
  modbus4j) and RTU over TCP (j2mod, jsmodbus, modbus-esp8266, modbus4j):
  the server has Modbus TCP only.
- TLS, RTU and ASCII on a serial line, RTU over UDP and ASCII over TCP are
  covered by this repository's own tests, not by an independent
  implementation.
- Certification: none. No conformance test of the Modbus Organization has been
  attempted, and nothing here is a claim of conformance.

## Security

Only `tcp+tls://` protects the connection. Authorization on the server is the
handler's job. See [SECURITY.md](SECURITY.md).

A `tcp+tls://` server counts a connection against `MaxClients` before its TLS
handshake. When every slot is taken, a new connection replaces the oldest one
still handshaking (from the host with the most of them), never one that
completed its handshake. The server has no per-source connection limit: peers
that keep opening connections without handshaking can still delay clients
from the same host, or from many hosts a client whose handshake is slow.

## Custom dialer

- `Config.Dialer` covers the network transports only; `rtu` and `ascii` open
  their serial device directly.
- `Open` takes no context: its dials are bounded by `DialTimeout` (or its
  default) and cannot be cancelled. `OpenContext` is `Open` with a context that
  ends its dials. The dials that replace a lost connection during a request run
  under the request's context as well.
- `Close` does not end an `Open` or `OpenContext` that is in progress. Called
  while either is still dialling it returns nil and closes nothing; the open
  then completes and the client is open. A shutdown path that may race an open
  cancels the context of its `OpenContext`, or calls `Close` again after `Open`
  has returned.
- A dialer ends its dial when the context of `OpenContext` ends only if it
  honours its context. When the dial succeeds although the context has ended,
  `OpenContext` closes the connection and returns the context's error.
- The serial lines (`rtu`, `ascii`) open their device without a context:
  `OpenContext` checks its context before and after, not during.
- For `udp` and `rtuoverudp`, a connection from the dialer must keep datagram
  boundaries (one `Read` per datagram, one datagram per `Write`); a stream
  connection, such as most proxies give, will not frame Modbus/UDP correctly.
- `modbus-cli` has no option for a dialer or a proxy.

## Known deviations

- The server's exception order: a request for a unit the handler would reject
  with exception 11 is answered with exception 1 or 3 when it also has an
  unknown function code or an invalid quantity, because the handler sees a
  request only after those checks. The specification leaves this open;
  libmodbus, PyModbus, digitalpetri/modbus, NModbus and tokio-modbus
  servers answer 11 ([INTEROPERABILITY.md](INTEROPERABILITY.md)).
- The server does not filter unit identifiers: that is the handler's job.
- `Server.Stop` and `Server.Shutdown` called from a handler method, a
  `ServerMetrics` or `ConnectionMetrics` method or the `Logger` do not wait for
  the connection (or the accept loop) they were called on: when they return,
  that connection's goroutines are still finishing the request in progress (its
  outcome hook included) and end afterwards
  ([API.md § 3.2](API.md#32-lifecycle)). Only the first `Stop` or `Shutdown`
  waits: after a `Shutdown` whose context ended, nothing waits for the handlers
  that are still running.
- The guard on the application's code stops a call that does not return
  normally, not one that ends its goroutine with `runtime.Goexit`. The methods
  of errors a `Config.Dialer` or its connections return, and the interfaces of
  `batch`, `sunspec` and `codec`, run on the caller's goroutine unguarded
  ([OBSERVABILITY.md](OBSERVABILITY.md#callbacks-that-do-not-return-normally)).
- The serial framings (`rtu`, `ascii`, `rtuovertcp`, `rtuoverudp`,
  `asciiovertcp`) carry no transaction ID. The tunnelled ones replace their
  socket after a timeout, a cancelled request or a response that does not match
  its request, so an earlier answer cannot reach a later request. On a serial
  line (`rtu`, `ascii`) the client instead discards what the line delivers
  before the next request (at least 3 × t3.5 for RTU or 20 ms for ASCII, and
  until the line is quiet, bounded by `Timeout`). An answer that arrives later
  than that can still be read as the answer to the next request when it has
  the same unit, function code and length: nothing else tells them apart. The
  requests that follow a discard first check that the line is quiet (and
  discard again if it is not), and a response that does not match its request
  is reported as an error and discards the line too, so one late answer does
  not shift the answers of later requests; the check may miss an answer that
  is still on its way when the next request starts. On a serial line prefer `MaskWriteRegister` (FC22), which the
  device applies atomically, over a client-side read-modify-write
  (`UpdateRegisterMask`, `WriteRegisterBit` and similar), which writes back a
  value computed from what was read.
- Modbus/UDP is not standardised: `udp` and `rtuoverudp` assume one ADU per
  datagram. Bytes left in a datagram are dropped when the next request is
  written, and a datagram longer than 260 bytes is cut off by the socket.
