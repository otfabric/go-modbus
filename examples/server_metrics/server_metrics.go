// SPDX-License-Identifier: MIT

// Command server_metrics shows the server's observation hooks for requests
// and connections: ServerMetrics for the outcome of each request,
// ConnectionMetrics for connections refused at accept,
// ConnectionLifecycleMetrics and ConnectionCloseMetrics for the connections
// served and why they ended, and ServerConfig.PDUTap for the raw PDUs of every
// request and its answer. One value implements the metrics interfaces; the
// server finds the optional ones by type assertion. The security extensions
// (TLS handshakes, protocol violations, accept errors, the authorizer) are in
// examples/security_hooks.
//
// It runs the server and a few clients of its own on the loopback interface:
//
//	go run ./examples/server_metrics
//
// The counters are atomics, which the hooks update without blocking; an
// application would export them instead of printing them.
//
// The run triggers every interface it implements: requests, responses and
// one exception; one connection served and closed by its client
// (CloseClientClosed); one refused for MaxClients (RejectMaxClients); and
// the PDUs of the three requests. The other reject and close reasons need
// other settings or failures (AllowClient, MaxClientsPerHost, timeouts, a
// malformed frame, Stop).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-modbus"
)

// serverCounters implements modbus.ServerMetrics, modbus.ConnectionMetrics,
// modbus.ConnectionLifecycleMetrics and modbus.ConnectionCloseMetrics, and
// modbus.ServerPDUTap. OnRequest sees the function code before the server
// checked it, so the arrays have room for all 256 values.
type serverCounters struct {
	requests, responses, errs [256]atomic.Uint64
	rejected                  [8]atomic.Uint64 // by RejectReason
	closed                    [16]atomic.Uint64
	opened, open              atomic.Int64
	pdus                      atomic.Uint64

	mu   sync.Mutex
	last string // the last exchange, for the audit-trail example
}

// ServerMetrics: one call per request, and its outcome.
func (s *serverCounters) OnRequest(_ uint8, fc modbus.FunctionCode) { s.requests[fc].Add(1) }
func (s *serverCounters) OnResponse(_ uint8, fc modbus.FunctionCode, _ time.Duration) {
	s.responses[fc].Add(1)
}
func (s *serverCounters) OnError(_ uint8, fc modbus.FunctionCode, _ time.Duration, _ error) {
	s.errs[fc].Add(1)
}

// ConnectionMetrics: connections the server closed as soon as it accepted
// them (MaxClients, MaxClientsPerHost, AllowClient) or evicted mid-handshake.
func (s *serverCounters) OnConnectionRejected(_ string, reason modbus.RejectReason) {
	if int(reason) < len(s.rejected) {
		s.rejected[reason].Add(1)
	}
}

// ConnectionLifecycleMetrics: every connection served is opened once and
// closed once.
func (s *serverCounters) OnConnectionOpened(string, string) { s.opened.Add(1); s.open.Add(1) }
func (s *serverCounters) OnConnectionClosed(string)         { s.open.Add(-1) }

// ConnectionCloseMetrics: why, right after OnConnectionClosed.
func (s *serverCounters) OnConnectionCloseReason(_ string, reason modbus.CloseReason, _ error) {
	if int(reason) < len(s.closed) {
		s.closed[reason].Add(1)
	}
}

// OnServerPDU (ServerConfig.PDUTap): the raw request and the answer decided
// on, with the client's address and role, for an audit trail.
func (s *serverCounters) OnServerPDU(_ context.Context, ex modbus.ServerPDUExchange) {
	s.pdus.Add(1)
	s.mu.Lock()
	s.last = fmt.Sprintf("%s unit %d: % X -> % X", ex.ClientAddr, ex.UnitID, ex.Request, ex.Response)
	s.mu.Unlock()
}

func (s *serverCounters) print(w io.Writer) error {
	var b strings.Builder
	for fc := range s.requests {
		if n := s.requests[fc].Load(); n > 0 {
			fmt.Fprintf(&b, "%-35s requests %d, responses %d, errors %d\n", modbus.FunctionCode(fc),
				n, s.responses[fc].Load(), s.errs[fc].Load())
		}
	}
	fmt.Fprintf(&b, "connections opened %d, still open %d\n", s.opened.Load(), s.open.Load())
	for r := range s.rejected {
		if n := s.rejected[r].Load(); n > 0 {
			fmt.Fprintf(&b, "rejected (%s): %d\n", modbus.RejectReason(r), n)
		}
	}
	for r := range s.closed {
		if n := s.closed[r].Load(); n > 0 {
			fmt.Fprintf(&b, "closed (%s): %d\n", modbus.CloseReason(r), n)
		}
	}
	s.mu.Lock()
	fmt.Fprintf(&b, "pdus %d, last: %s\n", s.pdus.Load(), s.last)
	s.mu.Unlock()
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

func dial(ctx context.Context, srv *modbus.Server) (*modbus.Client, error) {
	c, err := modbus.New(modbus.Config{URL: "tcp://" + srv.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	return c, c.OpenContext(ctx)
}

// run serves one client a read, a write and a read out of range; refuses a
// second client while the first is connected (MaxClients is 1); then stops
// and prints the counters to w. Once Stop has returned no hook runs, so the
// counters are final.
func run(ctx context.Context, w io.Writer) (*serverCounters, error) {
	counters := &serverCounters{}
	srv, err := modbus.NewServer(&modbus.ServerConfig{
		URL:        "tcp://127.0.0.1:0",
		Timeout:    time.Minute,
		MaxClients: 1,
		Metrics:    counters, // ServerMetrics, and the optional extensions it implements
		PDUTap:     counters,
	}, tenRegisters{})
	if err != nil {
		return nil, err
	}
	if err := srv.Start(); err != nil {
		return nil, err
	}
	stopped := false
	defer func() {
		if !stopped {
			_ = srv.Stop()
		}
	}()

	first, err := dial(ctx, srv)
	if err != nil {
		return nil, err
	}
	if _, err := first.ReadHoldingRegisters(ctx, 1, 0, 4); err != nil {
		return nil, err
	}
	if err := first.WriteRegister(ctx, 1, 2, 42); err != nil {
		return nil, err
	}
	if _, err := first.ReadHoldingRegister(ctx, 1, 50); !errors.Is(err, modbus.ErrIllegalDataAddress) {
		return nil, fmt.Errorf("read of address 50: %w, want an exception", err)
	}

	// The only slot is taken: the server closes the second connection as
	// soon as it accepts it, and its request fails.
	second, err := dial(ctx, srv)
	if err == nil {
		_, _ = second.ReadHoldingRegister(ctx, 1, 0)
		_ = second.Close()
	}
	for counters.rejected[modbus.RejectMaxClients].Load() == 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}

	_ = first.Close()
	for counters.open.Load() != 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	stopped = true
	if err := srv.Stop(); err != nil {
		return nil, err
	}
	return counters, counters.print(w)
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := run(ctx, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
