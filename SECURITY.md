# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability in `go-modbus`, please report it responsibly.

**Do not open a public GitHub issue for security vulnerabilities.**

Instead, please email **security@otfabric.com**, or open a private advisory on GitHub (the [Security](https://github.com/otfabric/go-modbus/security) tab → **Advisories** → **Report a vulnerability**), with:

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

### Running a Server

- A plain `tcp://` server accepts any client that can reach it. Bind it to a specific interface rather than all interfaces where you can, and restrict access at the network level
- Set `MaxClients` and `Timeout` so that idle or excess connections cannot exhaust the server
- Handlers receive data from the network: validate addresses, quantities and values before acting on them, especially for writes that drive physical equipment. A panic in a handler is recovered and answered with a Server Device Failure exception, but it is not a substitute for validation

### Writing to Devices

- Writes change the state of physical equipment. Validate what your application writes and to which unit
- With a `RetryPolicy`, a write can be delivered more than once if the response is lost after the request was sent. Do not enable retries for writes that are not safe to repeat (see [API.md](API.md))

### Logging and Metrics

- Logging is off by default. Debug-level logging prints every frame sent and received, including register values; treat such logs as sensitive operational data (see [OBSERVABILITY.md](OBSERVABILITY.md))

### Dependencies

`make check` runs `govulncheck`. Vulnerabilities reported in the Go standard library are fixed by building with a patched Go release.
