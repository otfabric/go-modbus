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
- The server passes the role from the client certificate's Modbus role extension to handlers as `ClientRole`. Authorization is the handler's job: check the role before serving or writing data

### Restricting Destinations

`Config.Dialer` sets the function that opens every network connection of a client (`tcp`, `tcp+tls`, `udp`, `rtuovertcp`, `rtuoverudp`, `asciiovertcp`), including the ones it dials again after a lost connection and the connections of the pool. An application that lets users reach only approved hosts should enforce the allowlist there, for example in a `net.Dialer.Control` hook, which sees the resolved IP right before connecting; checking the host name in `Config.URL` before `New` leaves a DNS-rebinding gap. The serial transports (`rtu`, `ascii`) do not dial, and `modbus-cli` does not set a dialer. For `tcp+tls` the TLS handshake and certificate checks run on the connection the dialer returns, against the host name of the URL, so a dialer that sends the connection elsewhere (a proxy, a pinned address) does not weaken them. See [API.md § 2.1](API.md#21-config).

### Running a Server

- A plain `tcp://` server accepts any client that can reach it. Bind it to a specific interface rather than all interfaces where you can, and restrict access at the network level
- Set `MaxClients` and `Timeout` so that idle or excess connections cannot exhaust the server. A client that sends requests but does not read the answers is disconnected once a response cannot be written within `Timeout`
- On a `tcp+tls://` server a connection counts against `MaxClients` from accept, before its TLS handshake, which must complete within `TLSHandshakeTimeout` (default 10 s). When every slot is taken, a new connection replaces the oldest one that has not completed its handshake, taken from the host with the most such connections; connections that completed their handshake are never closed to make room. Peers that keep opening connections without handshaking can still delay clients from the same host, and many hosts can still crowd out a client whose handshake is slow: limit connections per source at the network edge (firewall, rate limiter) where untrusted hosts can reach the listener
- Handlers receive data from the network: validate addresses, quantities and values before acting on them, especially for writes that drive physical equipment. A panic in a handler is recovered and answered with a Server Device Failure exception, but it is not a substitute for validation
- `ServerMetrics` hooks receive the unit ID and the function code as the client sent them, `OnRequest` before the server has checked the function code: bound them before using them as an index or a label. A hook that does not return normally is logged with its stack, and the request is answered as usual
- When the listener cannot accept (no file descriptors left, for example), the server retries with a wait that grows from 5 ms to 1 s between attempts and logs each failure
- A handler that serves state shared between connections returns a copy of it: the server encodes the returned slice after the handler has returned ([API.md § 13](API.md#13-ownership-of-slices))

### Writing to Devices

- Writes change the state of physical equipment. Validate what your application writes and to which unit
- With a `RetryPolicy`, a write can be delivered more than once if the response is lost after the request was sent. Do not enable retries for writes that are not safe to repeat (see [API.md](API.md))

### Logging and Metrics

- Logging is off by default. Debug-level logging prints every frame sent and received, including register values; treat such logs as sensitive operational data (see [OBSERVABILITY.md](OBSERVABILITY.md))

### Dependencies

`make check` runs `govulncheck`. Vulnerabilities reported in the Go standard library are fixed by building with a patched Go release.
