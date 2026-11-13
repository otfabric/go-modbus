// SPDX-License-Identifier: MIT

package modbus

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"testing"
	"time"
)

// addrConn is a net.Conn that only knows its remote address.
type addrConn struct {
	net.Conn
	remote net.Addr
}

func (c *addrConn) RemoteAddr() net.Addr { return c.remote }

func pendingFrom(hosts ...string) []net.Conn {
	conns := make([]net.Conn, len(hosts))
	for i, h := range hosts {
		conns[i] = &addrConn{remote: &net.TCPAddr{IP: net.ParseIP(h), Port: 40000 + i}}
	}
	return conns
}

// When every slot is taken, the connection replaced is the oldest one still
// handshaking from the host with the most of them, or from the new
// connection's own host when it has as many.
func TestPickPendingVictim(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pending []string
		host    string
		want    int
	}{
		{"none pending", nil, "10.0.0.9", -1},
		{"same host", []string{"10.0.0.1", "10.0.0.1"}, "10.0.0.1", 0},
		{"other host with more", []string{"10.0.0.2", "10.0.0.1", "10.0.0.1"}, "10.0.0.9", 1},
		{"new host, tie: oldest", []string{"10.0.0.2", "10.0.0.1"}, "10.0.0.9", 0},
		{"own host as many as any", []string{"10.0.0.2", "10.0.0.1"}, "10.0.0.1", 1},
		{"own host fewer", []string{"10.0.0.1", "10.0.0.2", "10.0.0.2"}, "10.0.0.1", 1},
	} {
		if got := pickPendingVictim(pendingFrom(tc.pending...), tc.host); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestRemoteHost(t *testing.T) {
	if got := remoteHost(&addrConn{remote: &net.TCPAddr{IP: net.ParseIP("::1"), Port: 1}}); got != "::1" {
		t.Errorf("remoteHost: %q", got)
	}
	if got := remoteHost(&addrConn{remote: &net.UnixAddr{Name: "sock", Net: "unix"}}); got != "sock" {
		t.Errorf("remoteHost without port: %q", got)
	}
	if got := remoteHost(&addrConn{}); got != "" {
		t.Errorf("remoteHost without address: %q", got)
	}
}

func TestTLSHandshakeTimeoutDefault(t *testing.T) {
	pair, err := tls.X509KeyPair([]byte(serverCert), []byte(serverKey))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(&ServerConfig{
		URL:           "tcp+tls://127.0.0.1:0",
		TLSServerCert: &pair,
		TLSClientCAs:  x509.NewCertPool(),
	}, chaosNewDevice(1))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if got := srv.conf.TLSHandshakeTimeout; got != 10*time.Second {
		t.Errorf("default TLSHandshakeTimeout %v, want 10 s", got)
	}
}
