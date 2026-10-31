# AGENTS.md — go-modbus

## What this is

Pure Go Modbus library: client over TCP, TLS (MBAPS), RTU, ASCII, UDP and the tunnelled serial framings; server over TCP and TLS; typed codecs (`codec/`), batched reads (`batch/`), SunSpec discovery (`sunspec/`) and the `modbus-cli` tool (`cmd/modbus-cli/`). Profile: **protocol-library**. Claim levels: [INTEROPERABILITY.md](INTEROPERABILITY.md), [LIMITATIONS.md](LIMITATIONS.md).

## Organisation standards

This repository follows **otfabric/engineering policy 1.2**, profile **protocol-library** (`.otfabric/policy.yaml`, mode `enforce`). Where this file and the organisation standards disagree, this file wins here — say so in your report.

## Where the truth lives

| Question | Source |
|---|---|
| Wire behaviour | Modbus Application Protocol Specification V1.1b3; notes in spec/MODBUS-SPEC.md |
| What is implemented and against which stacks it was run | INTEROPERABILITY.md |
| What is not done or not verified | LIMITATIONS.md |
| Public API | API.md, CODECS.md |
| Errors | ERRORS.md |
| Layering | ARCHITECTURE.md |
| Release history | RELEASE.md (`## vX.Y.Z` … `---`) |

## Hard rules

1. Never lower the coverage minimum in `.github/workflows/ci.yml`, add lint suppressions, or skip or delete tests to make CI pass.
2. Never invent protocol semantics: cite the specification or INTEROPERABILITY.md.
3. Never claim interoperability, conformance or certification beyond recorded evidence.
4. The module is v1 and other OT Fabric repositories depend on it: exported API is semver-binding, and it stays on v1.
5. The code must build and pass on Go 1.23 (`go.mod`): no newer language or standard-library features.
6. The reference images in the `Makefile` stay in step with `interop/harness.go` and `.github/workflows/interop.yml`.

## Commands

| When you change… | Run |
|---|---|
| anything | `make check` |
| a decoder or a parser | `make fuzz` |
| retries, pooling, transports or server shutdown | `make soak` |
| wire behaviour | `make interop` (Docker) |

## Definition of Done

`make check` green; tests for new behaviour incl. error paths; fuzz target for new decoders of untrusted input; API.md, ERRORS.md, INTEROPERABILITY.md and LIMITATIONS.md updated in the same change; RELEASE.md section for user-visible changes.

## Git and releases

Branches and PRs. Do not push to `main`, tag or run the Release workflow unless the maintainer asks in the session.
