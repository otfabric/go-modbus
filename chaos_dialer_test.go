// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// This file injects transport faults through Config.Dialer: failed dials, the
// contract violations a dialer can commit, dials that stall, and connections
// that are reset, cut short or go silent. The client must never return wrong
// data, must close every connection it was handed, must recover once the
// faults stop, and must not leak goroutines.

type chaosDialFault int

const (
	chaosDialHealthy    chaosDialFault = iota
	chaosDialRefused                   // dial fails with ECONNREFUSED
	chaosDialConnAndErr                // a connection together with an error
	chaosDialNilConn                   // neither a connection nor an error
	chaosDialStall                     // the dial blocks until its context ends
	chaosDialResetWrite                // the connection is reset after the request is sent
	chaosDialTruncate                  // the response is cut short, then EOF
	chaosDialSilent                    // the response never arrives
	chaosDialFaultCount
)

func (f chaosDialFault) String() string {
	return [...]string{"healthy", "refused", "conn+err", "nil-conn", "stall", "reset-after-write",
		"truncate", "silent"}[f]
}

// chaosDialConn is a connection handed to the client, with a fault applied.
type chaosDialConn struct {
	net.Conn
	fault  chaosDialFault
	closed atomic.Bool
	writes atomic.Int32
	once   sync.Once
}

// chaosDialLifetime is the number of requests a healthy connection carries
// before it is reset, so that the client keeps dialling.
const chaosDialLifetime = 3

func (c *chaosDialConn) Write(b []byte) (int, error) {
	if c.fault == chaosDialHealthy && c.writes.Add(1) > chaosDialLifetime {
		chaosAbortConn(c.Conn)
		return 0, net.ErrClosed
	}
	n, err := c.Conn.Write(b)
	if c.fault == chaosDialResetWrite {
		c.once.Do(func() { chaosAbortConn(c.Conn) })
	}
	return n, err
}

func (c *chaosDialConn) Read(b []byte) (int, error) {
	if c.fault == chaosDialTruncate {
		n, err := c.Conn.Read(b)
		if n > 3 {
			n = 3
		}
		_ = c.Conn.Close()
		return n, err
	}
	if c.fault == chaosDialSilent {
		// Swallow the response; the read deadline set by the transport ends the read.
		for {
			if _, err := c.Conn.Read(b); err != nil {
				return 0, err
			}
		}
	}
	return c.Conn.Read(b)
}

func (c *chaosDialConn) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

// chaosFaultyDialer dials upstream and applies the next fault of its schedule;
// once the schedule is used up, every dial is healthy.
type chaosFaultyDialer struct {
	upstream string
	mu       sync.Mutex
	schedule []chaosDialFault
	handed   []*chaosDialConn
	counts   [chaosDialFaultCount]int
}

func (d *chaosFaultyDialer) next() chaosDialFault {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := chaosDialHealthy
	if len(d.schedule) > 0 {
		f, d.schedule = d.schedule[0], d.schedule[1:]
	}
	d.counts[f]++
	return f
}

func (d *chaosFaultyDialer) dial(ctx context.Context, network, _ string) (net.Conn, error) {
	f := d.next()
	if f == chaosDialRefused {
		return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ECONNREFUSED}
	}
	if f == chaosDialNilConn {
		return nil, nil
	}
	if f == chaosDialStall {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	var nd net.Dialer
	raw, err := nd.DialContext(ctx, network, d.upstream)
	if err != nil {
		return nil, err
	}
	c := &chaosDialConn{Conn: raw, fault: f}
	d.mu.Lock()
	d.handed = append(d.handed, c)
	d.mu.Unlock()
	if f == chaosDialConnAndErr {
		return c, errors.New("dialer reports an error with its connection")
	}
	return c, nil
}

// remaining returns the number of scheduled faults not applied yet.
func (d *chaosFaultyDialer) remaining() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.schedule)
}

// unclosed returns the connections handed to the client that it has not closed.
func (d *chaosFaultyDialer) unclosed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for i, c := range d.handed {
		if !c.closed.Load() {
			out = append(out, fmt.Sprintf("#%d (%s)", i, c.fault))
		}
	}
	return out
}

func chaosDialerSeed(t *testing.T) int64 {
	if v := os.Getenv(chaosEnvSeed); v != "" {
		s, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatalf("%s=%q: %v", chaosEnvSeed, v, err)
		}
		return s
	}
	return 20261109
}

func TestChaosDialerFaults(t *testing.T) {
	for _, pooled := range []bool{false, true} {
		name := "single"
		if pooled {
			name = "pooled"
		}
		t.Run(name, func(t *testing.T) { chaosRunDialerFaults(t, pooled) })
	}
}

func chaosRunDialerFaults(t *testing.T, pooled bool) {
	baseline := chaosGoroutines()
	seed := chaosDialerSeed(t)
	rng := rand.New(rand.NewSource(seed))

	dev := chaosNewDevice(seed)
	srv := chaosStartServer(t, "tcp", dev, nil)

	// Every fault several times, in a seeded order, with healthy dials between.
	var schedule []chaosDialFault
	for range 4 {
		for f := chaosDialRefused; f < chaosDialFaultCount; f++ {
			schedule = append(schedule, f, chaosDialHealthy)
		}
	}
	rng.Shuffle(len(schedule), func(i, j int) { schedule[i], schedule[j] = schedule[j], schedule[i] })
	d := &chaosFaultyDialer{upstream: srv.addr, schedule: schedule}

	conf := Config{
		URL:         "tcp://device.chaos:502",
		Timeout:     150 * time.Millisecond,
		DialTimeout: 200 * time.Millisecond,
		Dialer:      d.dial,
		RetryPolicy: NewExponentialBackoff(ExponentialBackoffConfig{
			BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, MaxAttempts: 3, RetryOnTimeout: true,
		}),
	}
	workers := 1
	if pooled {
		conf.MaxConns = 3
		workers = 3
	}
	c, err := New(conf)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Open may itself meet a fault; it is retried like an application would.
	for i := 0; ; i++ {
		if err = c.Open(); err == nil {
			break
		}
		if i == len(schedule) {
			t.Fatalf("seed %d: Open never succeeded: %v", seed, err)
		}
	}

	var (
		mu      sync.Mutex
		classes = map[string]int{}
		wrong   int
	)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() {
			// At least 60 reads each, and on until the schedule is used up.
			for i := 0; i < 600 && (i < 60 || d.remaining() > 0); i++ {
				addr := (w*61 + i*7) % 100
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				ok, err := chaosReadInputs(ctx, c, addr, 4)
				cancel()
				mu.Lock()
				classes[chaosClassify(err)]++
				if err == nil && !ok {
					wrong++
				}
				mu.Unlock()
			}
		})
	}
	chaosWait(t, &wg, 60*time.Second, "dialer chaos workload")

	// The faults are used up: the client recovers on its own.
	if ok, err := chaosReadInputs(context.Background(), c, 10, 4); err != nil || !ok {
		t.Fatalf("seed %d: read after the faults = %v, %v", seed, ok, err)
	}
	_ = c.Close()

	if wrong > 0 {
		t.Errorf("seed %d: %d reads returned wrong data", seed, wrong)
	}
	if classes[chaosClassOK] == 0 {
		t.Errorf("seed %d: no read succeeded: %v", seed, classes)
	}
	for f := chaosDialRefused; f < chaosDialFaultCount; f++ {
		if d.counts[f] == 0 {
			t.Errorf("seed %d: fault %s was never applied", seed, f)
		}
	}
	if !chaosEventually(2*time.Second, func() bool { return len(d.unclosed()) == 0 }) {
		t.Errorf("seed %d: connections not closed after Close: %v", seed, d.unclosed())
	}
	srv.stop()
	t.Logf("seed %d: outcomes %v, dial faults %v", seed, classes, d.counts)
	chaosLeakCheck(t, baseline)
}
