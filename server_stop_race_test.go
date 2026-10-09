// SPDX-License-Identifier: MIT

package modbus

import (
	"net"
	"testing"
	"time"
)

// A client that connects while the server is stopping must not race the accept
// loop's WaitGroup.Add against Shutdown's WaitGroup.Wait. Run with -race.
func TestServerStop_ConcurrentWithAccept(t *testing.T) {
	iterations := 300
	if testing.Short() {
		iterations = 50
	}
	for i := 0; i < iterations; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()

		server, err := NewServer(&ServerConfig{URL: "tcp://" + addr, MaxClients: 4, Timeout: time.Second}, &noSerialFCHandler{})
		if err != nil {
			t.Fatalf("NewServer: %v", err)
		}
		if err := server.Start(); err != nil {
			// The port was taken between Close and Start: try the next one.
			continue
		}

		// The connection is established by the kernel before the accept loop sees it, so
		// stopping right after the dial returns lets Stop overlap with that accept.
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		if i%2 == 1 {
			// Vary the interleaving: give the accept loop a head start on odd rounds.
			time.Sleep(time.Duration(i%7) * 20 * time.Microsecond)
		}
		if err := server.Stop(); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		_ = conn.Close()
	}
}
