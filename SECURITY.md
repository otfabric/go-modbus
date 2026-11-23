# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in `go-modbus`, please report it responsibly.

**Do not open a public GitHub issue for security vulnerabilities.**

Instead, please email **security@otfabric.io**, or open a private advisory on GitHub (the [Security](https://github.com/otfabric/go-modbus/security) tab → **Advisories** → **Report a vulnerability**), with:

- A description of the vulnerability
- Steps to reproduce the issue, ideally a minimal program or test
- The affected version(s)
- Any potential impact you've identified

We will acknowledge your report within 48 hours and aim to provide a fix or mitigation within 7 days for critical issues.

## Supported Versions

| Version | Supported |
|---------|-----------|
| Latest release | Yes |
| Older releases | Best effort |

## Security Considerations

### Modbus Has No Built-In Security

Modbus TCP, UDP, RTU and ASCII carry no authentication and no encryption: anyone who can reach a device can read and write it, and anyone on the path can read or alter the traffic. Of the transports this library offers, only `tcp+tls://` (Modbus Security, MBAPS) protects the connection.

- **Keep plain Modbus traffic on isolated, trusted networks**; never expose port 502 to the Internet
- **Use `tcp+tls://` across untrusted networks**, or put a VPN or secured gateway in front of the device

### TLS (`tcp+tls://`)

- Both sides authenticate: the client requires a client certificate (`TLSClientCert`) and the CA or server certificates it trusts (`TLSRootCAs`); the server requires a server certificate and the CAs it accepts client certificates from. A configuration without them is rejected
- TLS 1.2 is the minimum version
- Keep private keys out of source control and give key files restrictive permissions (`chmod 600`)
- The server passes the role from the client certificate's Modbus role extension to handlers as `ClientRole`. The library attaches no meaning to a role: what a role may do is the application's decision, made in `ServerConfig.Authorizer` (below) or in the handlers. A certificate whose role extension the server cannot use (more than one, or not exactly one DER UTF8String) is served with no role; `TLSHandshakeMetrics` reports it with `RoleErr` wrapping `ErrInvalidRole`, and the authorizer sees the same error in `AuthorizationRequest.ClientRoleErr`, so a policy can refuse such a client while still serving one without a role

### Restricting Destinations

`Config.Dialer` sets the function that opens every network connection of a client (`tcp`, `tcp+tls`, `udp`, `rtuovertcp`, `rtuoverudp`, `asciiovertcp`), including the ones it dials again after a lost connection and the connections of the pool. An application that lets users reach only approved hosts should enforce the allowlist there, for example in a `net.Dialer.Control` hook, which sees the resolved IP right before connecting; checking the host name in `Config.URL` before `New` leaves a DNS-rebinding gap. The serial transports (`rtu`, `ascii`) do not dial, and `modbus-cli` does not set a dialer. On Windows they open a device name only (`COM3`, `\\.\COM10`, a device interface name); `Config.SerialUncheckedAddress` opens the device of the URL as given, so set it only for a device from a trusted source. For `tcp+tls` the TLS handshake and certificate checks run on the connection the dialer returns, against the host name of the URL, so a dialer that sends the connection elsewhere (a proxy, a pinned address) does not weaken them. See [API.md § 2.1](API.md#21-config).

### Running a Server

- A plain `tcp://` server accepts any client that can reach it. Bind it to a specific interface rather than all interfaces where you can, and restrict access at the network level
- `ServerConfig.AllowClient` refuses a `tcp` or `tcp+tls` client by its remote address as soon as it is accepted, before anything is read from it and without taking a `MaxClients` slot. It is an allow-list inside the process, not a substitute for network-level filtering: the TCP connection has been established when it runs, a source address can be spoofed on a network you do not control, and on `tcp+tls` it runs before the handshake, so it cannot decide on the client certificate
- Set `MaxClients` and `Timeout` so that idle or excess connections cannot exhaust the server. A client that sends requests but does not read the answers is disconnected once a response cannot be written within `Timeout`
- On a `tcp+tls://` server a connection counts against `MaxClients` from accept, before its TLS handshake, which must complete within `TLSHandshakeTimeout` (default 10 s). When every slot is taken, a new connection replaces the oldest one that has not completed its handshake, taken from the host with the most such connections; connections that completed their handshake are never closed to make room. Peers that keep opening connections without handshaking can still delay clients from the same host, and many hosts can still crowd out a client whose handshake is slow: set `MaxClientsPerHost` (below), and limit connection rates at the network edge (firewall, rate limiter) where untrusted hosts can reach the listener
- Handlers receive data from the network: validate addresses, quantities and values before acting on them, especially for writes that drive physical equipment. A handler that does not return normally is stopped there and answered with a Server Device Failure exception, but that is not a substitute for validation
- `ServerMetrics` hooks receive the unit ID and the function code as the client sent them, `OnRequest` before the server has checked the function code: bound them before using them as an index or a label. A hook that does not return normally is logged with its stack, and the request is answered as usual
- When the listener cannot accept (no file descriptors left, for example), the server retries with a wait that grows from 5 ms to 1 s between attempts and logs each failure; `AcceptErrorMetrics` reports each one
- A handler that serves state shared between connections returns a copy of it: the server encodes the returned slice after the handler has returned ([API.md § 13](API.md#13-ownership-of-slices))

### Opt-in Server Hardening

Since v1.21.0. Every setting here is off by default, and the library chooses no thresholds: the right values depend on the clients, the network and the device. Left at zero, the server behaves as before.

| Setting | Bounds | Reported as |
|---|---|---|
| `MaxClientsPerHost` | connections at once from one IP address, handshaking ones included; refused at accept, before anything is read | `RejectPerHostLimit` |
| `FirstRequestTimeout` | time from the start of serving (after the TLS handshake) to the first complete request: a client that connects and stays silent | `CloseFirstRequestTimeout` |
| `FrameTimeout` | time from a request's first byte to its last: a client that trickles a request in, byte by byte, to hold the connection | `CloseFrameTimeout` |
| `Authorizer` | which requests reach the handlers (below) | `ServerMetrics.OnError` with `ErrNotAuthorized` |

- `MaxClientsPerHost` counts IP addresses: clients behind one NAT, proxy or gateway share a host, so size it for the largest legitimate group, or leave it off where that is unknown
- `FrameTimeout` well above the round-trip time of the slowest legitimate client avoids cutting off a request split over several TCP segments; it runs alongside `Timeout`, and the earlier of the two ends the wait
- There is no rate limit on new connections: a global rate would let one peer lock out every client, and per-peer rates need state that belongs in a firewall or a rate limiter in front of the listener

### Request Authorization

`ServerConfig.Authorizer` is asked about every request before a handler serves it, with the client's address and role, the unit, the function code, whether it reads, writes or diagnoses, and the addresses and quantities it touches ([API.md § 3.6](API.md#36-request-authorization)). A refused request never reaches the handler, so it has no effect on the device; the client is answered with Illegal Function (01), or the exception the authorizer chose. An authorizer that panics refuses the request (Server Device Failure): a failed authorizer never lets a request through. Typical policies: read-only for clients without an operator role, no writes to unit 0, writes only to an address range. `AccessDiagnostic` is not read-only: FC08 sub-functions restart the communications port, change the ASCII delimiter, force Listen Only Mode or clear counters (V1.1b3 section 6.8.1), so a read-only policy checks `SubFunction`, as [examples/security_hooks](examples/security_hooks/security_hooks.go) does.

The authorizer is a check inside the process. Role-based decisions are only as strong as the certificate that carries the role: they are meaningful on `tcp+tls://`, where the role comes from a verified certificate; on `tcp://` the address is all there is, and it can be spoofed on a network you do not control.

### What the Library Is Not

The observation hooks ([OBSERVABILITY.md](OBSERVABILITY.md), "Security-relevant facts") report what the library sees: refused and closed connections and why, TLS handshakes and their certificates, framing violations, refused requests. go-modbus is not an intrusion detection or prevention system, nor a SIEM: it has no detection rules, no severities, no event schema, no alerting and no dependency on a monitoring product. Deciding what is suspicious, correlating it and forwarding it is the application's. The facts are not a complete record of what reached the host: what the operating system or the TLS stack dropped before the library saw it is not reported.

### Writing to Devices

- Writes change the state of physical equipment. Validate what your application writes and to which unit
- With a `RetryPolicy`, a write can be delivered more than once if the response is lost after the request was sent. Do not enable retries for writes that are not safe to repeat (see [API.md](API.md))

### Logging and Metrics

- Logging is off by default. Debug-level logging prints every frame sent and received, including register values; treat such logs as sensitive operational data (see [OBSERVABILITY.md](OBSERVABILITY.md))
- A `PDUTap`, on the client or the server (`ServerConfig.PDUTap`), receives every request and response PDU, register values and write payloads included: treat what it records the same way. On the server the request PDUs are what clients sent, at most 253 bytes each, and the tap sees requests the handlers will reject: bound and validate them before using them as an index or a label

### Dependencies

`make check` runs `govulncheck`. Vulnerabilities reported in the Go standard library are fixed by building with a patched Go release.
