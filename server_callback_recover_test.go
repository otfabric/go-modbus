// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"crypto"
	"crypto/tls"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
)

// The application's code the server calls besides the handler and the
// ServerMetrics hooks (server_callbacks_test.go): the methods of a handler's
// error, the private key of TLSServerCert, the Logger. On v1.15.3 a call of any
// of them that did not return normally ended the process; now it is logged at
// error level with its stack, handled as an error of that call, and the server
// goes on.

// unrulyError is an error whose method named by fails does not return
// normally.
type unrulyError struct{ fails string }

func (e unrulyError) Error() string { return "unruly error" }

func (e unrulyError) Is(error) bool {
	if e.fails == "Is" {
		panic("Is of the handler's error")
	}
	return false
}

func (e unrulyError) Unwrap() error {
	if e.fails == "Unwrap" {
		panic("Unwrap of the handler's error")
	}
	return nil
}

// The server reads a handler's error with errors.Is to choose the answer. An
// error whose Is or Unwrap does not return normally is answered with
// ServerDeviceFailure, and the connection serves the next request.
func TestServerHandlerError_MethodThatDoesNotReturnNormally(t *testing.T) {
	for _, method := range []string{"Is", "Unwrap"} {
		t.Run(method, func(t *testing.T) {
			dev := e2eNewDevice()
			dev.setHolding(2, 0x0202)
			log := &e2eLogger{}
			h := &fnHandler{holding: func(req *HoldingRegistersRequest) ([]uint16, error) {
				if req.Addr == 1 {
					return nil, unrulyError{fails: method}
				}
				return dev.HandleHoldingRegisters(context.Background(), req)
			}}
			p := e2eStart(t, "tcp", h, e2eOpts{server: func(c *ServerConfig) { c.Logger = log }})

			_, err := p.client.ReadHoldingRegister(context.Background(), e2eUnit, 1)
			if !errors.Is(err, ErrServerDeviceFailure) {
				t.Fatalf("request whose handler error does not return normally: %v, want ServerDeviceFailure", err)
			}
			if !log.has("E ", "the error of the handler for FC 0x03 from", "did not return normally: "+method+" of the handler's error", "goroutine ") {
				t.Errorf("no error-level log line with the stack:\n%s", log.dump())
			}
			if v, err := p.client.ReadHoldingRegister(context.Background(), e2eUnit, 2); err != nil || v != 0x0202 {
				t.Errorf("next request on the connection: %#04x, %v", v, err)
			}
			if got := p.client.LastObservedTransactionID(); got != 2 {
				t.Errorf("transaction ID %d after two requests: the connection was replaced", got)
			}
		})
	}
}

// onceFailingSigner is a crypto.Signer whose next Sign does not return
// normally while fail is set; it clears fail.
type onceFailingSigner struct {
	crypto.Signer
	fail atomic.Bool
}

func (s *onceFailingSigner) Sign(rand io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	if s.fail.CompareAndSwap(true, false) {
		panic("the private key's Sign")
	}
	return s.Signer.Sign(rand, digest, opts)
}

// withFailingKey returns a copy of cert whose private key fails its next Sign.
func withFailingKey(t *testing.T, cert tls.Certificate) (*tls.Certificate, *onceFailingSigner) {
	t.Helper()
	key, ok := cert.PrivateKey.(crypto.Signer)
	if !ok {
		t.Fatalf("private key %T is not a crypto.Signer", cert.PrivateKey)
	}
	s := &onceFailingSigner{Signer: key}
	s.fail.Store(true)
	cert.PrivateKey = s
	return &cert, s
}

// The server's TLS handshake signs with the private key of TLSServerCert. One
// that does not return normally fails that handshake: the connection is closed
// and the next one is served.
func TestServerTLS_PrivateKeyThatDoesNotReturnNormally(t *testing.T) {
	dev := e2eNewDevice()
	dev.setHolding(1, 0x0101)
	log := &e2eLogger{}
	var signer *onceFailingSigner
	p := e2eStart(t, "tcp+tls", dev, e2eOpts{noOpen: true, server: func(c *ServerConfig) {
		c.TLSServerCert, signer = withFailingKey(t, *c.TLSServerCert)
		c.Logger = log
	}})

	if err := p.client.Open(); err == nil {
		t.Fatal("Open succeeded although the server's key did not sign")
	}
	if signer.fail.Load() {
		t.Fatal("the server's key was not used")
	}
	e2eEventually(t, "the error-level log line", func() bool {
		return log.has("E ", "TLS handshake with 127.0.0.1:", "did not return normally: the private key's Sign", "goroutine ")
	})
	e2eEventually(t, "the connection to be released", func() bool { return p.conns() == 0 })

	if err := p.client.Open(); err != nil {
		t.Fatalf("Open once the key signs: %v", err)
	}
	if v, err := p.client.ReadHoldingRegister(context.Background(), e2eUnit, 1); err != nil || v != 0x0101 {
		t.Errorf("request: %#04x, %v", v, err)
	}
}

// failingLogger is a Logger whose methods do not return normally while fail
// is set, and record their line otherwise.
type failingLogger struct {
	e2eLogger
	fail atomic.Bool
}

func (l *failingLogger) check(level, format string, args ...any) {
	if l.fail.Load() && level != "E" {
		panic("the Logger's " + level)
	}
	l.add(level, format, args...)
}

func (l *failingLogger) Debugf(format string, args ...any) { l.check("D", format, args...) }
func (l *failingLogger) Infof(format string, args ...any)  { l.check("I", format, args...) }
func (l *failingLogger) Warnf(format string, args ...any)  { l.check("W", format, args...) }
func (l *failingLogger) Errorf(format string, args ...any) { l.check("E", format, args...) }

// The server logs each request it reads, on the goroutine that reads the
// connection, at debug level. A Logger whose Debugf does not return normally
// loses that entry; the failure is reported through its Errorf, and the
// request is served.
func TestServerLogger_MethodThatDoesNotReturnNormally(t *testing.T) {
	dev := e2eNewDevice()
	dev.setHolding(1, 0x0101)
	log := &failingLogger{}
	log.fail.Store(true)
	p := e2eStart(t, "tcp", dev, e2eOpts{server: func(c *ServerConfig) { c.Logger = log }})
	for range 2 {
		if v, err := p.client.ReadHoldingRegister(context.Background(), e2eUnit, 1); err != nil || v != 0x0101 {
			t.Fatalf("request with a Logger that does not return normally: %#04x, %v", v, err)
		}
	}
	if !log.has("E ", "Logger.Debugf did not return normally: the Logger's D", "goroutine ") {
		t.Errorf("no report of the Logger's failure:\n%s", firstLines(log.dump()))
	}
}

// firstLines returns the lines of a log dump that are not part of a stack.
func firstLines(dump string) string {
	var out []string
	for line := range strings.SplitSeq(dump, "\n") {
		if strings.HasPrefix(line, "\t") || strings.HasPrefix(line, "goroutine ") || strings.Contains(line, ".go:") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
