// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// authRecorder is a RequestAuthorizer that records what it is asked and
// answers with decide (nil: serve).
type authRecorder struct {
	mu     sync.Mutex
	asked  []AuthorizationRequest
	decide func(*AuthorizationRequest) error
}

func (a *authRecorder) AuthorizeRequest(_ context.Context, req *AuthorizationRequest) error {
	a.mu.Lock()
	a.asked = append(a.asked, *req)
	a.mu.Unlock()
	if a.decide == nil {
		return nil
	}
	return a.decide(req)
}

func (a *authRecorder) take() []AuthorizationRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := a.asked
	a.asked = nil
	return out
}

// errorsMetrics records the errors ServerMetrics.OnError receives.
type errorsMetrics struct {
	countingServerMetrics
	mu   sync.Mutex
	errs []error
}

func (m *errorsMetrics) OnError(_ uint8, _ FunctionCode, _ time.Duration, err error) {
	m.mu.Lock()
	m.errs = append(m.errs, err)
	m.mu.Unlock()
}

func (m *errorsMetrics) take() []error {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.errs
	m.errs = nil
	return out
}

// Every request a handler serves is authorized first, once, with its own
// function code.
func TestAuthorizer_AskedForEveryServedRequest(t *testing.T) {
	a := &authRecorder{}
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp", dev, e2eOpts{server: func(c *ServerConfig) { c.Authorizer = a }})
	for _, op := range e2eAllOps() {
		if err := op.call(t.Context(), p.client); err != nil {
			t.Fatalf("%s: %v", op.name, err)
		}
		calls, asked := dev.takeCalls(), a.take()
		if len(asked) != len(calls) {
			t.Errorf("%s: %d authorizations for %d handler calls", op.name, len(asked), len(calls))
			continue
		}
		for i := range calls {
			if asked[i].FunctionCode != calls[i].FC || asked[i].UnitID != calls[i].UnitID || asked[i].Access == 0 {
				t.Errorf("%s: authorized %+v for handler call %+v", op.name, asked[i], calls[i])
			}
		}
	}
}

// A refused request never reaches a handler: no write changes the device, and
// the client gets Illegal Function. OnError sees ErrNotAuthorized and the
// authorizer's error.
func TestAuthorizer_RefusedNeverReachesHandler(t *testing.T) {
	denied := errors.New("policy: denied")
	a := &authRecorder{decide: func(*AuthorizationRequest) error { return denied }}
	m := &errorsMetrics{}
	dev := e2eNewDevice()
	dev.setHolding(920, 0x1111)
	p := e2eStart(t, "tcp", dev, e2eOpts{server: func(c *ServerConfig) {
		c.Authorizer = a
		c.Metrics = m
	}})
	for _, op := range e2eAllOps() {
		err := op.call(t.Context(), p.client)
		if !op.probe && !slices.Contains([]string{"SupportsFunction", "SupportsDeviceIdentification", "ProbeFunction"}, op.name) {
			e2eWantException(t, op.name, err, op.fc, exIllegalFunction)
		}
		if n := len(dev.takeCalls()); n != 0 {
			t.Errorf("%s: a refused request reached the handler %d time(s)", op.name, n)
		}
		if len(a.take()) == 0 {
			t.Errorf("%s: the authorizer was not asked", op.name)
		}
		for _, e := range m.take() {
			if !errors.Is(e, ErrNotAuthorized) || !errors.Is(e, denied) || !errors.Is(e, ErrIllegalFunction) {
				t.Errorf("%s: OnError(%v), want ErrNotAuthorized wrapping the authorizer's error", op.name, e)
			}
		}
	}
	if got := dev.holdingAt(920, 1)[0]; got != 0x1111 {
		t.Errorf("a refused write changed the device: 0x%04X", got)
	}
}

// What the authorizer is told about each kind of request.
func TestAuthorizer_RequestFields(t *testing.T) {
	a := &authRecorder{}
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp", dev, e2eOpts{server: func(c *ServerConfig) { c.Authorizer = a }})
	ctx := t.Context()
	u := e2eUnit
	base := func(fc FunctionCode, access RequestAccess) AuthorizationRequest {
		return AuthorizationRequest{UnitID: u, FunctionCode: fc, Access: access}
	}
	with := func(r AuthorizationRequest, f func(*AuthorizationRequest)) AuthorizationRequest { f(&r); return r }
	var seq uint64
	for _, tc := range []struct {
		name string
		call func() error
		want AuthorizationRequest
	}{
		{"FC01", func() error { _, err := p.client.ReadCoils(ctx, u, 5, 9); return err },
			with(base(FCReadCoils, AccessRead), func(r *AuthorizationRequest) { r.ReadAddr, r.ReadQuantity = 5, 9 })},
		{"FC04", func() error { _, err := p.client.ReadInputRegisters(ctx, u, 7, 2); return err },
			with(base(FCReadInputRegisters, AccessRead), func(r *AuthorizationRequest) { r.ReadAddr, r.ReadQuantity = 7, 2 })},
		{"FC05", func() error { return p.client.WriteCoil(ctx, u, 900, true) },
			with(base(FCWriteSingleCoil, AccessWrite), func(r *AuthorizationRequest) { r.WriteAddr, r.WriteQuantity = 900, 1 })},
		{"FC06", func() error { return p.client.WriteRegister(ctx, u, 901, 1) },
			with(base(FCWriteSingleRegister, AccessWrite), func(r *AuthorizationRequest) { r.WriteAddr, r.WriteQuantity = 901, 1 })},
		{"FC15", func() error { return p.client.WriteCoils(ctx, u, 910, []bool{true, false, true}) },
			with(base(FCWriteMultipleCoils, AccessWrite), func(r *AuthorizationRequest) { r.WriteAddr, r.WriteQuantity = 910, 3 })},
		{"FC16", func() error { return p.client.WriteRegisters(ctx, u, 930, []uint16{1, 2}) },
			with(base(FCWriteMultipleRegisters, AccessWrite), func(r *AuthorizationRequest) { r.WriteAddr, r.WriteQuantity = 930, 2 })},
		{"FC22", func() error { return p.client.MaskWriteRegister(ctx, u, 922, 1, 2) },
			with(base(FCMaskWriteRegister, AccessWrite), func(r *AuthorizationRequest) { r.WriteAddr, r.WriteQuantity = 922, 1 })},
		{"FC23", func() error {
			_, err := p.client.ReadWriteMultipleRegisters(ctx, u, 1, 2, 940, []uint16{1, 2, 3})
			return err
		},
			with(base(FCReadWriteMultipleRegs, AccessReadWrite), func(r *AuthorizationRequest) {
				r.ReadAddr, r.ReadQuantity, r.WriteAddr, r.WriteQuantity = 1, 2, 940, 3
			})},
		{"FC08", func() error { _, err := p.client.Diagnostics(ctx, u, DiagReturnQueryData, []byte{1, 2}); return err },
			with(base(FCDiagnostics, AccessDiagnostic), func(r *AuthorizationRequest) { r.SubFunction, r.HasSubFunction = 0, true })},
		{"FC08 clear counters", func() error { return p.client.DiagnosticClearCounters(ctx, u) },
			with(base(FCDiagnostics, AccessDiagnostic), func(r *AuthorizationRequest) { r.SubFunction, r.HasSubFunction = 0x000A, true })},
		{"FC07", func() error { _, err := p.client.ReadExceptionStatus(ctx, u); return err }, base(FCReadExceptionStatus, AccessDiagnostic)},
		{"FC11", func() error { _, err := p.client.GetCommEventCounter(ctx, u); return err }, base(FCGetCommEventCounters, AccessDiagnostic)},
		{"FC12", func() error { _, err := p.client.GetCommEventLog(ctx, u); return err }, base(FCGetCommEventLog, AccessDiagnostic)},
		{"FC17", func() error { _, err := p.client.ReportServerID(ctx, u); return err }, base(FCReportServerID, AccessDiagnostic)},
		{"FC43", func() error {
			_, err := p.client.ReadDeviceIdentification(ctx, u, ReadDeviceIDBasic, 0)
			return err
		}, with(base(FCEncapsulatedInterface, AccessDiagnostic), func(r *AuthorizationRequest) { r.SubFunction, r.HasSubFunction = 0x0E, true })},
		{"FC24", func() error { _, err := p.client.ReadFIFOQueue(ctx, u, 0x04DE); return err },
			with(base(FCReadFIFOQueue, AccessRead), func(r *AuthorizationRequest) { r.ReadAddr = 0x04DE })},
	} {
		if err := tc.call(); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		asked := a.take()
		if len(asked) != 1 {
			t.Fatalf("%s: %d authorizations, want 1", tc.name, len(asked))
		}
		got := asked[0]
		if got.ClientAddr == "" {
			t.Errorf("%s: no client address", tc.name)
		}
		if got.ConnectionID == 0 {
			t.Errorf("%s: no connection id", tc.name)
		}
		// One connection: its requests are numbered in order.
		seq++
		if got.RequestSeq != seq || got.TransactionID == 0 {
			t.Errorf("%s: RequestSeq %d, TransactionID %d, want %d and the request's", tc.name, got.RequestSeq, got.TransactionID, seq)
		}
		got.ClientAddr, got.ConnectionID, got.RequestSeq, got.TransactionID = "", 0, 0, 0
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("%s:\n got %+v\nwant %+v", tc.name, got, tc.want)
		}
	}

	if _, err := p.client.ReadFileRecords(ctx, u, []FileRecordRequest{{FileNumber: 4, RecordNumber: 7, RecordLength: 2}}); err != nil {
		t.Fatal(err)
	}
	if got := a.take(); len(got) != 1 || got[0].Access != AccessRead ||
		fmt.Sprint(got[0].FileRecords) != fmt.Sprint([]FileRecordRequest{{4, 7, 2}}) {
		t.Errorf("FC20: %+v", got)
	}
	if err := p.client.WriteFileRecords(ctx, u, []FileRecord{{FileNumber: 4, RecordNumber: 7, Data: []uint16{1, 2}}}); err != nil {
		t.Fatal(err)
	}
	if got := a.take(); len(got) != 1 || got[0].Access != AccessWrite ||
		fmt.Sprint(got[0].FileRecords) != fmt.Sprint([]FileRecordRequest{{4, 7, 2}}) {
		t.Errorf("FC21: %+v", got)
	}
}

// The authorizer's error chooses the exception; ErrNoResponse leaves the
// request unanswered and the connection open.
func TestAuthorizer_Answers(t *testing.T) {
	var mu sync.Mutex
	var answer error
	set := func(err error) { mu.Lock(); answer = err; mu.Unlock() }
	a := &authRecorder{decide: func(*AuthorizationRequest) error { mu.Lock(); defer mu.Unlock(); return answer }}
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp", dev, e2eOpts{
		server: func(c *ServerConfig) { c.Authorizer = a },
		client: func(c *Config) { c.Timeout = 200 * time.Millisecond },
	})
	for _, tc := range []struct {
		err  error
		code ExceptionCode
	}{
		{errors.New("no"), exIllegalFunction},
		{ErrIllegalDataAddress, exIllegalDataAddress},
		{fmt.Errorf("busy: %w", ErrServerDeviceBusy), exServerDeviceBusy},
		{ErrAcknowledge, exAcknowledge},
		{ErrGWTargetFailedToRespond, exGWTargetFailedToRespond},
	} {
		set(tc.err)
		_, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1)
		e2eWantException(t, tc.err.Error(), err, FCReadHoldingRegisters, tc.code)
	}

	set(ErrNoResponse)
	if _, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1); !errors.Is(err, ErrRequestTimedOut) {
		t.Errorf("ErrNoResponse: %v, want a timeout", err)
	}
	if dev.callCount() != 0 {
		t.Error("a refused request reached the handler")
	}
	set(nil)
	if _, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1); err != nil {
		t.Errorf("served after the refusals: %v", err)
	}
}

// An authorizer that does not return normally refuses the request with Server
// Device Failure; so does an error whose methods do not.
func TestAuthorizer_PanicRefuses(t *testing.T) {
	log := &e2eLogger{}
	var mode atomic.Value
	mode.Store("")
	a := RequestAuthorizerFunc(func(context.Context, *AuthorizationRequest) error {
		if mode.Load() == "authorizer" {
			panic("authorizer boom")
		}
		return panickyError{}
	})
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp", dev, e2eOpts{server: func(c *ServerConfig) {
		c.Authorizer = a
		c.Logger = log
	}})
	for _, m := range []string{"authorizer", "error"} {
		mode.Store(m)
		_, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1)
		e2eWantException(t, m, err, FCReadHoldingRegisters, exServerDeviceFailure)
	}
	if dev.callCount() != 0 {
		t.Error("a request reached the handler past a failed authorizer")
	}
	e2eEventually(t, "the log lines", func() bool {
		return log.has("E ", "RequestAuthorizer.AuthorizeRequest for FC 0x03 from", "did not return normally: authorizer boom") &&
			log.has("E ", "the error of the authorizer for FC 0x03 from", "did not return normally")
	})
}

type panickyError struct{}

func (panickyError) Error() string        { return "panicky" }
func (panickyError) Is(target error) bool { panic("Is boom") }

// Stop called from the authorizer returns; the request is not answered.
func TestAuthorizer_StopFromAuthorizer(t *testing.T) {
	stopped := make(chan error, 1)
	var ms *Server
	a := RequestAuthorizerFunc(func(context.Context, *AuthorizationRequest) error {
		stopped <- ms.Stop()
		return nil
	})
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp", dev, e2eOpts{
		server: func(c *ServerConfig) { c.Authorizer = a },
		client: func(c *Config) { c.Timeout = 500 * time.Millisecond },
	})
	ms = p.server
	if _, err := p.client.ReadHoldingRegister(t.Context(), e2eUnit, 1); err == nil {
		t.Error("the request was answered after Stop")
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop: %v", err)
		}
	case <-time.After(e2eWait):
		t.Fatal("Stop from the authorizer did not return")
	}
}

// Requests the server answers itself never reach the authorizer.
func TestAuthorizer_NotAskedForServerAnswers(t *testing.T) {
	a := &authRecorder{}
	p := e2eStart(t, "tcp", &fnHandler{}, e2eOpts{noOpen: true, server: func(c *ServerConfig) { c.Authorizer = a }})
	conn := dialRaw(t, p.server)
	for _, frame := range [][]byte{
		rawRequestFrame(1, 1, byte(FCReadHoldingRegisters), u16(0, 0)), // quantity 0
		rawRequestFrame(2, 1, 0x41, nil),                               // not served
		rawRequestFrame(3, 1, byte(FCMaskWriteRegister), u16(1, 2, 3)), // no MaskWriteHandler
	} {
		if _, err := conn.Write(frame); err != nil {
			t.Fatal(err)
		}
		readResponse9(t, conn)
	}
	if got := a.take(); len(got) != 0 {
		t.Errorf("authorizations %+v for requests the server answers itself", got)
	}
}

// readResponse9 reads an exception response.
func readResponse9(t *testing.T, conn interface {
	Read([]byte) (int, error)
	SetReadDeadline(time.Time) error
}) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(e2eWait))
	buf := make([]byte, 9)
	for n := 0; n < len(buf); {
		m, err := conn.Read(buf[n:])
		if err != nil {
			t.Fatalf("response: %v", err)
		}
		n += m
	}
	if buf[7]&0x80 == 0 {
		t.Fatalf("response % X, want an exception", buf)
	}
}

// Role-based authorization on tcp+tls: the authorizer sees the role of the
// verified certificate.
func TestAuthorizer_ByRole(t *testing.T) {
	pki := e2ePKI(t)
	a := &authRecorder{decide: func(r *AuthorizationRequest) error {
		if r.Access != AccessRead && r.ClientRole != "operator" {
			return ErrIllegalFunction
		}
		return nil
	}}
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{server: func(c *ServerConfig) { c.Authorizer = a }})
	if err := p.client.WriteRegister(t.Context(), e2eUnit, 1, 0x0A0A); err != nil {
		t.Fatalf("operator write: %v", err)
	}
	viewer := p.newClient(func(c *Config) { c.TLSClientCert = e2eIssue(t, pki.ca, "viewer", false, "viewer") })
	if err := viewer.Open(); err != nil {
		t.Fatal(err)
	}
	e2eWantException(t, "viewer write", viewer.WriteRegister(t.Context(), e2eUnit, 1, 0x0B0B), FCWriteSingleRegister, exIllegalFunction)
	if v, err := viewer.ReadHoldingRegister(t.Context(), e2eUnit, 1); err != nil || v != 0x0A0A {
		t.Errorf("viewer read = 0x%04X, %v", v, err)
	}
	roles := map[string]bool{}
	for _, r := range a.take() {
		roles[r.ClientRole] = true
	}
	if !roles["operator"] || !roles["viewer"] {
		t.Errorf("roles seen %v, want operator and viewer", roles)
	}
}

func TestNotAuthorizedError(t *testing.T) {
	cause := errors.New("cause")
	e := &notAuthorizedError{cause: cause, code: exIllegalDataAddress}
	if !errors.Is(e, ErrNotAuthorized) || !errors.Is(e, cause) || !errors.Is(e, ErrIllegalDataAddress) || errors.Is(e, ErrIllegalFunction) {
		t.Errorf("Is: %v", e)
	}
	if e.Error() != "modbus: request not authorized: cause" {
		t.Errorf("Error() = %q", e.Error())
	}
	silent := &notAuthorizedError{cause: cause, silent: true}
	if !errors.Is(silent, ErrNoResponse) || errors.Is(silent, ErrIllegalFunction) {
		t.Errorf("silent Is: %v", silent)
	}
	if (&notAuthorizedError{cause: cause, code: 0x7F}).Is(ErrIllegalFunction) {
		t.Error("an unknown code matched an exception")
	}
	for a, want := range map[RequestAccess]string{AccessRead: "read", AccessWrite: "write", AccessReadWrite: "read/write",
		AccessDiagnostic: "diagnostic", 0: "RequestAccess(0)"} {
		if got := a.String(); got != want {
			t.Errorf("%d: %q, want %q", a, got, want)
		}
	}
}

// ClientRoleErr tells the authorizer a certificate without a role from one
// whose role extension is unusable, for every request of the connection; the
// server refuses nothing because of it, and the handlers see the same
// ClientRole as without an authorizer.
func TestAuthorizer_ClientRoleErr(t *testing.T) {
	pki := e2ePKI(t)
	for _, tc := range []struct {
		name     string
		kind     string
		roles    []string // nil: no role extension; tcp: no certificate
		wantRole string
		invalid  bool
	}{
		{"plain tcp", "tcp", nil, "", false},
		{"valid role", "tcp+tls", []string{"operator"}, "operator", false},
		{"no role", "tcp+tls", nil, "", false},
		{"unusable role", "tcp+tls", []string{"printable:operator"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var handlerRoles []string
			h := &fnHandler{holding: func(r *HoldingRegistersRequest) ([]uint16, error) {
				mu.Lock()
				handlerRoles = append(handlerRoles, r.ClientRole)
				mu.Unlock()
				return make([]uint16, r.Quantity), nil
			}}
			a := &authRecorder{}
			p := e2eStart(t, tc.kind, h, e2eOpts{noOpen: true, server: func(c *ServerConfig) { c.Authorizer = a }})
			c := p.newClient(func(conf *Config) {
				if tc.kind == "tcp+tls" {
					conf.TLSClientCert = e2eIssue(t, pki.ca, tc.name, false, tc.roles...)
				}
			})
			if err := c.Open(); err != nil {
				t.Fatalf("Open: %v", err)
			}
			for range 2 {
				if _, err := c.ReadHoldingRegister(t.Context(), e2eUnit, 1); err != nil {
					t.Fatalf("read refused: %v", err)
				}
			}
			got := a.take()
			if len(got) != 2 {
				t.Fatalf("%d authorizations, want 2", len(got))
			}
			for i, r := range got {
				if r.ClientRole != tc.wantRole {
					t.Errorf("request %d: ClientRole %q, want %q", i, r.ClientRole, tc.wantRole)
				}
				if errors.Is(r.ClientRoleErr, ErrInvalidRole) != tc.invalid || (!tc.invalid && r.ClientRoleErr != nil) {
					t.Errorf("request %d: ClientRoleErr %v, want ErrInvalidRole: %v", i, r.ClientRoleErr, tc.invalid)
				}
			}
			if got[0].ClientRoleErr != got[1].ClientRoleErr {
				t.Errorf("ClientRoleErr differs between requests of one connection: %v, %v", got[0].ClientRoleErr, got[1].ClientRoleErr)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(handlerRoles) != 2 || handlerRoles[0] != tc.wantRole || handlerRoles[1] != tc.wantRole {
				t.Errorf("handler roles %q, want %q twice", handlerRoles, tc.wantRole)
			}
		})
	}
}
