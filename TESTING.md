# Testing go-modbus

How to run each test tier. No hardware is needed: tests use in-process servers, scripted transports and pseudo-terminals. What each suite is for, and how to add to it, is in [CONTRIBUTING.md](CONTRIBUTING.md#testing).

## Tiers

| Tier | Command | Prerequisites | In CI |
|---|---|---|---|
| Unit | `make test` | none | yes, Go 1.23 to 1.26, with `-race` |
| End-to-end | `make e2e` (`TestE2E_*`) | none | yes, as part of the package tests |
| Chaos, stress | `go test -race -count=1 -run 'Chaos\|Stress' .` | none | yes, as part of the package tests |
| Soak | `make soak SOAKTIME=10m` | none | no |
| Fuzz | `make fuzz` (`FUZZTIME`, default 30s) | none | yes, 10s per target |
| Benchmarks | `make bench` (`BENCHTIME`) | none | no |
| Interop | `make interop` | Docker | yes, Interop workflow |
| Cross-compile | `make cross` | none | no |

The end-to-end tier runs the real client against the real server on loopback, over `tcp` and `tcp+tls`; `e2e_matrix_test.go` lists what is covered and where.

Serial transports run on a pseudo-terminal on Linux and macOS, and those tests skip elsewhere.

## Environment variables

| Variable | Effect |
|---|---|
| `MODBUS_CHAOS_SEED` | Replays one chaos seed (the seed is logged on failure) |
| `MODBUS_CHAOS_DURATION`, `MODBUS_STRESS_DURATION` | Soak mode: loop each scenario for this long (`make soak` sets them) |
| `MODBUS_INTEROP_ADAPTERS` | Runs the named interop stacks only |
| `MODBUS_INTEROP_<STACK>_IMAGE` | Replaces the pinned image of one stack |

There is no `*_REQUIRE_*` switch yet: a skipped prerequisite-gated test does not fail a run.

## Coverage

`make coverage` writes `coverage.out` over the core library packages (the examples and the interop suite are excluded). `make coverage-check` fails below `COVER_MIN` (95), the same minimum as `coverage-min` in `.github/workflows/ci.yml`.

## Fuzz regressions

Fuzz targets: `FuzzServerRequest`, `FuzzClientResponseParse`, `FuzzServerFunctions` (root package) and `FuzzCompile` (`batch`). `make fuzz` lists its targets by name and does not run `FuzzServerFunctions`; run that one with `go test -run '^$' -fuzz '^FuzzServerFunctions$' .`.

A failing input is written to `testdata/fuzz/<target>/`. Commit that file with the fix: `go test` replays it from then on.

## Chaos regressions

Reduce a failing chaos scenario to a deterministic test, usually a scripted fault in the proxy, and add it to `chaos_regression_test.go`.
