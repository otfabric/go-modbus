// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Mutual TLS: the role in the client certificate reaches every handler, and a
// handler can use it for authorisation.
func TestE2E_TLS_MutualAuthAndRoles(t *testing.T) {
	pki := e2ePKI(t)
	dev := e2eNewDevice()
	// Only operators may write.
	dev.setHook(func(_ context.Context, c e2eCall) error {
		write := c.IsWrite || c.Kind == "mask" || c.Kind == "rw"
		if write && c.ClientRole != "operator" {
			return ErrIllegalFunction
		}
		return nil
	})
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{})
	ctx := context.Background()

	// The default client certificate carries the role "operator": every
	// handler type sees it.
	for _, op := range e2eAllOps() {
		if !op.served {
			continue
		}
		if err := op.call(ctx, p.client); err != nil {
			t.Fatalf("%s as operator: %v", op.name, err)
		}
		for _, call := range dev.takeCalls() {
			if call.ClientRole != "operator" {
				t.Errorf("%s: handler saw role %q, want \"operator\"", op.name, call.ClientRole)
			}
		}
	}

	roles := []struct {
		name     string
		cert     *tls.Certificate
		wantRole string
	}{
		{"viewer", e2eIssue(t, pki.ca, "viewer client", false, "viewer"), "viewer"},
		{"no role extension", e2eIssue(t, pki.ca, "roleless client", false), ""},
		{"role with the wrong ASN.1 type", e2eIssue(t, pki.ca, "printable-role client", false, "printable:operator"), ""},
		{"empty role", e2eIssue(t, pki.ca, "empty-role client", false, ""), ""},
	}
	for _, r := range roles {
		c := p.newClient(func(conf *Config) { conf.TLSClientCert = r.cert })
		if err := c.Open(); err != nil {
			t.Fatalf("%s: Open: %v", r.name, err)
		}
		dev.setHolding(40, 0x0BEE)
		// Reads are allowed, and the handler sees the role.
		if got, err := c.ReadHoldingRegister(ctx, e2eUnit, 40); err != nil || got != 0x0BEE {
			t.Errorf("%s: read = 0x%04X, %v", r.name, got, err)
		}
		if call := e2eOneCall(t, r.name, dev); call.ClientRole != r.wantRole {
			t.Errorf("%s: handler saw role %q, want %q", r.name, call.ClientRole, r.wantRole)
		}
		// Writes need the operator role: wrong role, no write.
		e2eWantException(t, r.name+" WriteRegister", c.WriteRegister(ctx, e2eUnit, 40, 1), FCWriteSingleRegister, exIllegalFunction)
		e2eWantException(t, r.name+" WriteRegisters", c.WriteRegisters(ctx, e2eUnit, 40, []uint16{1}), FCWriteMultipleRegisters, exIllegalFunction)
		e2eWantException(t, r.name+" WriteCoil", c.WriteCoil(ctx, e2eUnit, 40, true), FCWriteSingleCoil, exIllegalFunction)
		e2eWantException(t, r.name+" WriteCoils", c.WriteCoils(ctx, e2eUnit, 40, []bool{true}), FCWriteMultipleCoils, exIllegalFunction)
		e2eWantException(t, r.name+" MaskWriteRegister", c.MaskWriteRegister(ctx, e2eUnit, 40, 0, 0), FCMaskWriteRegister, exIllegalFunction)
		_, err := c.ReadWriteMultipleRegisters(ctx, e2eUnit, 40, 1, 40, []uint16{1})
		e2eWantException(t, r.name+" ReadWriteMultipleRegisters", err, FCReadWriteMultipleRegs, exIllegalFunction)
		if got := dev.holdingAt(40, 1)[0]; got != 0x0BEE || dev.coilsAt(40, 1)[0] {
			t.Errorf("%s: a refused write changed the device", r.name)
		}
		for _, call := range dev.takeCalls() {
			if call.ClientRole != r.wantRole {
				t.Errorf("%s: handler saw role %q, want %q", r.name, call.ClientRole, r.wantRole)
			}
		}
		_ = c.Close()
	}

	// The operator connection was not affected by the others.
	if err := p.client.WriteRegister(ctx, e2eUnit, 40, 0x0AAA); err != nil {
		t.Errorf("operator write: %v", err)
	}
	e2eEventually(t, "the other connections to be dropped", func() bool { return p.conns() == 1 })
}

// On plain TCP there is no certificate and hence no role.
func TestE2E_TLS_NoRoleOnPlainTCP(t *testing.T) {
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp", dev, e2eOpts{})
	for _, op := range e2eAllOps() {
		if !op.served {
			continue
		}
		if err := op.call(context.Background(), p.client); err != nil {
			t.Fatalf("%s: %v", op.name, err)
		}
		for _, call := range dev.takeCalls() {
			if call.ClientRole != "" {
				t.Errorf("%s: handler saw role %q on plain TCP", op.name, call.ClientRole)
			}
		}
	}
}

// A client whose certificate the server does not trust is never served.
func TestE2E_TLS_ClientCertNotTrusted(t *testing.T) {
	pki := e2ePKI(t)
	dev := e2eNewDevice()
	srvlog := &e2eLogger{}
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{server: func(c *ServerConfig) { c.Logger = srvlog }})
	ctx := context.Background()

	rogue := p.newClient(func(c *Config) {
		c.TLSClientCert = e2eIssue(t, pki.otherCA, "rogue client", false, "operator")
		c.Timeout = time.Second
	})
	// With TLS 1.3 the client finishes its handshake before the server has
	// judged the client certificate, so the rejection may only show on the
	// first request.
	err := rogue.Open()
	if err == nil {
		err = rogue.WriteRegister(ctx, e2eUnit, 1, 0x0BAD)
	}
	if err == nil {
		t.Fatal("a client with an untrusted certificate was served")
	}
	if errors.Is(err, ErrRequestTimedOut) {
		t.Errorf("untrusted client timed out instead of being rejected: %v", err)
	}
	if n := dev.callCount(); n != 0 {
		t.Errorf("untrusted client reached a handler (%d requests)", n)
	}
	if got := dev.holdingAt(1, 1)[0]; got != 0 {
		t.Errorf("untrusted client changed the device: 0x%04X", got)
	}
	e2eEventually(t, "the handshake failure to be logged", func() bool {
		return srvlog.has("W ", "TLS handshake with", "failed")
	})
	_ = rogue.Close()

	// A client without any usable certificate chain to the server's CA cannot
	// be configured without a certificate at all.
	conf := p.clientConf
	conf.TLSClientCert = nil
	if _, err := New(conf); !errors.Is(err, ErrConfigurationError) {
		t.Errorf("tcp+tls client without a certificate: %v", err)
	}

	// The trusted client is served, and the rejected connection left no slot behind.
	if err := p.client.WriteRegister(ctx, e2eUnit, 1, 0x600D); err != nil {
		t.Fatalf("trusted client: %v", err)
	}
	e2eEventually(t, "the rejected connection to be released", func() bool { return p.conns() == 1 })
}

// A client that does not trust the server certificate refuses to open.
func TestE2E_TLS_ServerCertNotTrusted(t *testing.T) {
	pki := e2ePKI(t)
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{noOpen: true})
	ctx := context.Background()

	wary := p.newClient(func(c *Config) { c.TLSRootCAs = pki.otherCA.pool })
	err := wary.Open()
	if err == nil {
		t.Fatal("Open succeeded although the server certificate is not trusted")
	}
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) {
		t.Errorf("Open: %v, want x509.UnknownAuthorityError", err)
	}
	if wary.Info().IsOpen {
		t.Error("client reports open")
	}
	if _, err := wary.ReadCoil(ctx, e2eUnit, 0); !errors.Is(err, ErrClientNotOpen) {
		t.Errorf("request after the failed Open: %v", err)
	}

	// The server certificate is valid for localhost and the loopback
	// addresses only: any other name is refused.
	_, port, _ := net.SplitHostPort(p.hostPort)
	byIP := p.newClient(func(c *Config) { c.URL = "tcp+tls://127.0.0.1:" + port })
	if err := byIP.Open(); err != nil {
		t.Errorf("Open by IP address covered by the certificate: %v", err)
	}
	_ = byIP.Close()
	other, err := NewServer(&ServerConfig{
		URL:           "tcp+tls://127.0.0.1:0",
		TLSServerCert: e2eIssue(t, pki.ca, "a client certificate used as server", false),
		TLSClientCAs:  pki.ca.pool,
	}, dev)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Stop() })
	mismatch, err := New(e2eClientConfig(t, "tcp+tls", other.tcpListener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	if err := mismatch.Open(); err == nil {
		_ = mismatch.Close()
		t.Error("Open succeeded against a certificate that is not valid for the server name")
	}

	if n := dev.callCount(); n != 0 {
		t.Errorf("%d requests reached a handler", n)
	}
	// The server keeps serving clients that do trust it.
	if err := p.client.Open(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.client.ReadCoil(ctx, e2eUnit, 0); err != nil {
		t.Errorf("trusting client: %v", err)
	}
	e2eEventually(t, "failed handshakes to be released", func() bool { return p.conns() == 1 })
}

// Both sides refuse anything older than TLS 1.2.
func TestE2E_TLS_MinimumVersion(t *testing.T) {
	pki := e2ePKI(t)
	dev := e2eNewDevice()
	dev.setHolding(0, 0x1234)
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{noOpen: true})

	dial := func(min, max uint16) (*tls.Conn, error) {
		d := &net.Dialer{Timeout: e2eWait}
		return tls.DialWithDialer(d, "tcp", p.hostPort, &tls.Config{
			Certificates: []tls.Certificate{pki.clientCert},
			RootCAs:      pki.ca.pool,
			ServerName:   "localhost",
			MinVersion:   min,
			MaxVersion:   max,
		})
	}
	// Server side.
	for _, v := range []uint16{tls.VersionTLS10, tls.VersionTLS11} {
		conn, err := dial(tls.VersionTLS10, v)
		if err == nil {
			_ = conn.Close()
			t.Errorf("server accepted a TLS 0x%04x handshake", v)
		}
	}
	for _, v := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
		conn, err := dial(v, v)
		if err != nil {
			t.Fatalf("server refused a TLS 0x%04x handshake: %v", v, err)
		}
		if got := conn.ConnectionState().Version; got != v {
			t.Errorf("negotiated 0x%04x, want 0x%04x", got, v)
		}
		// The connection really serves Modbus.
		_ = conn.SetDeadline(time.Now().Add(e2eWait))
		if _, err := conn.Write(rawRequestFrame(7, e2eUnit, byte(FCReadHoldingRegisters), u16(0, 1))); err != nil {
			t.Fatal(err)
		}
		res, err := readMBAPResponse(conn)
		if err != nil || res.TransactionID != 7 || res.FunctionCode != byte(FCReadHoldingRegisters) ||
			len(res.Payload) != 3 || res.Payload[1] != 0x12 || res.Payload[2] != 0x34 {
			t.Errorf("TLS 0x%04x: response %+v, %v", v, res, err)
		}
		_ = conn.Close()
	}
	e2eEventually(t, "raw connections to be released", func() bool { return p.conns() == 0 })

	// Client side: a peer limited to TLS 1.1 is refused, TLS 1.2 is fine.
	for _, tc := range []struct {
		max    uint16
		wantOK bool
	}{{tls.VersionTLS11, false}, {tls.VersionTLS12, true}} {
		ln := e2eTLSListener(t, func(c *tls.Config) { c.MaxVersion = tc.max })
		handshakes := make(chan error, 1)
		go func() {
			conn, err := ln.Accept()
			if err != nil {
				handshakes <- err
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(e2eWait))
			err = conn.(*tls.Conn).Handshake()
			if err == nil {
				// Keep the connection until the client is done with it.
				_, _ = io.Copy(io.Discard, conn)
			}
			handshakes <- err
		}()
		conf := e2eClientConfig(t, "tcp+tls", ln.Addr().String())
		conf.DialTimeout = e2eWait
		c, err := New(conf)
		if err != nil {
			t.Fatal(err)
		}
		err = c.Open()
		if (err == nil) != tc.wantOK {
			t.Errorf("client Open against a TLS<=0x%04x peer: %v, want success=%v", tc.max, err, tc.wantOK)
		}
		_ = c.Close()
		if err := <-handshakes; (err == nil) != tc.wantOK {
			t.Errorf("peer-side handshake limited to TLS 0x%04x: %v, want success=%v", tc.max, err, tc.wantOK)
		}
	}
}

// Mixing the plain and the TLS transport fails cleanly on both sides.
func TestE2E_TLS_TransportMismatch(t *testing.T) {
	ctx := context.Background()

	// Plain TCP client against a TLS server.
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{server: func(c *ServerConfig) { c.TLSHandshakeTimeout = time.Second }})
	plain, err := New(Config{URL: "tcp://" + p.hostPort, Timeout: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = plain.Close() })
	if err := plain.Open(); err != nil {
		t.Fatalf("plain TCP connect: %v", err)
	}
	if _, err := plain.ReadHoldingRegister(ctx, e2eUnit, 0); err == nil {
		t.Error("plain TCP request to a TLS server succeeded")
	}
	_ = plain.Close()
	if n := dev.callCount(); n != 0 {
		t.Errorf("plain TCP request reached a handler of the TLS server (%d)", n)
	}
	if _, err := p.client.ReadHoldingRegister(ctx, e2eUnit, 0); err != nil {
		t.Errorf("TLS client after the plain one: %v", err)
	}
	e2eEventually(t, "the plain connection to be released", func() bool { return p.conns() == 1 })

	// TLS client against a plain TCP server.
	dev2 := e2eNewDevice()
	p2 := e2eStart(t, "tcp", dev2, e2eOpts{})
	conf := e2eClientConfig(t, "tcp+tls", p2.hostPort)
	conf.DialTimeout = 500 * time.Millisecond
	tlsClient, err := New(conf)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err := tlsClient.Open(); err == nil {
		_ = tlsClient.Close()
		t.Error("TLS handshake with a plain TCP server succeeded")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("TLS client took %v to give up on a plain server", d)
	}
	if n := dev2.callCount(); n != 0 {
		t.Errorf("TLS handshake bytes reached a handler of the plain server (%d)", n)
	}
	if _, err := p2.client.ReadHoldingRegister(ctx, e2eUnit, 0); err != nil {
		t.Errorf("plain client after the TLS one: %v", err)
	}
	e2eEventually(t, "the TLS client's connection to be released", func() bool { return p2.conns() == 1 })
}

// A peer that connects and never starts the handshake is dropped after
// TLSHandshakeTimeout and does not keep a client slot.
func TestE2E_TLS_HandshakeTimeout(t *testing.T) {
	dev := e2eNewDevice()
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{server: func(c *ServerConfig) {
		c.TLSHandshakeTimeout = 50 * time.Millisecond
		c.MaxClients = 1
	}, noOpen: true})

	conn, err := net.Dial("tcp", p.hostPort)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	e2eEventually(t, "the silent connection to be tracked", func() bool { return p.conns() == 1 })
	start := time.Now()
	_ = conn.SetReadDeadline(time.Now().Add(e2eWait))
	if _, err := conn.Read(make([]byte, 1)); err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("silent connection: read returned %v, want the server to close it", err)
	}
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		t.Fatal("the server did not drop a connection that never started a handshake")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("the server took %v to drop the silent connection", d)
	}
	e2eEventually(t, "the slot to be freed", func() bool { return p.conns() == 0 })
	// The only slot is available again. A real client has to complete its
	// handshake within the same short timeout, so allow it a few tries.
	e2eEventually(t, "a client to be served on the freed slot", func() bool {
		_ = p.client.Close()
		if err := p.client.Open(); err != nil {
			return false
		}
		_, err := p.client.ReadCoil(context.Background(), e2eUnit, 0)
		return err == nil
	})
}

// Certificate pools loaded from PEM files with LoadCertPool work on both sides.
func TestE2E_TLS_LoadCertPool(t *testing.T) {
	pki := e2ePKI(t)
	path := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pki.ca.cert.Raw})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	pool, err := LoadCertPool(path)
	if err != nil {
		t.Fatalf("LoadCertPool: %v", err)
	}

	dev := e2eNewDevice()
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{
		server: func(c *ServerConfig) { c.TLSClientCAs = pool },
		client: func(c *Config) { c.TLSRootCAs = pool },
	})
	if err := p.client.WriteRegister(context.Background(), e2eUnit, 3, 0x0303); err != nil {
		t.Fatalf("request with pools from LoadCertPool: %v", err)
	}
	if call := e2eOneCall(t, "WriteRegister", dev); call.ClientRole != "operator" {
		t.Errorf("handler saw role %q", call.ClientRole)
	}
}
