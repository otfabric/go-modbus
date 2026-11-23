// SPDX-License-Identifier: MIT

// Command security_hooks runs a Modbus TCP server that reports what the
// library observes about its connections and requests to an application-owned
// channel, refuses writes and state-changing diagnostics from clients that
// are not allowed to make them, and opts into the per-host and slow-client
// limits. It uses nothing but the standard library and go-modbus: what to do
// with the facts (log them, count them, send them to a SIEM) is the
// application's.
//
// Run it with
//
//	go run ./examples/security_hooks
//
// and try it with modbus-cli, for example a read (served) and a write
// (refused with exception 1, as are the FC08 sub-functions that change the
// device's state):
//
//	modbus-cli --target tcp://localhost:5502 rh:uint16:0
//	modbus-cli --target tcp://localhost:5502 wr:uint16:0:42
//
// Those two trigger OnConnectionOpened, OnConnectionCloseReason and, for the
// write, OnError with ErrNotAuthorized. The other hooks it implements need
// other conditions: OnTLSHandshake a tcp+tls listener, OnProtocolViolation a
// malformed request, OnAcceptError a listener that fails, and
// OnConnectionRejected a client beyond the limits.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-modbus"
)

// fact is one observation, as the application chooses to record it.
type fact struct {
	what  string
	attrs []any
}

// observer implements modbus.ServerMetrics and the optional extensions it
// wants. Every method hands a fact to a buffered channel without blocking: the
// library calls them on its own goroutines and they must not block. When the
// channel is full the fact is counted as dropped.
type observer struct {
	facts   chan fact
	dropped atomic.Uint64
}

func (o *observer) send(what string, attrs ...any) {
	select {
	case o.facts <- fact{what, attrs}:
	default:
		o.dropped.Add(1)
	}
}

// ServerMetrics: request outcomes. Refusals by the authorizer are errors that
// match modbus.ErrNotAuthorized.
func (o *observer) OnRequest(uint8, modbus.FunctionCode)                 {}
func (o *observer) OnResponse(uint8, modbus.FunctionCode, time.Duration) {}
func (o *observer) OnError(unit uint8, fc modbus.FunctionCode, _ time.Duration, err error) {
	if errors.Is(err, modbus.ErrNotAuthorized) {
		o.send("request refused", "unit", unit, "fc", fc, "err", err)
	}
}

// ConnectionMetrics, ConnectionLifecycleMetrics, ConnectionCloseMetrics.
func (o *observer) OnConnectionRejected(addr string, reason modbus.RejectReason) {
	o.send("connection rejected", "addr", addr, "reason", reason)
}
func (o *observer) OnConnectionOpened(addr, role string) {
	o.send("connection opened", "addr", addr, "role", role)
}
func (o *observer) OnConnectionClosed(string) {}
func (o *observer) OnConnectionCloseReason(addr string, reason modbus.CloseReason, err error) {
	o.send("connection closed", "addr", addr, "reason", reason, "err", err)
}

// TLSHandshakeMetrics (tcp+tls only), AcceptErrorMetrics and
// ProtocolViolationMetrics.
func (o *observer) OnTLSHandshake(info modbus.TLSHandshakeInfo) {
	o.send("tls handshake", "addr", info.RemoteAddr, "err", info.Err, "role", info.Role, "roleErr", info.RoleErr)
}
func (o *observer) OnAcceptError(err error, retryIn time.Duration) {
	o.send("accept failed", "err", err, "retryIn", retryIn)
}
func (o *observer) OnProtocolViolation(v modbus.ProtocolViolation) {
	o.send("protocol violation", "addr", v.ClientAddr, "unit", v.UnitID, "fc", v.FunctionCode,
		"hasFC", v.HasFunctionCode, "exception", v.Exception, "closed", v.Closed, "err", v.Err)
}

// authorize refuses every request of a client whose certificate carries an
// unusable role, and serves reads, and the diagnostics that only report, to
// every other client. Everything else, writes and the FC08 sub-functions that change the
// device's state, it serves only to clients whose verified tcp+tls
// certificate carries the "operator" role (so on plain tcp, as here, all of
// it is refused), and never to the broadcast unit.
func authorize(_ context.Context, req *modbus.AuthorizationRequest) error {
	if req.ClientRoleErr != nil {
		// A verified certificate whose role claim the server could not use
		// (it matches modbus.ErrInvalidRole): refuse it outright, while a
		// client without any role may still read.
		return req.ClientRoleErr
	}
	if req.Access == modbus.AccessRead || reportsOnly(req) {
		return nil
	}
	if req.UnitID == 0 {
		return errors.New("no broadcast changes")
	}
	if req.ClientRole == "operator" {
		return nil
	}
	return errors.New("changes need the operator role") // answered with exception 1
}

// reportsOnly reports whether req is a diagnostic that changes nothing on the
// device. FC07, FC11, FC12, FC17 and FC43 read state or identification; of the
// FC08 sub-functions (MODBUS Application Protocol V1.1b3, section 6.8.1), 0x00
// echoes its data and 0x02 and 0x0B-0x12 return a register or a counter,
// while 0x01 restarts the port, 0x03 changes the ASCII delimiter, 0x04
// forces Listen Only Mode and 0x0A and 0x14 clear counters. An unknown
// sub-function is not assumed to be harmless.
func reportsOnly(req *modbus.AuthorizationRequest) bool {
	if req.Access != modbus.AccessDiagnostic {
		return false
	}
	if req.FunctionCode != modbus.FCDiagnostics {
		return true
	}
	if !req.HasSubFunction {
		return false
	}
	switch sub := req.SubFunction; {
	case sub == 0x00, sub == 0x02, sub >= 0x0B && sub <= 0x12:
		return true
	default:
		return false
	}
}

type registers struct {
	mu   sync.Mutex
	regs [100]uint16
}

func (r *registers) HandleCoils(context.Context, *modbus.CoilsRequest) ([]bool, error) {
	return nil, modbus.ErrIllegalFunction
}
func (r *registers) HandleDiscreteInputs(context.Context, *modbus.DiscreteInputsRequest) ([]bool, error) {
	return nil, modbus.ErrIllegalFunction
}
func (r *registers) HandleInputRegisters(context.Context, *modbus.InputRegistersRequest) ([]uint16, error) {
	return nil, modbus.ErrIllegalFunction
}
func (r *registers) HandleHoldingRegisters(_ context.Context, req *modbus.HoldingRegistersRequest) ([]uint16, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if int(req.Addr)+int(req.Quantity) > len(r.regs) {
		return nil, modbus.ErrIllegalDataAddress
	}
	if req.IsWrite {
		copy(r.regs[req.Addr:], req.Args)
		return nil, nil
	}
	return append([]uint16(nil), r.regs[req.Addr:int(req.Addr)+int(req.Quantity)]...), nil
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	obs := &observer{facts: make(chan fact, 256)}

	srv, err := modbus.NewServer(&modbus.ServerConfig{
		URL:     "tcp://localhost:5502",
		Timeout: 2 * time.Minute,
		Metrics: obs,
		// Opt-in hardening: every one of these is off by default.
		MaxClients:          16,
		MaxClientsPerHost:   4,
		FirstRequestTimeout: 10 * time.Second,
		FrameTimeout:        5 * time.Second,
		Authorizer:          modbus.RequestAuthorizerFunc(authorize),
	}, &registers{})
	if err != nil {
		logger.Error("server", "err", err)
		os.Exit(1)
	}
	if err := srv.Start(); err != nil {
		logger.Error("start", "err", err)
		os.Exit(1)
	}
	logger.Info("listening", "addr", srv.Addr())

	// The application's consumer: here a logger; it could as well count,
	// alert or forward.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for f := range obs.facts {
			logger.Info(f.what, f.attrs...)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	<-ctx.Done()
	_ = srv.Stop()
	// No callback runs once Stop has returned.
	close(obs.facts)
	<-done
	logger.Info("stopped", "dropped", obs.dropped.Load())
}
