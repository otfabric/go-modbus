// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/otfabric/go-modbus"
)

func TestExitCode(t *testing.T) {
	exc := &modbus.ExceptionError{FunctionCode: 3, ExceptionCode: 2}
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, ExitOK},
		{"usage", usagef("no target"), ExitUsage},
		{"wrapped usage", fmt.Errorf("x: %w", usageError(errors.New("bad"))), ExitUsage},
		{"input", inputError(os.ErrNotExist), ExitInvalidInput},
		{"output", outputError(io.ErrShortWrite), ExitInternal},
		{"interrupted", errInterrupted, ExitInterrupted},
		{"cancelled", fmt.Errorf("read: %w", context.Canceled), ExitInterrupted},
		{"exception", exc, ExitProtocol},
		{"exception sentinel", fmt.Errorf("x: %w", modbus.ErrIllegalDataAddress), ExitProtocol},
		{"gateway target", modbus.ErrGWTargetFailedToRespond, ExitProtocol},
		{"protocol error", modbus.ErrProtocolError, ExitProtocol},
		{"bad crc", modbus.ErrBadCRC, ExitProtocol},
		{"bad unit id", modbus.ErrBadUnitID, ExitProtocol},
		{"mbap length", modbus.ErrInvalidMBAPLength, ExitProtocol},
		{"request timeout", modbus.ErrRequestTimedOut, ExitTimeout},
		{"deadline", context.DeadlineExceeded, ExitTimeout},
		{"net timeout", &net.OpError{Op: "dial", Err: timeoutErr{}}, ExitTimeout},
		{"open timeout", &transportError{fmt.Errorf("open: %w", modbus.ErrRequestTimedOut)}, ExitTimeout},
		{"configuration", modbus.ErrConfigurationError, ExitUsage},
		{"parameters", modbus.ErrUnexpectedParameters, ExitUsage},
		{"open failure", &transportError{errors.New("no such device")}, ExitTransport},
		{"net error", &net.OpError{Op: "read", Err: syscall.ECONNRESET}, ExitTransport},
		{"errno", syscall.ECONNREFUSED, ExitTransport},
		{"eof", io.EOF, ExitTransport},
		{"unexpected eof", io.ErrUnexpectedEOF, ExitTransport},
		{"not open", modbus.ErrClientNotOpen, ExitTransport},
		{"serial not open", modbus.ErrSerialPortNotOpen, ExitTransport},
		{"closed", fmt.Errorf("read: %w", net.ErrClosed), ExitTransport},
		{"unclassified", errors.New("decode error"), ExitInternal},
		{"all failed", &operationsError{failed: 2, first: modbus.ErrIllegalDataAddress}, ExitProtocol},
		{"all timed out", &operationsError{failed: 2, first: modbus.ErrRequestTimedOut}, ExitTimeout},
		{"partial", &operationsError{failed: 1, succeeded: 1, first: modbus.ErrRequestTimedOut}, ExitPartial},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCode(tt.err); got != tt.want {
				t.Errorf("exitCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// failingWriter fails every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, syscall.EPIPE }

func TestCLI_ExitCodes(t *testing.T) {
	h := newTestHandler()
	h.limit = 0x100
	h.silent[9] = true
	target := startServer(t, h)

	t.Run("ok", func(t *testing.T) {
		expectRun(t, ExitOK, "--target", target, "rc:0", "ping:1")
	})
	t.Run("protocol", func(t *testing.T) {
		_, stderr := expectRun(t, ExitProtocol, "--target", target, "rh:uint16:0x100")
		if !strings.Contains(stderr, "1 of 1 operations failed") {
			t.Errorf("unexpected stderr %q", stderr)
		}
	})
	t.Run("timeout", func(t *testing.T) {
		expectRun(t, ExitTimeout, "--target", target, "--timeout", "200ms", "--unit-id", "9", "rc:0")
	})
	t.Run("partial", func(t *testing.T) {
		_, stderr := expectRun(t, ExitPartial, "--target", target, "rc:0", "rc:0x100", "rc:1")
		if !strings.Contains(stderr, "1 of 3 operations failed") {
			t.Errorf("unexpected stderr %q", stderr)
		}
	})
	t.Run("partial fail-fast", func(t *testing.T) {
		stdout, _ := expectRun(t, ExitPartial, "--target", target, "--fail-fast", "rc:0", "rc:0x100", "rc:1")
		if n := strings.Count(stdout, "\n"); n != 2 {
			t.Errorf("got %d lines, want 2:\n%s", n, stdout)
		}
	})
	t.Run("output", func(t *testing.T) {
		for _, format := range [][]string{nil, {"--json"}} {
			var stderr bytes.Buffer
			args := append(append([]string{"--target", target}, format...), "rc:0", "rc:1")
			code := Run(context.Background(), args, failingWriter{}, &stderr)
			if code != ExitInternal || !strings.Contains(stderr.String(), "failed to write results") {
				t.Errorf("%v: exit %d, stderr %q", format, code, stderr.String())
			}
		}
	})
	t.Run("interrupted", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var stdout, stderr bytes.Buffer
		code := Run(ctx, []string{"--target", target, "rc:0"}, &stdout, &stderr)
		if code != ExitInterrupted || !strings.Contains(stderr.String(), "Error: interrupted") {
			t.Errorf("exit %d, stderr %q", code, stderr.String())
		}
	})
}
