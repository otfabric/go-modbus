# Plan — cyber-security awareness for go-modbus

- **Date:** 2026-11-21
- **Base:** `93d95b6` (main, v1.19.0)
- **Derived from:** the go-iecp5 plan (`PLAN-cybersecurity-events.md`). It keeps
  that plan's event model, delivery rules, MAC design and decisions (§12 there),
  and maps them onto Modbus: no APCI sequence numbers, no time tags, no
  unsolicited traffic, but function codes, unit identifiers, exceptions,
  diagnostics sub-functions and FC43 device identification.
- **Scope:** root package (client and server), `internal/transport`,
  `internal/adu`, `cmd/modbus-cli`, docs, examples. New packages `secevent`,
  `netpeer`, `internal/secmon`.
- **Status:** plan only. No code written yet.
- **Constraints (AGENTS.md):** v1 stays v1 and every change is additive; no
  protocol semantics without a citation (spec/MODBUS-SPEC.md or
  INTEROPERABILITY.md); no interoperability or conformance claim beyond recorded
  evidence; Go 1.26; `make check` green per PR, `make fuzz` for new parsers,
  `make soak` for transport and shutdown changes, `make interop` for wire
  behaviour.

## 0. Goals and non-goals

### Goals

1. **Peer identity in the API.** Every TCP endpoint (server connection,
   client connection) can report its peer's MAC address together with IP,
   port, TLS identity, Modbus role and connect time. The library notices when
   the MAC behind a known IP changes. A client can also pin the identity a
   device reports over FC43 and notice when it changes (§8.3).
2. **A cyber-event channel.** One structured sink receives typed security
   events from every layer: transport (TCP, TLS, serial framing), MBAP, PDU,
   application (function codes and handlers). Server and client use the same
   event type. Kind names shared with go-iecp5 (`conn.*`, `tls.*`, `peer.*`,
   `meta.*`) have the same spelling and meaning, so one SIEM rule set covers
   both libraries.
3. **Detection of the requested event classes**, mapped to code locations in
   §4: unexpected connections · rejected certificates · invalid unit IDs,
   addresses and quantities · oversized requests · non-existent functions ·
   rejected requests (exceptions) · invalid MBAP headers, PDUs, fields ·
   transaction-ID anomalies and replayed writes · restart, listen-only, clear
   counters, file writes and other unusual functions · MAC change for an IP ·
   responses nobody asked for · write and request flooding · reconnaissance
   (FC17, FC43, unit and function sweeps) · DoS and connection exhaustion.
4. **Optional hardening hooks**: per-host connection limits, accept rates,
   function policy (per MBAPS role), a first-request timeout. All opt-in; the
   default is "observe and report".
5. **Zero cost and zero behaviour change when unused.** No sink and no policy:
   wire behaviour and performance stay as in v1.19.0. The only exception is the
   FC43 conformity-level fix in §8.2, called out explicitly.
6. **FC43 Read Device Identification** is supported end to end: client, server,
   CLI, and used as an identity source for goal 1 (§8).

### Non-goals

- A Modbus security protocol beyond MBAPS (`tcp+tls`), which the library
  already implements. No authentication for plain Modbus is invented.
- Server transports other than `tcp` and `tcp+tls` (LIMITATIONS.md). Serial
  events come from the client only.
- Kernel-level DoS (SYN floods, half-open connections): invisible to user
  space. Document it and recommend SYN cookies and a firewall.
- Being an IDS. The library reports what it sees as one endpoint; correlation
  across devices belongs to the consumer.

---

## 1. Findings from the current code that shape the design

The Modbus server is already more hardened than the IEC 104 one was: accept
back-off, write deadlines, a TLS handshake deadline, `AllowClient`, close and
reject reasons and a server PDU tap all exist. The plan builds on them instead
of re-fixing them.

| # | Location | Observation | Consequence for the plan |
|---|----------|-------------|--------------------------|
| F1 | `server_transport.go:55-72` | Accept errors already back off (5 ms → 1 s) and only `net.ErrClosed` ends the loop. They are only logged. | No fix needed. Emit `conn.accept_error` (Critical) at the log site. |
| F2 | `server_transport.go:75-90`, `metrics.go:213-230` | `AllowClient` and `RejectNotAllowed` exist; `RejectMaxClients` and `RejectTLSHandshakeEvicted` are reported to `ConnectionMetrics`. | These become `conn.unexpected`, `conn.rejected_limit`, `tls.handshake_evicted`. Same call sites, no new detection code. |
| F3 | `LIMITATIONS.md` § Security, `server_transport.go:97-111` | No per-source connection limit and no accept rate. Ten idle connections fill the default `MaxClients` for up to `Timeout` (120 s). | Opt-in `MaxClientsPerHost` and `AcceptRate` (§5.3); `conn.rejected_per_host`, `conn.rate`. |
| F4 | `server_transport.go:290-293`, `LIMITATIONS.md` § Known deviations | A failed TLS handshake is logged only and reaches no metric. The error is not classified. | `tls.*` classification (§4.2). This removes a documented limitation. |
| F5 | `server_transport.go:755-788` | A client certificate with two role OIDs or a badly encoded role yields role `""` with a log warning. The handler cannot tell "no role" from "forged or broken role". | `tls.role_invalid` (Warning) and `tls.role_missing` (Notice). |
| F6 | `server_transport.go:380, 441` | The read deadline is the idle `Timeout`, armed once when no request is pending. A partial frame therefore has up to `Timeout` to complete. A peer that connects and never sends holds a slot for `Timeout`. | Opt-in `FirstRequestTimeout` and `FrameAssemblyTimeout`; `conn.no_request`, `dos.slow_frame` (§7). |
| F7 | `server_transport.go:477-491` | Close reasons are classified (`CloseMalformedFrame`, `CloseProtocolError`, …) but carry no frame bytes. | `proto.bad_mbap` / `proto.bad_pdu` with `Raw` (copy, ≤ 64 B). |
| F8 | `server_transport.go:561-582` | Every exception the server answers with, its own and the handler's, is built in one place. | **Outbound exception observation** (§3.6): exceptions 1, 2, 3, 4, 10, 11 become events with no handler change. |
| F9 | `server_transport.go:707-712` | Unknown function codes (vendor and user-defined included) answer exception 1. | Function sweeps show up as many distinct FCs answered with 1: `access.function_scan`. |
| F10 | `server_functions.go:129-153` | FC08 passes the sub-function to the handler. Restart Communications (0x0001), Force Listen Only (0x0004) and Clear Counters (0x000A, 0x0014) are the Modbus "restart / halt / erase" operations. | Audit and flood events by sub-function (§4.6). Detection runs before the handler, so it works whatever the handler does. |
| F11 | `internal/transport/tcp.go:134-161` | The client skips responses with an unexpected transaction ID or protocol ID up to `maxAnomalies` times, with a log line each. | `unsol.response` (wrong TID) and `proto.bad_mbap` (wrong PID) on the client: possible injection, or a late answer. |
| F12 | `server_device_id.go:79` | An FC43 request is checked for `len < 3` only. Bytes after the Object Id are ignored, while FC01..06, FC22 and FC24 with a wrong length close the connection (INTEROPERABILITY.md, request table). | `proto.trailing_bytes` (report only). Rejecting would be a wire change: open question Q9. |
| F13 | `server_device_id.go:86, 140, 225-243` | Stream access filters by the **requested** category, not by the conformity level. A handler whose `ConformityLevel` is basic but whose object list contains private objects (0x80–0xFF) gives them to any extended request. The specification (§6.21): "If the server device is asked for a description level (readDevice Code) higher that its conformity level, it must respond in accordance with its actual conformity level." | Fix in §8.2 (behaviour change, called out). |
| F14 | `cmd/modbus-cli/help.go:5-18` | `modbus-cli` has no FC43 operation, although the library has had one for many releases. | Add `devid` (§8.4). |
| F15 | `server_tap.go:76`, `server.go:44-77` | `ServerConfig` was kept comparable on purpose (commit 7f67046, `ClientFilter`). | The new policy field is a **pointer** (`*SecurityPolicy`) and the sink an interface, so `ServerConfig` stays comparable. |
| F16 | `internal/callback/callback.go`, `server.go:172-195` | All application code runs through `callback.Run`; `Stop` from callbacks has precise rules tied to `goid`. | The sink runs through `callback.Run` on the dispatcher's goroutine, which is registered like the other run goroutines, so `Stop` from a sink neither deadlocks nor waits for itself (§3.3). |
| F17 | `go.mod` | One dependency (`go-serial`). Downstream OT Fabric repos depend on this module. | MAC lookup uses only the standard library (`syscall` netlink/route RIBs, `syscall.LazyDLL` on Windows). See Q4. |

---

## 2. Architecture overview

```
           ┌────────────────────────────────────────────────────────┐
           │                    application                         │
           │   sink := secevent.SinkFunc(func(e secevent.Event){…}) │
           └──────────────▲─────────────────────────────▲───────────┘
                          │ async, bounded, never blocks │ PeerFromContext(ctx), Client.Peer()
                 ┌────────┴──────────┐         ┌────────┴─────────┐
                 │ secevent.Dispatcher│         │ netpeer (MAC)    │
                 │ (queue, aggregate, │         │ resolver + cache │
                 │  drop accounting)  │         └────────▲─────────┘
                 └────────▲──────────┘                   │
        ┌─────────────────┼──────────────────┬───────────┴──────┐
   internal/secmon detectors (token buckets, distinct-miss windows, conn tracker, write dedup)
        │                 │                  │                  │
   Server accept loop   serveConn/serveRequest   Client (exec, dial)   internal/transport (TCP, RTU, ASCII)
```

### New packages

| Package | Visibility | Contents |
|---------|-----------|----------|
| `secevent` | public | `Event`, `Kind`, `Category`, `Severity`, `Action`, `PeerInfo`, `TLSPeer`, `Sink`/`SinkFunc`, `Dispatcher`, ready-made sinks, `Report(ctx, e)`, `PeerFromContext(ctx)`, `Stats`, `Rate`, and the protocol-neutral part of the policy (`secevent.Policy`: delivery, connection, identity, TLS). No Modbus types beyond the `FunctionCode` and `ExceptionCode` aliases from `internal/protocol`. |
| `netpeer` | public | `LookupMAC`, `Resolver`, OS backends, `Cache` (IP↔MAC↔cert history), `OnLink`. Usable with any `net.Conn`. Same API as the go-iecp5 `netpeer`. |
| `internal/secmon` | internal | Token bucket, distinct-miss window (scan detector), connection tracker, write dedup window, aggregator. |

Modbus-specific policy (function policy, per-class limits, scan thresholds)
stays in the root package as `modbus.SecurityPolicy`, which embeds
`secevent.Policy`.

Dependency rules added to ARCHITECTURE.md:

```
internal/protocol ← secevent ← modbus (root)
netpeer           ← secevent
internal/secmon   ← modbus (root)
```

- `secevent` and `netpeer` must not import the root package, `internal/transport`
  or `internal/session`.
- `internal/transport` reports through a small interface it defines
  (`transport.Observer`), implemented in the root package, so the transport
  does not import `secevent`.

---

## 3. Event model and delivery

### 3.1 `secevent.Event`

```
Event {
    Time        time.Time
    Kind        Kind        // stable dotted code, e.g. "access.function_scan"
    Category    Category    // Connection | Peer | TLS | Protocol | Access | Control |
                            //   Recon | Unsolicited | Flood | Resource | Remote | Meta
    Severity    Severity    // Info | Notice | Warning | Alert | Critical (RFC 5424 aligned)
    Action      Action      // Observed | Rejected | Throttled | Disconnected | Blocked
    Protocol    string      // "modbus"
    Transport   string      // "tcp" | "tcp+tls" | "udp" | "rtu" | "ascii" | "rtuovertcp" | …
    Role        string      // "server" | "client"
    ConnID      uint64      // per process, monotonically increasing; correlates one connection
    Peer        PeerInfo    // §3.2
    Local       string      // local address or serial device

    // Protocol context. Fields says which are set.
    Fields        FieldMask
    UnitID        uint8
    FunctionCode  FunctionCode   // alias of modbus.FunctionCode
    SubFunction   uint16         // FC08 sub-function, FC43 MEI type
    Exception     ExceptionCode  // answered or received exception
    Address       uint16
    Quantity      uint16
    TransactionID uint16
    ExpectedTxnID uint16
    MBAPRole      string         // MBAPS role from the client certificate

    Count       int         // > 1 when aggregated
    FirstSeen   time.Time
    Detail      string      // one line, no secrets, no register values
    Err         error
    Raw         []byte      // copy of the offending PDU or ADU, ≤ MaxRaw (default 64 B)
    Technique   string      // MITRE ATT&CK for ICS hint (§3.7)
}
```

Rules (unchanged from the IEC plan):

- `Kind` values are public API, never renamed. One table (`kinds.go`) fixes each
  Kind's default Category, Severity and Technique.
- `Raw` is always a copy. Register values and write payloads appear only in
  `Raw`, never in `Detail` (SECURITY.md treats them as sensitive, like the
  PDU tap).
- `Event.String()` gives one line; `MarshalJSON` a stable form (golden file).

### 3.2 `secevent.PeerInfo`

```
PeerInfo {
    Network     string           // "tcp", "udp", "serial"
    RemoteAddr  netip.AddrPort
    LocalAddr   netip.AddrPort
    MAC         net.HardwareAddr // nil when unknown
    MACSource   MACSource        // None | Neighbor | Gateway | Static | Custom
    OnLink      bool
    Interface   string
    TLS         *TLSPeer         // nil when plaintext
    Role        string           // MBAPS role ("" when none)
    ConnectedAt time.Time
    SerialPort  string
}
TLSPeer { Version, CipherSuite uint16; ServerName, Subject, Issuer, SerialHex string;
          NotAfter time.Time; FingerprintSHA256 string; Verified bool }
```

Accessors:

- **Server handlers** receive `ctx`, the connection's context. The server stores
  the connection's `PeerInfo` and a reporter in it:
  `secevent.PeerFromContext(ctx) (PeerInfo, bool)`. No request struct and no
  handler interface changes. `ClientAddr` and `ClientRole` stay as they are.
- **Client:** `(*Client).Peer() (secevent.PeerInfo, bool)` for the current
  connection (false while closed, and for pooled clients it returns the most
  recent connection; `Client.Peers()` lists all pooled ones).
- `netpeer.LookupMAC(addr net.Addr)` for users with their own connection
  (`Config.Dialer`).
- `PeerInfo` is computed once per connection and cached; MAC re-checks (§5.2)
  update it under a mutex; accessors return a copy.

### 3.3 Delivery: `secevent.Dispatcher`

Same as the IEC plan:

- Call sites check one `atomic.Pointer[Dispatcher]`. Nil returns at once: no
  allocation, no clock read.
- Bounded channel (default 1024), one goroutine calling the sink. Full channel →
  drop, count, and deliver `meta.events_dropped{Count}` before the next event.
- Aggregation per `(ConnID, Kind, key)`, key = unit/FC/address where relevant,
  `AggregateWindow` default 1 s, LRU-bounded (4096 keys). Always-deliver kinds
  bypass it (`peer.mac_changed`, `tls.cert_rejected`, `ctrl.listen_only`,
  `ctrl.restart_comms`, `peer.identity_changed`).
- `secevent.SyncSink` for tests only.

Modbus-specific rules (F16):

- The sink is called through `internal/callback.Run`. A sink that does not
  return normally is logged with its stack and reported once as
  `meta.sink_panic`; delivery goes on.
- A dispatcher the server creates (from `SecurityPolicy.Sink`) belongs to the
  server's run: its goroutine is registered with `Server.join`, so `Stop` or
  `Shutdown` called from the sink returns without waiting for the sink, as it
  does from any other callback (server.go:172-195). `Shutdown` flushes the
  queue, bounded by its context. A dispatcher the application passes in
  (`secevent.Policy.Dispatcher`) is shared and closed by the application.
- API.md § 3.2 and the `RequestHandler` doc list the sink among the callbacks
  that may call `Stop`.

### 3.4 Ready-made sinks

| Sink | Purpose |
|------|---------|
| `SinkFunc(func(Event))` | adapter |
| `ChannelSink(chan<- Event)` | non-blocking send, counts drops |
| `LoggerSink(l)` | maps Severity onto the library's `Logger` levels, prefix `[CYBER]`; uses `FieldLogger` when the logger implements it (API.md § 5) |
| `JSONLinesSink(io.Writer)` | one JSON object per line (the IEC answer to Q5: JSON) |
| `MultiSink(...Sink)` | fan-out |
| `FilterSink(min Severity, cats ...Category, s Sink)` | filtering |

### 3.5 Reporting from handlers: `secevent.Report(ctx, e)`

Handlers know the data model; the library does not. A handler that refuses a
write to a protected register does:

```go
secevent.Report(ctx, secevent.Event{Kind: secevent.KindWriteDenied,
    UnitID: req.UnitID, Address: req.Addr, Quantity: req.Quantity})
```

`Report` fills Time, Peer, ConnID, Protocol, Transport and Role from the
context and returns false when the context carries no reporter (no sink, or a
context not from the server). Helpers keep calls short:
`secevent.UnknownUnit(ctx, unit)`, `secevent.WriteDenied(ctx, unit, addr, qty)`,
`secevent.RoleDenied(ctx, fc)`.

### 3.6 Automatic detection from outbound exceptions

At the single place the server builds an exception response
(`server_transport.go:573-582`), only when a dispatcher is attached:

| Exception answered | Event | Notes |
|--------------------|-------|-------|
| 01 Illegal Function | `access.unknown_function` | server's own (unknown FC, no handler, MEI ≠ 14) or the handler's |
| 02 Illegal Data Address | `access.illegal_address` | Address, Quantity from the request |
| 03 Illegal Data Value | `proto.illegal_value` | quantity limits, FC05 values, FC43 code outside 1..4 (INTEROPERABILITY.md request table) |
| 04 Server Device Failure | `app.device_failure` | Notice. A handler that did not return normally (recovered panic) is `app.handler_panic`, Warning: a crafted request that crashes application code |
| 06 Server Device Busy | — | not reported (normal flow control) |
| 0A / 0B Gateway | `access.unknown_unit` | the handler's way to reject a unit (API.md § 3.3) |
| `ErrNoResponse` (silent) | `access.silent_drop` | Info; aggregated |

Exception events feed the scan detector (§4.4) and the brute-force escalation
(`access.brute_force`, §4.10).

Client side, mirrored: an exception received from the device is
`remote.exception` (Notice), aggregated per (unit, FC, code). It flags a master
being steered toward addresses or units that do not exist.

### 3.7 MITRE ATT&CK for ICS hints

`Technique` is a routing hint, not attribution. Starting mapping (**check IDs
against the current matrix when implementing**): T0814 Denial of Service ·
T0855 Unauthorized Command Message · T0836 Modify Parameter · T0846 Remote
System Discovery · T0888 Remote System Information Discovery · T0861 Point & Tag
Identification · T0830 Adversary-in-the-Middle · T0816 Device Restart/Shutdown ·
T0843 Program Download · T0845 Program Upload · T0806 Brute Force I/O · T0849
Masquerading · T0856 Spoof Reporting Message · T0858 Change Operating Mode ·
T0872 Indicator Removal on Host · T0866 Exploitation of Remote Services.

---

## 4. Event catalogue

Columns: **Kind** · default **Sev** · trigger · where · side (S = server,
C = client).

### 4.1 Connection (`conn.*`) — names shared with go-iecp5

| Kind | Sev | Trigger | Where | Side |
|------|-----|---------|-------|------|
| `conn.accepted` | Info | Every accepted connection, with PeerInfo incl. MAC (Q3 there: Info) | `server_transport.go:73` after Accept | S |
| `conn.unexpected` | Warning | `AllowClient` refused, or peer not in `Policy.AllowedPeers` (CIDR). Action `Rejected` or `Observed` (`RejectUnlisted=false`) | `server_transport.go:81-90` | S |
| `conn.unexpected_mac` | Alert | IP allowed, MAC not the one pinned in `Policy.PeerBindings` | after MAC lookup, before serving | S |
| `conn.rejected_limit` | Warning | `MaxClients` reached (`RejectMaxClients`) | `server_transport.go:127-134` | S |
| `conn.rejected_per_host` | Warning | new `MaxClientsPerHost` reached (F3) | conn tracker | S |
| `conn.rate` | Alert | accepts from one host exceed `AcceptRate` (default 10 / 10 s), or distinct-host burst over `GlobalAcceptRate`. Connection storm or sweep | conn tracker | S |
| `conn.no_request` | Notice | closed (by the peer or by `FirstRequestTimeout`) without one valid request: TCP-connect scan, banner grab, or a slot holder | `serveConn` defer | S |
| `conn.idle_timeout` | Info | `CloseIdleTimeout` | `server_transport.go:484` | S |
| `conn.closed` | Info | end of connection: CloseReason, duration, requests, octets | `serveConn` defer, next to `reportCloseReason` | S |
| `conn.accept_error` | Critical | listener Accept error (EMFILE, ENFILE, …) | `server_transport.go:61-62` | S |
| `conn.dial_failed` | Notice | dial or TLS handshake failure (`dialError`); Warning after N consecutive | `client.go:758-800` | C |

### 4.2 TLS (`tls.*`) — MBAPS

The handshake is already explicit and bounded (`startTLS`,
`server_transport.go:717-752`). Classify its error with `errors.As`:

| Kind | Sev | Trigger / classification | Side |
|------|-----|--------------------------|------|
| `tls.cert_rejected` | Alert | `*tls.CertificateVerificationError` → `x509.UnknownAuthorityError`, `CertificateInvalidError{Expired,…}`, `HostnameError`. Carries the unverified leaf's subject, issuer, serial, NotAfter, SHA-256 fingerprint | S, C |
| `tls.no_client_cert` | Alert | client sent no certificate (`RequireAndVerifyClientCert`) | S |
| `tls.local_cert_rejected` | Warning | peer sent alert bad_certificate / unknown_ca / certificate_expired: the peer does not trust us | S, C |
| `tls.plaintext_on_tls_port` | Warning | `tls.RecordHeaderError` whose header bytes 2..3 are `00 00` (the MBAP protocol identifier of Modbus/TCP, spec MBAP header): a plain Modbus/TCP client talking to the MBAPS port | S |
| `tls.non_tls_probe` | Notice | `RecordHeaderError` with anything else (`GET `, `SSH-`, null probe) | S |
| `tls.handshake_failed` | Warning | other failures (version, cipher, protocol) | S, C |
| `tls.handshake_timeout` | Warning | `TLSHandshakeTimeout` exceeded: slow-handshake DoS | S |
| `tls.handshake_evicted` | Notice | `RejectTLSHandshakeEvicted` (`server_transport.go:118-126`); escalates to Warning when one host causes most evictions | S |
| `tls.role_invalid` | Warning | certificate carries several role OIDs or a badly encoded role (`server_transport.go:762-785`). The connection is still served with role `""`, as today | S |
| `tls.role_missing` | Notice | verified certificate without a role extension | S |
| `tls.cert_expiring` | Notice | peer leaf `NotAfter` within `CertExpiryWarning` (30 days) | S, C |
| `tls.cert_changed` | Notice | same peer IP, different leaf fingerprint than last time (history in the `netpeer.Cache`) | S, C |

TLS 1.2 is already the minimum (SECURITY.md), so the IEC `tls.weak_params`
event only fires if a later change ever allows lower versions; it is not
implemented now.

### 4.3 Peer identity (`peer.*`) — names shared with go-iecp5

| Kind | Sev | Trigger |
|------|-----|---------|
| `peer.mac_changed` | Alert | IP X seen with MAC M2 after M1, within `MACHistoryTTL` (30 days). ARP spoofing / MITM, or a device swap. Always delivered |
| `peer.mac_changed_midsession` | Critical | opt-in periodic re-check (`MACRecheckInterval`) finds a live connection's MAC changed |
| `peer.mac_pinning_violation` | Critical | pinned binding mismatch |
| `peer.ip_changed` | Info | known MAC with a new IP |
| `peer.mac_shared` | Notice | one MAC behind several on-link IPs (suppressed for `MACSource=Gateway`) |
| `peer.mac_unavailable` | Info | on-link peer, no neighbour entry; once per IP |
| `peer.identity_changed` | Alert | client only: the FC43 basic identity (VendorName, ProductCode, MajorMinorRevision) of a unit differs from the pinned or last seen one (§8.3). A changed revision alone is `peer.firmware_changed` (Warning, T0857 System Firmware) |

### 4.4 Addressing and access (`access.*`)

| Kind | Sev | Trigger | Where |
|------|-----|---------|-------|
| `access.unknown_function` | Warning | exception 01 answered (§3.6) | exception site |
| `access.illegal_address` | Warning | exception 02 answered | exception site |
| `access.unknown_unit` | Warning | exception 0A/0B answered, or `Policy.Units` set and the unit is not in it | exception site / before dispatch |
| `access.function_scan` | Alert | one connection or host hits ≥ `FunctionScanThreshold` distinct function codes answered with 01 within `ScanWindow` (default 8 in 60 s). Function-code sweep (T0846) | `internal/secmon` distinct-miss window |
| `access.unit_scan` | Alert | ≥ `UnitScanThreshold` distinct unit IDs within `ScanWindow` that the handler rejected (0A/0B, silent drop) or that are outside `Policy.Units` (default 10 in 60 s). Unit-ID sweep | idem |
| `access.address_scan` | Alert | ≥ `AddressScanThreshold` distinct (unit, FC, address) answered with 02 within `ScanWindow` (default 20 in 60 s). Point and register discovery (T0861) | idem |
| `access.function_blocked` | Warning | FC (or FC08 sub-function, or FC for the client's MBAPS role) denied by `Policy.Functions` (§6.3). Answered with exception 01; the handler is not called | before dispatch |
| `access.broadcast` | Notice → Warning | request to unit 0. The serial-line specification defines address 0 as broadcast; on Modbus/TCP the server serves it like any unit and the handler decides (server.go:136-143, ErrNoResponse doc). Notice for reads, Warning for writes | before dispatch |
| `access.oversized_request` | Warning | read or write quantity at the protocol maximum and the address range over `Policy.MaxRequestSpan` (opt-in), or FC20/FC21 with the maximum number of sub-requests. A legal request that is unusually large for the deployment | before dispatch |
| `access.write_denied` | Warning | from handlers via `secevent.WriteDenied(ctx, …)` | handler |
| `access.role_denied` | Warning | from handlers via `secevent.RoleDenied(ctx, fc)`, or `Policy.Functions` per role | handler / dispatch |

The library does not know the device's address space. Three layers cover that,
as in the IEC plan: (a) exception observation (§3.6), free; (b) optional
`Policy.Units` (allowed unit IDs) checked before dispatch, report only unless
`RejectUnknownUnits` (answered with exception 0B, Gateway Target Device Failed
to Respond, which is how the reference servers in INTEROPERABILITY.md answer an
unknown unit; **verify per stack before enabling by default**); (c)
`secevent.Report` from handlers.

### 4.5 Protocol validity (`proto.*`)

| Kind | Sev | Trigger | Where | Side |
|------|-----|---------|-------|------|
| `proto.bad_mbap` | Warning | MBAP length outside 2..254 (`ErrInvalidMBAPLength`) or protocol identifier ≠ 0 (`ErrUnknownProtocolID`). Server: `CloseMalformedFrame`. Client: each skipped frame (F11) | `server_transport.go:486`, `internal/transport/tcp.go:140-148, 177-199` | S, C |
| `proto.bad_pdu` | Warning | `ErrProtocolError` from dispatch: PDU too short for its FC, wrong fixed length, byte count ≠ data length; the server closes the link (`CloseProtocolError`) | `server_transport.go:561-567` | S |
| `proto.illegal_value` | Warning | exception 03 (§3.6) | exception site | S |
| `proto.trailing_bytes` | Notice | FC43 request with bytes after the Object Id (F12); any other FC the server accepts with surplus bytes found during implementation | `server_device_id.go:79` | S |
| `proto.reserved_subfunction` | Warning | FC08 sub-function not in the specification's list (spec §6.8.1), or MEI type 0–12 / 15–255 (spec §6.19: reserved) | before dispatch | S |
| `proto.user_function` | Notice | FC in the user-defined ranges 65–72 and 100–110 (spec §5, Figure 10) — answered 01 as today, but tagged apart from unknown public codes, because vendor tooling and exploit modules use them | dispatch default | S |
| `proto.bad_crc` / `proto.bad_lrc` | Notice (serial) / Warning (tunnelled) | `ErrBadCRC`, `ErrBadLRC`, `ErrShortFrame`. Noise is normal on a serial line, not on RTU over TCP/UDP. Aggregated | `internal/transport/rtu.go`, `ascii.go` | C |
| `proto.wrong_unit` | Warning | response from another unit than the request's (`ErrBadUnitID`) | client exec | C |
| `proto.response_invalid` | Warning | `*ProtocolError` on a response: byte count, echo, FC43 conformity level, pagination loop (client_device_id.go:426-545) | client exec | C |

### 4.6 Control and unusual functions, as an audit trail (`ctrl.*`, `cmd.*`, `recon.*`)

Not errors: operations an operator wants on record. All opt-out with
`Policy.AuditControl=false`. Detection runs before the handler, from the
request bytes, so it works whether or not the handler implements the function.

| Kind | Sev | Trigger | Spec |
|------|-----|---------|------|
| `ctrl.restart_comms` | Alert | FC08 / 0x0001 Restart Communications Option (with "clear log" flag in Detail). Restart (T0816). Always delivered | §6.8.1 |
| `ctrl.listen_only` | Critical | FC08 / 0x0004 Force Listen Only Mode: the device stops answering until restarted. DoS by one request (T0814, T0858). Always delivered | §6.8.1 |
| `ctrl.clear_counters` | Notice | FC08 / 0x000A Clear Counters and Diagnostic Register, 0x0014 Clear Overrun Counter: removes the evidence the counters held (T0872) | §6.8.1 |
| `ctrl.change_delimiter` | Warning | FC08 / 0x0003 Change ASCII Input Delimiter | §6.8.1 |
| `ctrl.file_write` | Alert | FC21 Write File Record: unit, file numbers, record ranges (T0843 when the files hold program or configuration) | §6.15 |
| `ctrl.file_read` | Notice | FC20 Read File Record (T0845) | §6.14 |
| `cmd.write` | Notice | every write: FC05, 06, 15, 16, 22, 23 (write part), with unit, FC, address, quantity. The write audit trail (T0855, T0836). Values only in `Raw` | §6.5–6.17 |
| `cmd.duplicate` | Warning | opt-in: byte-identical write PDU (unit + PDU) seen again within `DuplicateWriteWindow` on **another** connection of the same server. Modbus has no sequence numbers or time tags, so a replay over a new connection is otherwise invisible. Off by default: some masters legitimately re-send the same setpoint | — |
| `recon.device_id` | Info | FC43 / 14 request: category and object (T0888). Escalates via `flood.recon` | §6.21 |
| `recon.server_id` | Info | FC17 Report Server ID (T0888) | §6.13 |
| `recon.comm_log` | Info | FC0B, FC0C, FC07, and FC08 counter reads (0x000B–0x0012): device and bus state discovery | §6.7–6.11 |

### 4.7 Transaction and sequence (`seq.*`)

Modbus has no N(S)/N(R) and no FCB. What remains:

| Kind | Sev | Trigger | Side |
|------|-----|---------|------|
| `seq.txn_reused_inflight` | Notice | a pipelined request whose transaction ID equals one still pending on the same connection. The MBAP transaction identifier pairs requests and responses; reusing one in flight makes responses ambiguous. **Report only; verify the exact wording in the Modbus Messaging on TCP/IP Implementation Guide before raising the severity** | S |
| `unsol.response` | Warning | client receives a response with an unexpected transaction ID (F11): a late answer after a timeout, or an injected frame on the path (T0830). Carries `TransactionID` and `ExpectedTxnID` | C |
| `unsol.late_answer` | Notice | serial client discards bytes found on the line before a request (LIMITATIONS.md § Known deviations) | C |

### 4.8 Flooding and resource (`flood.*`, `dos.*`)

Token buckets per connection per class, defaults chosen so normal polling never
trips them (validated in §9 against `stress_parallel_test.go`, `bench_test.go`
and the interop suite):

| Kind | Class / default | Notes |
|------|-----------------|-------|
| `flood.requests` | all requests > 1000/s per connection | generic |
| `flood.writes` | write FCs > 100/s burst 200 | T0806 |
| `flood.diagnostics` | FC08 > 10/min | |
| `flood.recon` | FC17 + FC43 + FC0B/0C/07 > 10/min | repeated identification |
| `flood.exceptions` | exceptions answered > 20/s | escalates to `access.brute_force` (Alert, T0806) when mostly 02/03 on writes |
| `dos.pipeline_full` | the reader holds `serverPipelineDepth` (16) unanswered requests (`server_transport.go:319, 404-408`): the handler cannot keep up. Hysteresis: once until it drops below 8 | |
| `dos.write_stall` | `CloseWriteFailed` (the peer does not read its responses) | Action `Disconnected`, existing behaviour |
| `dos.slow_frame` | partial MBAP frame not complete within `FrameAssemblyTimeout` (opt-in) | slowloris; Action `Disconnected` |
| `dos.slow_handler` | handler time > `SlowHandlerThreshold` (opt-in) | a request that is expensive to serve |

`Policy.FloodAction`: `Report` (default) · `Throttle` (excess requests are not
dispatched; answer per Q4) · `Disconnect`. Note: the specification (§7)
defines exception 06 Server Device Busy for "specialized use in conjunction
with programming commands", so answering throttled requests with 06 is an
extension of its meaning, not something the specification prescribes.

### 4.9 Remote device (`remote.*`, client side)

| Kind | Sev | Trigger |
|------|-----|---------|
| `remote.exception` | Notice | exception received (§3.6) |
| `remote.busy` | Info | exception 06 repeatedly from one unit (aggregated) |
| `remote.silent` | Notice | a unit that answered before stops answering for N consecutive requests while others on the same link answer: possible listen-only mode (§4.6) |

---

## 5. Key mechanisms in detail

### 5.1 MAC lookup (`netpeer`)

As in the IEC plan (on-link decision first, gateway MAC reported as
`MACSource=Gateway`, loopback `None`, 200 ms bounded lookup in the
per-connection goroutine, never blocks or rejects unless pinning is set),
with backends that need no new module (F17, Q4):

- **Linux:** `syscall.NetlinkRIB(syscall.RTM_GETNEIGH, AF_UNSPEC)` +
  `syscall.ParseNetlinkMessage`, parse `NDA_DST`/`NDA_LLADDR`; fallback
  `/proc/net/arp`.
- **Darwin/FreeBSD:** `syscall.RouteRIB(NET_RT_FLAGS, RTF_LLINFO)` with a small
  hand parser of `rt_msghdr` + `sockaddr_dl`.
- **Windows:** `GetIpNetTable2` via `syscall.NewLazyDLL("iphlpapi.dll")`.
- **Others:** `ErrUnsupported`.

On the server the lookup runs in `handleTCPClient` before `serveConn`, so
`conn.accepted` and `ConnectionLifecycleMetrics.OnConnectionOpened` see the
same peer. On the client it runs after each dial, including pool dials and
re-dials, and only for network transports.

### 5.2 MAC/IP/cert history cache (`netpeer.Cache`)

Unchanged from the IEC plan: process-wide default, injectable, LRU 4096,
`MACHistoryTTL`, optional `Save/Load` (JSON). The mid-session re-check needs a
timer per connection on the server (the Modbus server has no per-connection
ticker): one `time.AfterFunc` per connection, only when `MACRecheckInterval > 0`.

### 5.3 Connection tracker (server)

In the accept loop, after `AllowClient` (which keeps running first, unchanged)
and before the `MaxClients` check, so rejected peers cost almost nothing:

1. `Policy.AllowedPeers` → `conn.unexpected`.
2. Per-host and global accept-rate buckets → `conn.rate`.
3. `MaxClientsPerHost` → `conn.rejected_per_host`.
4. Existing `MaxClients` / TLS eviction.
5. Connection goroutine: TLS handshake (classified, §4.2) → MAC lookup and
   pinning → `serveConn`.

New `RejectReason` constants (additive): `RejectPerHostLimit`, `RejectRate`,
`RejectPeerBinding`. Connections rejected by the policy are reported to
`ConnectionMetrics.OnConnectionRejected` like the existing ones, and take no
`MaxClients` slot.

### 5.4 Request classification before dispatch

One pure function, `classifyRequest(req) requestClass`, run in `serveRequest`
before `safeDispatch` when a dispatcher or a function policy is set. It reads
FC, FC08 sub-function, MEI type, unit, address and quantity from the PDU
without allocating and returns flags (write, diagnostic-control, recon, file,
broadcast, reserved). It drives §4.6 audit events, §4.8 buckets and §6.3
function policy. It is fuzzed (it parses untrusted input).

### 5.5 Client-side detection

- `internal/transport` gets an `Observer` interface
  (`OnAnomaly(kind, txnID, expected uint16, raw []byte)`), nil by default,
  called at the existing log sites (F11, CRC/LRC, discard). The root package
  adapts it to the dispatcher.
- `executeRequest` (`client_exec.go`) emits `remote.exception`,
  `proto.wrong_unit`, `proto.response_invalid`.
- `Config.AllowedFunctionCodes` already blocks function codes on the client;
  `ErrFunctionCodeNotAllowed` becomes a `client.function_blocked` Info event
  (local policy hit, useful to spot a compromised caller).

---

## 6. Policy / configuration surface

### 6.1 Structs

```
secevent.Policy {                       // protocol-neutral, same as go-iecp5
    Sink            Sink
    Dispatcher      *Dispatcher         // shared; nil = endpoint creates one from Sink
    AggregateWindow time.Duration
    MaxRaw          int
    AuditControl    bool                // default true when a sink is set

    AllowedPeers    []netip.Prefix
    RejectUnlisted  bool
    PeerBindings    map[netip.Addr]net.HardwareAddr
    RejectBindingMismatch bool
    AcceptRate, GlobalAcceptRate Rate

    Resolver        netpeer.Resolver
    MACCache        *netpeer.Cache
    MACLookupTimeout, MACRecheckInterval, MACHistoryTTL time.Duration
    CertExpiryWarning time.Duration
}

modbus.SecurityPolicy {
    secevent.Policy

    // Connection (server)
    MaxClientsPerHost    uint
    FirstRequestTimeout  time.Duration  // 0 = off (F6)
    FrameAssemblyTimeout time.Duration  // 0 = off (F6)

    // Application (server)
    Units                []uint8        // nil = any
    RejectUnknownUnits   bool
    Functions            *FunctionPolicy
    MaxRequestSpan       uint16         // 0 = off
    DuplicateWriteWindow time.Duration  // 0 = off
    SlowHandlerThreshold time.Duration  // 0 = off

    // Detection
    Limits               Limits         // per class, §4.8
    FloodAction          secevent.Action
    ScanWindow           time.Duration
    FunctionScanThreshold, UnitScanThreshold, AddressScanThreshold int

    // Client
    IdentityPins         map[uint8]DeviceIdentity // §8.3
    CheckIdentityOnOpen  bool
}
```

- `modbus.DefaultSecurityPolicy()` is report-only with the §4.8 thresholds
  (Q2 there: report only).
- Validation follows the existing `newConfigurationError` style and runs in
  `NewServer` / `New`, so `ValidateServerConfig` and `ValidateConfig` catch it.

### 6.2 Where it plugs in (additive only)

- `ServerConfig.Security *SecurityPolicy` (pointer: `ServerConfig` stays
  comparable, F15). `Server.SecurityStats() secevent.Stats`.
- `Config.Security *SecurityPolicy` on the client, and in
  `ObservabilityConfig` for `NewConfig`. `Client.Peer()`, `Client.Peers()`,
  `Client.SecurityStats()`.
- No new methods on any exported interface. Handlers get peer data through
  `ctx` (§3.2).

### 6.3 `FunctionPolicy` (hardening, server)

```
FunctionPolicy {
    Deny            []FunctionCode
    DenyDiagnostics []DiagnosticSubFunction   // e.g. 0x0001, 0x0004
    DenyFileWrite   bool                      // FC21
    ReadOnly        bool                      // deny FC05, 06, 15, 16, 22, 23 (FC23 entirely)
    DenyBroadcastWrites bool                  // unit 0
    ByRole          map[string][]FunctionCode // MBAPS role → allowed FCs; role "" = no role
}
```

A denied request is answered with exception 01 (as the server already answers
any function it does not serve) and reported as `access.function_blocked`; the
handler is not called and `ServerMetrics.OnError` sees `ErrIllegalFunction`, as
for an unimplemented optional handler. `ByRole` gives the MBAPS role-based
authorization the specification leaves to the application, without handler
code. INTEROPERABILITY.md's request table gains one row (after "function code
not implemented") describing it.

### 6.4 Stats

`secevent.Stats`: counters per Kind and Category, dropped, aggregated, current
connections, rejected connections by reason. Snapshot by value. No Prometheus
dependency (OBSERVABILITY.md's existing stance).

---

## 7. Robustness changes found during analysis

Most of the IEC plan's §7 already exists here (F1: accept back-off; write
deadline per response, `server_transport.go:431`; explicit TLS handshake with
deadline, `startTLS`). What remains, all **opt-in**, so no default wire change:

1. **Per-host limit and accept rate** (F3). Removes the gap LIMITATIONS.md
   § Security documents; that paragraph is rewritten to say what the options
   do and what they still cannot do (spoofed sources, SYN floods).
2. **First-request timeout** (F6). A connection that sends no complete request
   within `FirstRequestTimeout` is closed (`conn.no_request`,
   `CloseIdleTimeout`). Shorter than the idle `Timeout`, it frees `MaxClients`
   slots held by idle sockets. Regression test next to
   `stress_slowloris_test.go`.
3. **Frame-assembly timeout** (F6). Once the first MBAP header byte is read,
   the frame must complete within `FrameAssemblyTimeout`; `dos.slow_frame`.
   Implemented in `internal/transport/tcp.go` (`readMBAPFrameRaw`): set a
   deadline after the first byte, restore the idle deadline after the frame.
   `make soak` and `make fuzz` required.
4. **FC43 conformity-level cap** (F13) — the one default behaviour change, §8.2.

Each is its own commit with a test and lands before the event work that
reports it.

---

## 8. FC43 Read Device Identification

### 8.1 Current support (verified in this analysis)

| Part | State | Where |
|------|-------|-------|
| Client, stream access 01–03, pagination over MoreFollows, loop protection (256 pages, repeated NextObjectId), conformity-level and category consistency, duplicate-object checks, trailing data | done | `client_device_id.go:360-546`, `fc43_test.go` |
| Client, individual access 04 (MoreFollows must be 00, exactly one object) | done | `client_device_id.go:501-531` |
| `ReadAllDeviceIdentification` (extended) | done | `client_device_id.go:553` |
| Server, `DeviceIdentificationHandler`: category filtering, ordering, packing into 246 bytes, MoreFollows / NextObjectId, unknown start object restarts at 0, individual miss → exception 02, code outside 1..4 → exception 03, MEI ≠ 14 → exception 01, conformity derived when 0 | done | `server_device_id.go`, `server_device_id_test.go` |
| Detection probe uses FC43 | done | `internal/protocol/detect.go:66-75` |
| Interop: FC43 against j2mod, modbus-esp8266, modbus4j (some through raw PDUs) | recorded | INTEROPERABILITY.md lines 141-144 |
| MEI type 13 (CANopen) | not supported, documented | LIMITATIONS.md |
| `modbus-cli` operation | **missing** | F14 |

### 8.2 Server fix: respond at the actual conformity level (F13, behaviour change)

Spec §6.21: "If the server device is asked for a description level (readDevice
Code) higher that its conformity level, It must respond in accordance with its
actual conformity level." Today the filter uses the requested category only.

Change in `buildStreamDeviceIDResponse`: when the handler sets
`ConformityLevel`, filter with `min(requested category, ConformityLevel & 0x7F)`.
When it leaves it 0 the level is derived from the objects, so nothing changes.
The response keeps echoing the requested ReadDevId code ("Same as request",
spec §6.21 response description).

- Only handlers that declare a level **lower** than the objects they return
  are affected; they stop leaking objects above their level. Release note and
  INTEROPERABILITY.md FC43 paragraph updated; `make interop` re-run.
- Event `recon.device_id` carries the category requested and the level served.
- Open, not changed without a decision (Q10): individual access (code 04) when
  the declared level has no 0x80 bit ("stream access only"). The specification
  does not say which exception to answer; the server keeps serving it.

### 8.3 Client: FC43 as an identity source

```
type DeviceIdentity struct { VendorName, ProductCode, Revision string }
func (di *DeviceIdentification) Identity() DeviceIdentity      // from objects 0x00–0x02
func (mc *Client) CheckIdentity(ctx, unitID) (DeviceIdentity, error)
```

- `CheckIdentity` reads basic identification (code 01, one request for most
  devices), compares with `SecurityPolicy.IdentityPins[unit]` or, when none,
  with the last identity seen for (peer IP, unit) in the `netpeer.Cache`
  (stored alongside MAC and cert fingerprint), and emits
  `peer.identity_changed` / `peer.firmware_changed`.
- `CheckIdentityOnOpen` runs it after every (re)dial for the pinned units.
  Off by default; a device without FC43 (exception 01) yields
  `peer.identity_unavailable` (Info) once.
- `ErrIdentityMismatch` (new sentinel, ERRORS.md) returned when a pin is set and
  `RejectIdentityMismatch` is true; the connection is closed.
- Caveat in SECURITY.md: FC43 data is self-reported and spoofable; it detects
  device swaps and firmware changes, it does not authenticate. Only MBAPS
  authenticates.

### 8.4 `modbus-cli devid`

```
devid                     basic stream (code 01)
devid:regular|extended    stream 02 / 03, all pages
devid:<objid>             individual access (code 04), objid hex or decimal
```

Output: conformity level, then one line per object
(`0x00 VendorName  "…"`); `--json` like the other operations. Exit codes as
in `docs/exitcodes.md` (exception → ExitProtocol). Parser, help text,
completion, `docsync_manifest_test.go` and `e2e_test.go` updated. The CLI
module bumps its go-modbus requirement only if it needs §8.3.

### 8.5 FC43 tests to add

- Server: handler with `ConformityLevel 0x81` and private objects; extended
  request returns basic objects only (regression for F13).
- Server: request with trailing bytes → served as today + `proto.trailing_bytes`.
- Client: identity pin match, mismatch, firmware-only change, device without
  FC43.
- Fuzz: extend `fuzz_test.go` with server FC43 requests (any 0–253 byte payload)
  through `serveRequest` with a sink attached.

---

## 9. Performance and safety requirements

- No sink, no policy → one atomic pointer load per request and per accept. No
  detector state is updated. Benchmarks: `bench_test.go` and
  `internal/benchserver` with nil and with a no-op `SinkFunc`. Target < 2 %
  throughput loss with a sink, 0 allocations per request on the happy path.
- Buckets and windows per connection are owned by the serving goroutine
  (lock-free). Shared structures (conn tracker, MAC cache, write dedup) use one
  mutex each, bounded.
- Every detector is bounded against hostile input (LRU caps on aggregation keys,
  distinct-miss windows, dedup hashes, MAC cache).
- `Detail` is built lazily, only when an event is emitted.
- Threshold defaults produce **zero** events in `stress_parallel_test.go`,
  `stress_lifecycle_test.go`, the chaos suite and the interop suite run with
  `DefaultSecurityPolicy()` and a counting sink (asserted in tests).

---

## 10. Tests

| Layer | Tests |
|-------|-------|
| `secevent` | non-blocking under a slow sink; drops and `meta.events_dropped`; aggregation; always-deliver kinds; sink panic via `callback.Run`; `Stop` from a sink (style of `server_stop_test.go`); JSON golden file; Kind table completeness |
| `netpeer` | netlink, route-socket and `/proc/net/arp` parsers on canned bytes (**fuzz**); on-link logic; cache change detection, TTL, LRU, Save/Load; fake Resolver |
| `internal/secmon` | bucket math; distinct-miss window; dedup window expiry |
| `classifyRequest` | table tests over every FC and FC08 sub-function; **fuzz** |
| Server, socket level (style of `hostile_server_test.go`, `server_connection_hooks_test.go`) | one test per wire-triggerable Kind: connect-no-request; AllowedPeers reject; per-host limit; accept rate; first-request and frame-assembly timeouts; bad MBAP length and PID; short PDU; exceptions 01/02/03/0B; function, unit and address sweeps; FC08 0x0001/0x0004/0x000A; FC21; FC43 and FC17 recon; writes audit; duplicate write on a second connection; pipeline full (handler blocked, 16+ pipelined requests); write stall (client that never reads); function policy incl. `ByRole` |
| Server TLS (style of `server_tls_test.go`) | unknown CA, expired, no client cert, plaintext Modbus/TCP on the TLS port, HTTP probe, handshake timeout, eviction, role invalid / missing (certs generated in-test) |
| Client | TID mismatch (`unsol.response`) with `client_scripted_test.go`; wrong unit; CRC errors on RTU over TCP; exceptions; server cert rejected; identity pin |
| MAC end-to-end | Linux-only, skipped unless `GOMODBUS_NETNS=1` and root: veth pair in a netns, change the MAC, reconnect → `peer.mac_changed` |
| Regression | `make check` (race, coverage minimum unchanged); `make interop` green with policy unset **and** with `DefaultSecurityPolicy()` + sink: no wire change except §8.2; `make soak` for §7.2–7.3 |
| Fuzz | existing `fuzz_test.go` targets run with a sink attached: every malformed input yields an event, never a panic or blocked goroutine |

---

## 11. Phased delivery (each phase = one PR, green CI, RELEASE.md section)

| Phase | Content | Depends | Release |
|-------|---------|---------|---------|
| **P0** | Decide §12; freeze Kind names shared with go-iecp5 | — | — |
| **P1** | FC43: `modbus-cli devid` (§8.4); conformity-level cap (§8.2) with tests and INTEROPERABILITY.md update; `make interop` | — | v1.20.0 |
| **P2** | `secevent` core: Event, Kinds, Dispatcher, sinks, `Report(ctx)`, `PeerFromContext`, Stats; `ServerConfig.Security`; `conn.accepted/closed/no_request/idle_timeout/accept_error`, mapping of existing reject and close reasons | P0 | v1.21.0 |
| **P3** | §7.1–7.3 opt-in robustness (per-host limit, accept rate, first-request and frame-assembly timeouts) and their events; `make soak` | P2 | v1.22.0 |
| **P4** | TLS classification and `tls.*`, role events | P2 | |
| **P5** | `classifyRequest`; exception observation (§3.6); `access.*`, `proto.*` (server); `ctrl.*`, `cmd.*`, `recon.*` audit; scan detectors; `FunctionPolicy` | P2 | v1.23.0 |
| **P6** | Flood detectors and actions; `dos.*` | P5 | |
| **P7** | Client: transport Observer, `unsol.*`, `proto.*` (client), `remote.*`, `conn.dial_failed`, `Client.Peer()` | P2 | v1.24.0 |
| **P8** | `netpeer` Linux + cache; `peer.*`; pinning; mid-connection re-check | P2 | |
| **P9** | FC43 identity (§8.3), `peer.identity_changed` / `firmware_changed` | P7, P8 | v1.25.0 |
| **P10** | `netpeer` Windows and Darwin/BSD | P8 | |
| **P11** | Docs (below) and examples: `examples/tcp_server` with a JSON sink and a policy; new `examples/security_events` | all | |

Docs per phase, not only at the end (Definition of Done): API.md (new § 14
"Security events" and § 3.1 / § 2.1 fields), ERRORS.md (`ErrIdentityMismatch`,
secevent errors), INTEROPERABILITY.md (FC43 cap, function-policy row),
LIMITATIONS.md (per-host limit paragraph rewritten; "failed TLS handshake is
logged only" removed; MAC caveats; what user space cannot see), SECURITY.md
(event channel, FC43 identity caveat, deployment guidance), OBSERVABILITY.md
(how events relate to Logger, metrics and PDU taps), ARCHITECTURE.md (new
packages and import rules), README.md, `cmd/modbus-cli/README.md`.

Rough size: P2–P6 are the bulk (~2.5–3 k lines with tests); the rest ≤ 1 k each.

---

## 12. Open questions (decisions needed before P2)

Decisions taken in the go-iecp5 plan, plus Q1, Q2 and Q11 below (answered 2026-11-21), and carried over unless you say otherwise:
split packages (Q1 there), report-only defaults (Q2), `conn.accepted` at Info
(Q3), JSON as the output format (Q5), one dispatcher per endpoint unless shared
(Q8).

1. **Shared modules or in-repo copies?** **Decided: no shared modules.**
   `secevent` and `netpeer` live in this repository, with the same names,
   Kind spellings and JSON form as in go-iecp5. A change to one is mirrored in
   the other by hand; a golden JSON file in each repo guards the format.
2. **New dependency for MAC lookup.** **Decided: standard library `syscall`
   only** (§5.1). `go.mod` keeps its single dependency.
3. **`RejectUnknownUnits` answer:** exception 0B (what the reference servers
   answer for an unknown unit, INTEROPERABILITY.md), or 0A, or silence? Default
   stays report-only either way.
4. **`FloodAction=Throttle` answer:** exception 06 (Server Device Busy; the
   specification ties it to programming commands, so this stretches its
   meaning), exception 04, or no answer (the client times out, like
   `ErrNoResponse`)? Recommendation: no answer, which invents nothing.
5. **`cmd.duplicate`** (replayed writes across connections): ship it opt-in as
   proposed, or leave it out because legitimate masters re-send setpoints?
6. **`cmd.write` audit by default** when a sink is set (proposed), or only with
   `AuditControl`? It is one event per write; busy masters write a lot
   (aggregation applies).
7. **Client identity check:** `CheckIdentityOnOpen` opt-in (proposed), or on by
   default when `IdentityPins` is set?
8. **`seq.txn_reused_inflight`:** keep at Notice until the Messaging
   Implementation Guide wording is checked (proposed)?
9. **FC43 trailing bytes (F12):** report only (proposed), or close the
   connection like the fixed-length function codes (wire change)?
10. **FC43 individual access with a stream-only conformity level:** keep
    serving it (proposed; the specification names no exception), or answer
    exception 01 / 03?
11. **FC43 conformity cap (§8.2):** **Decided: ship as a behaviour change in
    a minor release (P1, v1.20.0), no option.** It is what the specification
    requires and only affects handlers that declare a lower level than they
    serve. RELEASE.md and INTEROPERABILITY.md say so explicitly.
