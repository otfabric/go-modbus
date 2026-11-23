// SPDX-License-Identifier: MIT

// Command client_metrics shows the client's observation hooks: ClientMetrics
// for the outcome of each call, AttemptMetrics for each attempt and re-dial,
// the security extensions ResponseAnomalyMetrics and TLSHandshakeMetrics,
// ClientConnectionMetrics for the dials, discards and closes of the client's
// connections, and Config.PDUTap for the raw request and response PDUs. One value implements
// all the interfaces; the client finds the optional ones by type assertion.
//
// It starts a small server of its own on the loopback interface, so it needs
// no device:
//
//	go run ./examples/client_metrics
//
// The counters are atomics indexed by function code, which every hook can
// update without blocking; an application would export them (Prometheus,
// expvar, OpenTelemetry, a log line) instead of printing them.
//
// What the run triggers, on loopback tcp with a healthy server: OnRequest,
// OnResponse and OnError (one exception), OnAttempt, OnPDU, and
// OnClientConnection for the dial of Open and, once Close ran, the close.
// Implemented but not triggered here: OnTimeout and OnRetryDial (a slow or
// lost link; OnClientConnection then also reports discards and re-dials),
// OnResponseAnomaly (a peer that sends late or foreign frames, or stale
// serial bytes) and OnTLSHandshake (tcp+tls only).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-modbus"
)

// clientCounters implements modbus.ClientMetrics, modbus.AttemptMetrics,
// modbus.ResponseAnomalyMetrics, modbus.TLSHandshakeMetrics,
// modbus.ClientConnectionMetrics and modbus.PDUTap. The arrays have room for all 256 function codes.
type clientCounters struct {
	requests, responses, errs, timeouts [256]atomic.Uint64
	exceptions                          atomic.Uint64
	attempts, failedAttempts, redials   atomic.Uint64
	anomalies                           [4]atomic.Uint64 // by ResponseAnomalyKind
	handshakes, failedHandshakes        atomic.Uint64
	pdus                                atomic.Uint64
	lastLatency                         atomic.Int64 // nanoseconds
	dials, failedDials, closes          atomic.Uint64
	discards                            [8]atomic.Uint64 // by DiscardReason
	lastConnection                      atomic.Uint64    // a ConnectionID, for a log line
}

// ClientMetrics: one call per client method, whatever its retries.
func (c *clientCounters) OnRequest(_ uint8, fc modbus.FunctionCode) { c.requests[fc].Add(1) }
func (c *clientCounters) OnResponse(_ uint8, fc modbus.FunctionCode, d time.Duration) {
	c.responses[fc].Add(1)
	c.lastLatency.Store(int64(d))
}
func (c *clientCounters) OnError(_ uint8, fc modbus.FunctionCode, _ time.Duration, err error) {
	c.errs[fc].Add(1)
	var exc *modbus.ExceptionError
	if errors.As(err, &exc) {
		c.exceptions.Add(1) // the device answered, with an exception
	}
}
func (c *clientCounters) OnTimeout(_ uint8, fc modbus.FunctionCode, _ time.Duration) {
	c.timeouts[fc].Add(1)
}

// AttemptMetrics: every attempt of a call, and every re-dial. err is the
// attempt's error on the link; an attempt the device answered, with an
// exception too, has none (the call's outcome is in OnError).
func (c *clientCounters) OnAttempt(_ uint8, _ modbus.FunctionCode, _ int, _ time.Duration, err error) {
	c.attempts.Add(1)
	if err != nil {
		c.failedAttempts.Add(1)
	}
}
func (c *clientCounters) OnRetryDial(int, time.Duration, error) { c.redials.Add(1) }

// ResponseAnomalyMetrics: frames the client skipped (another transaction ID,
// a protocol ID other than 0) and stale serial bytes it discarded. These do
// not show in the outcome of the call.
func (c *clientCounters) OnResponseAnomaly(a modbus.ResponseAnomaly) {
	if int(a.Kind) < len(c.anomalies) {
		c.anomalies[a.Kind].Add(1)
	}
}

// TLSHandshakeMetrics: every handshake of a tcp+tls dial (not called on tcp,
// as here).
func (c *clientCounters) OnTLSHandshake(info modbus.TLSHandshakeInfo) {
	c.handshakes.Add(1)
	if info.Err != nil {
		c.failedHandshakes.Add(1)
	}
}

// ClientConnectionMetrics: every dial (Open, a pool's pre-warm and on-demand
// dials, a re-dial), every connection discarded after a failure, and every
// connection closed by Close. Count by Kind and reason; keep the ConnectionID
// for logs (it also is in OnPDU, OnTLSHandshake and OnResponseAnomaly), never
// as a metric label: it is new for every connection.
func (c *clientCounters) OnClientConnection(ev modbus.ClientConnectionEvent) {
	switch ev.Kind {
	case modbus.ClientConnectionDialed:
		c.dials.Add(1)
		if ev.Err != nil {
			c.failedDials.Add(1)
		} else {
			c.lastConnection.Store(uint64(ev.ConnectionID))
		}
	case modbus.ClientConnectionDiscarded:
		if int(ev.DiscardReason) < len(c.discards) {
			c.discards[ev.DiscardReason].Add(1)
		}
	case modbus.ClientConnectionClosed:
		c.closes.Add(1)
	}
}

// PDUTap (Config.PDUTap): the raw PDUs of every answered request. The slices
// are the tap's own copies.
func (c *clientCounters) OnPDU(_ context.Context, _ modbus.PDUExchange) { c.pdus.Add(1) }

func (c *clientCounters) print(w io.Writer) error {
	var b strings.Builder
	for fc := range c.requests {
		if n := c.requests[fc].Load(); n > 0 {
			fmt.Fprintf(&b, "%-35s requests %d, responses %d, errors %d, timeouts %d\n", modbus.FunctionCode(fc),
				n, c.responses[fc].Load(), c.errs[fc].Load(), c.timeouts[fc].Load())
		}
	}
	fmt.Fprintf(&b, "exceptions %d; attempts %d (failed %d); re-dials %d; last latency %v\n",
		c.exceptions.Load(), c.attempts.Load(), c.failedAttempts.Load(), c.redials.Load(),
		time.Duration(c.lastLatency.Load()))
	fmt.Fprintf(&b, "skipped transaction ids %d, protocol ids %d, discarded %d; tls handshakes %d (failed %d); pdus %d\n",
		c.anomalies[modbus.AnomalyTransactionID].Load(), c.anomalies[modbus.AnomalyProtocolID].Load(),
		c.anomalies[modbus.AnomalyDiscarded].Load(), c.handshakes.Load(), c.failedHandshakes.Load(), c.pdus.Load())
	fmt.Fprintf(&b, "dials %d (failed %d), transport-error discards %d, closes %d; last connection %s\n",
		c.dials.Load(), c.failedDials.Load(), c.discards[modbus.DiscardTransportError].Load(), c.closes.Load(),
		modbus.ConnectionID(c.lastConnection.Load()))
	_, err := io.WriteString(w, b.String())
	return err
}

// tenRegisters serves holding registers 0-9 and nothing else.
type tenRegisters struct{}

func (tenRegisters) HandleCoils(context.Context, *modbus.CoilsRequest) ([]bool, error) {
	return nil, modbus.ErrIllegalFunction
}
func (tenRegisters) HandleDiscreteInputs(context.Context, *modbus.DiscreteInputsRequest) ([]bool, error) {
	return nil, modbus.ErrIllegalFunction
}
func (tenRegisters) HandleInputRegisters(context.Context, *modbus.InputRegistersRequest) ([]uint16, error) {
	return nil, modbus.ErrIllegalFunction
}
func (tenRegisters) HandleHoldingRegisters(_ context.Context, req *modbus.HoldingRegistersRequest) ([]uint16, error) {
	if int(req.Addr)+int(req.Quantity) > 10 {
		return nil, modbus.ErrIllegalDataAddress
	}
	if req.IsWrite {
		return nil, nil
	}
	return make([]uint16, req.Quantity), nil
}

// run starts the server, makes a few calls with the counters attached and
// prints them to w.
func run(ctx context.Context, w io.Writer) (*clientCounters, error) {
	srv, err := modbus.NewServer(&modbus.ServerConfig{URL: "tcp://127.0.0.1:0", Timeout: time.Minute}, tenRegisters{})
	if err != nil {
		return nil, err
	}
	if err := srv.Start(); err != nil {
		return nil, err
	}
	defer func() { _ = srv.Stop() }()

	counters := &clientCounters{}
	client, err := modbus.New(modbus.Config{
		URL:         "tcp://" + srv.Addr().String(),
		Timeout:     2 * time.Second,
		RetryPolicy: modbus.ExponentialBackoff(50*time.Millisecond, time.Second, 3),
		Metrics:     counters, // ClientMetrics, and the optional extensions it implements
		PDUTap:      counters,
	})
	if err != nil {
		return nil, err
	}
	if err := client.Open(); err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()

	if _, err := client.ReadHoldingRegisters(ctx, 1, 0, 4); err != nil {
		return nil, err
	}
	if err := client.WriteRegister(ctx, 1, 2, 42); err != nil {
		return nil, err
	}
	// Address 50 is not served: an Illegal Data Address exception, an
	// OnError, and not retried.
	if _, err := client.ReadHoldingRegister(ctx, 1, 50); !errors.Is(err, modbus.ErrIllegalDataAddress) {
		return nil, fmt.Errorf("read of address 50: %w, want an exception", err)
	}
	return counters, counters.print(w)
}

func main() {
	if _, err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
