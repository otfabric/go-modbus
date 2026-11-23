// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// decisionRecorder records, in order, the decisions, the OnError calls, the
// protocol violations and the PDU exchanges of a server.
type decisionRecorder struct {
	mu         sync.Mutex
	events     []string
	decisions  []AuthorizationDecisionEvent
	violations []ProtocolViolation
	exchanges  []ServerPDUExchange
	onDecision func(AuthorizationDecisionEvent)
}

func (r *decisionRecorder) add(ev string) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *decisionRecorder) OnRequest(uint8, FunctionCode)                     { r.add("request") }
func (r *decisionRecorder) OnResponse(uint8, FunctionCode, time.Duration)     { r.add("response") }
func (r *decisionRecorder) OnError(uint8, FunctionCode, time.Duration, error) { r.add("error") }

func (r *decisionRecorder) OnAuthorizationDecision(ev AuthorizationDecisionEvent) {
	r.mu.Lock()
	r.decisions = append(r.decisions, ev)
	r.events = append(r.events, fmt.Sprintf("decision %v", ev.Allowed))
	fn := r.onDecision
	r.mu.Unlock()
	if fn != nil {
		fn(ev)
	}
}

func (r *decisionRecorder) OnProtocolViolation(v ProtocolViolation) {
	r.mu.Lock()
	r.violations = append(r.violations, v)
	r.events = append(r.events, "violation")
	r.mu.Unlock()
}

func (r *decisionRecorder) OnServerPDU(_ context.Context, ex ServerPDUExchange) {
	r.mu.Lock()
	r.exchanges = append(r.exchanges, ex)
	r.events = append(r.events, "tap")
	r.mu.Unlock()
}

func (r *decisionRecorder) snapshot() ([]string, []AuthorizationDecisionEvent, []ProtocolViolation, []ServerPDUExchange) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...), append([]AuthorizationDecisionEvent(nil), r.decisions...),
		append([]ProtocolViolation(nil), r.violations...), append([]ServerPDUExchange(nil), r.exchanges...)
}

// rawConn is a raw Modbus/TCP connection of a test.
type rawConn struct {
	t *testing.T
	net.Conn
}

func (c *rawConn) send(frame []byte) {
	c.t.Helper()
	if _, err := c.Write(frame); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

// readFrame reads one MBAP frame, header included.
func (c *rawConn) readFrame() []byte {
	c.t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(e2eWait))
	f, err := readMBAPFrame(c.Conn)
	if err != nil {
		c.t.Fatalf("read: %v", err)
	}
	return f
}

// startDecisionServer starts a tcp server with r as its Metrics and PDUTap
// and authorizer as its Authorizer, and returns a raw connection to it.
func startDecisionServer(t *testing.T, r *decisionRecorder, h RequestHandler, authorizer RequestAuthorizer, mut func(*ServerConfig)) (*e2ePair, *rawConn) {
	t.Helper()
	p := e2eStart(t, "tcp", h, e2eOpts{noOpen: true, server: func(c *ServerConfig) {
		c.Metrics = r
		c.PDUTap = r
		c.Authorizer = authorizer
		if mut != nil {
			mut(c)
		}
	}})
	return p, &rawConn{t: t, Conn: dialRaw(t, p.server)}
}

// Every decision is reported, allowed or refused, with its inputs, before
// the handler runs or OnError is called; ConnectionID, RequestSeq and
// TransactionID join it to its PDU exchange.
func TestAuthorizationDecision_AllowedAndRefused(t *testing.T) {
	errDenied := fmt.Errorf("writes are for operators: %w", ErrIllegalDataAddress)
	r := &decisionRecorder{}
	a := RequestAuthorizerFunc(func(_ context.Context, ar *AuthorizationRequest) error {
		if ar.Access != AccessRead {
			return errDenied
		}
		return nil
	})
	_, conn := startDecisionServer(t, r, holdingOne(), a, nil)
	conn.send(rawRequestFrame(0x1001, 1, byte(FCReadHoldingRegisters), u16(1, 1)))
	conn.readFrame()
	conn.send(rawRequestFrame(0x1001, 1, byte(FCWriteSingleRegister), u16(7, 9)))
	conn.readFrame()

	e2eEventually(t, "two exchanges", func() bool { _, _, _, ex := r.snapshot(); return len(ex) == 2 })
	events, ds, _, ex := r.snapshot()
	want := "request,decision true,response,tap,request,decision false,error,tap"
	if strings.Join(events, ",") != want {
		t.Errorf("events %v, want %s", events, want)
	}
	if len(ds) != 2 {
		t.Fatalf("decisions %+v, want 2", ds)
	}
	allowed, refused := ds[0], ds[1]
	if !allowed.Allowed || allowed.Err != nil || allowed.Failed || allowed.Exception != 0 {
		t.Errorf("allowed %+v", allowed)
	}
	if q := allowed.Request; q.FunctionCode != FCReadHoldingRegisters || q.Access != AccessRead ||
		q.ReadAddr != 1 || q.ReadQuantity != 1 || q.UnitID != 1 {
		t.Errorf("allowed request %+v", q)
	}
	if refused.Allowed || refused.Err != errDenied || refused.Failed || refused.Exception != exIllegalDataAddress {
		t.Errorf("refused %+v, want the authorizer's error and exception 2", refused)
	}
	if q := refused.Request; q.FunctionCode != FCWriteSingleRegister || q.Access != AccessWrite ||
		q.WriteAddr != 7 || q.WriteQuantity != 1 {
		t.Errorf("refused request %+v", q)
	}
	// The client may repeat a transaction ID; RequestSeq tells the two apart.
	for i := range 2 {
		d, x := ds[i].Request, ex[i]
		if d.ConnectionID == 0 || d.ConnectionID != x.ConnectionID {
			t.Errorf("request %d: ConnectionID %v, exchange %v", i, d.ConnectionID, x.ConnectionID)
		}
		if d.RequestSeq != uint64(i+1) || x.RequestSeq != d.RequestSeq {
			t.Errorf("request %d: RequestSeq %d, exchange %d, want %d", i, d.RequestSeq, x.RequestSeq, i+1)
		}
		if d.TransactionID != 0x1001 || x.TransactionID != 0x1001 {
			t.Errorf("request %d: TransactionID %#x, exchange %#x", i, d.TransactionID, x.TransactionID)
		}
	}
}

// A request refused with ErrNoResponse is reported with no exception.
func TestAuthorizationDecision_Silent(t *testing.T) {
	r := &decisionRecorder{}
	a := RequestAuthorizerFunc(func(context.Context, *AuthorizationRequest) error { return ErrNoResponse })
	_, conn := startDecisionServer(t, r, holdingOne(), a, nil)
	conn.send(rawRequestFrame(5, 1, byte(FCReadHoldingRegisters), u16(1, 1)))
	e2eEventually(t, "the decision", func() bool { _, ds, _, _ := r.snapshot(); return len(ds) == 1 })
	_, ds, _, _ := r.snapshot()
	if d := ds[0]; d.Allowed || d.Exception != 0 || !errors.Is(d.Err, ErrNoResponse) {
		t.Errorf("decision %+v, want refused, unanswered", d)
	}
}

// An authorizer that does not return normally is reported as Failed, with
// the exception the request is refused with.
func TestAuthorizationDecision_AuthorizerFailed(t *testing.T) {
	r := &decisionRecorder{}
	a := RequestAuthorizerFunc(func(context.Context, *AuthorizationRequest) error { panic("authorizer boom") })
	_, conn := startDecisionServer(t, r, holdingOne(), a, func(c *ServerConfig) { c.Logger = NopLogger() })
	conn.send(rawRequestFrame(5, 1, byte(FCReadHoldingRegisters), u16(1, 1)))
	conn.readFrame()
	e2eEventually(t, "the decision", func() bool { _, ds, _, _ := r.snapshot(); return len(ds) == 1 })
	_, ds, _, _ := r.snapshot()
	d := ds[0]
	if d.Allowed || !d.Failed || d.Exception != exServerDeviceFailure || d.Err == nil ||
		!strings.Contains(d.Err.Error(), "did not return normally: authorizer boom") {
		t.Errorf("decision %+v, want failed, exception 4", d)
	}
	if d.Request.RequestSeq != 1 || d.Request.FunctionCode != FCReadHoldingRegisters {
		t.Errorf("request %+v", d.Request)
	}
}

// What the event reports is what the authorizer was asked, not what it
// changed afterwards.
func TestAuthorizationDecision_RequestIsASnapshot(t *testing.T) {
	r := &decisionRecorder{}
	a := RequestAuthorizerFunc(func(_ context.Context, ar *AuthorizationRequest) error {
		ar.UnitID, ar.RequestSeq, ar.ClientRole = 99, 99, "tampered"
		for i := range ar.FileRecords {
			ar.FileRecords[i].FileNumber = 99
		}
		return nil
	})
	_, conn := startDecisionServer(t, r, e2eNewDevice(), a, nil)
	// FC20: one sub-request of file 4, record 1, 2 registers.
	conn.send(rawRequestFrame(1, e2eUnit, byte(FCReadFileRecord), []byte{7, 6, 0, 4, 0, 1, 0, 2}))
	conn.readFrame()
	e2eEventually(t, "the decision", func() bool { _, ds, _, _ := r.snapshot(); return len(ds) == 1 })
	_, ds, _, _ := r.snapshot()
	q := ds[0].Request
	if q.UnitID != e2eUnit || q.RequestSeq != 1 || q.ClientRole != "" {
		t.Errorf("request %+v: the authorizer's changes reached the event", q)
	}
	if len(q.FileRecords) != 1 || q.FileRecords[0].FileNumber != 4 || q.FileRecords[0].RecordLength != 2 {
		t.Errorf("file records %+v, want file 4 as asked", q.FileRecords)
	}
}

// A decision callback that does not return normally is logged; the request
// is served or refused as decided.
func TestAuthorizationDecision_CallbackPanics(t *testing.T) {
	log := &e2eLogger{}
	r := &decisionRecorder{onDecision: func(AuthorizationDecisionEvent) { panic("decision boom") }}
	a := RequestAuthorizerFunc(func(_ context.Context, ar *AuthorizationRequest) error {
		if ar.Access != AccessRead {
			return ErrIllegalFunction
		}
		return nil
	})
	_, conn := startDecisionServer(t, r, holdingOne(), a, func(c *ServerConfig) { c.Logger = log })
	conn.send(rawRequestFrame(1, 1, byte(FCReadHoldingRegisters), u16(1, 1)))
	if res := conn.readFrame(); res[7] != byte(FCReadHoldingRegisters) {
		t.Errorf("allowed request answered % X, want a response", res)
	}
	conn.send(rawRequestFrame(2, 1, byte(FCWriteSingleRegister), u16(1, 1)))
	if res := conn.readFrame(); res[7] != byte(FCWriteSingleRegister)|0x80 || res[8] != byte(exIllegalFunction) {
		t.Errorf("refused request answered % X, want exception 1", res)
	}
	e2eEventually(t, "the log line", func() bool {
		return log.has("E ", "AuthorizationDecisionMetrics.OnAuthorizationDecision for FC 0x06 from", "did not return normally: decision boom", "goroutine ")
	})
}

// Stop called from the decision callback returns; the request is not
// answered.
func TestAuthorizationDecision_StopFromCallback(t *testing.T) {
	for _, allow := range []bool{true, false} {
		t.Run(fmt.Sprintf("allowed=%v", allow), func(t *testing.T) {
			stopped := make(chan error, 1)
			var ms *Server
			r := &decisionRecorder{onDecision: func(AuthorizationDecisionEvent) { stopped <- ms.Stop() }}
			a := RequestAuthorizerFunc(func(context.Context, *AuthorizationRequest) error {
				if allow {
					return nil
				}
				return ErrIllegalFunction
			})
			p, conn := startDecisionServer(t, r, holdingOne(), a, nil)
			ms = p.server
			conn.send(rawRequestFrame(1, 1, byte(FCReadHoldingRegisters), u16(1, 1)))
			select {
			case err := <-stopped:
				if err != nil {
					t.Fatalf("Stop: %v", err)
				}
			case <-time.After(e2eWait):
				t.Fatal("Stop from OnAuthorizationDecision did not return")
			}
			wantClosedUnanswered(t, conn.Conn)
			time.Sleep(20 * time.Millisecond)
			if events, _, _, ex := r.snapshot(); len(ex) != 0 {
				t.Errorf("PDUTap called after the Stop: %v", events)
			}
		})
	}
}

// ProtocolViolation carries the RequestSeq of the request it reports, the
// same as its PDU exchange; an MBAP header the server could not frame is not
// a request and has none.
func TestAuthorizationDecision_ViolationRequestSeq(t *testing.T) {
	r := &decisionRecorder{}
	_, conn := startDecisionServer(t, r, holdingOne(), nil, nil)
	conn.send(rawRequestFrame(1, 1, byte(FCReadHoldingRegisters), u16(1, 1)))
	conn.readFrame()
	// FC03 with a field missing: answered with exception 3, connection closed.
	conn.send(rawRequestFrame(2, 1, byte(FCReadHoldingRegisters), []byte{0, 1, 0}))
	conn.readFrame()
	e2eEventually(t, "the violation", func() bool { _, _, vs, _ := r.snapshot(); return len(vs) == 1 })
	_, _, vs, ex := r.snapshot()
	if vs[0].RequestSeq != 2 || vs[0].TransactionID != 2 {
		t.Errorf("violation %+v, want RequestSeq 2", vs[0])
	}
	if len(ex) != 2 || ex[1].RequestSeq != 2 || ex[1].ConnectionID != vs[0].ConnectionID {
		t.Errorf("exchanges %+v, want the second with RequestSeq 2", ex)
	}

	// A bad MBAP header.
	r2 := &decisionRecorder{}
	_, conn2 := startDecisionServer(t, r2, holdingOne(), nil, nil)
	conn2.send(rawRequestFrame(1, 1, byte(FCReadHoldingRegisters), u16(1, 1)))
	conn2.readFrame()
	conn2.send([]byte{0, 9, 0, 7, 0, 6, 1, 3, 0, 1, 0, 1})
	e2eEventually(t, "the violation", func() bool { _, _, vs, _ := r2.snapshot(); return len(vs) == 1 })
	_, _, vs2, _ := r2.snapshot()
	if vs2[0].RequestSeq != 0 || vs2[0].HasFunctionCode {
		t.Errorf("violation %+v, want no RequestSeq for an unframed header", vs2[0])
	}
}

// The requests of concurrent connections are numbered per connection, from
// 1, without gaps, in the decisions as in the exchanges. Run with -race.
func TestAuthorizationDecision_ConcurrentSequences(t *testing.T) {
	r := &decisionRecorder{}
	a := RequestAuthorizerFunc(func(_ context.Context, ar *AuthorizationRequest) error {
		if ar.RequestSeq%3 == 0 {
			return ErrIllegalFunction
		}
		return nil
	})
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp", dev, e2eOpts{noOpen: true, server: func(c *ServerConfig) {
		c.Metrics, c.PDUTap, c.Authorizer = r, r, a
	}})
	const clients, requests = 8, 20
	var wg sync.WaitGroup
	for range clients {
		c := p.newClient(nil)
		if err := c.Open(); err != nil {
			t.Fatal(err)
		}
		wg.Go(func() {
			for range requests {
				_, _ = c.ReadHoldingRegister(context.Background(), e2eUnit, 1)
			}
		})
	}
	wg.Wait()
	e2eEventually(t, "every exchange", func() bool { _, _, _, ex := r.snapshot(); return len(ex) == clients*requests })
	_, ds, _, ex := r.snapshot()
	if len(ds) != clients*requests {
		t.Fatalf("%d decisions, want %d", len(ds), clients*requests)
	}
	next := map[ConnectionID]uint64{}
	type key struct {
		id  ConnectionID
		seq uint64
	}
	decided := map[key]AuthorizationDecisionEvent{}
	for _, d := range ds {
		q := d.Request
		next[q.ConnectionID]++
		if q.RequestSeq != next[q.ConnectionID] {
			t.Fatalf("connection %v: RequestSeq %d, want %d", q.ConnectionID, q.RequestSeq, next[q.ConnectionID])
		}
		decided[key{q.ConnectionID, q.RequestSeq}] = d
	}
	if len(next) != clients {
		t.Errorf("%d connections, want %d", len(next), clients)
	}
	for _, x := range ex {
		d, ok := decided[key{x.ConnectionID, x.RequestSeq}]
		if !ok || d.Request.TransactionID != x.TransactionID {
			t.Fatalf("exchange %v/%d: no decision with its transaction ID", x.ConnectionID, x.RequestSeq)
		}
		if exc := x.Response[0]&0x80 != 0; exc == d.Allowed {
			t.Fatalf("exchange %v/%d answered % X, decision allowed=%v", x.ConnectionID, x.RequestSeq, x.Response, d.Allowed)
		}
	}
}
