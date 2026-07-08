// SPDX-License-Identifier: MIT

package modbus

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
	"time"
)

// These tests cover the configuration-validation and dial error paths for
// client and server construction that the happy-path harness never reaches.

// freeClosedAddr returns an address that was bound and then released, so a
// subsequent dial reliably fails with "connection refused".
func freeClosedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

func testTLSClientConfig(t *testing.T) Config {
	t.Helper()
	kp, err := tls.X509KeyPair([]byte(clientCert), []byte(clientKey))
	if err != nil {
		t.Fatalf("client keypair: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(serverCert)) {
		t.Fatal("append server cert")
	}
	return Config{TLSClientCert: &kp, TLSRootCAs: pool, Timeout: time.Second, DialTimeout: time.Second}
}

func TestNewClient_ConfigErrors(t *testing.T) {
	tlsCfg := testTLSClientConfig(t)
	cases := []struct {
		name string
		conf Config
	}{
		{"missing scheme", Config{URL: "127.0.0.1:502"}},
		{"unsupported scheme", Config{URL: "carrier-pigeon://127.0.0.1:502"}},
		{"tls missing client cert", Config{URL: "tcp+tls://localhost:502"}},
		{"tls missing root cas", Config{URL: "tcp+tls://localhost:502", TLSClientCert: tlsCfg.TLSClientCert}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.conf); err == nil {
				t.Fatalf("New(%q): expected error, got nil", tc.conf.URL)
			}
			if err := ValidateConfig(tc.conf); err == nil {
				t.Fatalf("ValidateConfig(%q): expected error, got nil", tc.conf.URL)
			}
		})
	}
}

func TestClientOpen_DialErrors(t *testing.T) {
	refused := freeClosedAddr(t)

	t.Run("tcp refused", func(t *testing.T) {
		c, err := New(Config{URL: "tcp://" + refused, Timeout: time.Second, DialTimeout: 500 * time.Millisecond})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := c.Open(); err == nil {
			_ = c.Close()
			t.Fatal("Open: expected dial error, got nil")
		}
	})

	t.Run("rtuovertcp refused", func(t *testing.T) {
		c, err := New(Config{URL: "rtuovertcp://" + refused, Timeout: time.Second, DialTimeout: 500 * time.Millisecond})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := c.Open(); err == nil {
			_ = c.Close()
			t.Fatal("Open: expected dial error, got nil")
		}
	})

	t.Run("rtu bad device", func(t *testing.T) {
		c, err := New(Config{URL: "rtu:///dev/nonexistent-modbus-serial", Timeout: time.Second})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := c.Open(); err == nil {
			_ = c.Close()
			t.Fatal("Open: expected serial open error, got nil")
		}
	})

	t.Run("tcp+tls refused", func(t *testing.T) {
		cfg := testTLSClientConfig(t)
		cfg.URL = "tcp+tls://" + refused
		c, err := New(cfg)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := c.Open(); err == nil {
			_ = c.Close()
			t.Fatal("Open: expected TLS dial error, got nil")
		}
	})
}

func TestNewServer_ConfigErrors(t *testing.T) {
	dev := newRefDevice()
	serverKP, err := tls.X509KeyPair([]byte(serverCert), []byte(serverKey))
	if err != nil {
		t.Fatalf("server keypair: %v", err)
	}

	t.Run("nil conf", func(t *testing.T) {
		if _, err := NewServer(nil, dev); err == nil {
			t.Fatal("expected error for nil conf")
		}
	})
	t.Run("nil handler", func(t *testing.T) {
		if _, err := NewServer(&ServerConfig{URL: "tcp://127.0.0.1:0"}, nil); err == nil {
			t.Fatal("expected error for nil handler")
		}
	})
	t.Run("missing host", func(t *testing.T) {
		if _, err := NewServer(&ServerConfig{URL: "tcp://"}, dev); err == nil {
			t.Fatal("expected error for missing host")
		}
	})
	t.Run("unsupported scheme", func(t *testing.T) {
		if _, err := NewServer(&ServerConfig{URL: "smoke-signal://127.0.0.1:502"}, dev); err == nil {
			t.Fatal("expected error for unsupported scheme")
		}
	})
	t.Run("tls missing server cert", func(t *testing.T) {
		if _, err := NewServer(&ServerConfig{URL: "tcp+tls://127.0.0.1:0"}, dev); err == nil {
			t.Fatal("expected error for missing server cert")
		}
	})
	t.Run("tls missing client cas", func(t *testing.T) {
		if _, err := NewServer(&ServerConfig{URL: "tcp+tls://127.0.0.1:0", TLSServerCert: &serverKP}, dev); err == nil {
			t.Fatal("expected error for missing client CAs")
		}
	})
}

func TestServerStart_ListenError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	inUse := ln.Addr().String()

	// A second server binding the already-in-use port must fail to start.
	srv, err := NewServer(&ServerConfig{URL: "tcp://" + inUse}, newRefDevice())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if err := srv.Start(); err == nil {
		_ = srv.Stop()
		t.Fatal("Start: expected listen error on in-use port, got nil")
	}
}
