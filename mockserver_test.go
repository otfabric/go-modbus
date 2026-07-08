// SPDX-License-Identifier: MIT

package modbus

import (
	"net"
	"testing"
	"time"
)

// This file provides a small programmable mock Modbus/TCP server used to drive
// the client through adversarial and error-path responses that a well-behaved
// server would never produce. It complements the reference-device harness in
// backtoback_test.go (which tests the happy path against our real server).

// mockResponder receives the decoded parts of one request and returns the raw
// bytes to write back to the client. Returning nil sends nothing (simulating a
// silent/timed-out device).
type mockResponder func(txid []byte, unitID, fc byte, payload []byte) []byte

// startMockServer starts a TCP server that feeds every request to respond, and
// returns a connected client plus a cleanup function.
func startMockServer(t *testing.T, respond mockResponder) (*Client, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			sock, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				for {
					frame, err := readMBAPFrame(c)
					if err != nil {
						return
					}
					if len(frame) < 8 {
						return
					}
					txid := frame[0:2]
					unitID := frame[6]
					fc := frame[7]
					payload := frame[8:]
					if out := respond(txid, unitID, fc, payload); out != nil {
						if _, err := c.Write(out); err != nil {
							return
						}
					}
				}
			}(sock)
		}
	}()

	client, err := New(Config{URL: "tcp://" + ln.Addr().String(), Timeout: 500 * time.Millisecond})
	if err != nil {
		_ = ln.Close()
		t.Fatalf("New: %v", err)
	}
	if err := client.Open(); err != nil {
		_ = ln.Close()
		t.Fatalf("Open: %v", err)
	}

	cleanup := func() {
		_ = client.Close()
		_ = ln.Close()
	}
	return client, cleanup
}

// normalFrame builds a normal (non-exception) MBAP response frame.
func normalFrame(txid []byte, unitID, fc byte, payload []byte) []byte {
	length := uint16ToBytes(BigEndian, uint16(2+len(payload)))
	frame := []byte{txid[0], txid[1], 0x00, 0x00, length[0], length[1], unitID, fc}
	return append(frame, payload...)
}

// exceptionFrame builds an exception MBAP response frame for the given FC.
func exceptionFrame(txid []byte, unitID, fc, code byte) []byte {
	return []byte{txid[0], txid[1], 0x00, 0x00, 0x00, 0x03, unitID, fc | 0x80, code}
}
