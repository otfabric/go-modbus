// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// This file holds the shared harness of the chaos and stress suites: run-time
// knobs, a call watchdog, a goroutine leak check, an instrumented reference
// device, metrics recorders, and the self-checking workload.

// Environment knobs.
const (
	// chaosEnvSeed overrides the seed of every chaos scenario, to replay a failure.
	chaosEnvSeed = "MODBUS_CHAOS_SEED"
	// chaosEnvDuration and stressEnvDuration switch on soak mode: the scenarios of the
	// respective suite keep looping (with a fresh seed per round) until their share of
	// the duration has elapsed.
	chaosEnvDuration  = "MODBUS_CHAOS_DURATION"
	stressEnvDuration = "MODBUS_STRESS_DURATION"

	// chaosSoakShare and stressSoakShare are the number of soak-capable tests per suite:
	// each gets an equal share of the soak duration.
	chaosSoakShare  = 7
	stressSoakShare = 14
)

func chaosEnvDur(tb testing.TB, name string) time.Duration {
	v := os.Getenv(name)
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		tb.Fatalf("%s=%q: %v", name, v, err)
	}
	return d
}

// chaosAwaitPorts waits until the machine can open loopback connections again.
// Every connection a client closes first keeps its ephemeral port in TIME_WAIT
// (30 s on macOS, out of roughly 16000 ports): a soak that churns connections
// faster than that drains the range and dials fail with EADDRNOTAVAIL, which is a
// property of the machine and not of the code under test.
func chaosAwaitPorts(t *testing.T) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	// The kernel completes the handshake from the listen backlog: nothing needs to
	// accept the probe connection.
	deadline := time.Now().Add(90 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", ln.Addr().String(), time.Second)
		if err == nil {
			chaosAbortConn(c)
			return
		}
		if !errors.Is(err, syscall.EADDRNOTAVAIL) || time.Now().After(deadline) {
			t.Fatalf("environment: cannot open a loopback connection: %v", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// chaosAbortConn closes conn with a reset, which frees its port at once.
func chaosAbortConn(conn net.Conn) {
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.SetLinger(0)
	}
	_ = conn.Close()
}

// chaosSoak runs round once in a default run. In soak mode (envName set) it repeats
// round, with a different seed each time, until this test's share of the soak
// duration has elapsed. pace is the minimum duration of a soak round: it keeps
// tests that open many connections per round below the rate at which the machine
// recycles ephemeral ports (see chaosAwaitPorts). The seed in use is logged when
// the test fails.
func chaosSoak(t *testing.T, envName string, share int, pace time.Duration, defaultSeed int64, round func(t *testing.T, seed int64)) {
	t.Helper()
	seed := defaultSeed
	soak := chaosEnvDur(t, envName) / time.Duration(share)
	if soak > 0 {
		seed = time.Now().UnixNano() & 0xffffffff
	}
	if v := os.Getenv(chaosEnvSeed); v != "" {
		s, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatalf("%s=%q: %v", chaosEnvSeed, v, err)
		}
		seed = s
	}
	start := time.Now()
	rounds := 0
	for {
		cur := seed + int64(rounds)
		if soak > 0 {
			chaosAwaitPorts(t)
		}
		roundStart := time.Now()
		round(t, cur)
		rounds++
		if t.Failed() {
			t.Logf("FAILED in round %d: replay with %s=%d", rounds, chaosEnvSeed, cur)
			return
		}
		if time.Since(start) >= soak {
			break
		}
		time.Sleep(pace - time.Since(roundStart))
	}
	if soak > 0 {
		t.Logf("soak: %d rounds in %v (seeds %d..%d)", rounds, time.Since(start).Round(time.Millisecond), seed, seed+int64(rounds)-1)
	}
}

// chaosAllStacks returns the stacks of all goroutines.
func chaosAllStacks() string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}

// chaosGoroutines returns the stack of every live goroutine, keyed by goroutine ID.
func chaosGoroutines() map[string]string {
	out := make(map[string]string)
	for _, g := range strings.Split(chaosAllStacks(), "\n\n") {
		fields := strings.Fields(g)
		if len(fields) < 2 || fields[0] != "goroutine" {
			continue
		}
		out[fields[1]] = g
	}
	return out
}

// chaosLeakCheck fails the test if goroutines that did not exist in baseline are
// still alive after a grace period. Runtime and testing goroutines are ignored.
func chaosLeakCheck(t *testing.T, baseline map[string]string) {
	t.Helper()
	ignore := []string{
		"testing.(*T).Run", "testing.tRunner", "testing.(*M).", "testing.runTests",
		"runtime.gc", "runtime.MHeap", "runtime.bgsweep", "runtime.bgscavenge",
		"runtime.forcegchelper", "runtime.runfinq", "runtime.runFinalizers", "runtime.runCleanups",
		"runtime.ReadTrace", "os/signal.", "runtime.ensureSigM", "created by runtime.",
		"runtime/pprof.", "runtime.unique_runtime_registerUniqueMapCleanup",
		"(*chaosWatchdog).run",
	}
	var leaked []string
	deadline := time.Now().Add(5 * time.Second)
	for {
		leaked = leaked[:0]
		for id, stack := range chaosGoroutines() {
			if _, ok := baseline[id]; ok {
				continue
			}
			skip := false
			for _, pat := range ignore {
				if strings.Contains(stack, pat) {
					skip = true
					break
				}
			}
			if !skip {
				leaked = append(leaked, stack)
			}
		}
		if len(leaked) == 0 {
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("goroutine leak: %d goroutine(s) outlived the test:\n%s", len(leaked), strings.Join(leaked, "\n\n"))
}

// chaosEventually polls cond until it holds or the timeout expires.
func chaosEventually(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// chaosWatchdog enforces that every guarded call returns within its bound. A call
// that overruns fails the test with a dump of all goroutine stacks.
type chaosWatchdog struct {
	t *testing.T

	mu      sync.Mutex
	calls   map[uint64]chaosGuarded
	next    uint64
	tripped bool

	stopCh chan struct{}
	doneCh chan struct{}
}

type chaosGuarded struct {
	name     string
	start    time.Time
	deadline time.Time
}

func chaosNewWatchdog(t *testing.T) *chaosWatchdog {
	w := &chaosWatchdog{
		t:      t,
		calls:  make(map[uint64]chaosGuarded),
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	go w.run()
	t.Cleanup(w.stop)
	return w
}

func (w *chaosWatchdog) run() {
	defer close(w.doneCh)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-w.stopCh:
			return
		case now := <-ticker.C:
			w.mu.Lock()
			for _, c := range w.calls {
				if now.After(c.deadline) && !w.tripped {
					w.tripped = true
					w.t.Errorf("watchdog: %s has not returned after %v (bound %v)\n%s",
						c.name, now.Sub(c.start).Round(time.Millisecond),
						c.deadline.Sub(c.start), chaosAllStacks())
				}
			}
			w.mu.Unlock()
		}
	}
}

func (w *chaosWatchdog) stop() {
	select {
	case <-w.stopCh:
	default:
		close(w.stopCh)
	}
	<-w.doneCh
}

func (w *chaosWatchdog) hasTripped() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.tripped
}

// guard runs fn and fails the test if it takes longer than bound.
func (w *chaosWatchdog) guard(name string, bound time.Duration, fn func()) time.Duration {
	start := time.Now()
	w.mu.Lock()
	id := w.next
	w.next++
	w.calls[id] = chaosGuarded{name: name, start: start, deadline: start.Add(bound)}
	w.mu.Unlock()

	fn()

	elapsed := time.Since(start)
	w.mu.Lock()
	delete(w.calls, id)
	late := elapsed > bound && !w.tripped
	if late {
		w.tripped = true
	}
	w.mu.Unlock()
	if late {
		w.t.Errorf("watchdog: %s returned after %v (bound %v)", name, elapsed.Round(time.Millisecond), bound)
	}
	return elapsed
}

// chaosWait waits for wg, failing the test with a stack dump if it does not finish.
func chaosWait(t *testing.T, wg *sync.WaitGroup, timeout time.Duration, what string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatalf("%s did not finish within %v (deadlock?)\n%s", what, timeout, chaosAllStacks())
	}
}

// Error classes.
const (
	chaosClassOK        = "ok"
	chaosClassTimeout   = "timeout"
	chaosClassTransport = "transport"
	chaosClassProtocol  = "protocol"
	chaosClassException = "exception"
	chaosClassNotOpen   = "not-open"
	chaosClassCanceled  = "canceled"
	chaosClassOther     = "other"
)

// chaosClassify maps an error returned by the client to its class.
func chaosClassify(err error) string {
	var exc *ExceptionError
	var netErr net.Error
	switch {
	case err == nil:
		return chaosClassOK
	case errors.Is(err, ErrClientNotOpen):
		return chaosClassNotOpen
	case errors.Is(err, context.Canceled):
		return chaosClassCanceled
	case errors.Is(err, ErrRequestTimedOut), errors.Is(err, context.DeadlineExceeded):
		return chaosClassTimeout
	case errors.As(err, &exc):
		return chaosClassException
	case errors.Is(err, ErrProtocolError), errors.Is(err, ErrBadUnitID), errors.Is(err, ErrBadTransactionID),
		errors.Is(err, ErrUnknownProtocolID), errors.Is(err, ErrInvalidMBAPLength):
		return chaosClassProtocol
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, net.ErrClosed):
		return chaosClassTransport
	case errors.As(err, &netErr):
		if netErr.Timeout() {
			return chaosClassTimeout
		}
		return chaosClassTransport
	default:
		return chaosClassOther
	}
}

// chaosClientMetrics records ClientMetrics callbacks and checks that every request
// gets exactly one outcome.
type chaosClientMetrics struct {
	requests  [256]atomic.Int64
	outcomes  [256]atomic.Int64
	responses atomic.Int64
	errs      atomic.Int64
	timeouts  atomic.Int64
	attempts  atomic.Int64
	dials     atomic.Int64
	inflight  atomic.Int64
	negative  atomic.Bool
}

func (m *chaosClientMetrics) OnRequest(_ uint8, fc FunctionCode) {
	m.requests[fc].Add(1)
	m.inflight.Add(1)
}

func (m *chaosClientMetrics) outcome(fc FunctionCode) {
	m.outcomes[fc].Add(1)
	if m.inflight.Add(-1) < 0 {
		m.negative.Store(true)
	}
}

func (m *chaosClientMetrics) OnResponse(_ uint8, fc FunctionCode, _ time.Duration) {
	m.responses.Add(1)
	m.outcome(fc)
}

func (m *chaosClientMetrics) OnError(_ uint8, fc FunctionCode, _ time.Duration, err error) {
	if err == nil {
		m.negative.Store(true)
	}
	m.errs.Add(1)
	m.outcome(fc)
}

func (m *chaosClientMetrics) OnTimeout(_ uint8, fc FunctionCode, _ time.Duration) {
	m.timeouts.Add(1)
	m.outcome(fc)
}

func (m *chaosClientMetrics) OnAttempt(uint8, FunctionCode, int, time.Duration, error) {
	m.attempts.Add(1)
}

func (m *chaosClientMetrics) OnRetryDial(int, time.Duration, error) { m.dials.Add(1) }

func (m *chaosClientMetrics) total() int64 {
	var n int64
	for i := range m.requests {
		n += m.requests[i].Load()
	}
	return n
}

// check asserts request/outcome consistency once all calls have returned.
func (m *chaosClientMetrics) check(t *testing.T, who string) {
	t.Helper()
	if m.negative.Load() {
		t.Errorf("%s metrics: an outcome callback fired without a matching OnRequest (or OnError got a nil error)", who)
	}
	for fc := range m.requests {
		if req, out := m.requests[fc].Load(), m.outcomes[fc].Load(); req != out {
			t.Errorf("%s metrics: FC 0x%02x: %d OnRequest but %d outcome callbacks", who, fc, req, out)
		}
	}
	if sum := m.responses.Load() + m.errs.Load() + m.timeouts.Load(); sum != m.total() {
		t.Errorf("%s metrics: %d requests, %d outcomes", who, m.total(), sum)
	}
}

// chaosServerMetrics adapts chaosClientMetrics to ServerMetrics.
type chaosServerMetrics struct{ chaosClientMetrics }

// Handler faults.
const (
	chaosHandlerSlow = 1 << iota
	chaosHandlerError
	chaosHandlerPanic
	chaosHandlerBlock
)

// chaosHandlerPlan selects the faults injected by the device's handlers.
type chaosHandlerPlan struct {
	rate  float64
	kinds int
	// slowBy bounds the delay of a slow handler; blockFor is the safety net of a
	// handler that blocks until its context is cancelled.
	slowBy   time.Duration
	blockFor time.Duration
}

// chaosDevice is the reference device with a known read-only data set, a log of
// every applied holding-register write, and optional handler fault injection.
type chaosDevice struct {
	*refDevice

	plan atomic.Pointer[chaosHandlerPlan]
	// hook, when set, runs at the start of every handler; a non-nil error is returned
	// to the client as an exception without touching the device.
	hook atomic.Pointer[func(ctx context.Context) error]

	mu      sync.Mutex
	rng     *rand.Rand
	applied map[uint32]int

	handlerFaults atomic.Int64
	ctxCancelled  atomic.Int64
	blockExpired  atomic.Int64
}

func chaosInputValue(addr int) uint16  { return uint16(addr)*40503 + 1 }
func chaosDiscreteValue(addr int) bool { return (addr*7+addr/3)%3 == 0 }

func chaosNewDevice(seed int64) *chaosDevice {
	d := &chaosDevice{
		refDevice: newRefDevice(),
		rng:       rand.New(rand.NewSource(seed ^ 0x5eed)),
		applied:   make(map[uint32]int),
	}
	for a := 0; a < refSpace; a++ {
		d.input[a] = chaosInputValue(a)
		d.discreteInputs[a] = chaosDiscreteValue(a)
	}
	return d
}

func chaosKey(addr, value uint16) uint32 { return uint32(addr)<<16 | uint32(value) }

func (d *chaosDevice) recordApplied(addr uint16, values []uint16) {
	d.mu.Lock()
	for i, v := range values {
		d.applied[chaosKey(addr+uint16(i), v)]++
	}
	d.mu.Unlock()
}

// fault injects the planned handler fault, if any. A non-nil error must be returned
// by the handler without touching the device.
func (d *chaosDevice) fault(ctx context.Context) error {
	if h := d.hook.Load(); h != nil {
		if err := (*h)(ctx); err != nil {
			return err
		}
	}
	pl := d.plan.Load()
	if pl == nil || pl.kinds == 0 {
		return nil
	}
	d.mu.Lock()
	roll, choice, frac := d.rng.Float64(), d.rng.Intn(1<<16), d.rng.Float64()
	d.mu.Unlock()
	if roll >= pl.rate {
		return nil
	}
	var enabled []int
	for _, k := range []int{chaosHandlerSlow, chaosHandlerError, chaosHandlerPanic, chaosHandlerBlock} {
		if pl.kinds&k != 0 {
			enabled = append(enabled, k)
		}
	}
	d.handlerFaults.Add(1)
	switch enabled[choice%len(enabled)] {
	case chaosHandlerSlow:
		time.Sleep(time.Duration(frac * float64(pl.slowBy)))
		return nil
	case chaosHandlerError:
		if choice%2 == 0 {
			return ErrServerDeviceBusy
		}
		return errors.New("chaos: handler failed")
	case chaosHandlerPanic:
		panic("chaos: handler panic")
	default:
		timer := time.NewTimer(pl.blockFor)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			d.ctxCancelled.Add(1)
		case <-timer.C:
			d.blockExpired.Add(1)
		}
		return ErrServerDeviceFailure
	}
}

func (d *chaosDevice) HandleCoils(ctx context.Context, req *CoilsRequest) ([]bool, error) {
	if err := d.fault(ctx); err != nil {
		return nil, err
	}
	return d.refDevice.HandleCoils(ctx, req)
}

func (d *chaosDevice) HandleDiscreteInputs(ctx context.Context, req *DiscreteInputsRequest) ([]bool, error) {
	if err := d.fault(ctx); err != nil {
		return nil, err
	}
	return d.refDevice.HandleDiscreteInputs(ctx, req)
}

func (d *chaosDevice) HandleHoldingRegisters(ctx context.Context, req *HoldingRegistersRequest) ([]uint16, error) {
	if err := d.fault(ctx); err != nil {
		return nil, err
	}
	res, err := d.refDevice.HandleHoldingRegisters(ctx, req)
	if err == nil && req.IsWrite {
		d.recordApplied(req.Addr, req.Args)
	}
	return res, err
}

func (d *chaosDevice) HandleInputRegisters(ctx context.Context, req *InputRegistersRequest) ([]uint16, error) {
	if err := d.fault(ctx); err != nil {
		return nil, err
	}
	return d.refDevice.HandleInputRegisters(ctx, req)
}

func (d *chaosDevice) HandleMaskWrite(ctx context.Context, req *MaskWriteRequest) error {
	if err := d.fault(ctx); err != nil {
		return err
	}
	err := d.refDevice.HandleMaskWrite(ctx, req)
	if err == nil && req.AndMask == 0 {
		// With a zero AND mask the register ends up holding exactly the OR mask.
		d.recordApplied(req.Addr, []uint16{req.OrMask})
	}
	return err
}

func (d *chaosDevice) HandleReadWriteRegisters(ctx context.Context, req *ReadWriteRegistersRequest) ([]uint16, error) {
	if err := d.fault(ctx); err != nil {
		return nil, err
	}
	res, err := d.refDevice.HandleReadWriteRegisters(ctx, req)
	if err == nil {
		d.recordApplied(req.WriteAddr, req.WriteValues)
	}
	return res, err
}

func (d *chaosDevice) HandleDeviceIdentification(ctx context.Context, req *DeviceIdentificationRequest) (*DeviceIdentificationResponse, error) {
	if err := d.fault(ctx); err != nil {
		return nil, err
	}
	return d.refDevice.HandleDeviceIdentification(ctx, req)
}

// setHook installs (or, with nil, removes) the handler hook.
func (d *chaosDevice) setHook(h func(ctx context.Context) error) {
	if h == nil {
		d.hook.Store(nil)
		return
	}
	d.hook.Store(&h)
}

func (d *chaosDevice) holdingAt(addr int) uint16 {
	d.refDevice.mu.RLock()
	defer d.refDevice.mu.RUnlock()
	return d.holding[addr]
}

// chaosServer is a running server plus what the tests need to restart and inspect it.
type chaosServer struct {
	t       *testing.T
	kind    string
	conf    ServerConfig
	handler RequestHandler
	metrics *chaosServerMetrics
	addr    string

	mu  sync.Mutex
	srv *Server
}

var (
	chaosTLSOnce       sync.Once
	chaosTLSServerPair tls.Certificate
	chaosTLSClientPair tls.Certificate
	chaosTLSServerPool *x509.CertPool
	chaosTLSClientPool *x509.CertPool
	chaosTLSErr        error
)

func chaosTLSMaterial(tb testing.TB) {
	tb.Helper()
	chaosTLSOnce.Do(func() {
		chaosTLSServerPair, chaosTLSErr = tls.X509KeyPair([]byte(serverCert), []byte(serverKey))
		if chaosTLSErr != nil {
			return
		}
		chaosTLSClientPair, chaosTLSErr = tls.X509KeyPair([]byte(clientCert), []byte(clientKey))
		if chaosTLSErr != nil {
			return
		}
		chaosTLSServerPool = x509.NewCertPool()
		chaosTLSClientPool = x509.NewCertPool()
		if !chaosTLSServerPool.AppendCertsFromPEM([]byte(clientCert)) || !chaosTLSClientPool.AppendCertsFromPEM([]byte(serverCert)) {
			chaosTLSErr = errors.New("cannot build certificate pools")
		}
	})
	if chaosTLSErr != nil {
		tb.Fatalf("TLS material: %v", chaosTLSErr)
	}
}

// chaosStartServer starts a server of the given kind ("tcp" or "tcp+tls") on a free
// loopback port. mod may adjust the configuration. The server is stopped on cleanup.
func chaosStartServer(t *testing.T, kind string, handler RequestHandler, mod func(*ServerConfig)) *chaosServer {
	t.Helper()
	cs := &chaosServer{t: t, kind: kind, handler: handler, metrics: &chaosServerMetrics{}}
	cs.conf = ServerConfig{MaxClients: 64, Timeout: 5 * time.Second, Metrics: cs.metrics}
	if kind == "tcp+tls" {
		chaosTLSMaterial(t)
		cs.conf.TLSServerCert = &chaosTLSServerPair
		cs.conf.TLSClientCAs = chaosTLSServerPool
		cs.conf.TLSHandshakeTimeout = 2 * time.Second
	}
	if mod != nil {
		mod(&cs.conf)
	}
	var lastErr error
	for try := 0; try < 10; try++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		cs.addr = ln.Addr().String()
		_ = ln.Close()
		if lastErr = cs.start(); lastErr == nil {
			t.Cleanup(cs.stop)
			return cs
		}
	}
	t.Fatalf("cannot start server: %v", lastErr)
	return nil
}

// start creates and starts a new Server object on the server's address.
func (cs *chaosServer) start() error {
	conf := cs.conf
	conf.URL = cs.kind + "://" + cs.addr
	srv, err := NewServer(&conf, cs.handler)
	if err != nil {
		return err
	}
	if err := srv.Start(); err != nil {
		return err
	}
	cs.mu.Lock()
	cs.srv = srv
	cs.mu.Unlock()
	return nil
}

func (cs *chaosServer) server() *Server {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.srv
}

func (cs *chaosServer) stop() {
	if srv := cs.server(); srv != nil {
		_ = srv.Stop()
	}
}

// restart stops the server and starts a new Server object on the same port,
// reporting whether the port could be bound again.
func (cs *chaosServer) restart(down time.Duration) bool {
	cs.stop()
	time.Sleep(down)
	return chaosEventually(2*time.Second, func() bool { return cs.start() == nil })
}

// conns returns the number of client connections the server currently holds.
func (cs *chaosServer) conns() int { return chaosServerConns(cs.server()) }

func chaosServerConns(srv *Server) int {
	srv.lock.Lock()
	defer srv.lock.Unlock()
	return len(srv.tcpClients)
}

func (cs *chaosServer) port() string {
	_, port, _ := net.SplitHostPort(cs.addr)
	return port
}

// chaosMode is a client configuration under test.
type chaosMode struct {
	name     string
	maxConns int
	// retry builds the RetryPolicy (nil: none).
	retry func() RetryPolicy
}

func chaosRetryFast(onTimeout bool) func() RetryPolicy {
	return func() RetryPolicy {
		return NewExponentialBackoff(ExponentialBackoffConfig{
			BaseDelay:      time.Millisecond,
			MaxDelay:       4 * time.Millisecond,
			MaxAttempts:    3,
			RetryOnTimeout: onTimeout,
		})
	}
}

var (
	chaosModeSingle      = chaosMode{name: "single"}
	chaosModeSingleRetry = chaosMode{name: "single+retry", retry: chaosRetryFast(false)}
	chaosModePool        = chaosMode{name: "pool", maxConns: 4}
	chaosModePoolRetry   = chaosMode{name: "pool+retry", maxConns: 4, retry: chaosRetryFast(true)}
)

func (m chaosMode) pooled() bool { return m.maxConns > 1 }

// attempts is the maximum number of transport attempts per request.
func (m chaosMode) attempts() int {
	if m.retry != nil {
		return 4
	}
	return 1
}

// chaosClientConfig builds a client configuration for a server (or proxy) listening
// on the given loopback port.
func chaosClientConfig(tb testing.TB, kind, port string, mode chaosMode, timeout time.Duration, metrics ClientMetrics) Config {
	tb.Helper()
	conf := Config{
		URL:         "tcp://127.0.0.1:" + port,
		Timeout:     timeout,
		DialTimeout: time.Second,
		MaxConns:    mode.maxConns,
	}
	if metrics != nil {
		conf.Metrics = metrics
	}
	if mode.retry != nil {
		conf.RetryPolicy = mode.retry()
	}
	if kind == "tcp+tls" {
		chaosTLSMaterial(tb)
		// "localhost" matches the SAN of the test certificate.
		conf.URL = "tcp+tls://localhost:" + port
		conf.TLSClientCert = &chaosTLSClientPair
		conf.TLSRootCAs = chaosTLSClientPool
	}
	return conf
}

// chaosOpenClient creates and opens a client; it is closed on cleanup.
func chaosOpenClient(t *testing.T, conf Config) *Client {
	t.Helper()
	c, err := New(conf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// Workload.

const (
	// chaosRegsPer is the number of holding registers each worker owns.
	chaosRegsPer = 4
	// chaosCoilsPer is the number of coils each worker owns: three written together
	// as a pattern (FC15) and one written on its own (FC05).
	chaosCoilsPer = 4
	// chaosMaxWorkers is bounded by the reference device's address space.
	chaosMaxWorkers = refSpace / chaosRegsPer
)

// chaosValue is the value worker writes to its register at offset for sequence seq.
// The high byte identifies the worker (and is never zero), so a response carrying
// another worker's data, or another register's, is recognizable.
func chaosValue(worker, offset, seq int) uint16 {
	return uint16(worker+1)<<8 | uint16(offset&3)<<6 | uint16(seq&0x3f)
}

// chaosCell models what a memory cell owned by one worker may legitimately hold.
type chaosCell struct {
	confirmed uint16
	unknown   map[uint16]struct{}
}

// wrote records a write attempt of v that returned err. sticky keeps v possible
// forever even when confirmed (needed when retries may leave duplicates in flight).
func (c *chaosCell) wrote(v uint16, err error, sticky bool) {
	if err == nil {
		c.confirmed = v
		if !sticky {
			return
		}
	}
	if c.unknown == nil {
		c.unknown = make(map[uint16]struct{})
	}
	c.unknown[v] = struct{}{}
}

func (c *chaosCell) allowed(v uint16) bool {
	if v == c.confirmed {
		return true
	}
	_, ok := c.unknown[v]
	return ok
}

// chaosStats counts call outcomes per class.
type chaosStats struct {
	mu      sync.Mutex
	classes map[string]int
}

func (s *chaosStats) add(class string) {
	s.mu.Lock()
	if s.classes == nil {
		s.classes = make(map[string]int)
	}
	s.classes[class]++
	s.mu.Unlock()
}

func (s *chaosStats) count(class string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.classes[class]
}

func (s *chaosStats) totalCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.classes {
		n += c
	}
	return n
}

func (s *chaosStats) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return fmt.Sprintf("calls=%v", s.classes)
}

// chaosWorkload is a self-checking mixed workload. Each worker owns a disjoint
// range of holding registers and coils and writes values derived from (worker,
// offset, sequence); every successful read is checked against what that worker
// may have written, and reads of the read-only tables against their known
// contents. Any response that belongs to another request therefore shows up as
// wrong data.
type chaosWorkload struct {
	t   *testing.T
	wd  *chaosWatchdog
	dev *chaosDevice

	unit uint8
	// timeout is the client's request timeout; bound is the watchdog bound per call.
	timeout time.Duration
	bound   time.Duration
	// sticky: confirmed writes stay possible forever (retry modes).
	sticky bool
	// checkData is false when undetectable data corruption is injected.
	checkData bool
	// allowed is the set of acceptable error classes while faults are active.
	allowed map[string]bool
	// clean is set while no faults are active: every call must then succeed.
	clean atomic.Bool

	stats    chaosStats
	failures atomic.Int64
	workers  []*chaosWorker
}

type chaosWorker struct {
	wl  *chaosWorkload
	id  int
	rng *rand.Rand
	seq int

	regs       [chaosRegsPer]chaosCell
	coilWord   chaosCell
	coilSingle chaosCell

	attempted map[uint32]int
	confirmed map[uint32]int
}

func chaosNewWorkload(t *testing.T, wd *chaosWatchdog, dev *chaosDevice, workers int, seed int64) *chaosWorkload {
	t.Helper()
	if workers > chaosMaxWorkers {
		t.Fatalf("workload: %d workers exceed the address space (max %d)", workers, chaosMaxWorkers)
	}
	wl := &chaosWorkload{
		t:         t,
		wd:        wd,
		dev:       dev,
		unit:      refUnitID,
		timeout:   time.Second,
		bound:     5 * time.Second,
		checkData: true,
		allowed:   map[string]bool{},
	}
	wl.clean.Store(true)
	for i := 0; i < workers; i++ {
		wl.workers = append(wl.workers, &chaosWorker{
			wl:        wl,
			id:        i,
			rng:       rand.New(rand.NewSource(seed*1000003 + int64(i))),
			attempted: make(map[uint32]int),
			confirmed: make(map[uint32]int),
		})
	}
	return wl
}

func (wl *chaosWorkload) failf(format string, args ...any) {
	if wl.failures.Add(1) <= 10 {
		wl.t.Errorf(format, args...)
	}
}

func (w *chaosWorker) regAddr(offset int) uint16  { return uint16(w.id*chaosRegsPer + offset) }
func (w *chaosWorker) coilAddr(offset int) uint16 { return uint16(w.id*chaosCoilsPer + offset) }

// call runs one client call under the watchdog and checks its error class.
func (w *chaosWorker) call(name string, fn func(ctx context.Context) error) error {
	wl := w.wl
	clean := wl.clean.Load()
	ctx, cancel := context.Background(), context.CancelFunc(func() {})
	switch {
	case clean:
		// A generous deadline, so that a loaded machine cannot fake a timeout.
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
	case w.rng.Intn(4) == 0:
		ctx, cancel = context.WithTimeout(ctx, wl.timeout/2+time.Duration(w.rng.Int63n(int64(wl.timeout))))
	}
	defer cancel()

	var err error
	bound := wl.bound
	if clean {
		bound += 5 * time.Second
	}
	wl.wd.guard(fmt.Sprintf("worker %d %s", w.id, name), bound, func() { err = fn(ctx) })

	class := chaosClassify(err)
	wl.stats.add(class)
	switch {
	case err == nil:
	case clean:
		wl.failf("worker %d %s: failed on a healthy link: %v", w.id, name, err)
	case wl.allowed[class]:
	default:
		wl.failf("worker %d %s: unexpected error class %q: %v", w.id, name, class, err)
	}
	return err
}

func (w *chaosWorker) noteWrite(offset int, v uint16, err error) {
	key := chaosKey(w.regAddr(offset), v)
	w.attempted[key]++
	if err == nil {
		w.confirmed[key]++
	}
	w.regs[offset].wrote(v, err, w.wl.sticky)
}

func (w *chaosWorker) checkReg(op string, offset int, v uint16) {
	if !w.wl.checkData {
		return
	}
	if !w.regs[offset].allowed(v) {
		w.wl.failf("worker %d %s: register %d returned 0x%04x, which this worker never wrote there (last confirmed 0x%04x, %d unconfirmed): cross-talk or stale response",
			w.id, op, w.regAddr(offset), v, w.regs[offset].confirmed, len(w.regs[offset].unknown))
	}
}

// step performs one randomly chosen operation.
func (w *chaosWorker) step(c *Client) {
	w.seq++
	wl := w.wl
	unit := wl.unit
	switch op := w.rng.Intn(14); op {
	case 0:
		off := w.rng.Intn(chaosRegsPer)
		v := chaosValue(w.id, off, w.seq)
		err := w.call("WriteRegister", func(ctx context.Context) error {
			return c.WriteRegister(ctx, unit, w.regAddr(off), v)
		})
		w.noteWrite(off, v, err)
	case 1:
		_ = w.writeBlock(c)
	case 2, 3:
		_ = w.readBlock(c)
	case 4:
		off := w.rng.Intn(chaosRegsPer)
		var got uint16
		err := w.call("ReadRegister", func(ctx context.Context) (err error) {
			got, err = c.ReadRegister(ctx, unit, w.regAddr(off), HoldingRegister)
			return err
		})
		if err == nil {
			w.checkReg("ReadRegister", off, got)
		}
	case 5:
		off := w.rng.Intn(chaosRegsPer)
		v := chaosValue(w.id, off, w.seq)
		err := w.call("MaskWriteRegister", func(ctx context.Context) error {
			return c.MaskWriteRegister(ctx, unit, w.regAddr(off), 0x0000, v)
		})
		w.noteWrite(off, v, err)
	case 6:
		off := w.rng.Intn(chaosRegsPer)
		v := chaosValue(w.id, off, w.seq)
		var got []uint16
		err := w.call("ReadWriteMultipleRegisters", func(ctx context.Context) (err error) {
			got, err = c.ReadWriteMultipleRegisters(ctx, unit, w.regAddr(0), chaosRegsPer, w.regAddr(off), []uint16{v})
			return err
		})
		w.noteWrite(off, v, err)
		if err == nil {
			if len(got) != chaosRegsPer {
				wl.failf("worker %d FC23: got %d registers, want %d", w.id, len(got), chaosRegsPer)
				break
			}
			// The write is applied before the read, atomically: the register just
			// written must read back exactly.
			if wl.checkData && got[off] != v {
				wl.failf("worker %d FC23: wrote 0x%04x to register %d but the same transaction read back 0x%04x", w.id, v, w.regAddr(off), got[off])
			}
			for i, g := range got {
				w.checkReg("FC23", i, g)
			}
		}
	case 7, 8:
		addr := w.rng.Intn(refSpace - 32)
		qty := 1 + w.rng.Intn(32)
		var got []uint16
		err := w.call("ReadInputRegisters", func(ctx context.Context) (err error) {
			got, err = c.ReadRegisters(ctx, unit, uint16(addr), uint16(qty), InputRegister)
			return err
		})
		if err == nil {
			if len(got) != qty {
				wl.failf("worker %d FC04: got %d registers, want %d", w.id, len(got), qty)
				break
			}
			for i, g := range got {
				if wl.checkData && g != chaosInputValue(addr+i) {
					wl.failf("worker %d FC04: input register %d = 0x%04x, want 0x%04x: response of another request", w.id, addr+i, g, chaosInputValue(addr+i))
					break
				}
			}
		}
	case 9:
		addr := w.rng.Intn(refSpace - 64)
		qty := 1 + w.rng.Intn(64)
		var got []bool
		err := w.call("ReadDiscreteInputs", func(ctx context.Context) (err error) {
			got, err = c.ReadDiscreteInputs(ctx, unit, uint16(addr), uint16(qty))
			return err
		})
		if err == nil {
			if len(got) != qty {
				wl.failf("worker %d FC02: got %d inputs, want %d", w.id, len(got), qty)
				break
			}
			for i, g := range got {
				if wl.checkData && g != chaosDiscreteValue(addr+i) {
					wl.failf("worker %d FC02: discrete input %d = %v, want %v: response of another request", w.id, addr+i, g, !g)
					break
				}
			}
		}
	case 10:
		pattern := uint16(w.seq & 7)
		bits := []bool{pattern&1 != 0, pattern&2 != 0, pattern&4 != 0}
		err := w.call("WriteCoils", func(ctx context.Context) error {
			return c.WriteCoils(ctx, unit, w.coilAddr(0), bits)
		})
		w.coilWord.wrote(pattern, err, wl.sticky)
	case 11:
		var got []bool
		err := w.call("ReadCoils", func(ctx context.Context) (err error) {
			got, err = c.ReadCoils(ctx, unit, w.coilAddr(0), chaosCoilsPer)
			return err
		})
		if err == nil {
			if len(got) != chaosCoilsPer {
				wl.failf("worker %d FC01: got %d coils, want %d", w.id, len(got), chaosCoilsPer)
				break
			}
			var pattern, single uint16
			for i := 0; i < 3; i++ {
				if got[i] {
					pattern |= 1 << i
				}
			}
			if got[3] {
				single = 1
			}
			if wl.checkData && (!w.coilWord.allowed(pattern) || !w.coilSingle.allowed(single)) {
				wl.failf("worker %d FC01: coils %v were never written by this worker", w.id, got)
			}
		}
	case 12:
		v := w.seq&1 != 0
		var word uint16
		if v {
			word = 1
		}
		err := w.call("WriteCoil", func(ctx context.Context) error {
			return c.WriteCoil(ctx, unit, w.coilAddr(3), v)
		})
		w.coilSingle.wrote(word, err, wl.sticky)
	default:
		var di *DeviceIdentification
		err := w.call("ReadDeviceIdentification", func(ctx context.Context) (err error) {
			di, err = c.ReadDeviceIdentification(ctx, unit, 0x02, 0x00)
			return err
		})
		if err == nil && wl.checkData {
			want := regularDeviceIDObjects()
			ok := di != nil && len(di.Objects) == len(want)
			for i := 0; ok && i < len(want); i++ {
				ok = di.Objects[i].ID == want[i].ID && di.Objects[i].Value == want[i].Value
			}
			if !ok {
				wl.failf("worker %d FC43: unexpected device identification %+v", w.id, di)
			}
		}
	}
}

func (w *chaosWorker) writeBlock(c *Client) error {
	w.seq++
	values := make([]uint16, chaosRegsPer)
	for i := range values {
		values[i] = chaosValue(w.id, i, w.seq)
	}
	err := w.call("WriteRegisters", func(ctx context.Context) error {
		return c.WriteRegisters(ctx, w.wl.unit, w.regAddr(0), values)
	})
	for i, v := range values {
		w.noteWrite(i, v, err)
	}
	return err
}

func (w *chaosWorker) readBlock(c *Client) error {
	var got []uint16
	err := w.call("ReadRegisters", func(ctx context.Context) (err error) {
		got, err = c.ReadRegisters(ctx, w.wl.unit, w.regAddr(0), chaosRegsPer, HoldingRegister)
		return err
	})
	if err != nil {
		return err
	}
	if len(got) != chaosRegsPer {
		w.wl.failf("worker %d FC03: got %d registers, want %d", w.id, len(got), chaosRegsPer)
		return nil
	}
	for i, g := range got {
		w.checkReg("ReadRegisters", i, g)
	}
	return nil
}

// run starts every worker; worker i uses clientFor(i) and performs operations until
// stop is closed or it has done maxOps of them (0: no limit).
func (wl *chaosWorkload) run(clientFor func(i int) *Client, stop <-chan struct{}, maxOps int) *sync.WaitGroup {
	var wg sync.WaitGroup
	for _, w := range wl.workers {
		wg.Add(1)
		go func(w *chaosWorker) {
			defer wg.Done()
			c := clientFor(w.id)
			for n := 0; maxOps == 0 || n < maxOps; n++ {
				select {
				case <-stop:
					return
				default:
				}
				if wl.wd.hasTripped() {
					return
				}
				w.step(c)
			}
		}(w)
	}
	return &wg
}

// verify checks, on a healthy link, that every worker can write its block and read
// it back, and then audits the device against the model.
func (wl *chaosWorkload) verify(clientFor func(i int) *Client) {
	wl.t.Helper()
	wl.clean.Store(true)
	var wg sync.WaitGroup
	for _, w := range wl.workers {
		wg.Add(1)
		go func(w *chaosWorker) {
			defer wg.Done()
			c := clientFor(w.id)
			if w.writeBlock(c) == nil {
				_ = w.readBlock(c)
			}
		}(w)
	}
	chaosWait(wl.t, &wg, 30*time.Second, "verification")
}

// audit compares the device's final state and its write log with what the workers
// did. atMostOnce asserts that no write was applied more often than it was sent.
func (wl *chaosWorkload) audit(atMostOnce bool) {
	wl.t.Helper()
	if !wl.checkData {
		return
	}
	attempted := make(map[uint32]int)
	confirmed := make(map[uint32]int)
	for _, w := range wl.workers {
		for k, n := range w.attempted {
			attempted[k] += n
		}
		for k, n := range w.confirmed {
			confirmed[k] += n
		}
		for off := range w.regs {
			if v := wl.dev.holdingAt(int(w.regAddr(off))); !w.regs[off].allowed(v) {
				wl.failf("audit: register %d holds 0x%04x, which worker %d never wrote there", w.regAddr(off), v, w.id)
			}
		}
	}
	wl.dev.mu.Lock()
	defer wl.dev.mu.Unlock()
	for k, n := range wl.dev.applied {
		addr, value := uint16(k>>16), uint16(k)
		switch {
		case attempted[k] == 0:
			wl.failf("audit: the server applied a write of 0x%04x to register %d that no caller sent", value, addr)
		case atMostOnce && n > attempted[k]:
			wl.failf("audit: write of 0x%04x to register %d was sent %d time(s) but applied %d time(s) without a RetryPolicy", value, addr, attempted[k], n)
		}
	}
	for k, n := range confirmed {
		if wl.dev.applied[k] < n {
			wl.failf("audit: write of 0x%04x to register %d was confirmed %d time(s) but applied only %d time(s)", uint16(k), uint16(k>>16), n, wl.dev.applied[k])
		}
	}
}

// chaosConnSampler watches a connection count and reports when it stays above a
// limit. A count read through the proxy or the server lags behind the client by a
// few scheduler ticks, so only a sustained excess is a violation.
type chaosConnSampler struct {
	peak      atomic.Int64
	sustained atomic.Bool
	once      sync.Once
	stopCh    chan struct{}
	doneCh    chan struct{}
}

func chaosSampleConns(count func() int, limit int) *chaosConnSampler {
	s := &chaosConnSampler{stopCh: make(chan struct{}), doneCh: make(chan struct{})}
	go func() {
		defer close(s.doneCh)
		ticker := time.NewTicker(2 * time.Millisecond)
		defer ticker.Stop()
		var overSince time.Time
		for {
			select {
			case <-s.stopCh:
				return
			case now := <-ticker.C:
				n := count()
				if int64(n) > s.peak.Load() {
					s.peak.Store(int64(n))
				}
				switch {
				case n <= limit:
					overSince = time.Time{}
				case overSince.IsZero():
					overSince = now
				case now.Sub(overSince) > 300*time.Millisecond:
					s.sustained.Store(true)
				}
			}
		}
	}()
	return s
}

// stop ends the sampling; it may be called more than once.
func (s *chaosConnSampler) stop() {
	s.once.Do(func() { close(s.stopCh) })
	<-s.doneCh
}
