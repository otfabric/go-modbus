// SPDX-License-Identifier: MIT

// Command security_hooks shows how an application builds its own audit trail
// from what a go-modbus server reports, in one flow over tcp+tls (MBAPS):
//
//  1. the client's certificate is verified in the TLS handshake, and the
//     server gives the connection a ConnectionID;
//  2. ServerConfig.ConnectionAuthorizer sees the verified identity (peer
//     certificates, verified chains, Modbus Role) and accepts or refuses the
//     connection before any request is read; the application remembers the
//     identity under the ConnectionID;
//  3. ServerConfig.Authorizer decides every request, and
//     AuthorizationDecisionMetrics reports each decision with its inputs;
//  4. ServerConfig.PDUTap and ProtocolViolationMetrics report what was asked
//     and answered, and what broke the protocol, with the same ConnectionID
//     and RequestSeq as the decision;
//  5. ServerConnectionMetrics reports the end of the connection, or its
//     rejection;
//  6. each of these becomes an application-owned auditEvent, handed to a
//     buffered channel without blocking (the callbacks run on the server's
//     goroutines and must not block): a full channel drops the event and
//     counts it.
//
// The library reports facts and enforces what the application configures;
// what an audit event looks like, how serious a refusal is and where events
// go are the application's decisions, made here and nowhere in the library.
//
// Run it with
//
//	go run ./examples/security_hooks
//
// It creates a throw-away CA, a server certificate and three client
// certificates in memory, starts the server on a free loopback port, runs an
// operator (reads and writes), a viewer (reads; its write is refused), a
// revoked client (refused at connection) and a client that sends a malformed
// request, stops the server and prints the audit trail. With real
// certificates, load them with modbus.LoadCertPool and tls.LoadX509KeyPair
// instead.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/otfabric/go-modbus"
)

// auditEvent is the application's own record. Its fields and their meaning
// are this example's choice, not a schema of the library.
type auditEvent struct {
	Time          time.Time
	Kind          string // "connection accepted", "connection refused", "request", ...
	ConnectionID  modbus.ConnectionID
	RequestSeq    uint64 // with ConnectionID, identifies a request
	TransactionID uint16
	Subject       string // the verified client certificate's common name
	Role          string
	Detail        string
}

func (e auditEvent) String() string {
	s := fmt.Sprintf("conn=%v", e.ConnectionID)
	if e.RequestSeq != 0 {
		s += fmt.Sprintf(" seq=%d txn=%d", e.RequestSeq, e.TransactionID)
	}
	return fmt.Sprintf("%-22s %s subject=%q role=%q %s", e.Kind, s, e.Subject, e.Role, e.Detail)
}

// auditor turns the server's reports into audit events. It implements
// modbus.ServerMetrics with the optional extensions it needs,
// modbus.ConnectionAuthorizer, modbus.RequestAuthorizer and
// modbus.ServerPDUTap.
type auditor struct {
	// revoked holds the common names whose certificates the application no
	// longer accepts: its own revocation list.
	revoked map[string]bool
	// trusted is the CA whose certificates may connect, pinned: a chain to
	// any other root in ServerConfig.TLSClientCAs is refused.
	trusted *x509.Certificate

	events  chan auditEvent
	dropped atomic.Uint64

	// subjects maps a ConnectionID to the verified subject of its
	// certificate, from the connection authorizer until the connection ends.
	subjects sync.Map
}

// emit hands e to the consumer without blocking.
func (a *auditor) emit(e auditEvent) {
	e.Time = time.Now()
	if s, ok := a.subjects.Load(e.ConnectionID); ok && e.Subject == "" {
		e.Subject = s.(string)
	}
	select {
	case a.events <- e:
	default:
		a.dropped.Add(1)
	}
}

// AuthorizeConnection runs once the client's chain has been verified,
// before any request is read. It sees what TLSHandshakeMetrics sees.
func (a *auditor) AuthorizeConnection(_ context.Context, info modbus.TLSHandshakeInfo) error {
	leaf := info.PeerCertificates[0]
	// Remembered for the reports of the connection, a refusal included.
	a.subjects.Store(info.ConnectionID, leaf.Subject.CommonName)
	if a.revoked[leaf.Subject.CommonName] {
		return fmt.Errorf("certificate of %q revoked", leaf.Subject.CommonName)
	}
	pinned := false
	for _, chain := range info.VerifiedChains {
		if chain[len(chain)-1].Equal(a.trusted) {
			pinned = true
		}
	}
	if !pinned {
		return errors.New("not issued by the pinned CA")
	}
	if info.RoleErr != nil {
		return info.RoleErr // an unusable Modbus Role: matches modbus.ErrInvalidRole
	}
	a.emit(auditEvent{Kind: "connection accepted", ConnectionID: info.ConnectionID, Role: info.Role,
		Detail: "from " + info.RemoteAddr})
	return nil
}

// AuthorizeRequest is the request policy: reads, and the diagnostics that
// only report, for every accepted client; everything else only for the
// "operator" role, and never to the broadcast unit.
func (a *auditor) AuthorizeRequest(ctx context.Context, req *modbus.AuthorizationRequest) error {
	return authorize(ctx, req)
}

// authorize is the policy of AuthorizeRequest.
func authorize(_ context.Context, req *modbus.AuthorizationRequest) error {
	if req.ClientRoleErr != nil {
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

// OnAuthorizationDecision (AuthorizationDecisionMetrics): every decision,
// with the inputs it was made on.
func (a *auditor) OnAuthorizationDecision(ev modbus.AuthorizationDecisionEvent) {
	q := ev.Request
	detail := fmt.Sprintf("unit=%d fc=%v access=%v", q.UnitID, q.FunctionCode, q.Access)
	kind := "request allowed"
	if !ev.Allowed {
		kind = "request refused"
		detail += fmt.Sprintf(" exception=%d err=%v", ev.Exception, ev.Err)
	}
	a.emit(auditEvent{Kind: kind, ConnectionID: q.ConnectionID, RequestSeq: q.RequestSeq,
		TransactionID: q.TransactionID, Role: q.ClientRole, Detail: detail})
}

// OnServerPDU (ServerPDUTap): what was asked and answered, joined to its
// decision by ConnectionID and RequestSeq.
func (a *auditor) OnServerPDU(_ context.Context, ex modbus.ServerPDUExchange) {
	a.emit(auditEvent{Kind: "pdu", ConnectionID: ex.ConnectionID, RequestSeq: ex.RequestSeq,
		TransactionID: ex.TransactionID, Role: ex.ClientRole,
		Detail: fmt.Sprintf("request=% X response=% X", ex.Request, ex.Response)})
}

// OnProtocolViolation (ProtocolViolationMetrics).
func (a *auditor) OnProtocolViolation(v modbus.ProtocolViolation) {
	a.emit(auditEvent{Kind: "protocol violation", ConnectionID: v.ConnectionID, RequestSeq: v.RequestSeq,
		TransactionID: v.TransactionID, Role: v.ClientRole,
		Detail: fmt.Sprintf("fc=%v exception=%d closed=%v err=%v", v.FunctionCode, v.Exception, v.Closed, v.Err)})
}

// OnTLSHandshake (TLSHandshakeMetrics): only the failures are audited here;
// a completed handshake is audited by the connection authorizer.
func (a *auditor) OnTLSHandshake(info modbus.TLSHandshakeInfo) {
	if info.Err != nil {
		a.emit(auditEvent{Kind: "tls handshake failed", ConnectionID: info.ConnectionID,
			Detail: fmt.Sprintf("from %s: %v", info.RemoteAddr, info.Err)})
	}
}

// OnServerConnection (ServerConnectionMetrics): the end of a connection, or
// its rejection. The identity is forgotten afterwards.
func (a *auditor) OnServerConnection(ev modbus.ServerConnectionEvent) {
	switch ev.Kind {
	case modbus.ServerConnectionRejected:
		detail := "reason=" + ev.RejectReason.String()
		if ev.Err != nil {
			detail += fmt.Sprintf(" err=%v", ev.Err)
		}
		a.emit(auditEvent{Kind: "connection refused", ConnectionID: ev.ConnectionID, Role: ev.Role, Detail: detail})
	case modbus.ServerConnectionClosed:
		a.emit(auditEvent{Kind: "connection closed", ConnectionID: ev.ConnectionID, Role: ev.Role,
			Detail: fmt.Sprintf("reason=%v age=%v", ev.CloseReason, ev.Age.Round(time.Millisecond))})
	default:
		return
	}
	a.subjects.Delete(ev.ConnectionID)
}

// ServerMetrics itself: the counters an application would export. Nothing
// here is audited: OnError for a refused request has no ConnectionID; the
// decision event above carries it.
func (a *auditor) OnRequest(uint8, modbus.FunctionCode)                     {}
func (a *auditor) OnResponse(uint8, modbus.FunctionCode, time.Duration)     {}
func (a *auditor) OnError(uint8, modbus.FunctionCode, time.Duration, error) {}

// registers is the device: 100 holding registers.
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
	events, dropped, err := run(os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "security_hooks:", err)
		os.Exit(1)
	}
	fmt.Printf("%d audit events, %d dropped\n", len(events), dropped)
}

// run starts the server, runs the demo clients, stops the server and returns
// the audit events, as printed to out, and the number dropped.
func run(out io.Writer) ([]auditEvent, uint64, error) {
	pki, err := newPKI()
	if err != nil {
		return nil, 0, err
	}
	a := &auditor{
		revoked: map[string]bool{"revoked client": true},
		trusted: pki.ca,
		events:  make(chan auditEvent, 256),
	}
	srv, err := modbus.NewServer(&modbus.ServerConfig{
		URL:                  "tcp+tls://127.0.0.1:0",
		TLSServerCert:        &pki.server,
		TLSClientCAs:         pki.pool,
		ConnectionAuthorizer: a,
		Authorizer:           a,
		Metrics:              a,
		PDUTap:               a,
		Logger:               modbus.NopLogger(),
		// Opt-in hardening: every one of these is off by default.
		MaxClients:          16,
		MaxClientsPerHost:   4,
		FirstRequestTimeout: 10 * time.Second,
		FrameTimeout:        5 * time.Second,
	}, &registers{})
	if err != nil {
		return nil, 0, err
	}
	if err := srv.Start(); err != nil {
		return nil, 0, err
	}

	// The application's consumer: here it prints; it could as well count,
	// alert or forward.
	var trail []auditEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range a.events {
			trail = append(trail, e)
			_, _ = fmt.Fprintln(out, e)
		}
	}()

	_, port, _ := net.SplitHostPort(srv.Addr().String())
	demo(port, pki)

	if err := srv.Stop(); err != nil {
		return nil, 0, err
	}
	// No callback runs once Stop has returned.
	close(a.events)
	<-done
	return trail, a.dropped.Load(), nil
}

// demo runs the clients against the server on port, one after the other.
func demo(port string, pki *demoPKI) {
	ctx := context.Background()
	client := func(cert *tls.Certificate) *modbus.Client {
		c, err := modbus.New(modbus.Config{
			URL:           "tcp+tls://localhost:" + port,
			TLSClientCert: cert,
			TLSRootCAs:    pki.pool,
			Timeout:       time.Second,
			Logger:        modbus.NopLogger(),
		})
		if err != nil {
			panic(err)
		}
		return c
	}
	// The operator reads and writes.
	op := client(&pki.operator)
	if op.Open() == nil {
		_, _ = op.ReadHoldingRegister(ctx, 1, 0)
		_ = op.WriteRegister(ctx, 1, 0, 42)
		_ = op.Close()
	}
	// The viewer reads; its write is refused with exception 1.
	viewer := client(&pki.viewer)
	if viewer.Open() == nil {
		_, _ = viewer.ReadHoldingRegister(ctx, 1, 0)
		_ = viewer.WriteRegister(ctx, 1, 0, 7)
		_ = viewer.Close()
	}
	// The revoked client is refused before any request is read.
	revoked := client(&pki.revoked)
	if revoked.Open() == nil {
		_, _ = revoked.ReadHoldingRegister(ctx, 1, 0)
		_ = revoked.Close()
	}
	// The viewer sends an FC03 request one byte short: a protocol violation,
	// answered with exception 3, and the connection is closed.
	conn, err := tls.Dial("tcp", "localhost:"+port, &tls.Config{
		Certificates: []tls.Certificate{pki.viewer}, RootCAs: pki.pool, MinVersion: tls.VersionTLS12,
	})
	if err == nil {
		_, _ = conn.Write([]byte{0, 9, 0, 0, 0, 5, 1, 0x03, 0, 0, 0})
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		_, _ = io.Copy(io.Discard, conn)
		_ = conn.Close()
	}
}

// demoPKI is the throw-away PKI of the demo.
type demoPKI struct {
	ca                        *x509.Certificate
	pool                      *x509.CertPool
	server                    tls.Certificate
	operator, viewer, revoked tls.Certificate
}

// modbusRoleOID is the Modbus Role certificate extension (MBAPS, R-21).
var modbusRoleOID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 50316, 802, 1}

func newPKI() (*demoPKI, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "security_hooks demo CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	p := &demoPKI{ca: ca, pool: x509.NewCertPool()}
	p.pool.AddCert(ca)
	serial := int64(1)
	issue := func(cn string, server bool, role string) (tls.Certificate, error) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return tls.Certificate{}, err
		}
		serial++
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(serial),
			Subject:      pkix.Name{CommonName: cn},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}
		if server {
			tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			tmpl.DNSNames = []string{"localhost"}
		}
		if role != "" {
			val, err := asn1.MarshalWithParams(role, "utf8")
			if err != nil {
				return tls.Certificate{}, err
			}
			tmpl.ExtraExtensions = []pkix.Extension{{Id: modbusRoleOID, Value: val}}
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
		if err != nil {
			return tls.Certificate{}, err
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
	}
	for _, c := range []struct {
		dst      *tls.Certificate
		cn, role string
		server   bool
	}{
		{&p.server, "localhost", "", true},
		{&p.operator, "operator client", "operator", false},
		{&p.viewer, "viewer client", "viewer", false},
		{&p.revoked, "revoked client", "operator", false},
	} {
		if *c.dst, err = issue(c.cn, c.server, c.role); err != nil {
			return nil, err
		}
	}
	return p, nil
}
