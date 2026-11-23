# Support and feature matrix

What go-modbus supports, how each feature is tested, and against what. Status: v1.23.1. Function-by-function detail, server behaviour on invalid requests and per-stack results are in [INTEROPERABILITY.md](../INTEROPERABILITY.md); what is out of scope is in [LIMITATIONS.md](../LIMITATIONS.md).

None of this is a conformance claim: nothing is certified, and no Modbus Organization conformance test has been attempted.

## 1. Versions

| go-modbus | Go | modbus-interop | Supported |
|---|---|---|---|
| **v1.23.1** | 1.26 or later (CI: 1.26 and 1.27) | v0.4.2 | **yes** |

Security fixes: see [SECURITY.md](../SECURITY.md).

## 2. Platforms

| Platform | Tested | Notes |
|---|---|---|
| Linux amd64 | the test suite with the race detector; Interop | CI runs on `ubuntu-latest` |
| Linux and macOS | serial tests on a pseudo-terminal | they skip on other platforms |
| Linux 386 | the test suite without the race detector, nightly | a 32-bit `int`; the race detector does not exist for 386 |
| Linux arm64 and arm, macOS arm64, Windows amd64 | `make cross` builds them without cgo | a build, not a test run |

## 3. Feature support

✅ supported · ◐ partly (see the notes) · — not implemented. **Own tests**: this repository's client and server, or a scripted transport. **Independent**: software that is not go-modbus exchanged it with go-modbus in the Interop workflow (modbus4j, whose image is not published, only in a local run: [INTEROPERABILITY.md](../INTEROPERABILITY.md#verification-status)).

| Feature | Client | Server | Own tests | Independent |
|---|:---:|:---:|:---:|---|
| Modbus TCP (MBAP), IPv4 and IPv6 | ✅ | ✅ | ✅ e2e, chaos, stress | libmodbus, PyModbus, digitalpetri/modbus, NModbus, tokio-modbus, j2mod, jsmodbus, modbus-esp8266 (client and server); modbus4j |
| Modbus over TLS (MBAPS) | ✅ | ✅ | ✅ e2e | none |
| Modbus RTU, Modbus ASCII | ✅ | — | ✅ pseudo-terminal | none |
| RTU over TCP | ✅ | — | ✅ | client: j2mod, jsmodbus, modbus-esp8266; modbus4j |
| Modbus UDP | ✅ | — | ✅ | client: j2mod; modbus4j |
| RTU over UDP, ASCII over TCP | ✅ | — | ✅ | none |
| Standard function codes | ✅ | ◐ the optional ones need a handler | ✅ e2e | Modbus TCP, per stack in [INTEROPERABILITY.md](../INTEROPERABILITY.md#verification-status) |
| Vendor and user-defined function codes | — | — | n/a | n/a |
| FC43 sub-type 13 (CANopen general reference) | — | — | n/a | n/a |
| Function code allowlist (`Config.AllowedFunctionCodes`) | ✅ | n/a | ✅ unit | n/a |
| Custom dialer (`Config.Dialer`): allowlists, proxies, in-memory connections | ✅ all network transports | n/a | ✅ unit, e2e (TLS), chaos | n/a |
| Batch reads (`batch`) | ✅ | n/a | ✅ property tests, fuzz | none |
| Value codecs (`codec`) | ✅ | n/a | ✅ | none |
| SunSpec marker and model headers (`sunspec`) | ◐ no points; use go-sunspec | n/a | ✅ e2e | none |
| Opt-in server hardening (`MaxClientsPerHost`, `FirstRequestTimeout`, `FrameTimeout`) | n/a | ✅ | ✅ unit | none |
| Request authorization (`ServerConfig.Authorizer`, `AuthorizationDecisionMetrics`) | n/a | ✅ | ✅ unit | none |
| Connection authorization for mutual TLS (`ServerConfig.ConnectionAuthorizer`) | n/a | ✅ `tcp+tls` only | ✅ unit | none |
| Security observability: PDU taps, protocol violations, TLS handshakes, refused connections ([OBSERVABILITY.md](../OBSERVABILITY.md)) | ✅ | ✅ | ✅ unit, e2e | n/a |
| Connection identity and lifecycle (`ConnectionID`, `ConnectionMetrics`, `ServerConnectionMetrics`) | ✅ | ✅ | ✅ unit, e2e | n/a |
