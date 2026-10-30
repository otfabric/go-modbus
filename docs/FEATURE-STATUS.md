# Support and feature matrix

What go-modbus supports, how each feature is tested, and against what. Status: v1.10.0. Function-by-function detail, server behaviour on invalid requests and per-stack results are in [INTEROPERABILITY.md](../INTEROPERABILITY.md); what is out of scope is in [LIMITATIONS.md](../LIMITATIONS.md).

None of this is a conformance claim: nothing is certified, and no Modbus Organization conformance test has been attempted.

## 1. Versions

| go-modbus | Go | modbus-interop | Supported |
|---|---|---|---|
| **v1.10.0** | 1.23 or later (CI: 1.23 to 1.26) | v0.2.0 | **yes** |

Security fixes: see [SECURITY.md](../SECURITY.md).

## 2. Platforms

| Platform | Tested | Notes |
|---|---|---|
| Linux amd64 | the test suite with the race detector; Interop | CI runs on `ubuntu-latest` |
| Linux and macOS | serial tests on a pseudo-terminal | they skip on other platforms |
| Linux arm64 and arm, macOS arm64, Windows amd64 | `make cross` builds them without cgo | a build, not a test run |

## 3. Feature support

✅ supported · ◐ partly (see the notes) · — not implemented. **Own tests**: this repository's client and server, or a scripted transport. **Independent**: software that is not go-modbus exchanged it with go-modbus in the Interop workflow.

| Feature | Client | Server | Own tests | Independent |
|---|:---:|:---:|:---:|---|
| Modbus TCP (MBAP), IPv4 and IPv6 | ✅ | ✅ | ✅ e2e, chaos, stress | libmodbus, PyModbus, digitalpetri/modbus, NModbus, tokio-modbus |
| Modbus over TLS (MBAPS) | ✅ | ✅ | ✅ e2e | none |
| Modbus RTU, Modbus ASCII | ✅ | — | ✅ pseudo-terminal | none |
| RTU over TCP, RTU over UDP, ASCII over TCP, Modbus UDP | ✅ | — | ✅ | none |
| Standard function codes | ✅ | ◐ the optional ones need a handler | ✅ e2e | Modbus TCP, per stack in [INTEROPERABILITY.md](../INTEROPERABILITY.md#verification-status) |
| Vendor and user-defined function codes | — | — | n/a | n/a |
| FC43 sub-type 13 (CANopen general reference) | — | — | n/a | n/a |
| Batch reads (`batch`) | ✅ | n/a | ✅ property tests, fuzz | none |
| Value codecs (`codec`) | ✅ | n/a | ✅ | none |
| SunSpec marker and model headers (`sunspec`) | ◐ no points; use go-sunspec | n/a | ✅ e2e | none |
