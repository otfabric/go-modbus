# Contributing to go-modbus

Thank you for your interest in contributing to `go-modbus`! This document covers the guidelines for contributing.

## Getting Started

1. Fork the repository on GitHub
2. Clone your fork locally:
   ```sh
   git clone git@github.com:<your-username>/go-modbus.git
   cd go-modbus
   ```
3. Create a feature branch:
   ```sh
   git checkout -b feature/my-change
   ```

## Development Setup

### Prerequisites

- Go 1.23+ (see [go.mod](go.mod)); the code must build and pass on Go 1.23, so do not use newer language or standard-library features
- [staticcheck](https://staticcheck.dev/), [golangci-lint](https://golangci-lint.run/) and [govulncheck](https://pkg.go.dev/golang.org/x/vuln/cmd/govulncheck) for the checks
- No hardware: tests use in-process servers, scripted transports and pseudo-terminals

### Build & Test

```sh
# Run all checks (format, lint, vet, vulnerability scan, tests, coverage)
make check

# Run the library tests
make test

# Run the library tests with the race detector and coverage
make coverage

# Run the fuzz targets (FUZZTIME=30s by default)
make fuzz

# Build modbus-cli and the examples into ./bin
make build

# Run the interop suite against libmodbus, PyModbus, digitalpetri/modbus, NModbus and tokio-modbus (needs Docker)
make interop
```

Run `make` (or `make help`) for the full list of targets.

## Making Changes

### Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Start every Go file with `// SPDX-License-Identifier: MIT`
- Keep functions focused and small
- Add comments only where the logic isn't self-evident
- Respect the package boundaries and import rules in [ARCHITECTURE.md](ARCHITECTURE.md): framing lives in `internal/adu`, transports in `internal/transport`, retry and pooling in `internal/session`, and the root package is the public API

### Formatting

`make fmt` formats with the `gofmt` of the Go version in `go.mod`, which is the version CI uses, not necessarily the one you have installed. `make fmt-check` (part of `make check`) fails if either that `gofmt` or your local one would change a file: `gofmt` output differs slightly between Go releases, and a file the two disagree on fails CI. If they disagree, restructure the code (for example, move trailing comments onto their own lines) rather than picking a side.

### Errors

Return the package's sentinel and typed errors so callers can use `errors.Is` / `errors.As`, and wrap with `%w`. See [ERRORS.md](ERRORS.md) for the taxonomy. A new failure mode needs a decision on whether it is retryable (`internal/session/retry.go`).

### Commit Messages

Use clear, concise commit messages:

```
component: short description

Optional longer explanation of the change, why it was made,
and any relevant context.
```

Examples:
- `transport: add Modbus ASCII framing`
- `codec: reject out-of-range 48-bit values`
- `docs: document function probing semantics`

### Testing

- Add tests for new functionality in the appropriate `_test.go` file; the repository uses only the standard `testing` package
- Assert behaviour: returned values, the specific error, bytes on the wire and metrics callbacks, not just that the code ran
- Keep tests fast, deterministic and race-clean; CI runs them with `-race` on every Go version from 1.23 and a 120 s per-package timeout
- Serial transports are tested on a pseudo-terminal (Linux and macOS; those tests skip elsewhere), so no serial adapter is needed
- Library coverage is above 90% and enforced by Codecov; new code should come with tests
- If your change affects what is sent to or accepted from a device, verify against real hardware when possible and say which device in the pull request

### End-to-end, chaos and stress tests

Three suites in the root package run the real client against the real server on loopback:

- `e2e_*_test.go`: the functional matrix (every client function over `tcp` and `tcp+tls`); `e2e_matrix_test.go` lists what is covered and where.
- `chaos_*_test.go`: a fault-injecting TCP proxy (`chaos_proxy_test.go`) and a self-checking workload (`chaos_harness_test.go`). `TestChaosMonkey` runs randomized scenarios, `TestChaosFaultMatrix` one precisely placed fault per case, and `chaos_regression_test.go` one test per defect found this way.
- `stress_*_test.go`: parallel load, lifecycle races and hostile peers.

```bash
# The three suites, as CI runs them
GOWORK=off go test -race -count=1 -run 'E2E|Chaos|Stress' .

# Soak: keep looping for SOAKTIME per suite (default 2m), new seed every round
make soak SOAKTIME=10m

# Replay a failing chaos round (the seed is logged on failure)
MODBUS_CHAOS_SEED=1234567 GOWORK=off go test -race -count=1 -run 'TestChaosMonkey/tcp/pool$' .
```

When you change connection handling, retries, timeouts, cancellation or the server's connection loop, run the soak for a few minutes. When a scenario fails, reduce it to a deterministic test (a scripted fault in the proxy usually does it) and add that to `chaos_regression_test.go` with the fix. Helpers are prefixed `e2e`, `chaos` or `stress`; the suites must stay fast (bounded rounds, short timeouts) in a normal run.

### Interop tests

The `interop` package (build tag `interop`, so it is not part of `go test ./...`) runs the client against five reference servers and five reference clients against a go-modbus server. The reference stacks are container images published by [otfabric/modbus-interop](https://github.com/otfabric/modbus-interop) and are consumed as images only: nothing of that repository is cloned or built here, and the scenarios, expected values and assertions are this repository's.

```bash
# All five stacks (needs Docker; the Interop workflow runs this on every push)
make interop

# One stack, or another build of an image
MODBUS_INTEROP_ADAPTERS=pymodbus make interop
make interop MODBUS_INTEROP_PYMODBUS_IMAGE=ghcr.io/otfabric/modbus-interop-pymodbus:dev
```

A change to what is sent to or accepted from a peer (framing, request validation, exception responses, FC43 paging) must keep `make interop` green. When go-modbus and a reference stack disagree, the specification decides, not the test: fix go-modbus if it is wrong, and if the other stack is, say so in a comment that names the stack and its version and keep the exception as narrow as the one request it is about. `make vet`, `make lint` and `make lint-ci` compile and lint the package (they pass `-tags=interop`); gopls needs `-tags=interop` in `buildFlags` to do the same.

### Documentation

When you change **public API or behaviour**, update:

- Doc comments on the affected symbols
- [API.md](API.md), and [README.md](README.md) where it references the changed behaviour
- [CODECS.md](CODECS.md), [ERRORS.md](ERRORS.md) or [OBSERVABILITY.md](OBSERVABILITY.md) where relevant
- [INTEROPERABILITY.md](INTEROPERABILITY.md) for protocol coverage and for how the server answers a request
- [RELEASE.md](RELEASE.md), calling out behaviour changes and anything that breaks existing callers

## Submitting Changes

1. Push your branch to your fork:
   ```sh
   git push origin feature/my-change
   ```
2. Open a Pull Request against the `main` branch
3. Describe what your change does and why
4. Reference any related issues

For larger changes, please open an issue first to discuss the approach.

## Reporting Issues

- Use GitHub Issues to report bugs or suggest features
- Include the go-modbus version, the Go version and the operating system
- Provide a minimal program or test that reproduces the problem, the transport (TCP, TLS, UDP, RTU, ASCII) and, for serial, the line settings
- A debug log of the frames exchanged helps a lot (see [OBSERVABILITY.md](OBSERVABILITY.md)); remove anything sensitive first
- Mention the device type/vendor for device-compatibility issues
- Report security vulnerabilities privately: see [SECURITY.md](SECURITY.md)

## Project Structure

```
.                      Public API: client, server, configuration, errors, metrics, logging
codec/                 Typed register codecs and the codec registry
sunspec/               SunSpec marker detection and model-header discovery
internal/adu/          ADU framing: MBAP, RTU CRC, ASCII LRC, wire encoding
internal/transport/    TCP, RTU and ASCII transports
internal/session/      Execute, retry and connection-pool layer
internal/protocol/     Function codes, exceptions, response validation, probes
internal/logging/      Logger plumbing
cmd/modbus-cli/        Command-line client
interop/               Tests against other Modbus stacks (build tag "interop", needs Docker)
examples/              Runnable examples
spec/                  Protocol reference notes
```

## License

By contributing to `go-modbus`, you agree that your contributions will be licensed under the [MIT License](LICENSE).
