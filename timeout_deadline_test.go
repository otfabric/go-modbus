// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// silentListener accepts connections, reads what they send and never answers.
func silentListener(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				buf := make([]byte, 512)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}()
		}
	}()
	return l.Addr().String()
}

// TestClient_TimeoutUnderContextDeadline checks that Config.Timeout ends a
// request whose context deadline is later, and that an earlier context
// deadline still wins.
func TestClient_TimeoutUnderContextDeadline(t *testing.T) {
	addr := silentListener(t)
	tests := []struct {
		name     string
		timeout  time.Duration // Config.Timeout
		deadline time.Duration // context deadline
	}{
		{"timeout first", 100 * time.Millisecond, 10 * time.Second},
		{"deadline first", 10 * time.Second, 100 * time.Millisecond},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(Config{URL: "tcp://" + addr, Timeout: tc.timeout, Logger: NopLogger()})
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Open(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), tc.deadline)
			defer cancel()

			start := time.Now()
			_, err = c.ReadRegisters(ctx, 1, 0, 1, HoldingRegister)
			// Either way the earlier of the two ends the request, well
			// before the later one.
			if took := time.Since(start); took > 3*time.Second {
				t.Errorf("request took %v; timeout %v, context deadline %v", took, tc.timeout, tc.deadline)
			}
			if !errors.Is(err, ErrRequestTimedOut) {
				t.Errorf("err = %v, want ErrRequestTimedOut", err)
			}
		})
	}
}
