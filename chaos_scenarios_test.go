// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"testing"
	"time"
)

// Randomized chaos scenarios: a mixed, self-checking workload runs through the
// fault-injecting proxy with the client in each of its modes. See the invariants
// listed on chaosRunScenario.

const (
	// chaosPartitionReset resets every connection and every new one for a while.
	chaosPartitionReset = iota + 1
	// chaosPartitionRefuse additionally refuses new connections for a while.
	chaosPartitionRefuse
)

// chaosScenario describes one randomized scenario.
type chaosScenario struct {
	name    string
	kind    string
	mode    chaosMode
	workers int
	timeout time.Duration
	plan    chaosPlan
	// partition selects a network partition in the middle of the fault phase.
	partition int
	// restart stops the server and starts a new one on the same port mid-run.
	restart bool
	// handler injects faults in the server's request handlers.
	handler *chaosHandlerPlan
	// exceptions allows Modbus exception responses as a failure class.
	exceptions bool
	// corruptData marks scenarios that inject undetectable corruption: data and
	// write-semantics assertions are then off (no panic, hang or leak remain).
	corruptData bool
}

// Faults every mode must survive, per direction. Header corruption towards the
// server excludes the function code (turning a read into a write is undetectable
// without a checksum); towards the client it is complete.
const (
	chaosCommonFaults = chaosLatency | chaosFragment | chaosDrop | chaosTruncate | chaosRSTBefore |
		chaosFINBefore | chaosRSTAfter | chaosFINAfter | chaosHalfOpen | chaosStale | chaosReorder
	chaosC2SFaults = chaosCommonFaults | chaosCorruptTxn | chaosCorruptProto | chaosCorruptLen | chaosCorruptUnit
	chaosS2CFaults = chaosCommonFaults | chaosDuplicate | chaosCorruptTxn | chaosCorruptProto |
		chaosCorruptLen | chaosCorruptUnit | chaosCorruptFC
)

func chaosScenarios() []chaosScenario {
	plan := func(c2s, s2c chaosFault, timeout time.Duration) chaosPlan {
		return chaosPlan{
			rate:       0.2,
			c2s:        c2s,
			s2c:        s2c,
			maxLatency: timeout / 4,
			staleDelay: timeout + timeout/4,
		}
	}
	const timeout = 80 * time.Millisecond
	const tlsTimeout = 150 * time.Millisecond
	return []chaosScenario{
		{
			name: "tcp/single", kind: "tcp", mode: chaosModeSingle, workers: 4, timeout: timeout,
			plan: plan(chaosC2SFaults, chaosS2CFaults, timeout), partition: chaosPartitionReset,
		},
		{
			name: "tcp/single+retry", kind: "tcp", mode: chaosModeSingleRetry, workers: 4, timeout: timeout,
			plan: plan(chaosC2SFaults, chaosS2CFaults, timeout), partition: chaosPartitionReset,
		},
		{
			name: "tcp/pool", kind: "tcp", mode: chaosModePool, workers: 8, timeout: timeout,
			plan: plan(chaosC2SFaults, chaosS2CFaults, timeout), partition: chaosPartitionRefuse,
		},
		{
			name: "tcp/pool+retry/restart", kind: "tcp", mode: chaosModePoolRetry, workers: 8, timeout: timeout,
			plan: plan(chaosC2SFaults, chaosS2CFaults, timeout), restart: true,
		},
		{
			name: "tls/single+retry", kind: "tcp+tls", mode: chaosModeSingleRetry, workers: 4, timeout: tlsTimeout,
			plan: plan(chaosRawFaults, chaosRawFaults, tlsTimeout), partition: chaosPartitionReset,
		},
		{
			name: "tcp/pool/handler-faults", kind: "tcp", mode: chaosModePool, workers: 8, timeout: timeout,
			plan: plan(chaosLatency|chaosFragment, chaosLatency|chaosFragment|chaosDuplicate, timeout),
			handler: &chaosHandlerPlan{
				rate:     0.25,
				kinds:    chaosHandlerSlow | chaosHandlerError | chaosHandlerPanic | chaosHandlerBlock,
				slowBy:   timeout / 2,
				blockFor: 2 * timeout,
			},
			exceptions: true,
		},
		{
			name: "tcp/pool+retry/data-corruption", kind: "tcp", mode: chaosModePoolRetry, workers: 8, timeout: timeout,
			plan:      plan(chaosC2SFaults|chaosCorruptFC|chaosCorruptData, chaosS2CFaults|chaosCorruptData, timeout),
			partition: chaosPartitionReset, exceptions: true, corruptData: true,
		},
	}
}

// chaosScenarioPace is the minimum duration of a scenario round in soak mode: a
// round opens on the order of a hundred connections (see chaosSoak).
const chaosScenarioPace = 500 * time.Millisecond

// TestChaosMonkey runs the randomized scenarios. In soak mode
// (MODBUS_CHAOS_DURATION) each scenario loops with a new seed per round.
func TestChaosMonkey(t *testing.T) {
	for i, sc := range chaosScenarios() {
		sc := sc
		seed := int64(1000 + i)
		t.Run(sc.name, func(t *testing.T) {
			chaosSoak(t, chaosEnvDuration, chaosSoakShare, chaosScenarioPace, seed, func(t *testing.T, seed int64) {
				chaosRunScenario(t, sc, seed)
			})
		})
	}
}

// chaosRunScenario runs one scenario round and asserts:
//
//   - no call outlives its bound (watchdog), no worker deadlocks;
//   - successful calls return data the caller may legitimately see (no cross-talk,
//     no stale response taken for the answer to another request);
//   - failed calls return an error of an expected class;
//   - after the faults stop the client recovers, in every mode, without being
//     closed, reopened or recreated, and a fresh client can use the server;
//   - the server applied only writes that were sent, at most once without a
//     RetryPolicy, and every confirmed write at least once;
//   - the pool never holds more than MaxConns connections;
//   - ClientMetrics and ServerMetrics pair every OnRequest with one outcome;
//   - after Close/Stop the server holds no connection and no goroutine is left.
func chaosRunScenario(t *testing.T, sc chaosScenario, seed int64) {
	t.Helper()
	baseline := chaosGoroutines()
	wd := chaosNewWatchdog(t)
	defer wd.stop()

	dev := chaosNewDevice(seed)
	srv := chaosStartServer(t, sc.kind, dev, nil)
	defer srv.stop()
	proxy := chaosStartProxy(t, srv.addr, seed, sc.kind == "tcp+tls")
	defer proxy.stop()
	defer func() {
		if t.Failed() {
			t.Logf("%s seed=%d: %s", sc.name, seed, proxy.summary())
		}
	}()

	metrics := &chaosClientMetrics{}
	client, err := New(chaosClientConfig(t, sc.kind, proxy.port(), sc.mode, sc.timeout, metrics))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = client.Close() }()
	clientFor := func(int) *Client { return client }

	limit := 1
	queue := sc.workers
	if sc.mode.pooled() {
		limit = sc.mode.maxConns
		queue = (sc.workers+limit-1)/limit + 1
	}
	sampler := chaosSampleConns(func() int { return int(proxy.active.Load()) }, limit)
	defer sampler.stop()

	wl := chaosNewWorkload(t, wd, dev, sc.workers, seed)
	wl.timeout = sc.timeout
	// A call may queue behind the other workers, retry, and (FC43) span several
	// round trips; each attempt is bounded by the timeout or a context deadline of
	// at most 1.5 times the timeout.
	wl.bound = time.Duration(queue*sc.mode.attempts()*2)*(sc.timeout*3/2) + time.Second
	wl.sticky = sc.mode.retry != nil
	wl.checkData = !sc.corruptData
	wl.allowed = map[string]bool{chaosClassTimeout: true, chaosClassTransport: true, chaosClassProtocol: true}
	if sc.exceptions {
		wl.allowed[chaosClassException] = true
	}
	var marks []string
	began := time.Now()
	mark := func(what string) {
		marks = append(marks, what+"="+time.Since(began).Round(time.Millisecond).String())
	}

	// Phase 1: healthy link, every call must succeed.
	chaosWait(t, wl.run(clientFor, nil, 3), 30*time.Second, "warm-up")

	mark("warm")

	// Phase 2: faults.
	phase := 200 * time.Millisecond
	if testing.Short() {
		phase = 100 * time.Millisecond
	}
	pl := sc.plan
	wl.clean.Store(false)
	proxy.setPlan(&pl)
	dev.plan.Store(sc.handler)
	stop := make(chan struct{})
	workers := wl.run(clientFor, stop, 0)

	time.Sleep(phase / 2)
	switch sc.partition {
	case chaosPartitionReset:
		proxy.acceptMode.Store(chaosAcceptReset)
		proxy.killAll(true)
		time.Sleep(30 * time.Millisecond)
		proxy.acceptMode.Store(chaosAcceptForward)
	case chaosPartitionRefuse:
		proxy.refuse()
		proxy.killAll(true)
		time.Sleep(30 * time.Millisecond)
		if err := proxy.listen(); err != nil {
			close(stop)
			chaosWait(t, workers, 2*wl.bound+10*time.Second, "workload")
			t.Skipf("environment: proxy port was taken while the listener was closed: %v", err)
		}
	default:
	}
	if sc.restart {
		if !srv.restart(20 * time.Millisecond) {
			close(stop)
			chaosWait(t, workers, 2*wl.bound+10*time.Second, "workload")
			t.Skipf("environment: server port was taken during the restart")
		}
	}
	time.Sleep(phase / 2)
	close(stop)
	chaosWait(t, workers, 2*wl.bound+10*time.Second, "workload")
	faultCalls := wl.stats.totalCalls()
	mark("faults")

	// Phase 3: heal, then recover.
	dev.plan.Store(nil)
	proxy.heal()
	// Let frames that were delayed past the client timeout land.
	time.Sleep(pl.staleDelay + 20*time.Millisecond)
	wl.clean.Store(true)
	chaosAssertRecovery(t, wd, client, sc)
	mark("recovered")
	wl.verify(clientFor)
	mark("verified")

	fresh := chaosOpenClient(t, chaosClientConfig(t, sc.kind, srv.port(), chaosModeSingle, 5*time.Second, nil))
	chaosAssertUsable(t, fresh, "fresh client after heal")

	// Phase 4: tear down and audit.
	_ = fresh.Close()
	_ = client.Close()
	sampler.stop()
	proxy.stop()
	if !chaosEventually(3*time.Second, func() bool { return srv.conns() == 0 }) {
		t.Errorf("server still holds %d client connection(s) after every client was closed", srv.conns())
	}
	srv.stop()

	wl.audit(sc.mode.retry == nil)
	metrics.check(t, "client")
	srv.metrics.check(t, "server")
	if sampler.sustained.Load() {
		t.Errorf("client held more than %d connection(s) (peak seen at the proxy: %d)", limit, sampler.peak.Load())
	}
	if proxy.injectedTotal() == 0 {
		t.Errorf("no fault was injected: the scenario did not test anything (%s)", proxy.summary())
	}
	if wl.stats.count(chaosClassOK) == 0 || faultCalls == 0 {
		t.Errorf("no throughput: %s", wl.stats.String())
	}
	chaosLeakCheck(t, baseline)
	mark("done")
	if testing.Verbose() {
		t.Logf("seed=%d %v %s; %s; handler faults=%d", seed, marks, wl.stats.String(), proxy.summary(), dev.handlerFaults.Load())
	}
}

// chaosProbe performs one checked read of the read-only table.
func chaosProbe(c *Client, timeout time.Duration) (ok bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	const addr, qty = 17, 8
	got, err := c.ReadRegisters(ctx, refUnitID, addr, qty, InputRegister)
	if err != nil {
		return false, err
	}
	if len(got) != qty {
		return false, nil
	}
	for i, g := range got {
		if g != chaosInputValue(addr+i) {
			return false, nil
		}
	}
	return true, nil
}

// chaosAssertUsable asserts that c works: reads return the reference data and a
// write is read back.
func chaosAssertUsable(t *testing.T, c *Client, who string) {
	t.Helper()
	ok, err := chaosProbe(c, 5*time.Second)
	if err != nil || !ok {
		t.Errorf("%s: read failed or returned wrong data (ok=%v, err=%v)", who, ok, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The last coil is outside every worker's range in the chaos scenarios.
	const addr = uint16(refSpace - 1)
	for _, value := range []bool{true, false} {
		if err := c.WriteCoil(ctx, refUnitID, addr, value); err != nil {
			t.Errorf("%s: write failed: %v", who, err)
			return
		}
		got, err := c.ReadCoil(ctx, refUnitID, addr)
		if err != nil || got != value {
			t.Errorf("%s: read back %v, %v; want %v", who, got, err, value)
			return
		}
	}
}

// chaosAssertRecovery asserts that, once the faults have stopped, the client works
// again without any help. Requests that hit a connection the faults broke may
// still fail, a bounded number of times: without a RetryPolicy every such
// connection costs one request, with one a retry absorbs it.
func chaosAssertRecovery(t *testing.T, wd *chaosWatchdog, client *Client, sc chaosScenario) {
	t.Helper()
	conns := 1
	if sc.mode.pooled() {
		conns = sc.mode.maxConns
	}
	budget := conns + 1
	if sc.mode.retry != nil {
		budget = 2
	}
	need := 2*conns + 2
	probeTimeout := 6 * sc.timeout
	failures, streak := 0, 0
	for streak < need {
		var ok bool
		var err error
		wd.guard("recovery probe", time.Duration(sc.mode.attempts())*probeTimeout+2*time.Second, func() {
			ok, err = chaosProbe(client, probeTimeout)
		})
		if err == nil {
			if !ok {
				t.Errorf("recovery: a successful read returned wrong data")
				return
			}
			streak++
			continue
		}
		streak = 0
		if failures++; failures > budget {
			t.Errorf("recovery: client (%s) still fails after the faults stopped: %d failed probe(s), last error (%s): %v",
				sc.mode.name, failures, chaosClassify(err), err)
			return
		}
	}
	if testing.Verbose() && failures > 0 {
		t.Logf("recovery: %d failed probe(s) before the client was healthy", failures)
	}
}
