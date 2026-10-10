// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// e2eMultiUnit routes requests to one device per unit ID, like a gateway. Units
// it does not know are answered with Gateway Target Device Failed To Respond.
type e2eMultiUnit map[uint8]*e2eDevice

func (m e2eMultiUnit) HandleCoils(ctx context.Context, req *CoilsRequest) ([]bool, error) {
	if d, ok := m[req.UnitID]; ok {
		return d.HandleCoils(ctx, req)
	}
	return nil, ErrGWTargetFailedToRespond
}

func (m e2eMultiUnit) HandleDiscreteInputs(ctx context.Context, req *DiscreteInputsRequest) ([]bool, error) {
	if d, ok := m[req.UnitID]; ok {
		return d.HandleDiscreteInputs(ctx, req)
	}
	return nil, ErrGWTargetFailedToRespond
}

func (m e2eMultiUnit) HandleHoldingRegisters(ctx context.Context, req *HoldingRegistersRequest) ([]uint16, error) {
	if d, ok := m[req.UnitID]; ok {
		return d.HandleHoldingRegisters(ctx, req)
	}
	return nil, ErrGWTargetFailedToRespond
}

func (m e2eMultiUnit) HandleInputRegisters(ctx context.Context, req *InputRegistersRequest) ([]uint16, error) {
	if d, ok := m[req.UnitID]; ok {
		return d.HandleInputRegisters(ctx, req)
	}
	return nil, ErrGWTargetFailedToRespond
}

func TestE2E_Server_UnitIDFiltering(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		units := []uint8{0, 1, 2, 247, 255}
		gw := e2eMultiUnit{}
		for _, u := range units {
			d := e2eNewDevice()
			d.unit = u
			gw[u] = d
		}
		p := e2eStart(t, kind, gw, e2eOpts{})
		c := p.client
		ctx := context.Background()

		// Each unit keeps its own state; the response carries the unit ID back.
		for _, u := range units {
			if err := c.WriteRegister(ctx, u, 7, 0x1000+uint16(u)); err != nil {
				t.Fatalf("unit %d: WriteRegister: %v", u, err)
			}
			if err := c.WriteCoil(ctx, u, 7, u%2 == 1); err != nil {
				t.Fatalf("unit %d: WriteCoil: %v", u, err)
			}
			gw[u].setInput(7, 0x2000+uint16(u))
			gw[u].setDiscrete(7, u%2 == 0)
		}
		for _, u := range units {
			if got, err := c.ReadHoldingRegister(ctx, u, 7); err != nil || got != 0x1000+uint16(u) {
				t.Errorf("unit %d: holding = 0x%04X, %v", u, got, err)
			}
			if got, err := c.ReadInputRegister(ctx, u, 7); err != nil || got != 0x2000+uint16(u) {
				t.Errorf("unit %d: input = 0x%04X, %v", u, got, err)
			}
			if got, err := c.ReadCoil(ctx, u, 7); err != nil || got != (u%2 == 1) {
				t.Errorf("unit %d: coil = %v, %v", u, got, err)
			}
			if got, err := c.ReadDiscreteInput(ctx, u, 7); err != nil || got != (u%2 == 0) {
				t.Errorf("unit %d: discrete input = %v, %v", u, got, err)
			}
			calls := gw[u].takeCalls()
			if len(calls) != 6 {
				t.Errorf("unit %d: handler saw %d requests, want 6", u, len(calls))
			}
			for _, call := range calls {
				if call.UnitID != u {
					t.Errorf("unit %d: handler saw unit %d", u, call.UnitID)
				}
			}
		}

		// Units behind no device: the gateway exception reaches the client.
		for _, u := range []uint8{3, 100, 248} {
			_, err := c.ReadHoldingRegisters(ctx, u, 0, 1)
			e2eWantException(t, "ReadHoldingRegisters", err, FCReadHoldingRegisters, exGWTargetFailedToRespond)
			_, err = c.ReadInputRegisters(ctx, u, 0, 1)
			e2eWantException(t, "ReadInputRegisters", err, FCReadInputRegisters, exGWTargetFailedToRespond)
			_, err = c.ReadCoils(ctx, u, 0, 1)
			e2eWantException(t, "ReadCoils", err, FCReadCoils, exGWTargetFailedToRespond)
			_, err = c.ReadDiscreteInputs(ctx, u, 0, 1)
			e2eWantException(t, "ReadDiscreteInputs", err, FCReadDiscreteInputs, exGWTargetFailedToRespond)
			e2eWantException(t, "WriteCoil", c.WriteCoil(ctx, u, 0, true), FCWriteSingleCoil, exGWTargetFailedToRespond)
			e2eWantException(t, "WriteRegisters", c.WriteRegisters(ctx, u, 0, []uint16{1}), FCWriteMultipleRegisters, exGWTargetFailedToRespond)
		}
		if p.conns() != 1 {
			t.Errorf("server connections = %d, want 1", p.conns())
		}

		// A device filtering on its own unit ID, as e2eDevice does.
		dev := e2eNewDevice()
		p2 := e2eStart(t, kind, dev, e2eOpts{})
		for _, op := range e2eAllOps() {
			if !op.served || op.probe {
				continue
			}
			dev.setUnit(9)
			err := op.call(ctx, p2.client)
			dev.setUnit(e2eUnit)
			e2eWantException(t, op.name+" on a unit the device does not serve", err, op.fc, exGWPathUnavailable)
		}
	})
}

// e2eHandlerErrors lists every error a handler can return together with the
// exception the client must see.
func e2eHandlerErrors() []struct {
	err  error
	code ExceptionCode
} {
	return []struct {
		err  error
		code ExceptionCode
	}{
		{ErrIllegalFunction, exIllegalFunction},
		{ErrIllegalDataAddress, exIllegalDataAddress},
		{ErrIllegalDataValue, exIllegalDataValue},
		{ErrServerDeviceFailure, exServerDeviceFailure},
		{ErrAcknowledge, exAcknowledge},
		{ErrServerDeviceBusy, exServerDeviceBusy},
		{ErrMemoryParityError, exMemoryParityError},
		{ErrGWPathUnavailable, exGWPathUnavailable},
		{ErrGWTargetFailedToRespond, exGWTargetFailedToRespond},
		{fmt.Errorf("wrapped: %w", ErrServerDeviceBusy), exServerDeviceBusy},
		{mapExceptionCodeToError(FCReadCoils, exIllegalDataValue), exIllegalDataValue},
		{errors.New("some application error"), exServerDeviceFailure},
		{context.DeadlineExceeded, exServerDeviceFailure},
	}
}

// Every error a handler can return reaches the client as the matching Modbus
// exception, for every function code the server dispatches to a handler.
func TestE2E_Server_ExceptionMatrix(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		ctx := context.Background()

		var failMu sync.Mutex
		var fail error
		setFail := func(err error) {
			failMu.Lock()
			fail = err
			failMu.Unlock()
		}
		dev.setHook(func(context.Context, e2eCall) error {
			failMu.Lock()
			defer failMu.Unlock()
			return fail
		})

		requests := 0
		for _, op := range e2eAllOps() {
			if !op.served || op.probe {
				continue
			}
			for _, he := range e2eHandlerErrors() {
				setFail(he.err)
				err := op.call(ctx, p.client)
				what := fmt.Sprintf("%s with handler error %q", op.name, he.err)
				e2eWantException(t, what, err, op.fc, he.code)
				call := e2eOneCall(t, what, dev)
				if call.UnitID != e2eUnit {
					t.Errorf("%s: handler saw %+v", what, call)
				}
				requests++
			}
		}
		// All of it happened on one connection, which is still in sync.
		if got := p.client.LastObservedTransactionID(); int(got) != requests {
			t.Errorf("transaction ID %d after %d requests", got, requests)
		}
		if p.conns() != 1 {
			t.Errorf("server connections = %d, want 1", p.conns())
		}
		setFail(nil)
		for _, op := range e2eAllOps() {
			if !op.served {
				continue
			}
			if err := op.call(ctx, p.client); err != nil {
				t.Errorf("%s after the exception matrix: %v", op.name, err)
			}
		}
	})
}

// A panicking handler costs the client one Server Device Failure; the server,
// and the very connection the panic happened on, keep serving.
func TestE2E_Server_HandlerPanic(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		log := &e2eLogger{}
		p := e2eStart(t, kind, dev, e2eOpts{server: func(c *ServerConfig) { c.Logger = log }})
		ctx := context.Background()

		var boom atomic.Bool
		dev.setHook(func(context.Context, e2eCall) error {
			if boom.Load() {
				panic("e2e handler panic")
			}
			return nil
		})

		requests := 0
		for _, op := range e2eAllOps() {
			if !op.served || op.probe {
				continue
			}
			boom.Store(true)
			err := op.call(ctx, p.client)
			e2eWantException(t, op.name+" with a panicking handler", err, op.fc, exServerDeviceFailure)
			boom.Store(false)
			if err := op.call(ctx, p.client); err != nil {
				t.Errorf("%s after a handler panic: %v", op.name, err)
			}
			requests += 1 + op.requests
		}
		if got := p.client.LastObservedTransactionID(); int(got) != requests {
			t.Errorf("transaction ID %d after %d requests: the connection was replaced", got, requests)
		}
		if p.conns() != 1 {
			t.Errorf("server connections = %d, want 1", p.conns())
		}
		if !log.has("E ", "panic in handler", "e2e handler panic") {
			t.Errorf("the panic was not logged as an error:\n%s", log.dump())
		}
		// Other clients are unaffected.
		other := p.newClient(nil)
		if err := other.Open(); err != nil {
			t.Fatal(err)
		}
		if _, err := other.ReadCoil(ctx, e2eUnit, 0); err != nil {
			t.Errorf("second client after handler panics: %v", err)
		}
	})
}

// Handlers that break their contract (wrong result length, nil response) are
// reported to the client as Server Device Failure.
func TestE2E_Server_MisbehavingHandler(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		h := &fnHandler{
			coils:     func(req *CoilsRequest) ([]bool, error) { return make([]bool, req.Quantity+1), nil },
			inputs:    func(*DiscreteInputsRequest) ([]bool, error) { return nil, nil },
			holding:   func(req *HoldingRegistersRequest) ([]uint16, error) { return make([]uint16, req.Quantity-1), nil },
			inputRegs: func(req *InputRegistersRequest) ([]uint16, error) { return make([]uint16, 2*req.Quantity), nil },
			readWrite: func(*ReadWriteRegistersRequest) ([]uint16, error) { return nil, nil },
			devID:     func(*DeviceIdentificationRequest) (*DeviceIdentificationResponse, error) { return nil, nil },
		}
		p := e2eStart(t, kind, h, e2eOpts{})
		c := p.client
		ctx := context.Background()

		_, err := c.ReadCoils(ctx, e2eUnit, 0, 8)
		e2eWantException(t, "ReadCoils", err, FCReadCoils, exServerDeviceFailure)
		_, err = c.ReadDiscreteInputs(ctx, e2eUnit, 0, 8)
		e2eWantException(t, "ReadDiscreteInputs", err, FCReadDiscreteInputs, exServerDeviceFailure)
		_, err = c.ReadHoldingRegisters(ctx, e2eUnit, 0, 4)
		e2eWantException(t, "ReadHoldingRegisters", err, FCReadHoldingRegisters, exServerDeviceFailure)
		_, err = c.ReadInputRegisters(ctx, e2eUnit, 0, 4)
		e2eWantException(t, "ReadInputRegisters", err, FCReadInputRegisters, exServerDeviceFailure)
		_, err = c.ReadWriteMultipleRegisters(ctx, e2eUnit, 0, 4, 0, []uint16{1})
		e2eWantException(t, "ReadWriteMultipleRegisters", err, FCReadWriteMultipleRegs, exServerDeviceFailure)
		_, err = c.ReadDeviceIdentification(ctx, e2eUnit, DeviceIDBasic, 0)
		e2eWantException(t, "ReadDeviceIdentification", err, FCEncapsulatedInterface, exServerDeviceFailure)
		// Writes ignore the returned slice.
		if err := c.WriteCoil(ctx, e2eUnit, 0, true); err != nil {
			t.Errorf("WriteCoil: %v", err)
		}
		if err := c.WriteRegisters(ctx, e2eUnit, 0, []uint16{1, 2}); err != nil {
			t.Errorf("WriteRegisters: %v", err)
		}
		if p.conns() != 1 {
			t.Errorf("server connections = %d, want 1", p.conns())
		}
	})
}

// Every handler receives the address of the connection its request came in on.
func TestE2E_Server_ClientAddr(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{})
		ctx := context.Background()

		remote := func() string {
			p.server.lock.Lock()
			defer p.server.lock.Unlock()
			if len(p.server.tcpClients) != 1 {
				t.Fatalf("server tracks %d connections, want 1", len(p.server.tcpClients))
			}
			return p.server.tcpClients[0].RemoteAddr().String()
		}
		e2eEventually(t, "the server to accept the connection", func() bool { return p.conns() == 1 })
		first := remote()
		for _, op := range e2eAllOps() {
			if !op.served {
				continue
			}
			if err := op.call(ctx, p.client); err != nil {
				t.Fatalf("%s: %v", op.name, err)
			}
			for _, call := range dev.takeCalls() {
				if call.ClientAddr != first {
					t.Errorf("%s: handler saw client address %q, want %q", op.name, call.ClientAddr, first)
				}
			}
		}
		if host, _, err := net.SplitHostPort(first); err != nil || !net.ParseIP(host).IsLoopback() {
			t.Errorf("client address %q is not a loopback host:port", first)
		}

		// A second client is told apart; a reconnect gets a new address.
		other := p.newClient(nil)
		if err := other.Open(); err != nil {
			t.Fatal(err)
		}
		if _, err := other.ReadCoil(ctx, e2eUnit, 0); err != nil {
			t.Fatal(err)
		}
		otherAddr := e2eOneCall(t, "second client", dev).ClientAddr
		if otherAddr == first || otherAddr == "" {
			t.Errorf("second client address %q, first %q", otherAddr, first)
		}
		_ = other.Close()
		_ = p.client.Close()
		e2eEventually(t, "server to drop both connections", func() bool { return p.conns() == 0 })
		if err := p.client.Open(); err != nil {
			t.Fatal(err)
		}
		if _, err := p.client.ReadCoil(ctx, e2eUnit, 0); err != nil {
			t.Fatal(err)
		}
		if again := e2eOneCall(t, "reconnected client", dev).ClientAddr; again == first {
			t.Errorf("reconnected client kept address %q", again)
		}
	})
}

func TestE2E_Server_MaxClients(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		p := e2eStart(t, kind, dev, e2eOpts{server: func(c *ServerConfig) { c.MaxClients = 2 }})
		ctx := context.Background()
		c1 := p.client
		c2 := p.newClient(nil)
		c3 := p.newClient(func(c *Config) { c.Timeout = 500 * time.Millisecond })

		if err := c2.Open(); err != nil {
			t.Fatal(err)
		}
		for i, c := range []*Client{c1, c2} {
			if err := c.WriteRegister(ctx, e2eUnit, uint16(i), 0x0101); err != nil {
				t.Fatalf("client %d: %v", i+1, err)
			}
		}
		e2eEventually(t, "two server connections", func() bool { return p.conns() == 2 })
		dev.takeCalls()

		// The third client is turned away: it either cannot open (TLS) or its
		// first request fails (TCP); its request never reaches a handler.
		err := c3.Open()
		if err == nil {
			_, err = c3.ReadHoldingRegister(ctx, e2eUnit, 0)
		}
		if err == nil {
			t.Fatal("third client was served although MaxClients is 2")
		}
		if errors.Is(err, ErrRequestTimedOut) {
			t.Errorf("third client timed out instead of being disconnected: %v", err)
		}
		if n := dev.callCount(); n != 0 {
			t.Errorf("rejected client reached a handler (%d requests)", n)
		}
		if p.conns() != 2 {
			t.Errorf("server connections = %d, want 2", p.conns())
		}
		_ = c3.Close()
		// The two admitted clients are unaffected.
		for i, c := range []*Client{c1, c2} {
			if got, err := c.ReadHoldingRegister(ctx, e2eUnit, uint16(i)); err != nil || got != 0x0101 {
				t.Errorf("client %d after the rejection: 0x%04X, %v", i+1, got, err)
			}
		}

		// Closing an admitted client frees its slot for the third one.
		if err := c1.Close(); err != nil {
			t.Fatal(err)
		}
		e2eEventually(t, "the slot to be freed", func() bool { return p.conns() == 1 })
		if err := c3.Open(); err != nil {
			t.Fatalf("third client after a slot was freed: %v", err)
		}
		if got, err := c3.ReadHoldingRegister(ctx, e2eUnit, 1); err != nil || got != 0x0101 {
			t.Errorf("third client after a slot was freed: 0x%04X, %v", got, err)
		}
		if p.conns() != 2 {
			t.Errorf("server connections = %d, want 2", p.conns())
		}
	})
}

// The per-connection context a handler receives is cancelled once the client
// has gone, and only that client's context.
func TestE2E_Server_HandlerContext_ClientDisconnect(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		var mu sync.Mutex
		ctxs := map[string]context.Context{}
		dev.setHook(func(ctx context.Context, c e2eCall) error {
			mu.Lock()
			ctxs[c.ClientAddr] = ctx
			mu.Unlock()
			return nil
		})
		p := e2eStart(t, kind, dev, e2eOpts{})
		bg := context.Background()
		other := p.newClient(nil)
		if err := other.Open(); err != nil {
			t.Fatal(err)
		}
		for _, c := range []*Client{p.client, other} {
			for i := 0; i < 3; i++ {
				if _, err := c.ReadCoil(bg, e2eUnit, 0); err != nil {
					t.Fatal(err)
				}
			}
		}
		calls := dev.takeCalls()
		firstAddr, otherAddr := calls[0].ClientAddr, calls[3].ClientAddr
		mu.Lock()
		firstCtx, otherCtx := ctxs[firstAddr], ctxs[otherAddr]
		n := len(ctxs)
		mu.Unlock()
		if n != 2 || firstCtx == nil || otherCtx == nil {
			t.Fatalf("handlers saw %d connections, want 2", n)
		}
		if firstCtx.Err() != nil || otherCtx.Err() != nil {
			t.Fatal("a connection context is cancelled while the client is connected")
		}

		if err := p.client.Close(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-firstCtx.Done():
		case <-time.After(e2eWait):
			t.Fatal("connection context not cancelled after the client disconnected")
		}
		if !errors.Is(firstCtx.Err(), context.Canceled) {
			t.Errorf("connection context error = %v", firstCtx.Err())
		}
		if otherCtx.Err() != nil {
			t.Error("disconnecting one client cancelled another client's context")
		}
		if _, err := other.ReadCoil(bg, e2eUnit, 0); err != nil {
			t.Errorf("other client after the disconnect: %v", err)
		}
	})
}

// A handler that is still running when its client disconnects must see its
// context cancelled, as documented on RequestHandler.
func TestE2E_Server_HandlerContext_ClientDisconnectInFlight(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		sawCancel := make(chan bool, 1)
		dev.setHook(func(ctx context.Context, _ e2eCall) error {
			entered <- struct{}{}
			select {
			case <-ctx.Done():
				sawCancel <- true
			case <-release:
				sawCancel <- false
			}
			return nil
		})
		p := e2eStart(t, kind, dev, e2eOpts{client: func(c *Config) { c.Timeout = 30 * time.Millisecond }})

		if _, err := p.client.ReadCoil(context.Background(), e2eUnit, 0); !errors.Is(err, ErrRequestTimedOut) {
			t.Fatalf("request against a blocked handler: %v", err)
		}
		<-entered
		if err := p.client.Close(); err != nil {
			t.Fatal(err)
		}
		// Give the server a generous moment to notice the closed socket.
		timer := time.AfterFunc(150*time.Millisecond, func() { close(release) })
		defer timer.Stop()
		if !<-sawCancel {
			t.Fatalf("the server does not watch the socket while a handler runs, so a handler blocked on " +
				"ctx.Done() is not cancelled when its client disconnects (it also keeps a MaxClients slot)")
		}
		e2eEventually(t, "the connection to be dropped", func() bool { return p.conns() == 0 })
	})
}

// Stopping the server cancels the context of a handler that is in flight;
// Shutdown waits for it, and the client's request fails instead of hanging.
func TestE2E_Server_ShutdownWithInFlightHandler(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		dev := e2eNewDevice()
		entered := make(chan struct{})
		handlerErr := make(chan error, 1)
		dev.setHook(func(ctx context.Context, _ e2eCall) error {
			close(entered)
			select {
			case <-ctx.Done():
				handlerErr <- ctx.Err()
			case <-time.After(e2eWait):
				handlerErr <- errors.New("handler context was not cancelled")
			}
			return ErrServerDeviceBusy
		})
		p := e2eStart(t, kind, dev, e2eOpts{})

		clientErr := make(chan error, 1)
		go func() {
			_, err := p.client.ReadHoldingRegister(context.Background(), e2eUnit, 0)
			clientErr <- err
		}()
		<-entered

		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), e2eWait)
		defer cancel()
		if err := p.server.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		if d := time.Since(start); d > e2eWait/2 {
			t.Errorf("Shutdown took %v", d)
		}
		// Shutdown returned, so the handler has returned too.
		select {
		case err := <-handlerErr:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("handler context: %v, want context.Canceled", err)
			}
		default:
			t.Fatal("Shutdown returned while the handler was still running")
		}
		select {
		case err := <-clientErr:
			// The handler's answer may or may not make it out before the
			// socket is closed; either way the request ends promptly.
			if err == nil || errors.Is(err, ErrRequestTimedOut) {
				t.Errorf("in-flight request: %v, want the handler's exception or a connection error", err)
			}
		case <-time.After(e2eWait):
			t.Fatal("in-flight request still blocked after Shutdown")
		}
		if p.conns() != 0 {
			t.Errorf("server connections = %d after Shutdown", p.conns())
		}
		// Shutdown and Stop are idempotent.
		if err := p.server.Shutdown(context.Background()); err != nil {
			t.Errorf("second Shutdown: %v", err)
		}
		if err := p.server.Stop(); err != nil {
			t.Errorf("Stop after Shutdown: %v", err)
		}
	})
}

// A server can be replaced by a new one on the same address: the handler state
// carries over, a client with a retry policy reconnects by itself, and two
// servers cannot share the address.
func TestE2E_Server_RestartOnSameAddress(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		addr := e2eFreeAddr(t)
		dev := e2eNewDevice()
		sconf := e2eServerConfig(t, kind, addr)
		ctx := context.Background()

		cconf := e2eClientConfig(t, kind, addr)
		plain, err := New(cconf)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = plain.Close() })
		cconf.RetryPolicy = ExponentialBackoff(time.Millisecond, 5*time.Millisecond, 3)
		retrying, err := New(cconf)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = retrying.Close() })

		// Not started yet: nothing listens.
		if err := plain.Open(); err == nil {
			t.Fatal("Open succeeded before the server was started")
		}
		if plain.Info().IsOpen {
			t.Error("client reports open after a failed Open")
		}

		var server *Server
		for round := 1; round <= 3; round++ {
			// A new Server per round: see TestE2E_Server_RestartSameInstance
			// for restarting one and the same Server.
			server, err = NewServer(&sconf, dev)
			if err != nil {
				t.Fatal(err)
			}
			if err := server.Start(); err != nil {
				t.Fatalf("round %d: Start: %v", round, err)
			}
			t.Cleanup(func() { _ = server.Stop() })
			if err := server.Start(); err != nil {
				t.Fatalf("round %d: second Start: %v", round, err)
			}
			// The client without a retry policy has to be reopened by hand.
			_ = plain.Close()
			if err := plain.Open(); err != nil {
				t.Fatalf("round %d: Open: %v", round, err)
			}
			if err := plain.WriteRegister(ctx, e2eUnit, uint16(round), uint16(round)); err != nil {
				t.Fatalf("round %d: WriteRegister: %v", round, err)
			}
			// The retrying client reconnects by itself from the second round on. It
			// is not used while the server is down: see
			// TestE2E_Client_RetryRecoversAfterFailedRedial.
			if round == 1 {
				if err := retrying.Open(); err != nil {
					t.Fatalf("Open: %v", err)
				}
			}
			got, err := retrying.ReadHoldingRegisters(ctx, e2eUnit, 1, 3)
			want := []uint16{1, 2, 3}
			for i := round; i < 3; i++ {
				want[i] = 0
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("round %d: retrying client read %v, %v; want %v", round, got, err, want)
			}
			e2eEventually(t, "two server connections", func() bool { return e2eServerConns(server) == 2 })

			if round == 3 {
				break
			}
			if err := server.Stop(); err != nil {
				t.Fatalf("round %d: Stop: %v", round, err)
			}
			if n := e2eServerConns(server); n != 0 {
				t.Errorf("round %d: %d connections after Stop", round, n)
			}
			if _, err := plain.ReadHoldingRegister(ctx, e2eUnit, 0); err == nil {
				t.Errorf("round %d: request succeeded against a stopped server", round)
			}
			if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
				_ = c.Close()
				t.Errorf("round %d: the address still accepts connections after Stop", round)
			}
		}

		// Two servers cannot share the address.
		second, err := NewServer(&sconf, e2eNewDevice())
		if err != nil {
			t.Fatal(err)
		}
		if err := second.Start(); err == nil {
			_ = second.Stop()
			t.Error("a second server started on an address that is in use")
		}
		if got, err := retrying.ReadHoldingRegister(ctx, e2eUnit, 3); err != nil || got != 3 {
			t.Errorf("first server after the failed second Start: %d, %v", got, err)
		}
	})
}

// One and the same Server can be stopped and started again.
func TestE2E_Server_RestartSameInstance(t *testing.T) {
	dev := e2eNewDevice()
	sconf := e2eServerConfig(t, "tcp", e2eFreeAddr(t))
	server, err := NewServer(&sconf, dev)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Stop() })
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	if err := server.Stop(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		if err := server.Start(); err != nil {
			t.Fatalf("restart %d: %v", i, err)
		}
		if i%20 == 0 {
			conf := Config{URL: "tcp://" + server.tcpListener.Addr().String(), Timeout: e2eWait}
			c, err := New(conf)
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Open(); err != nil {
				t.Fatalf("restart %d: Open: %v", i, err)
			}
			if err := c.WriteRegister(context.Background(), e2eUnit, 0, uint16(i)); err != nil {
				t.Errorf("restart %d: %v", i, err)
			}
			_ = c.Close()
		}
		if err := server.Stop(); err != nil {
			t.Fatalf("stop %d: %v", i, err)
		}
	}
}

// The server closes a connection that stays idle for longer than Timeout. A
// client with a retry policy does not notice; one without sees one failure.
func TestE2E_Server_IdleTimeout(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		const idle = 200 * time.Millisecond
		dev := e2eNewDevice()
		am := &e2eAttemptMetrics{}
		p := e2eStart(t, kind, dev, e2eOpts{
			server: func(c *ServerConfig) { c.Timeout = idle },
			client: func(c *Config) {
				c.RetryPolicy = ExponentialBackoff(time.Millisecond, 5*time.Millisecond, 2)
				c.Metrics = am
			},
		})
		ctx := context.Background()
		retrying := p.client
		plain := p.newClient(func(c *Config) { c.RetryPolicy = NoRetry(); c.Metrics = nil })
		if err := plain.Open(); err != nil {
			t.Fatal(err)
		}

		// Regular traffic keeps a connection open well past the idle timeout.
		start := time.Now()
		for time.Since(start) < idle+idle/2 {
			if _, err := retrying.ReadHoldingRegister(ctx, e2eUnit, 0); err != nil {
				t.Fatalf("request on an active connection: %v", err)
			}
			if _, err := plain.ReadHoldingRegister(ctx, e2eUnit, 0); err != nil {
				t.Fatalf("request on an active connection: %v", err)
			}
			time.Sleep(20 * time.Millisecond)
		}
		addrs := map[string]bool{}
		for _, call := range dev.takeCalls() {
			addrs[call.ClientAddr] = true
		}
		if len(addrs) != 2 {
			t.Fatalf("active clients used %d connections, want 2", len(addrs))
		}
		for _, e := range am.take() {
			if e.Kind == "retrydial" || e.Attempt != 0 {
				t.Fatalf("active connection needed a retry: %v", e)
			}
		}

		// Idle connections are closed by the server.
		e2eEventually(t, "the server to close the idle connections", func() bool { return p.conns() == 0 })

		// Without a retry policy the next request fails; the client has to be
		// reopened.
		_, err := plain.ReadHoldingRegister(ctx, e2eUnit, 0)
		if err == nil {
			t.Error("request on a connection the server closed succeeded without a retry policy")
		}
		if errors.Is(err, ErrRequestTimedOut) {
			t.Errorf("request on a closed connection timed out instead of failing fast: %v", err)
		}
		if n := dev.callCount(); n != 0 {
			t.Errorf("failed request reached a handler (%d)", n)
		}
		_ = plain.Close()
		if err := plain.Open(); err != nil {
			t.Fatal(err)
		}
		if _, err := plain.ReadHoldingRegister(ctx, e2eUnit, 0); err != nil {
			t.Errorf("request after reopening: %v", err)
		}
		dev.takeCalls()

		// With a retry policy the request transparently uses a new connection.
		dev.setHolding(3, 0x4321)
		got, err := retrying.ReadHoldingRegister(ctx, e2eUnit, 3)
		if err != nil || got != 0x4321 {
			t.Fatalf("request after the idle timeout with a retry policy: 0x%04X, %v", got, err)
		}
		call := e2eOneCall(t, "retried request", dev)
		if addrs[call.ClientAddr] {
			t.Errorf("retried request arrived on the old connection %s", call.ClientAddr)
		}
		events := am.take()
		kinds := e2eKinds(events)
		want := []string{"request:03", "attempt:03", "retrydial:00", "attempt:03", "response:03"}
		if !reflect.DeepEqual(kinds, want) {
			t.Fatalf("client metrics = %v, want %v", events, want)
		}
		if events[1].Attempt != 0 || events[1].Err == nil || events[2].Attempt != 1 || events[2].Err != nil ||
			events[3].Attempt != 1 || events[3].Err != nil {
			t.Errorf("attempt metrics = %v", events)
		}
	})
}

// A response must still be delivered when the time a connection sat idle plus
// the time the handler took exceeds the idle timeout.
func TestE2E_Server_IdleTimeoutDoesNotCutResponses(t *testing.T) {
	forEachTransport(t, func(t *testing.T, kind string) {
		const idle = 40 * time.Millisecond
		dev := e2eNewDevice()
		dev.setHolding(0, 0x0BAD)
		dev.setHook(func(ctx context.Context, _ e2eCall) error {
			select {
			case <-time.After(2 * idle):
			case <-ctx.Done():
			}
			return nil
		})
		p := e2eStart(t, kind, dev, e2eOpts{
			server: func(c *ServerConfig) { c.Timeout = idle },
			client: func(c *Config) { c.Timeout = 2 * time.Second },
			noOpen: true,
		})

		// The request has to arrive within the idle timeout of the connect;
		// try again on a slow machine where it does not.
		for attempt := 0; attempt < 20; attempt++ {
			_ = p.client.Close()
			if err := p.client.Open(); err != nil {
				continue
			}
			start := time.Now()
			got, err := p.client.ReadHoldingRegister(context.Background(), e2eUnit, 0)
			if n := len(dev.takeCalls()); n == 0 {
				continue
			}
			if err != nil {
				t.Fatalf("the server sets one deadline for reading a request and writing its response "+
					"(internal/transport/tcp.go ReadRequest), so a handler that outlasts ServerConfig.Timeout loses its "+
					"response: client got %v after %v", err, time.Since(start))
			}
			if got != 0x0BAD {
				t.Errorf("read 0x%04X, want 0x0BAD", got)
			}
			return
		}
		t.Fatal("no request reached the handler in 20 attempts")
	})
}
