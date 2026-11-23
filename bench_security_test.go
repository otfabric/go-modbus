// SPDX-License-Identifier: MIT

package modbus

import (
	"context"
	"testing"
	"time"
)

// nopSecurityMetrics implements ServerMetrics, ClientMetrics and every
// optional observation interface, doing nothing.
type nopSecurityMetrics struct{}

func (nopSecurityMetrics) OnRequest(uint8, FunctionCode)                            {}
func (nopSecurityMetrics) OnResponse(uint8, FunctionCode, time.Duration)            {}
func (nopSecurityMetrics) OnError(uint8, FunctionCode, time.Duration, error)        {}
func (nopSecurityMetrics) OnTimeout(uint8, FunctionCode, time.Duration)             {}
func (nopSecurityMetrics) OnConnectionRejected(string, RejectReason)                {}
func (nopSecurityMetrics) OnConnectionOpened(string, string)                        {}
func (nopSecurityMetrics) OnConnectionClosed(string)                                {}
func (nopSecurityMetrics) OnConnectionCloseReason(string, CloseReason, error)       {}
func (nopSecurityMetrics) OnTLSHandshake(TLSHandshakeInfo)                          {}
func (nopSecurityMetrics) OnAcceptError(error, time.Duration)                       {}
func (nopSecurityMetrics) OnProtocolViolation(ProtocolViolation)                    {}
func (nopSecurityMetrics) OnResponseAnomaly(ResponseAnomaly)                        {}
func (nopSecurityMetrics) OnAttempt(uint8, FunctionCode, int, time.Duration, error) {}
func (nopSecurityMetrics) OnRetryDial(int, time.Duration, error)                    {}

// plainMetrics implements ServerMetrics and ClientMetrics only.
type plainMetrics struct{}

func (plainMetrics) OnRequest(uint8, FunctionCode)                     {}
func (plainMetrics) OnResponse(uint8, FunctionCode, time.Duration)     {}
func (plainMetrics) OnError(uint8, FunctionCode, time.Duration, error) {}
func (plainMetrics) OnTimeout(uint8, FunctionCode, time.Duration)      {}

// BenchmarkSecurityHooks measures one FC03 round trip on loopback tcp,
// server and client in the process, with the security observation hooks,
// the authorizer and the hardening settings absent and present.
func BenchmarkSecurityHooks(b *testing.B) {
	allow := RequestAuthorizerFunc(func(context.Context, *AuthorizationRequest) error { return nil })
	for _, bc := range []struct {
		name   string
		server func(*ServerConfig)
		client func(*Config)
	}{
		{"absent", nil, nil},
		{"plain-metrics", func(c *ServerConfig) { c.Metrics = plainMetrics{} }, func(c *Config) { c.Metrics = plainMetrics{} }},
		{"observation-hooks", func(c *ServerConfig) { c.Metrics = nopSecurityMetrics{} }, func(c *Config) { c.Metrics = nopSecurityMetrics{} }},
		{"authorizer", func(c *ServerConfig) { c.Authorizer = allow }, nil},
		{"hardening", func(c *ServerConfig) {
			c.MaxClientsPerHost = 4
			c.FirstRequestTimeout = 10 * time.Second
			c.FrameTimeout = 10 * time.Second
		}, nil},
		{"all", func(c *ServerConfig) {
			c.Metrics = nopSecurityMetrics{}
			c.Authorizer = allow
			c.MaxClientsPerHost = 4
			c.FirstRequestTimeout = 10 * time.Second
			c.FrameTimeout = 10 * time.Second
		}, func(c *Config) { c.Metrics = nopSecurityMetrics{} }},
	} {
		b.Run(bc.name, func(b *testing.B) {
			h := &fnHandler{holding: func(r *HoldingRegistersRequest) ([]uint16, error) {
				return make([]uint16, r.Quantity), nil
			}}
			sconf := ServerConfig{URL: "tcp://127.0.0.1:0", Timeout: time.Minute}
			if bc.server != nil {
				bc.server(&sconf)
			}
			srv, err := NewServer(&sconf, h)
			if err != nil {
				b.Fatal(err)
			}
			if err := srv.Start(); err != nil {
				b.Fatal(err)
			}
			defer func() { _ = srv.Stop() }()
			cconf := Config{URL: "tcp://" + srv.Addr().String(), Timeout: 5 * time.Second}
			if bc.client != nil {
				bc.client(&cconf)
			}
			c, err := New(cconf)
			if err != nil {
				b.Fatal(err)
			}
			if err := c.Open(); err != nil {
				b.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := c.ReadHoldingRegisters(ctx, 1, 0, 4); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
