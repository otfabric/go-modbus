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
- No pinned reference server serves FC12, so the client's check of its byte
  count (at most 70) is covered by this repository's tests only. That the
  server closes the connection after it answers a PDU length error with
  exception 3 is checked by this repository's tests, not by a reference
  client, which opens a connection per operation.
- Certification: none. No conformance test of the Modbus Organization has been
  attempted, and nothing here is a claim of conformance.

## Security

Only `tcp+tls://` protects the connection. Authorization on the server is the
application's: `ServerConfig.ConnectionAuthorizer` (which verified clients
connect), `ServerConfig.Authorizer` or the handlers decide; the library gives
roles no meaning. See [SECURITY.md](SECURITY.md).

A `tcp+tls://` server counts a connection against `MaxClients` before its TLS
handshake. When every slot is taken, a new connection replaces the oldest one
still handshaking (from the host with the most of them), never one that
completed its handshake. `MaxClientsPerHost` (opt-in, off by default) bounds
the connections of one IP address; without it, peers that keep opening
connections without handshaking can still delay clients from the same host.
Either way, many hosts can still crowd out a client whose handshake is slow.

- **No connection-rate limit.** The server does not limit how fast connections
  are accepted, per host or overall; that belongs at the network edge.
- **No defaults for the hardening settings.** `MaxClientsPerHost`,
  `FirstRequestTimeout`, `FrameTimeout`, `Authorizer` and
  `ConnectionAuthorizer` are off unless set.
- **Connection authorization is tcp+tls only.** `ConnectionAuthorizer` needs a
  verified certificate; setting it on a `tcp://` server is a configuration
  error. It runs after the TLS handshake completed, so a refused client has
  completed its handshake and sees its connection closed, not a TLS alert.
  The library checks no CRL or OCSP itself: revocation is the authorizer's.
- **Request sequence numbers are per connection.** `RequestSeq` (since
  v1.23.0) counts the requests the server read from one connection, from 1; it
  identifies a request only together with its `ConnectionID`, is not exchanged
  on the wire, and starts over on every connection. The MBAP transaction ID
  beside it is the client's and may repeat. Neither is meant as a metric
  label.
- **Decisions only of the authorizer.** `AuthorizationDecisionMetrics` reports
  the decisions of `ServerConfig.Authorizer`; a request the server answers
  itself (malformed, unserved function code) is never decided and is reported
  through `ServerMetrics.OnError`, `ProtocolViolationMetrics` and
  `ServerConfig.PDUTap` only.
- **Per-host means per IP address.** Clients behind one NAT or proxy share a
  `MaxClientsPerHost` budget; an IPv6 client that rotates addresses within its
  prefix counts as several hosts.
- **No detection.** The observation hooks report facts (handshakes,
  violations, refusals, closes, client-side skipped frames); they classify
  nothing as an attack, assign no severity and keep no history. go-modbus is
  not an IDS, IPS or SIEM ([SECURITY.md](SECURITY.md)).
- **Connection IDs are per process.** Since v1.22.0 the structured reports
  carry a `ConnectionID`, unique within the process while it runs; it is not
  stable across restarts, is not shared by the two ends of a connection, and
  is not exchanged on the wire. The positional callbacks
  (`ConnectionMetrics`, `ConnectionLifecycleMetrics`, `ConnectionCloseMetrics`)
  still identify a connection by its remote `host:port`; use
  `ServerConnectionMetrics` for the identifier.
- **No replacement pairs.** `ClientConnectionMetrics` reports a pool's discard
  and its later on-demand dial as separate events: the pool does not dial for
  a given connection, so it cannot say which one replaced which.
- **Not reported.** The server's own exception for an
  out-of-range value or an unserved function code goes to
  `ServerMetrics.OnError`, not to `ProtocolViolationMetrics`. The serial
  framings' discarded bytes (`ResponseAnomalyMetrics`) are counted when the
  client reads them, before the next request, so bytes that arrive later and
  are dropped with a failed response are reported through that request's
  error instead.
- **FrameTimeout and the variable-length function codes.** The server reads
  what the MBAP length announces; an overcounting length waits for bytes of
  the next request, and `FrameTimeout` then bounds that wait as for any
  incomplete request.

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
- A request PDU whose length disagrees with its function code or byte count
  is answered with exception 3 and the connection is then closed: on a TCP
  stream it cannot be told from an MBAP length that is wrong. The answer is a
  best effort: a client may see the close before it reads it. A client keeps
  its connection after an exception, so its next request on it fails once
  (the next one reconnects). Requests the client pipelined behind the bad
  one are not served. For the variable-length function codes (FC08, FC15,
  FC16, FC20, FC21, FC23, FC43) the server reads what the MBAP length
  announces before it can check, so an overcounting length still waits for,
  and takes, bytes of the next request. A request whose function code has
  the exception bit set (`0x80`..`0xFF`) is not answered. The Modbus Messaging
  on TCP/IP Implementation Guide was not consulted for either
  ([INTEROPERABILITY.md](INTEROPERABILITY.md)).
- FC07, FC11, FC12 and FC17 with data are answered with exception 3 under
  section 7 of V1.1b3, although the exception lists of sections 6.7, 6.9,
  6.10 and 6.13 name only 1 and 4.
- The server keeps no Listen Only Mode of its own (FC08 0x0004): a handler
  that emulates one decides which later requests it answers.
- FC17: where the Server ID ends cannot be told from a response, so the
  client's `RunIndicatorStatus` is a guess from the last byte and the server
  does not check the run indicator. `ReportServerIDResponse.RunIndicator`
  needs the Server ID length from the device's documentation.
- FC43 client: a device that counts Number Of Objects over the whole
  identification (as the two-page worked example of V1.1b3 section 6.21
  does) is reported as a protocol error. No such device is recorded in
  INTEROPERABILITY.md; there is no lenient mode. A lower stream ReadDevId
  code in the response, which 6.21 does not allow, is tolerated.
- FC43 server: the mandatory basic objects (0x00–0x02) are not checked; a
  handler without them is served as it is.
- FC01/FC02: nonzero padding bits in a response are not reported (a
  deliberate tolerance; sections 6.1 and 6.2 say "zero filled").
- FC20: the 0x07 minimum of the response table (section 6.14) is not
  enforced; it contradicts the worked example.
- `ServerConfig.AllowClient` sees only the remote address, at accept: on
  `tcp+tls` it runs before the handshake, so it cannot decide on the client
  certificate (`TLSClientCAs` and the client's role do that).
- A failed `tcp+tls` handshake is logged and reported to `TLSHandshakeMetrics`
  only: the connection was never served, so `ConnectionCloseMetrics` does not
  report it.
- `ServerConfig.PDUTap` reports the answer the server decided on, before it is
  written: a response whose write then fails is reported as sent
  (`CloseWriteFailed` says it was not).
- `Server.Stop` and `Server.Shutdown` called from a handler method, a
  `ServerMetrics` method or a method of one of its optional extensions,
  `ServerConfig.AllowClient`, `ServerConfig.Authorizer`,
  `ServerConfig.ConnectionAuthorizer`, `ServerConfig.PDUTap` or the `Logger`
  do not wait for
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
