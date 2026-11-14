# Testing go-modbus

How to run each test tier. No hardware is needed: tests use in-process servers, scripted transports and pseudo-terminals. What each suite is for, and how to add to it, is in [CONTRIBUTING.md](CONTRIBUTING.md#testing).

## Tiers

| Tier | Command | Prerequisites | In CI |
|---|---|---|---|
| Unit | `make test` | none | yes, Go 1.26 and 1.27, with `-race` |
| End-to-end | `make e2e` (`TestE2E_*`) | none | yes, as part of the package tests |
| Chaos, stress | `go test -race -count=1 -run 'Chaos\|Stress' .` | none | yes, as part of the package tests |
| Soak | `make soak SOAKTIME=10m` | none | nightly, 30 minutes |
| Under load | `make stress-load` | none | nightly |
| Fuzz | `make fuzz` (`FUZZTIME`, default 30s) | none | yes, 10s per target; nightly, 5 minutes per target |
| Benchmarks | `make bench` (`BENCHTIME`) | none | no |
| Interop | `make interop` | Docker | yes, Interop workflow |
| Cross-compile | `make cross` | none | no |
| 32-bit | `GOARCH=386 go test ./...` | linux/amd64 (the 386 binaries run there) | nightly |

The end-to-end tier runs the real client against the real server on loopback, over `tcp` and `tcp+tls`; `e2e_matrix_test.go` lists what is covered and where.

`Config.Dialer` is tested in `client_dialer_test.go` (the network and address each transport dials, the dial timeout in the context, wrapped errors, the dialer contract, TLS on a dialer's connection, in-memory connections, the pool and re-dials under the request's context) and in `chaos_dialer_test.go`, which injects faults through the dialer (refused, stalled and contract-breaking dials; connections that are reset, cut short or go silent) and checks that no read returns wrong data, that every connection handed to the client is closed, that the client recovers and that no goroutine leaks. `MODBUS_CHAOS_SEED` replays its fault order.

Serial transports run on a pseudo-terminal on Linux and macOS, and those tests skip elsewhere. The `coverage-packages` job of `ci.yml` sets `MODBUS_REQUIRE_PTY=1`, so there a missing pseudo-terminal is a failure.

The 32-bit tier runs the whole suite with a 32-bit `int`, without the race detector (it does not exist for 386); the sizes the framers and the codecs compute are the same there, and the boundary tables say so (`TestTextCodecs_RegisterCountBoundaries`, `TestExecuteRequest_PayloadSizeBoundary`). `GOOS=linux GOARCH=386 go vet ./...` checks from any host that the tests compile for it.

Tests do not synchronise by sleeping: they wait for the event (a channel a handler or a connection closes, `waitFor` or `e2eEventually` on a condition) or run on the fake clock of `testing/synctest`, which makes a schedule exact (`TestServerAccept_BacksOffAfterErrors`). Sleeps that model a slow peer, pace a stress loop or are the polling step of such a helper remain.

`make bench` runs the end-to-end read-strategy benchmarks over loopback (root package, `batch`) and the CPU-only microbenchmarks of the frame decoders (`BenchmarkDecodeMBAP`, `BenchmarkDecodeRTU`, `BenchmarkDecodeASCII` in `internal/adu`) and of one codec (`BenchmarkDecodeFloat32` in `codec`). Compare a framer or codec change on the microbenchmarks; the loopback ones vary too much from run to run for that. No document states a performance figure.

## Lifecycle contract matrix

The long-lived types are `Client` (with the session engine and the connection pool inside it) and `Server`. Rows L1 to L16 are those of the organisation's lifecycle standard; a row is **n/a** where the type cannot have the feature. A test named `…L<n>_…` was written for that row; the others cover it as part of a wider scenario.

| Row | Scenario | `Client` | `Server` |
|---|---|---|---|
| L1 | Close twice | `TestE2E_Client_OpenCloseLifecycle` (Close before Open, three Closes), `TestEngine_Close_NoOp`, `TestPool_CloseAllIdempotent` | `TestServerStart_LifecycleEdgeCases` (second Stop, Shutdown before Start) |
| L2 | Two goroutines call Close | `TestStressClientOpenCloseRace`, `TestConcurrentOpenClose` | `TestServerL2_OnlyTheFirstStopWaits`: both return, the second at once and with nil while the first still waits (the documented outcome); `TestStressServerStopWithClients` |
| L3 | Close during a blocked read | `TestE2E_Client_CloseDuringRequest`, `TestCloseDuringInFlightRequest`, `TestHostileClient_CloseWhileTrickled`, `TestEngine_CloseInterrupts`, `TestStressCloseWithRequestsInFlight`, `TestPool_CloseAllClosesBusyAndLateConnections` | `TestHostileServer_ShutdownWithStalledClients`, `TestStressServerStopWithClients` |
| L4 | Close during a write | `TestClientL4_CloseDuringWrite`: the request returns `ErrClientNotOpen`, the peer gets no byte of it | A response that cannot be written ends its connection within `Timeout` (`TestHostileServer_ClientNeverReads`); `Stop` closes the socket under the write. No test stops the server during a blocked write |
| L5 | Close during handshake | `TestClientL5_CloseDuringOpen`, `TestClientL5_CloseDuringOpenContext`: `Close` returns nil and closes nothing, the open then succeeds (the documented outcome, see LIMITATIONS.md). `OpenContext` with a gated dialer: its context ends the dial (`TestClientL5_OpenContextCancelledWhileDialling`, single and pool; `TestClientL5_OpenContextDeadline`), a done context dials nothing (`TestClientL5_OpenContextAlreadyDone`), a context that ends after a successful dial closes it (`TestClientL5_OpenContextEndsAfterTheDial`, `TestClientL5_OpenContextDialErrorWhileCancelled`), two at once leave one connection (`TestClientL5_ConcurrentOpenContext`). The session engine dials without its lock: `TestEngine_CloseDuringOpen`, `TestEngine_ConcurrentOpen` (single and pool). A dial inside a request: `TestEngine_CloseInterrupts` ("while dialling"), `TestPool_CloseAllClosesBusyAndLateConnections` | TLS handshake: `TestE2E_TLS_HandshakeTimeout`, `TestStressSlowLorisTLSHandshake`, `server_tls_pending_test.go` |
| L6 | Cancel a pending request | `TestE2E_Client_ContextCancelInFlight`, `TestChaosRegressionCancelInFlight`, `TestRTUOverTCPCancelAfterWriteNotMisread`, `TestEngine_ContextEndsInFlight`, `TestEngine_WaitingForTurnHonoursContext`, `TestStressContextCancellationStorm`; a cancellation that arrives while the request deadline is set, or during a serial drain: `TestClientL6_CancelWhileRequestDeadlineIsSet`, `TestExecuteRequest_CancelledWhileDeadlineIsSet`, `TestExecuteRequest_CancelledWhileDraining` | n/a: the server makes no requests. A handler's context ends when its client leaves (`TestE2E_Server_HandlerContext_ClientDisconnectInFlight`) |
| L7 | Response after the request timed out | `TestChaosRegressionNoStaleBytesAfterTimeout`, `TestSerialTunnelLateAnswerNotMisread`, `TestRTULateAnswerDrained`, `TestASCIILateAnswerDrained`, `TestHostileUDP_Datagrams`; the limit on a serial line is pinned by `TestSerialLateAnswerMisreadOnce` (LIMITATIONS.md). Late answers are dropped by closing or draining the link; they are not counted | n/a |
| L8 | Response after reconnect | By construction: a transport belongs to one socket and the transaction counter to one connection. `TestChaosRegressionHalfOpenConnectionReplaced`, `TestSerialTunnelSuspectResponseRedials` | n/a |
| L9 | Callback calls a public method | `TestClientL9_CallbackMakesARequest` (`PDUTap`, `ClientMetrics.OnResponse`). `AttemptMetrics` methods run while the request holds its connection and must not make requests. `TestClientClose_ConnectionCloseRunsWithoutTheLock`: the `Close` of a `Config.Dialer` connection calls `Info`, which returns | The server's public methods are `Start`, `Stop` and `Shutdown`: see L10 |
| L10 | Callback calls Close | `TestClientL10_CallbackCallsClose` (`PDUTap`, `ClientMetrics.OnResponse`, `AttemptMetrics.OnAttempt`): Close returns, the client is closed and can be opened again | `TestServerL10_CallbackStopsTheServer` (`Stop`, `Shutdown` from a handler and from `OnRequest`: returns, the listener and the connection are closed, no further request is dispatched), `TestServerL10_CallbackStopWaitsForOtherConnections` (from a handler and from `OnResponse`: waits, shown by its stack, for another connection's handler, or until its context ends), `TestServerL10_HandlerCallsShutdownWithItsContext` (returns `context.Canceled` at once), `TestServerL10_StopOnAnotherGoroutineWaitsForTheHandler` (a `Stop` started by the handler or by the test waits for the handler), `TestServerL10_ConnectionMetricsStopsTheServer` (the accept loop), `TestServerL10_LoggerStopsTheServer` (the goroutine that reads requests) |
| L11 | Slow callback | `TestPDUTapBlockingTapDoesNotHoldConnection` | `TestChaosRegressionSlowHandlerResponseDelivered`, `TestE2E_Server_HandlerContext_ClientDisconnectInFlight`. That a blocked handler leaves other connections served follows from one goroutine per connection; no test is dedicated to it |
| L12 | Queue at capacity | Pool: `TestStressPoolExhaustionCancelledWaiters`, `TestChaosRegressionPoolWaitersWoken`, `TestPool_ExhaustedWaiterGetsReleasedConnection` | `TestHostileServer_PipelinedBurst` (pipeline depth), `TestE2E_Server_MaxClients`, `TestHostileServer_ConnectionFlood`, `TestStressTLSHandshakenNotReplaced`. Refused and replaced connections are logged and reported to `ConnectionMetrics`: `TestE2E_Server_MaxClients`, `TestServerConnectionMetrics_MaxClients`, `TestServerConnectionMetrics_TLSHandshakeEvicted`, `TestServerAccept_ConnectionDuringStopIsNotReported` |
| L13 | Concurrent sessions | `TestHostileClient_ConcurrentNoCrossTalk`, `TestStressSharedPooledClient`, `TestStressPDUTapConcurrentCallers` | `TestStressManyClients`, `TestE2E_ServerFunctions_Parallel`, `TestOwnership_HandlerReturnsACopyOfSharedState` |
| L14 | Failure during construction or startup | `TestClientOpen_DialErrors`, `TestDialer_ConnWithErrorIsClosed`, `TestDialer_NilConnWithoutError`, `TestDialer_TLSHandshakeFailure`, `TestNewPool_PreWarmFailure`, `TestEngine_OpenPoolPreWarmFails`, `TestClientOpen_ConcurrentCallsDoNotLeakConnections`, `TestChaosDialerFaults` | `TestServerStart_ListenError`; accept errors: `TestServerAccept_BacksOffAfterErrors`, `TestServerAccept_SurvivesTransientErrors` |
| L15 | `Shutdown(ctx)` | n/a: the client has no graceful drain (documented on `Close`) | `TestServerShutdown_ContextExpiresWhileHandlerRuns`, `TestStressShutdownWithBusyHandlers`, `TestE2E_Server_ShutdownWithInFlightHandler`. After a Shutdown whose context ended nothing waits for the remaining handlers (L2) |
| L16 | Close racing reconnect | `TestEngine_CloseInterrupts`, `TestEngine_SingleTransport_ClosedDuringRetryStaysClosed`, `TestEngine_PoolMode_ClosedDuringRetry`, `TestStressCloseDuringRetryBackoff` | n/a: the server does not reconnect |

Callbacks that do not return normally have contract tests of their own, one per place the library calls the application's code. Server: `TestE2E_Server_HandlerPanic`, `TestServerMetrics_HookThatDoesNotReturnIsContained`, `TestServerHandlerError_MethodThatDoesNotReturnNormally`, `TestServerTLS_PrivateKeyThatDoesNotReturnNormally`, `TestServerConnectionMetrics_HookThatDoesNotReturn`, `TestServerLogger_MethodThatDoesNotReturnNormally`. Client: `TestClientMetrics_HookThatDoesNotReturnIsRecovered` (the four `ClientMetrics` and two `AttemptMetrics` methods), `TestPDUTapThatDoesNotReturnNormallyIsRecovered`, `TestClientRetryPolicy_ThatDoesNotReturnNormally`, `TestClientDialer_ThatDoesNotReturnNormally`, `TestClientDialerConn_MethodThatDoesNotReturnNormally`, `TestClientDialerConn_OtherMethodsThatDoNotReturnNormally` (the methods the transports do not call), `TestClientDialerConn_InterruptThatDoesNotReturnNormally` (the client's interrupt goroutine), `TestClientTLS_PrivateKeyThatDoesNotReturnNormally`, `TestClientLogger_MethodThatDoesNotReturnNormally`. The guard itself: `internal/callback`, `TestPrefixedLogger_CallThatDoesNotReturnNormally` (every `Logger` and `FieldLogger` method). Stop from a callback: L10. Retry jitter is tested with an injected random source (`internal/session/jitter_test.go`): the delays stay within the bounds of each mode and never exceed `MaxDelay`, up to attempt 10 000, and the zero value keeps the schedule without drawing. The ownership of slices that cross the API is tested in `ownership_test.go` ([API.md § 13](API.md#13-ownership-of-slices)).

## Environment variables

| Variable | Effect |
|---|---|
| `MODBUS_CHAOS_SEED` | Replays one chaos seed (the seed is logged on failure) |
| `MODBUS_CHAOS_DURATION`, `MODBUS_STRESS_DURATION` | Soak mode: loop each scenario for this long (`make soak` sets them) |
| `MODBUS_REQUIRE_PTY` | `1` fails a pseudo-terminal test that would skip |
| `MODBUS_INTEROP_ADAPTERS` | Runs the named interop stacks only |
| `MODBUS_INTEROP_<STACK>_IMAGE` | Replaces the pinned image of one stack |

## Coverage

`make coverage` writes `coverage.out` over the core library packages (the examples and the interop suite are excluded). `make coverage-check` fails below `COVER_MIN` (95), the same minimum as `coverage-min` in `.github/workflows/ci.yml`. `make coverage-pkg-check` fails when one package is below `COVER_PKG_MIN` (90); CI runs it in the `coverage-packages` job.

## Fuzz regressions

Fuzz targets: `FuzzServerRequest`, `FuzzClientResponseParse`, `FuzzClientResponseDecode`, `FuzzServerFunctions` (root package), `FuzzCompile` (`batch`), `FuzzHostileRuntimeDecode` (`codec`), `FuzzHostileTCPResponse`, `FuzzHostileRTUResponse`, `FuzzHostileASCIIResponse` (`internal/transport`) and `FuzzHostileDiscover` (`sunspec`). `make fuzz` lists its targets by name; a new target must be added there.

`FuzzClientResponseParse` and the three `FuzzHostile…Response` targets feed bytes to the framers; `FuzzClientResponseDecode` feeds a response PDU to the client's function-level decoders through the scripted transport, with no framing in between. Each asserts more than the absence of a failure: an accepted frame is a frame of the input and fits its framing, and a call that succeeds returns a result that agrees with its request (lengths, echoes, at most one of each device identification object, at most 256 round trips). `TestFuzzClientResponseDecode_Seeds` checks that the seeds reach the accepting side of every decoder.

Allocation is tested apart from fuzzing: `TestResponseReaders_AllocationBudget` feeds each framer the largest length its field can declare (MBAP length `0xFFFF`, RTU byte count 255, FIFO byte count `0xFFFF`, a 64 KiB ASCII line) and bounds the allocations and the bytes allocated per request.

A failing input is written to `testdata/fuzz/<target>/`. Commit that file with the fix: `go test` replays it from then on.

## Chaos regressions

Reduce a failing chaos scenario to a deterministic test, usually a scripted fault in the proxy, and add it to `chaos_regression_test.go`.

## When CI runs

| Event | Workflow | What runs |
|---|---|---|
| Pull request | `pr.yml` | `docs.yml` (docsync, otfabric-policy) always; `ci.yml`, `interop.yml` unless every changed file is documentation; then `OT Fabric Required Checks` |
| Push to a branch | none | nothing; dispatch `ci.yml`, `interop.yml` to test a branch without a pull request |
| Push to `main` | `docs.yml` | the light checks; the merged commit was tested as the pull request's merge commit |
| Schedule | `nightly.yml` | the scheduled runs, on `main`: long fuzzing, soak, the chaos regressions under load, the suite on linux/386 |
| Release | `release.yml` | the release workflow tests the release commit again |

Documentation means an added or modified `*.md` file outside `.github/`, `testdata/` and `vendor/`, other than `AGENTS.md` and `CLAUDE.md`. A deleted or renamed document, or any other file, runs everything. `OT Fabric Required Checks` fails unless every job that applies succeeded; its summary lists them.
